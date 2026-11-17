//! Slash commands (`handleInput`, `handleCmd`) and `/set`.
//!
//! Every command from `input.go` is dispatched here. Methods take
//! [`Arc<Client>`](std::sync::Arc) because `/connect` and `/open` spawn threads.
//! The lock is dropped before `send`, `add_msg`, `server_msg` or `err_msg`.

use std::sync::Arc;
use std::time::Duration;

use chrono::Utc;

#[path = "commands_settings.rs"]
mod settings;

use crate::client::channels::with_cmd_feedback;
use crate::client::session::{open_url, split_for_privmsg, term_write, MOUSE_OFF, MOUSE_ON};
use crate::client::types::{new_msg, Client, MsgType, SERVER_WINDOW};
use crate::config::{
    is_settable, key_action_names, keys_path, parse_bool_value, save_config_setting, SETTABLE_KEYS,
};
use crate::sanitize::{sanitize_chan_name, sanitize_path, sanitize_text};
use crate::ui::{
    hl_color_name, messages_to_json_indent, nick_coloring, set_highlight_color, set_nick_colors,
    set_theme, set_timestamps, set_ts_color, theme_dark_mode, timestamps_enabled, ts_color_name,
    ts_format,
};
use crate::validate::{
    is_valid_channel_for, is_valid_channel_key, is_valid_list_filter, is_valid_mode_spec,
    is_valid_mode_target, is_valid_nick, is_valid_server, is_valid_target, is_valid_who_target,
};

use super::first_field;
use super::sensitive::is_sensitive_history;

/// Go `strings.SplitN(s, " ", 2)`. An empty string yields `("", None)`.
fn split2(s: &str) -> (&str, Option<&str>) {
    if s.is_empty() {
        return ("", None);
    }
    match s.split_once(' ') {
        Some((a, b)) => (a, Some(b)),
        None => (s, None),
    }
}

fn rune_len(s: &str) -> i32 {
    s.chars().count() as i32
}

fn fold_eq(a: &str, b: &str) -> bool {
    a.to_lowercase() == b.to_lowercase()
}

fn bool_cfg(b: bool) -> &'static str {
    if b {
        "true"
    } else {
        "false"
    }
}

/// Timestamp layout accepted by `/set ts_format` (`isValidTSFormat`).
pub fn is_valid_ts_format(v: &str) -> bool {
    if v.is_empty() || v.len() > 16 || v.contains("  ") {
        return false;
    }
    v.chars()
        .all(|r| r.is_ascii_digit() || matches!(r, ':' | '.' | '-' | ' '))
}

fn valid_named_color(v: &str) -> bool {
    matches!(
        v.to_lowercase().as_str(),
        "gray" | "blue" | "green" | "cyan" | "none"
    )
}

fn valid_highlight_color(v: &str) -> bool {
    matches!(
        v.to_lowercase().as_str(),
        "red" | "blue" | "green" | "magenta" | "cyan" | "yellow" | "none"
    )
}

fn valid_theme(v: &str) -> bool {
    matches!(v.to_lowercase().as_str(), "dark" | "light")
}

/// Render one key sequence in `keys`-file syntax (`escapeKeySeq`).
pub fn escape_key_seq(seq: &str) -> String {
    let mut out = String::new();
    for b in seq.bytes() {
        match b {
            0x1b => out.push_str("\\e"),
            b'\n' => out.push_str("\\n"),
            b'\r' => out.push_str("\\r"),
            b'\t' => out.push_str("\\t"),
            b'\\' => out.push_str("\\\\"),
            b' ' => out.push_str("\\x20"),
            33..=126 => out.push(b as char),
            _ => out.push_str(&format!("\\x{b:02x}")),
        }
    }
    out
}

fn or_default(s: &str, def: &str) -> String {
    if s.is_empty() {
        def.to_string()
    } else {
        s.to_string()
    }
}

impl Client {
    /// A submitted input line: `/command` or a chat message (`handleInput`).
    ///
    /// Errors it reports are shown in the window the user is typing in
    /// (see [`with_cmd_feedback`]).
    pub fn handle_input(self: &Arc<Self>, text: &str) {
        with_cmd_feedback(|| self.handle_input_inner(text));
    }

    fn handle_input_inner(self: &Arc<Self>, text: &str) {
        if let Some(rest) = text.strip_prefix('/') {
            let (cmd, args) = match rest.split_once(' ') {
                Some((c, a)) => (c.to_lowercase(), a.to_string()),
                None => (rest.to_lowercase(), String::new()),
            };
            self.handle_cmd(&cmd, &args);
            return;
        }
        let active_name = self.active_name();
        if active_name.is_empty() || active_name == SERVER_WINDOW {
            self.server_msg(
                "Tai serverio langas — čia žinutės nesiunčiamos. Įeikite į kanalą \
                 (/join #kanalas) arba atidarykite pokalbį (/query <nick>).",
            );
            return;
        }
        if !self.is_connected() {
            self.err_msg(
                "Neprisijungta prie serverio — žinutė neišsiųsta (/reconnect arba /connect)",
            );
            return;
        }
        let nick = self.my_nick();
        for part in split_for_privmsg(&active_name, text) {
            self.send(&format!("PRIVMSG {active_name} :{part}"));
            self.add_msg(
                &active_name,
                new_msg(Utc::now(), &nick, &part, MsgType::Chat),
            );
        }
    }

