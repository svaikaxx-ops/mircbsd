//! Key=value configuration and key bindings.

use std::collections::HashMap;
use std::fs::File;
use std::io::{self, BufRead, BufReader};
use std::path::{Path, PathBuf};
use std::sync::OnceLock;

use crate::persist::atomic_write;
use crate::validate::{is_valid_nick, is_valid_port};

/// IRC client configuration (mirrors Go `Config`).
#[derive(Clone, Debug, Default)]
pub struct Config {
    pub server: String,
    pub port: String,
    pub tls: bool,
    pub nick: String,
    pub real_name: String,
    pub nick_serv: String,
    pub sasl_pass: String,
    pub server_pass: String,
    pub auto_join: Vec<String>,
    pub perform: Vec<String>,
    pub highlight_words: Vec<String>,
    pub log_dir: String,
    pub max_messages: i32,
    pub reconnect: bool,
    pub ctcp_version: bool,
    pub notifications: bool,
    pub mouse: bool,
    pub timestamp_color: String,
    pub timestamps: bool,
    pub ts_format: String,
    pub highlight_color: String,
    pub nick_colors: bool,
    pub tls_cert: String,
    pub tls_key: String,
    pub sasl_external: bool,
    pub tls_pinning: bool,
    pub tls_pin: String,
    pub theme: String,
}

fn home_dir() -> PathBuf {
    std::env::var("HOME")
        .ok()
        .filter(|h| !h.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
}

pub fn default_config() -> Config {
    let home = home_dir();
    Config {
        port: "6667".into(),
        nick: "mircbsd".into(),
        real_name: "mircBSD IRC Client".into(),
        log_dir: home.join("irclogs").to_string_lossy().into_owned(),
        max_messages: 5000,
        reconnect: true,
        ctcp_version: true,
        notifications: true,
        mouse: true,
        timestamps: true,
        ts_format: "15:04".into(),
        nick_colors: true,
        ..Config::default()
    }
}

fn parse_config_bool(v: &str) -> bool {
    matches!(v, "true" | "1" | "yes")
}

fn strip_surrounding_quotes(v: &str) -> &str {
    let b = v.as_bytes();
    if b.len() >= 2
        && ((b[0] == b'"' && b[b.len() - 1] == b'"') || (b[0] == b'\'' && b[b.len() - 1] == b'\''))
    {
        &v[1..b.len() - 1]
    } else {
        v
    }
}

/// Load config from `path`; missing file yields defaults. Validation warnings are returned.
pub fn load_config(path: &Path) -> (Config, Vec<String>) {
    let mut warnings = Vec::new();
    let mut cfg = default_config();

    let file = match File::open(path) {
        Ok(f) => f,
        Err(_) => return (cfg, warnings),
    };

    let reader = BufReader::new(file);
    for line in reader.lines().flatten() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let Some((k, v)) = line.split_once('=') else {
            continue;
        };
        let k = k.trim();
        let v = strip_surrounding_quotes(v.trim());
        let key = k.to_ascii_lowercase();

        match key.as_str() {
            "server" => cfg.server = v.to_string(),
            "port" => {
                if is_valid_port(v) {
                    cfg.port = v.to_string();
                } else {
                    cfg.port = "6667".into();
                    warnings.push(format!("Neteisingas portas: {v} (numatytas 6667)"));
                }
            }
            "tls" => cfg.tls = parse_config_bool(v),
            "nick" => {
                if is_valid_nick(v) {
                    cfg.nick = v.to_string();
                } else {
                    warnings.push(format!("Neteisingas nick: {v} (numatytas mircbsd)"));
                }
            }
            "realname" => cfg.real_name = v.to_string(),
            "nickserv" => cfg.nick_serv = v.to_string(),
            "password" => cfg.server_pass = v.to_string(),
            "autojoin" => {
                for ch in v.split(',') {
                    let ch = ch.trim();
                    if !ch.is_empty() {
                        cfg.auto_join.push(ch.to_string());
                    }
                }
            }
            "logdir" => {
                let mut logdir = v.to_string();
                if let Some(rest) = logdir.strip_prefix("~/") {
                    logdir = home_dir().join(rest).to_string_lossy().into_owned();
                }
                cfg.log_dir = logdir;
            }
            "maxmessages" => {
                if let Ok(n) = v.parse::<i32>() {
                    if n > 0 {
                        cfg.max_messages = n;
                    }
                }
            }
            "reconnect" => cfg.reconnect = parse_config_bool(v),
            "ctcpversion" => cfg.ctcp_version = parse_config_bool(v),
            "notifications" => cfg.notifications = parse_config_bool(v),
            "mouse" => cfg.mouse = parse_config_bool(v),
            "tscolor" => cfg.timestamp_color = v.to_string(),
            "timestamps" => cfg.timestamps = parse_config_bool(v),
            "ts_format" => cfg.ts_format = v.to_string(),
            "highlight_color" => cfg.highlight_color = v.to_string(),
            "nick_colors" => cfg.nick_colors = parse_config_bool(v),
            "sasl" => cfg.sasl_pass = v.to_string(),
            "perform" => {
                for cmd in v.split(';') {
                    let cmd = cmd.trim();
                    if !cmd.is_empty() {
                        cfg.perform.push(cmd.to_string());
                    }
                }
            }
            "highlight_words" => {
                for w in v.split(',') {
                    let w = w.trim();
                    if !w.is_empty() {
                        cfg.highlight_words.push(w.to_string());
                    }
                }
            }
            "tls_cert" => cfg.tls_cert = expand_home_path(v),
            "tls_key" => cfg.tls_key = expand_home_path(v),
            "sasl_external" => cfg.sasl_external = parse_config_bool(v),
            "tls_pinning" => cfg.tls_pinning = parse_config_bool(v),
            "tls_pin" => cfg.tls_pin = v.trim().to_ascii_lowercase(),
            "theme" => cfg.theme = v.to_ascii_lowercase(),
            _ => {}
        }
    }

    (cfg, warnings)
}

