//! Live settings display, validation and persistence.

use super::*;

impl Client {
    /// `/set` with no arguments (`listSettings`).
    pub(super) fn list_settings(&self) {
        for k in SETTABLE_KEYS {
            self.show_setting(k);
        }
        self.server_msg("/set <raktas> <reikšmė> — keisti (perrašoma į config failą)");
    }

    /// `/set <key>` (`showSetting`).
    pub(super) fn show_setting(&self, key: &str) {
        let v = match key {
            "mouse" => bool_cfg(self.mu.lock().mouse).to_string(),
            "notifications" => bool_cfg(self.mu.lock().notify).to_string(),
            "ctcpversion" => bool_cfg(self.mu.lock().ctcp_version).to_string(),
            "reconnect" => bool_cfg(self.mu.lock().reconnect).to_string(),
            "timestamps" => bool_cfg(timestamps_enabled()).to_string(),
            "ts_format" => ts_format(),
            "tscolor" => ts_color_name().to_string(),
            "highlight_color" => hl_color_name(),
            "nick_colors" => bool_cfg(nick_coloring()).to_string(),
            "maxmessages" => self.mu.lock().max_messages.to_string(),
            "maxmemory" => (self.mu.lock().max_buffer_bytes / (1024 * 1024)).to_string(),
            "theme" => if theme_dark_mode() { "dark" } else { "light" }.to_string(),
            "show_joins" => show_joins().as_str().to_string(),
            _ => String::new(),
        };
        self.server_msg(&format!("{key} = {v}"));
    }

    /// `/set <key> <value>`: validate, apply live, then write the config file.
    pub(super) fn apply_setting(&self, key: &str, value: &str) {
        let Some(ok_msg) = self.apply_setting_live(key, value) else {
            return;
        };
        if let Err(e) = save_config_setting(key, value) {
            self.err_msg(&format!("Pritaikyta gyvai, bet į failą NEĮRAŠYTA: {e}"));
            return;
        }
        self.server_msg(&format!(
            "nustatymas: {key} = {}{ok_msg}",
            sanitize_text(value)
        ));
    }

    /// Validate and apply one setting in memory (no file write). `None` —
    /// rejected (the error is already shown); `Some(note)` — applied.
    pub(super) fn apply_setting_live(&self, key: &str, value: &str) -> Option<&'static str> {
        let mut ok_msg = "";
        let reject_bool = |v: &str| -> Option<bool> {
            let (b, known) = parse_bool_value(v);
            if !known {
                self.err_msg(&format!(
                    "Reikšmė turi būti true/false (on, off, yes, no, 1, 0): {}",
                    sanitize_text(v)
                ));
                return None;
            }
            Some(b)
        };
        match key {
            "mouse" => {
                let b = reject_bool(value)?;
                self.mu.lock().mouse = b;
                term_write(if b { MOUSE_ON } else { MOUSE_OFF });
            }
            "notifications" => {
                let b = reject_bool(value)?;
                self.mu.lock().notify = b;
            }
            "ctcpversion" => {
                let b = reject_bool(value)?;
                self.mu.lock().ctcp_version = b;
            }
            "reconnect" => {
                let b = reject_bool(value)?;
                self.mu.lock().reconnect = b;
            }
            "timestamps" => {
                let b = reject_bool(value)?;
                set_timestamps(b, "");
            }
            "ts_format" => {
                if !is_valid_ts_format(value) {
                    self.err_msg(&format!(
                        "Nevalidus laikmatis (pvz. „15:04“, „15:04:05“): {}",
                        sanitize_text(value)
                    ));
                    return None;
                }
                set_timestamps(timestamps_enabled(), value);
            }
            "tscolor" => {
                if !valid_named_color(value) {
                    self.err_msg(&format!(
                        "Nežinoma spalva (gray, blue, green, cyan, none): {}",
                        sanitize_text(value)
                    ));
                    return None;
                }
                set_ts_color(value);
            }
            "highlight_color" => {
                if !valid_highlight_color(value) {
                    self.err_msg(&format!(
                        "Nežinoma spalva (red, blue, green, magenta, cyan, yellow, none): {}",
                        sanitize_text(value)
                    ));
                    return None;
                }
                set_highlight_color(value);
            }
            "nick_colors" => {
                let b = reject_bool(value)?;
                set_nick_colors(b);
                self.invalidate_render_caches();
            }
            "theme" => {
                if !valid_theme(value) {
                    self.err_msg(&format!(
                        "Nežinoma tema (dark, light): {}",
                        sanitize_text(value)
                    ));
                    return None;
                }
                set_theme(value);
                self.invalidate_render_caches();
            }
            "show_joins" => {
                if JoinFilter::parse(value).is_none() {
                    self.err_msg(&format!(
                        "Reikšmė turi būti all, smart arba none: {}",
                        sanitize_text(value)
                    ));
                    return None;
                }
                set_show_joins(value);
                // Esama istorija perpiešiama pagal naują filtrą.
                self.invalidate_render_caches();
            }
            "maxmessages" => {
                let n: i32 = match value.parse() {
                    Ok(n) if (100..=100_000).contains(&n) => n,
                    _ => {
                        self.err_msg(&format!(
                            "Reikšmė turi būti sveikas skaičius 100–100000: {}",
                            sanitize_text(value)
                        ));
                        return None;
                    }
                };
                {
                    let mut st = self.mu.lock();
                    st.max_messages = n;
                    for ch in &mut st.channels {
                        ch.max_messages = n as usize;
                    }
                }
                ok_msg = " (esamiems langams)";
            }
            "maxmemory" => {
                use crate::config::{max_memory_bytes, parse_max_memory};
                use crate::config::{MAX_MAX_MEMORY_MB, MIN_MAX_MEMORY_MB};
                let Some(n) = parse_max_memory(value) else {
                    self.err_msg(&format!(
                        "Reikšmė — MB, sveikas skaičius {MIN_MAX_MEMORY_MB}–{MAX_MAX_MEMORY_MB}: {}",
                        sanitize_text(value)
                    ));
                    return None;
                };
                // Lowered below the current use: trim now, not on the next line.
                let mut st = self.mu.lock();
                st.max_buffer_bytes = max_memory_bytes(n);
                st.enforce_memory_budget();
                drop(st);
                self.queue_redraw();
            }
            _ => return None,
        }
        // Atvaizduotos eilutės saugo laiko žymą ir spalvų SGR — be cache
        // išvalymo senos žinutės liktų su senu formatu (kaip temos atveju).
        if matches!(
            key,
            "timestamps" | "ts_format" | "tscolor" | "highlight_color"
        ) {
            self.invalidate_render_caches();
        }
        Some(ok_msg)
    }
}