    /// Dispatch one slash command (`handleCmd`). `cmd` is already lower-case.
    pub fn handle_cmd(self: &Arc<Self>, cmd: &str, args: &str) {
        match cmd {
            "connect" | "server" => self.cmd_connect(args),
            "reconnect" => {
                self.server_msg("Jungiamasi iš naujo...");
                self.start_connect();
            }
            "nick" => self.cmd_nick(args),
            "join" | "j" => self.cmd_join(args),
            "part" | "leave" => self.cmd_part(args),
            "query" => self.cmd_query(args),
            "msg" | "m" => self.cmd_msg(args),
            "notice" => self.cmd_notice(args),
            "invite" => self.cmd_invite(args),
            "cycle" => self.cmd_cycle(args),
            "op" => self.cmd_mode_nick(args, "+o", "/op <nick>"),
            "deop" => self.cmd_mode_nick(args, "-o", "/deop <nick>"),
            "voice" => self.cmd_mode_nick(args, "+v", "/voice <nick>"),
            "devoice" => self.cmd_mode_nick(args, "-v", "/devoice <nick>"),
            "me" => self.cmd_me(args),
            "topic" => self.cmd_topic(args),
            "kick" | "k" => self.cmd_kick(args),
            "mode" => self.cmd_mode(args),
            "whois" | "wi" => self.cmd_whois(args),
            "away" => self.cmd_away(args),
            "time" => {
                if self.is_connected() {
                    self.send("TIME");
                } else {
                    self.err_msg("Neprisijungta");
                }
            }
            "clear" => self.cmd_clear(),
            "clearlogs" => self.cmd_clearlogs(args),
            "names" => self.cmd_names(),
            "back" => {
                self.mu.lock().away.clear();
                if self.is_connected() {
                    self.send("AWAY");
                }
                self.server_msg("Away išjungtas");
                self.queue_redraw();
            }
            "ignore" => self.cmd_ignore(args),
            "unignore" => self.cmd_unignore(args),
            "ignores" => self.cmd_ignores(),
            "list" => self.cmd_list(args),
            "search" => self.cmd_search(args),
            "set" => self.cmd_set(args),
            "notify" => self.cmd_notify(args),
            "exportjson" => self.cmd_exportjson(args),
            "logsearch" => self.cmd_logsearch(args),
            "open" => self.cmd_open(args),
            "urls" => self.cmd_urls(),
            "raw" => {
                if self.is_connected() && !args.is_empty() {
                    self.send(args);
                }
            }
            "w" => self.cmd_who(args),
            "win" | "window" => self.cmd_window(args),
            "close" | "wc" => self.cmd_close(args),
            "quit" | "exit" | "q" => self.cmd_quit(args),
            "keys" => self.cmd_keys(args),
            "help" => {
                for line in help_lines() {
                    self.active_info_msg(&line);
                }
            }
            _ => self.err_msg(&format!(
                "Nežinoma komanda: /{}  (komandų sąrašas: /help)",
                sanitize_text(cmd)
            )),
        }
    }

    fn cmd_connect(self: &Arc<Self>, args: &str) {
        let parts: Vec<&str> = args.split_whitespace().collect();
        if parts.len() < 2 {
            self.err_msg("Naudojimas: /connect <serveris> <portas> [nick] [tls]");
            return;
        }
        match parts[1].parse::<i32>() {
            Ok(p) if (1..=65535).contains(&p) => {}
            _ => {
                self.err_msg(&format!(
                    "Netinkamas porto numeris {:?} (leistina: 1–65535)",
                    parts[1]
                ));
                return;
            }
        }
        if parts.len() >= 3 && !is_valid_nick(parts[2]) {
            self.err_msg(
                "Netinkamas slapyvardis (leistini simboliai: raidės, skaičiai, _-[]\\^{}|, max 30)",
            );
            return;
        }
        if !is_valid_server(parts[0]) {
            self.err_msg(
                "Netinkamas serverio pavadinimas (leistini simboliai: raidės, skaičiai, . - _ max 100)",
            );
            return;
        }
        {
            let mut st = self.mu.lock();
            st.server = parts[0].to_string();
            st.port = parts[1].to_string();
            if parts.len() >= 3 {
                st.nick = parts[2].to_string();
            }
            st.use_tls = parts.len() >= 4 && parts[3] == "tls";
            if st.nick.is_empty() {
                st.nick = "mircbsd".into();
            }
        }
        self.start_connect();
    }

    fn cmd_nick(&self, args: &str) {
        if args.is_empty() {
            let n = self.my_nick();
            self.server_msg(&format!("Dabartinis slapyvardis: {n}"));
            return;
        }
        let new_nick = first_field(args);
        if new_nick.is_empty() || !is_valid_nick(new_nick) {
            self.err_msg(
                "Netinkamas slapyvardis (leistini simboliai: raidės, skaičiai, _-[]\\^{}|, max 30)",
            );
            return;
        }
        if !self.is_valid_nick_len(new_nick) {
            self.err_msg(&format!(
                "Slapyvardis per ilgas — serveris leidžia {} simbolius",
                self.nick_len_limit()
            ));
            return;
        }
        let has_conn = {
            let mut st = self.mu.lock();
            st.nick = new_nick.to_string();
            st.conn.is_some()
        };
        if has_conn {
            self.send(&format!("NICK {new_nick}"));
        }
    }