fn user_config_dir() -> Option<PathBuf> {
    if let Ok(xdg) = std::env::var("XDG_CONFIG_HOME") {
        if !xdg.is_empty() {
            return Some(PathBuf::from(xdg));
        }
    }
    std::env::var("HOME")
        .ok()
        .filter(|h| !h.is_empty())
        .map(|h| PathBuf::from(h).join(".config"))
}

/// `$XDG_CONFIG_HOME/mircbsd` or `$HOME/.config/mircbsd`.
pub fn config_dir() -> Option<PathBuf> {
    user_config_dir().map(|d| d.join("mircbsd"))
}

pub fn config_path() -> Option<PathBuf> {
    config_dir().map(|d| d.join("config"))
}

pub fn history_path() -> Option<PathBuf> {
    config_dir().map(|d| d.join("history"))
}

pub fn ignore_path() -> Option<PathBuf> {
    config_dir().map(|d| d.join("ignore"))
}

pub fn monitor_path() -> Option<PathBuf> {
    config_dir().map(|d| d.join("notify"))
}

pub fn keys_path() -> Option<PathBuf> {
    config_dir().map(|d| d.join("keys"))
}

/// Expand `~/…` using `$HOME`.
pub fn expand_home_path(v: &str) -> String {
    if let Some(rest) = v.strip_prefix("~/") {
        if let Ok(home) = std::env::var("HOME") {
            if !home.is_empty() {
                return PathBuf::from(home)
                    .join(rest)
                    .to_string_lossy()
                    .into_owned();
            }
        }
    }
    v.to_string()
}

static KEY_ACTIONS: OnceLock<HashMap<&'static str, &'static [u8]>> = OnceLock::new();

/// Canonical control sequences for remappable actions.
pub fn key_actions() -> &'static HashMap<&'static str, &'static [u8]> {
    KEY_ACTIONS.get_or_init(|| {
        HashMap::from([
            ("chan-next", &[0x0e][..]),
            ("chan-prev", &[0x10][..]),
            ("nick-up", &[0x1b, b'[', b'1', b';', b'5', b'A'][..]),
            ("nick-down", &[0x1b, b'[', b'1', b';', b'5', b'B'][..]),
            ("word-left", &[0x1b, b'[', b'1', b';', b'5', b'D'][..]),
            ("word-right", &[0x1b, b'[', b'1', b';', b'5', b'C'][..]),
            ("msg-up", &[0x1b, b'[', b'5', b'~'][..]),
            ("msg-down", &[0x1b, b'[', b'6', b'~'][..]),
            ("msg-bottom", &[0x1b, b'[', b'6', b';', b'2', b'~'][..]),
            ("search", &[0x12][..]),
            ("hist-up", &[0x1b, b'[', b'A'][..]),
            ("hist-down", &[0x1b, b'[', b'B'][..]),
            ("complete", &[b'\t'][..]),
            ("word-del", &[23][..]),
        ])
    })
}

pub fn key_action_names() -> Vec<String> {
    let mut names: Vec<String> = key_actions().keys().map(|s| s.to_string()).collect();
    names.sort();
    names
}

fn go_string_bytes(seq: &[u8]) -> String {
    // Go `string([]byte)` — arbitrary bytes as string key.
    String::from_utf8_lossy(seq).into_owned()
}