    fn cmd_join(&self, args: &str) {
        if args.is_empty() {
            self.err_msg("Naudojimas: /join #kanalas [slaptažodis]");
            return;
        }
        let (raw_name, key) = split2(args);
        let mut ch_name = raw_name.to_string();
        if !self.is_channel(&ch_name) {
            ch_name.insert(0, '#');
        }
        let chantypes = self.isup_snapshot().0;
        if !is_valid_channel_for(&ch_name, &chantypes) {
            self.err_msg("Netinkamas kanalo pavadinimas (raidės, skaičiai ir .-_ ; max 50)");
            return;
        }
        if rune_len(&ch_name) > self.channel_len_limit() {
            self.err_msg(&format!(
                "Kanalo pavadinimas per ilgas — serveris leidžia {} simbolius",
                self.channel_len_limit()
            ));
            return;
        }
        let refused = {
            let mut st = self.mu.lock();
            if self.ensure_ch(&mut st, &ch_name).is_none() {
                let hit = st.chan_limit_hit.clone();
                if !hit.is_empty() && hit == sanitize_chan_name(&ch_name) {
                    st.chan_limit_hit.clear();
                    Some(hit)
                } else {
                    None
                }
            } else {
                None
            }
        };
        if let Some(hit) = refused {
            self.err_msg(&format!(
                "ĮSPĖJIMAS: pasiektas kanalų limitas — kanalas „{hit}“ neatidarytas"
            ));
        }
        if self.is_connected() {
            if let Some(key) = key {
                if !key.is_empty() {
                    if !is_valid_channel_key(key) {
                        self.err_msg(
                            "Netinkamas kanalo raktas (be tarpų ir specialių simbolių; max 64)",
                        );
                        return;
                    }
                    self.send(&format!("JOIN {ch_name} {key}"));
                    return;
                }
            }
            self.send(&format!("JOIN {ch_name}"));
        }
    }

    fn cmd_part(&self, args: &str) {
        let ch_name = self.active_name();
        let msg = if args.is_empty() { "Viso gero!" } else { args };
        if self.is_connected() && self.is_channel(&ch_name) {
            self.send(&format!("PART {ch_name} :{msg}"));
        }
    }

    fn cmd_query(&self, args: &str) {
        if args.is_empty() {
            self.err_msg("Naudojimas: /query <nick> — atidaro privatų langą");
            return;
        }
        let target = args.split_whitespace().next().unwrap_or("");
        if !is_valid_target(target) {
            self.err_msg("Netinkamas nick'as (raidės, skaičiai ir .-_ ; max 100)");
            return;
        }
        let (idx, hit) = {
            let mut st = self.mu.lock();
            let idx = self.ensure_ch(&mut st, target);
            if let Some(i) = idx {
                st.channels[i].is_private = true;
            }
            let hit = if idx.is_none() {
                let hit = st.chan_limit_hit.clone();
                if !hit.is_empty() && hit == sanitize_chan_name(target) {
                    st.chan_limit_hit.clear();
                    Some(hit)
                } else {
                    None
                }
            } else {
                None
            };
            (idx, hit)
        };
        if idx.is_none() {
            if let Some(hit) = hit {
                self.err_msg(&format!(
                    "ĮSPĖJIMAS: pasiektas kanalų limitas — langas „{hit}“ neatidarytas"
                ));
            }
            return;
        }
        if let Some(i) = idx {
            self.switch_ch(i);
        }
        self.server_msg(&format!(
            "Privatus „{target}“ langas atidarytas — rašyk „/msg {target} <žinutė>“ arba „/me <veiksmas>“"
        ));
    }

    fn cmd_msg(&self, args: &str) {
        let (target, text) = match split2(args) {
            (t, Some(text)) if !t.is_empty() => (t, text),
            _ => {
                self.err_msg("Naudojimas: /msg <nick> <žinutė>");
                return;
            }
        };
        if !is_valid_target(target) {
            self.err_msg("Netinkamas gavėjas (raidės, skaičiai ir .-_ ; max 100)");
            return;
        }
        if self.is_connected() {
            for part in split_for_privmsg(target, text) {
                self.send(&format!("PRIVMSG {target} :{part}"));
            }
        }
        {
            let mut st = self.mu.lock();
            self.ensure_ch(&mut st, target);
        }
        self.add_msg(
            target,
            new_msg(Utc::now(), &self.my_nick(), text, MsgType::Chat),
        );
    }

    fn cmd_notice(&self, args: &str) {
        let (target, text) = match split2(args) {
            (t, Some(text)) if !t.is_empty() => (t, text),
            _ => {
                self.err_msg("Naudojimas: /notice <nick> <žinutė>");
                return;
            }
        };
        if !is_valid_target(target) {
            self.err_msg("Netinkamas gavėjas (raidės, skaičiai ir .-_ ; max 100)");
            return;
        }
        if self.is_connected() {
            self.send(&format!("NOTICE {target} :{text}"));
            self.server_msg(&format!("-> -{target}- {text}"));
        }
    }

    fn cmd_invite(&self, args: &str) {
        let parts: Vec<&str> = args.split_whitespace().collect();
        if parts.is_empty() {
            self.err_msg("Naudojimas: /invite <nick> [#kanalas]");
            return;
        }
        let nick = parts[0];
        if !is_valid_nick(nick) {
            self.err_msg("Netinkamas nick'as (raidės, skaičiai ir specialūs simboliai; max 30)");
            return;
        }
        let ch_name = if parts.len() >= 2 {
            let ch = parts[1];
            let chantypes = self.isup_snapshot().0;
            if !is_valid_channel_for(ch, &chantypes) {
                self.err_msg("Netinkamas kanalo pavadinimas (raidės, skaičiai ir .-_ ; max 50)");
                return;
            }
            ch.to_string()
        } else {
            self.active_name()
        };
        if self.is_connected() {
            self.send(&format!("INVITE {nick} {ch_name}"));
            self.server_msg(&format!("Pakviestas {nick} į {ch_name}"));
        }
    }

    fn cmd_cycle(&self, args: &str) {
        let ch_name = self.active_name();
        if !self.is_channel(&ch_name) {
            self.err_msg("/cycle galima tik kanale");
            return;
        }
        if self.is_connected() {
            let msg = if args.is_empty() { "cycle" } else { args };
            self.send(&format!("PART {ch_name} :{msg}"));
            self.send(&format!("JOIN {ch_name}"));
        }
    }

    fn cmd_mode_nick(&self, args: &str, mode: &str, usage: &str) {
        if args.is_empty() {
            self.err_msg(&format!("Naudojimas: {usage}"));
            return;
        }
        let ch_name = self.active_name();
        let nick = first_field(args);
        if !is_valid_nick(nick) {
            self.err_msg("Netinkamas nick'as (raidės, skaičiai ir specialūs simboliai; max 30)");
            return;
        }
        if self.is_connected() {
            self.send(&format!("MODE {ch_name} {mode} {nick}"));
        }
    }

    fn cmd_me(&self, args: &str) {
        let active = self.active_name();
        if self.is_connected() && !active.is_empty() && active != SERVER_WINDOW {
            self.send(&format!("PRIVMSG {active} :\u{1}ACTION {args}\u{1}"));
            self.add_msg(
                &active,
                new_msg(Utc::now(), &self.my_nick(), args, MsgType::Action),
            );
        }
    }

    fn cmd_topic(&self, args: &str) {
        let active = self.active_name();
        if self.is_connected() && !active.is_empty() {
            if args.is_empty() {
                self.send(&format!("TOPIC {active}"));
            } else if rune_len(args) > self.topic_len_limit() {
                self.err_msg(&format!(
                    "Tema per ilga — serveris leidžia {} simbolius",
                    self.topic_len_limit()
                ));
            } else {
                self.send(&format!("TOPIC {active} :{args}"));
            }
        }
    }

    fn cmd_kick(&self, args: &str) {
        let (nick, reason) = split2(args);
        if !self.is_connected() || nick.is_empty() {
            self.err_msg("Naudojimas: /kick <nick> [priežastis]");
            return;
        }
        let ch_name = self.active_name();
        if !self.is_channel(&ch_name) {
            self.err_msg("/kick galima tik kanale");
            return;
        }
        if !is_valid_nick(nick) {
            self.err_msg("Netinkamas nick'as (raidės, skaičiai ir specialūs simboliai; max 30)");
            return;
        }
        let reason = match reason {
            Some(r) if !r.is_empty() => r,
            _ => "Kicked",
        };
        let limit = self.kick_len_limit();
        if limit > 0 && rune_len(reason) > limit {
            self.err_msg(&format!(
                "Priežastis per ilga — serveris leidžia {limit} simbolius"
            ));
            return;
        }
        self.send(&format!("KICK {ch_name} {nick} :{reason}"));
    }

    fn cmd_mode(&self, args: &str) {
        if !self.is_connected() || args.is_empty() {
            self.err_msg("Naudojimas: /mode <target|+/-mode> [parametrai]");
            return;
        }
        let fields: Vec<&str> = args.split_whitespace().collect();
        if fields.is_empty() {
            self.err_msg("Naudojimas: /mode <target|+/-mode> [parametrai]");
            return;
        }
        let joined = fields.join(" ");
        if fields[0].starts_with('+') || fields[0].starts_with('-') {
            let ch_name = self.active_name();
            if !self.is_channel(&ch_name) {
                self.err_msg("/mode galima tik kanale");
                return;
            }
            if !is_valid_mode_spec(&fields[1..]) {
                self.err_msg(
                    "Netinkami mode parametrai (per daug parametrų arba specialūs simboliai)",
                );
                return;
            }
            self.send(&format!("MODE {ch_name} {joined}"));
        } else {
            if !is_valid_mode_target(fields[0]) {
                self.err_msg("Netinkamas MODE tikslas (kanalas, nick'as ar serverio kaukė)");
                return;
            }
            if !is_valid_mode_spec(&fields[1..]) {
                self.err_msg(
                    "Netinkami mode parametrai (per daug parametrų arba specialūs simboliai)",
                );
                return;
            }
            self.send(&format!("MODE {joined}"));
        }
    }

    fn cmd_whois(&self, args: &str) {
        let w = first_field(args);
        if !(self.is_connected() && !w.is_empty()) {
            return;
        }
        if !is_valid_nick(w) {
            self.err_msg("Netinkamas nick'as (raidės, skaičiai ir specialūs simboliai; max 30)");
            return;
        }
        let act = self.active_name();
        if self.is_channel(&act) {
            self.set_whois_target(&act);
        } else {
            self.set_whois_target("");
        }
        self.send(&format!("WHOIS {w}"));
    }