/// Parse keys file escape syntax (`\e`, `\xHH`, `\n`, …).
pub fn unescape_key_seq(s: &str) -> Result<Vec<u8>, String> {
    let bytes = s.as_bytes();
    let mut out = Vec::new();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] != b'\\' {
            out.push(bytes[i]);
            i += 1;
            continue;
        }
        if i + 1 >= bytes.len() {
            return Err("pabaigos „\\“ be simbolio".into());
        }
        match bytes[i + 1] {
            b'e' | b'E' => {
                out.push(0x1b);
                i += 2;
            }
            b'n' => {
                out.push(b'\n');
                i += 2;
            }
            b'r' => {
                out.push(b'\r');
                i += 2;
            }
            b't' => {
                out.push(b'\t');
                i += 2;
            }
            b'\\' => {
                out.push(b'\\');
                i += 2;
            }
            b'x' => {
                if i + 4 <= bytes.len() {
                    let hex = &s[i + 2..i + 4];
                    let v = u8::from_str_radix(hex, 16)
                        .map_err(|_| format!("nevalidus \\xXX: \\x{hex}"))?;
                    out.push(v);
                    i += 4;
                } else {
                    return Err("\\x be dvejetainių reikšmių".into());
                }
            }
            ch => {
                return Err(format!("nežinoma seka: \\{}", ch as char));
            }
        }
    }
    Ok(out)
}

/// Read `action = seq` bindings; map key is the byte sequence (Go `string(seq)`).
pub fn parse_keys_file(path: &Path) -> (HashMap<String, String>, Vec<String>) {
    let mut binds = HashMap::new();
    let mut warns = Vec::new();

    let file = match File::open(path) {
        Ok(f) => f,
        Err(_) => return (binds, warns),
    };

    let reader = BufReader::new(file);
    let mut line_no = 0u32;
    for line in reader.lines() {
        line_no += 1;
        let Ok(line) = line else { continue };
        let trimmed = line.trim();
        if trimmed.is_empty() || trimmed.starts_with('#') {
            continue;
        }
        let Some((action_part, seq_part)) = trimmed.split_once('=') else {
            warns.push(format!("keys:{line_no}: nėra „=“"));
            continue;
        };
        let action = action_part.trim().to_ascii_lowercase();
        let seq = match unescape_key_seq(seq_part.trim()) {
            Ok(s) => s,
            Err(e) => {
                warns.push(format!("keys:{line_no}: {e}"));
                continue;
            }
        };
        if seq.is_empty() || seq.len() > 16 {
            warns.push(format!("keys:{line_no}: seka per ilga (maks. 16 baitų)"));
            continue;
        }
        if !key_actions().contains_key(action.as_str()) {
            let known = key_action_names().join(", ");
            warns.push(format!(
                "keys:{line_no}: nežinomas veiksmas „{action}“ (žinomi: {known})"
            ));
            continue;
        }
        let seq_key = go_string_bytes(&seq);
        if binds.contains_key(&seq_key) {
            warns.push(format!("keys:{line_no}: seka jau perrašyta kitu veiksmu"));
            continue;
        }
        binds.insert(seq_key, action);
    }

    (binds, warns)
}

pub const SETTABLE_KEYS: &[&str] = &[
    "mouse",
    "notifications",
    "ctcpversion",
    "reconnect",
    "timestamps",
    "ts_format",
    "tscolor",
    "highlight_color",
    "nick_colors",
    "maxmessages",
    "theme",
];

pub fn is_settable(key: &str) -> bool {
    let low = key.to_ascii_lowercase();
    SETTABLE_KEYS.iter().any(|k| *k == low)
}

/// `/set` — update one key in the config file (atomic, 0600).
pub fn save_config_setting(key: &str, value: &str) -> Result<(), String> {
    let path =
        config_path().ok_or_else(|| "nepavyko nustatyti konfigūracijos kelio".to_string())?;
    let key_low = key.to_ascii_lowercase();

    let mut lines: Vec<String> = Vec::new();
    let mut replaced = false;

    match std::fs::read(&path) {
        Ok(b) => {
            let content = String::from_utf8_lossy(&b);
            for line in content.lines() {
                let mut write_line = line.to_string();
                let trimmed = line.trim();
                if !trimmed.starts_with('#') {
                    if let Some((k_part, _)) = trimmed.split_once('=') {
                        if k_part.trim().eq_ignore_ascii_case(&key_low) {
                            if let Some(eq_pos) = line.find('=') {
                                write_line = format!("{}{value}", &line[..=eq_pos]);
                                replaced = true;
                            }
                        }
                    }
                }
                lines.push(write_line);
            }
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => {}
        Err(e) => {
            return Err(format!("config neperskaitytas, nekeičiamas: {e}"));
        }
    }

    if !replaced {
        lines.push(format!("{key}={value}"));
    }

    let mut out = lines.join("\n");
    if !out.is_empty() {
        out.push('\n');
    }

    atomic_write(&path, out.as_bytes()).map_err(|e| e.to_string())
}

/// Recognized boolean tokens for `/set`; unknown words return `(false, false)`.
pub fn parse_bool_value(v: &str) -> (bool, bool) {
    match v.trim().to_ascii_lowercase().as_str() {
        "true" | "1" | "yes" | "on" => (true, true),
        "false" | "0" | "no" | "off" => (false, true),
        _ => (false, false),
    }
}