    fn cmd_away(&self, args: &str) {
        if args.is_empty() {
            self.mu.lock().away.clear();
            if self.is_connected() {
                self.send("AWAY");
            }
            self.server_msg("Away išjungtas");
        } else {
            self.mu.lock().away = args.to_string();
            if self.is_connected() {
                self.send(&format!("AWAY :{args}"));
            }
            self.server_msg(&format!("Away: {args}"));
        }
        self.queue_redraw();
    }

    fn cmd_clear(&self) {
        {
            let mut st = self.mu.lock();
            if st.active_chan().is_none() {
                return;
            }
            if let Some(ch) = st.active_chan_mut() {
                ch.clear_messages();
            }
            st.scroll_off = 0;
            st.chase_until = None;
        }
        self.queue_redraw();
    }

    fn cmd_clearlogs(&self, args: &str) {
        let n = self.clear_logs(args);
        let label = if args.is_empty() {
            "šis serveris"
        } else {
            args
        };
        if n == 0 {
            self.server_msg(&format!("Log failų („{label}“) nerasta"));
        } else {
            self.server_msg(&format!("Išvalyta {n} log failas(-ai) („{label}“)"));
        }
    }

    fn cmd_names(&self) {
        let (do_it, ch_name) = {
            let mut st = self.mu.lock();
            let ch_name = st.active_chan().map(|c| c.name.clone()).unwrap_or_default();
            let do_it = st.connected && self.is_channel(&ch_name);
            if do_it {
                if let Some(ch) = st.active_chan_mut() {
                    ch.clear_nicks();
                }
            }
            (do_it, ch_name)
        };
        if do_it {
            self.send(&format!("NAMES {ch_name}"));
        }
    }

    fn cmd_ignore(self: &Arc<Self>, args: &str) {
        let nick = first_field(args);
        if nick.is_empty() {
            self.err_msg("Naudojimas: /ignore <nick>");
            return;
        }
        self.mu.lock().ignore_list.push(nick.to_string());
        self.server_msg(&format!("Ignoruojamas: {nick}"));
        let c = Arc::clone(self);
        std::thread::spawn(move || c.save_ignore());
    }

    fn cmd_unignore(self: &Arc<Self>, args: &str) {
        let nick = first_field(args);
        if nick.is_empty() {
            self.err_msg("Naudojimas: /unignore <nick>");
            return;
        }
        {
            let mut st = self.mu.lock();
            let kept: Vec<String> = st
                .ignore_list
                .iter()
                .filter(|ig| !fold_eq(ig, nick))
                .cloned()
                .collect();
            st.ignore_list = kept;
        }
        self.server_msg(&format!("Ignoravimas pašalintas: {nick}"));
        let c = Arc::clone(self);
        std::thread::spawn(move || c.save_ignore());
    }

    fn cmd_ignores(&self) {
        let list = self.mu.lock().ignore_list.clone();
        if list.is_empty() {
            self.server_msg("Ignoruojamų sąrašas tuščias");
        } else {
            self.server_msg(&format!("Ignoruojami: {}", list.join(", ")));
        }
    }

    fn cmd_list(&self, args: &str) {
        if !self.is_connected() {
            return;
        }
        if !args.is_empty() {
            if !is_valid_list_filter(args) {
                self.err_msg("Netinkamas LIST filtras (be tarpų ir specialių simbolių; max 64)");
                return;
            }
            self.send(&format!("LIST {args}"));
        } else {
            self.send("LIST");
        }
    }

    fn cmd_search(&self, args: &str) {
        let low = args.to_lowercase();
        if low == "all " || low.starts_with("all ") {
            let q = if args.is_char_boundary(4) {
                args[4..].trim()
            } else {
                ""
            };
            if q.is_empty() {
                self.err_msg("Naudojimas: /search all <žodis>");
                return;
            }
            self.search_all_channels(q);
            return;
        }
        if args.is_empty() {
            let mut st = self.mu.lock();
            st.search_mode = true;
            st.search_query.clear();
        } else {
            self.do_search(args);
        }
        self.queue_redraw();
    }

    fn cmd_set(&self, args: &str) {
        if args.is_empty() {
            self.list_settings();
            return;
        }
        let parts: Vec<&str> = args.split_whitespace().collect();
        let key = parts[0].to_lowercase();
        if !is_settable(&key) {
            self.err_msg(&format!(
                "Nežinomas arba nekeičiamas raktas: {}",
                sanitize_text(parts[0])
            ));
            return;
        }
        if parts.len() == 1 {
            self.show_setting(&key);
            return;
        }
        let value = args.strip_prefix(parts[0]).unwrap_or(args).trim();
        self.apply_setting(&key, value);
    }

    fn cmd_notify(&self, args: &str) {
        if args.is_empty() || fold_eq(args.trim(), "list") {
            let (list, online) = {
                let st = self.mu.lock();
                (st.monitor_list.clone(), st.monitor_online.clone())
            };
            if list.is_empty() {
                self.server_msg(
                    "/notify sąrašas tuščias. Naudojimas: /notify add <nick>  |  /notify del <nick>",
                );
                return;
            }
            for n in &list {
                let key = self.mon_key(n);
                if online.contains(&key) {
                    self.server_msg(&format!("notify: {n} — online"));
                } else {
                    // 731 deletes the key, so a known-offline nick is "nežinoma"
                    // until the next 730 — same as the Go map after delete.
                    self.server_msg(&format!("notify: {n} — nežinoma"));
                }
            }
            return;
        }
        let sub: Vec<&str> = args.split_whitespace().collect();
        if sub.len() < 2 {
            self.err_msg("Naudojimas: /notify add <nick>  |  /notify del <nick>  |  /notify list");
            return;
        }
        match sub[0].to_lowercase().as_str() {
            "add" => {
                let mut added = Vec::new();
                for raw in &sub[1..] {
                    let nick = raw.strip_suffix(',').unwrap_or(raw);
                    if !is_valid_nick(nick) {
                        self.err_msg(&format!("Nevalidus nick: {}", sanitize_text(nick)));
                        continue;
                    }
                    let outcome = {
                        let mut st = self.mu.lock();
                        let exists = st.monitor_list.iter().any(|n| self.nick_equal(n, nick));
                        let mon_limit = self.isup.read().limits.monitor;
                        if !exists && mon_limit > 0 && st.monitor_list.len() >= mon_limit as usize {
                            Err(mon_limit)
                        } else {
                            if !exists {
                                st.monitor_list.push(nick.to_string());
                            }
                            Ok(exists)
                        }
                    };
                    match outcome {
                        Err(lim) => self.err_msg(&format!(
                            "MONITOR sąrašas pilnas (limitas {lim}) — „{}“ nepridėtas",
                            sanitize_text(nick)
                        )),
                        Ok(true) => self.server_msg(&format!("notify: {nick} jau sąraše")),
                        Ok(false) => {
                            self.server_msg(&format!("notify: {nick} pridėtas"));
                            added.push(nick.to_string());
                        }
                    }
                }
                if !added.is_empty() {
                    self.send(&format!("MONITOR + {}", added.join(",")));
                }
                self.save_monitor();
            }
            "del" | "del_" | "delete" | "remove" => {
                let mut removed = Vec::new();
                for raw in &sub[1..] {
                    let nick = raw.strip_suffix(',').unwrap_or(raw);
                    let mut st = self.mu.lock();
                    if let Some(i) = st
                        .monitor_list
                        .iter()
                        .position(|n| self.nick_equal(n, nick))
                    {
                        removed.push(st.monitor_list.remove(i));
                    }
                    let key = self.mon_key(nick);
                    st.monitor_online.remove(&key);
                }
                if removed.is_empty() {
                    let shown = sanitize_text(&sub[1..].join(", "));
                    self.server_msg(&format!("notify: niekos iš „{shown}“ nerasta sąraše"));
                } else {
                    self.send(&format!("MONITOR - {}", removed.join(",")));
                    self.save_monitor();
                }
            }
            _ => self
                .err_msg("Naudojimas: /notify add <nick>  |  /notify del <nick>  |  /notify list"),
        }
    }

    fn cmd_exportjson(&self, args: &str) {
        let mut target = args.trim().to_string();
        if target.is_empty() {
            let name = self.active_name();
            if name.is_empty() {
                self.err_msg("Nėra aktyvaus lango");
                return;
            }
            target = name;
        }
        self.export_json(&target);
    }

    fn cmd_logsearch(&self, args: &str) {
        if args.is_empty() {
            self.err_msg("Naudojimas: /logsearch [kanalas] <žodis>");
            return;
        }
        let fields: Vec<&str> = args.split_whitespace().collect();
        let (chan_filter, query) = if fields.len() == 1 {
            ("", fields[0])
        } else {
            let rest = args.strip_prefix(fields[0]).unwrap_or(args).trim();
            if !self.is_channel(fields[0]) {
                ("", args)
            } else {
                (fields[0], rest)
            }
        };
        if query.is_empty() {
            self.err_msg("Naudojimas: /logsearch [kanalas] <žodis>");
            return;
        }
        self.log_search(chan_filter, query);
    }

    fn cmd_open(self: &Arc<Self>, args: &str) {
        let urls = self.mu.lock().last_urls.clone();
        if urls.is_empty() {
            self.err_msg("Nėra URL. Naudok /urls kad pamatytum istoriją");
            return;
        }
        let url = if args.is_empty() {
            urls[urls.len() - 1].clone()
        } else {
            match args.parse::<i64>() {
                Ok(n) if n >= 1 && (n as usize) <= urls.len() => urls[n as usize - 1].clone(),
                _ => {
                    self.err_msg(&format!("/open [n] — n nuo 1 iki {}", urls.len()));
                    return;
                }
            }
        };
        self.server_msg(&format!("Atidaroma: {url}"));
        let c = Arc::clone(self);
        std::thread::spawn(move || {
            if let Err(e) = open_url(&url) {
                c.err_msg(&format!("Nepavyko atidaryti naršyklėje: {e}"));
            }
        });
    }

    fn cmd_urls(&self) {
        let urls = self.mu.lock().last_urls.clone();
        if urls.is_empty() {
            self.server_msg("URL istorija tuščia");
        } else {
            for (i, u) in urls.iter().enumerate() {
                self.server_msg(&format!("  {}: {u}", i + 1));
            }
        }
    }

    fn cmd_who(&self, args: &str) {
        let mut target = String::new();
        if !args.is_empty() {
            target = first_field(args).to_string();
            if !is_valid_who_target(&target) {
                self.err_msg("Netinkamas WHO tikslas (kanalas, nick'as arba „*“)");
                return;
            }
        }
        if target.is_empty() {
            target = self.active_name();
        }
        if target.is_empty() {
            target = "*".into();
        }
        if self.is_channel(&target) {
            self.set_who_target(&target);
        } else {
            self.set_who_target("");
        }
        self.send(&format!("WHO {target}"));
    }

    fn cmd_window(&self, args: &str) {
        if args.is_empty() {
            let (rows, active) = {
                let st = self.mu.lock();
                let rows: Vec<(usize, String)> = st
                    .channels
                    .iter()
                    .enumerate()
                    .map(|(i, ch)| (i, ch.name.clone()))
                    .collect();
                (rows, st.active_idx)
            };
            for (idx, name) in rows {
                let marker = if idx == active { "> " } else { "  " };
                self.server_msg(&format!("{marker}{}: {name}", idx + 1));
            }
            return;
        }
        let w = first_field(args);
        if let Ok(n) = w.parse::<i64>() {
            if n > 0 {
                self.switch_ch((n as usize).saturating_sub(1));
            }
        }
    }

    fn cmd_close(&self, args: &str) {
        let (idx, ch_name) = {
            let mut st = self.mu.lock();
            let mut idx = st.active_idx;
            if !args.is_empty() {
                let needle = sanitize_text(args);
                for (i, ch) in st.channels.iter().enumerate() {
                    if fold_eq(&ch.name, &needle) {
                        idx = i;
                        break;
                    }
                }
            }
            if idx == 0 || idx >= st.channels.len() {
                return;
            }
            let ch_name = st.channels[idx].name.clone();
            st.remove_ch(idx);
            (idx, ch_name)
        };
        let _ = idx;
        if self.is_connected() && self.is_channel(&ch_name) {
            self.send(&format!("PART {ch_name} :Langas uždarytas"));
        }
        self.queue_redraw();
    }

    fn cmd_quit(&self, args: &str) {
        let msg = if args.is_empty() { "mircBSD" } else { args };
        if self.is_connected() {
            self.send(&format!("QUIT :{msg}"));
            self.finish_writes(Duration::from_millis(300));
        }
        self.stop();
    }

    fn cmd_keys(&self, args: &str) {
        if args.trim() == "debug" {
            self.mu.lock().keys_debug = true;
            self.server_msg("[KEYS] debug įjungtas. 'q' - išjungti");
            return;
        }
        for w in self.load_keys() {
            self.err_msg(&w);
        }
        self.show_key_bindings();
    }

    /// `/keys` listing (`showKeyBindings`).
    fn show_key_bindings(&self) {
        let binds = self.mu.lock().custom_binds.clone();
        if binds.is_empty() {
            let path = keys_path()
                .map(|p| p.display().to_string())
                .unwrap_or_default();
            self.server_msg(&format!(
                "Nėra perrašytų klavišų. Failas: {}",
                or_default(&path, "(nėra config katalogo)")
            ));
        } else {
            self.server_msg("— Perrašyti klavišai (configDir/keys) —");
            let mut pairs: Vec<(String, String)> = binds.into_iter().collect();
            pairs.sort();
            for name in key_action_names() {
                for (seq, act) in &pairs {
                    if *act == name {
                        self.server_msg(&format!("  {name} = {}", escape_key_seq(seq)));
                    }
                }
            }
        }
        self.server_msg(&format!(
            "Veiksmų vardai: {}",
            key_action_names().join(", ")
        ));
        self.server_msg(
            "Sekos sintaksė: \\e (ESC), \\xHH, \\n, \\r, \\t, \\\\ — pvz.: search = \\eS",
        );
    }

    /// `/exportjson` (`exportJSON`): messages of one window as indented JSON.
    fn export_json(&self, chan_name: &str) {
        let (log_dir, found) = {
            let st = self.mu.lock();
            if st.log_dir.is_empty() {
                (String::new(), None)
            } else {
                let found = st
                    .channels
                    .iter()
                    .find(|ch| self.nick_equal(&ch.name, chan_name))
                    .map(|ch| {
                        (
                            ch.messages.clone(),
                            sanitize_path(&ch.name),
                            st.server.clone(),
                            st.log_dir.clone(),
                        )
                    });
                (st.log_dir.clone(), found)
            }
        };
        if log_dir.is_empty() {
            self.err_msg("Nėra log katalogo (logdir)");
            return;
        }
        let Some((mut msgs, name, srv, dir_root)) = found else {
            self.err_msg(&format!("Langas „{}“ nerastas", sanitize_text(chan_name)));
            return;
        };
        if self.nick_equal(chan_name, "NickServ") {
            for msg in &mut msgs {
                if is_sensitive_history(&crate::ui::strip_irc_format(&msg.text)) {
                    msg.text = "[slaptažodis paslėptas]".into();
                }
            }
        }
        let dir = if srv.is_empty() {
            std::path::PathBuf::from(&dir_root)
        } else {
            std::path::PathBuf::from(&dir_root).join(sanitize_path(&srv))
        };
        if let Err(e) = std::fs::create_dir_all(&dir) {
            self.err_msg(&format!("Nepavyko sukurti katalogo: {e}"));
            return;
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o700));
        }
        let date = chrono::Local::now().format("%Y-%m-%d");
        let path = dir.join(format!("{name}-{date}.json"));
        let buf = messages_to_json_indent(&msgs);
        if let Err(e) = write_private(&path, buf.as_bytes()) {
            self.err_msg(&format!("Nebepavyko išsaugoti: {e}"));
            return;
        }
        self.server_msg(&format!(
            "Eksportas: {} žinučių → {}",
            msgs.len(),
            path.display()
        ));
    }
}

fn write_private(path: &std::path::Path, data: &[u8]) -> std::io::Result<()> {
    crate::persist::atomic_write(path, data)
}

/// `/help` text: grouped by topic, two aligned columns. Keep in sync with
/// [`Client::handle_cmd`] and README.md (tests check that every dispatched
/// command is listed).
fn help_lines() -> Vec<String> {
    const SECTIONS: &[(&str, &[(&str, &str)])] = &[
        (
            "Ryšys",
            &[
                (
                    "/connect <srv> <port> [nick] [tls]",
                    "prisijungti (dar: /server)",
                ),
                ("/reconnect", "jungtis iš naujo"),
                ("/nick <vardas>", "keisti slapyvardį"),
                ("/away [priežastis]  /back", "pasitraukęs / grįžau"),
                ("/quit [žinutė]", "išeiti (dar: /exit, /q)"),
            ],
        ),
        (
            "Kanalai ir langai",
            &[
                ("/join #kanalas [raktas]", "įeiti į kanalą (/j)"),
                ("/part [priežastis]", "išeiti iš kanalo (/leave)"),
                ("/cycle [priežastis]", "išeiti ir vėl įeiti"),
                ("/close [langas]", "uždaryti langą (+ PART; /wc)"),
                ("/window [n]", "langų sąrašas / perjungti (/win)"),
                ("/topic [tema]", "rodyti / keisti temą"),
                ("/names", "atnaujinti nick sąrašą"),
                ("/list [filtras]", "serverio kanalų sąrašas"),
                ("/invite <nick> [#kanalas]", "pakviesti į kanalą"),
            ],
        ),
        (
            "Žinutės",
            &[
                ("/msg <nick|#kanalas> <tekstas>", "žinutė (/m)"),
                ("/query <nick>", "atidaryti privatų langą"),
                ("/me <veiksmas>", "veiksmas (* nick ...)"),
                ("/notice <nick> <tekstas>", "NOTICE žinutė"),
                ("/clear", "išvalyti lango istoriją ekrane"),
            ],
        ),
        (
            "Vartotojai ir moderavimas",
            &[
                ("/whois <nick>", "informacija apie vartotoją (/wi)"),
                ("/w [nick|#kanalas]", "WHO užklausa"),
                (
                    "/ignore <nick|maskė>",
                    "ignoruoti (nick!user@host, *!*@host)",
                ),
                ("/unignore <nick|maskė>  /ignores", "atšaukti / sąrašas"),
                (
                    "/notify add|del|list [nick]",
                    "stebėti prisijungimą (MONITOR)",
                ),
                ("/kick <nick> [priežastis]", "išmesti iš kanalo (/k)"),
                (
                    "/op /deop /voice /devoice <nick>",
                    "suteikti / atimti teises",
                ),
                ("/mode [taikinys] <+/-režimai> [arg]", "nustatyti režimus"),
            ],
        ),
        (
            "Paieška, URL ir žurnalai",
            &[
                ("/search [žodis]", "paieška lange (Ctrl+R); n / p — kitas"),
                ("/search all <žodis>", "paieška visuose languose"),
                ("/logsearch [kanalas] <žodis>", "paieška žurnalų failuose"),
                ("/urls  /open [n]", "URL istorija / atidaryti naršyklėje"),
                ("/exportjson [kanalas]", "eksportuoti langą į JSON"),
                ("/clearlogs [kanalas]", "ištrinti šio serverio žurnalus"),
            ],
        ),
        (
            "Nustatymai ir kita",
            &[
                ("/set [raktas] [reikšmė]", "nustatymai (įrašomi į config)"),
                ("/keys [debug]", "klavišų perrašymai (keys failas)"),
                ("/time", "serverio laikas"),
                ("/raw <IRC eilutė>", "siųsti tiesiai serveriui"),
                ("/help", "šis sąrašas"),
            ],
        ),
        (
            "Klavišai",
            &[
                ("PgUp / PgDn, pelės ratukas", "slinkti istoriją"),
                ("Shift+PgDn", "grįžti į apačią"),
                ("↑ / ↓", "komandų istorija (tuščia eilutė: slinkti)"),
                ("Ctrl+N / Ctrl+P", "kitas / ankstesnis langas"),
                ("Alt+1…9, F1…F12", "langas pagal numerį"),
                ("Tab", "užbaigti nick, komandą, kanalą"),
                ("Ctrl+R", "paieška"),
                ("Ctrl+←/→, Ctrl+W", "per žodį / ištrinti žodį"),
                ("Ctrl+A / Ctrl+E, Home / End", "eilutės pradžia / pabaiga"),
                ("Ctrl+U / Ctrl+K", "ištrinti eilutę / iki galo"),
                ("Ctrl+↑ / Ctrl+↓", "slinkti nick sąrašą"),
                ("Ctrl+L", "perpiešti ekraną"),
            ],
        ),
    ];
    const COL: usize = 36;
    let mut lines = vec![format!(
        "══ mircBSD v{} — pagalba ══",
        crate::client::VERSION
    )];
    for (title, rows) in SECTIONS {
        lines.push(format!("── {title} ──"));
        for (cmd, what) in rows.iter() {
            let pad = COL.saturating_sub(cmd.chars().count()).max(1);
            lines.push(format!("  {cmd}{}{what}", " ".repeat(pad)));
        }
    }
    lines.push("Daugiau: README.md · nustatymų sąrašas: /set · langų sąrašas: /window".to_string());
    lines
}

#[cfg(test)]
#[path = "commands_tests.rs"]
mod tests;
