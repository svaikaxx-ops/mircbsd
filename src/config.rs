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

/// `$HOME` (netuščias). Testuose — izoliuotas laikinas katalogas, kad
/// testai NIEKADA neliestų tikro `~/.config/mircbsd` ar `~/irclogs`.
fn env_home() -> Option<String> {
    #[cfg(test)]
    {
        Some(test_home().to_string_lossy().into_owned())
    }
    #[cfg(not(test))]
    {
        std::env::var("HOME").ok().filter(|h| !h.is_empty())
    }
}

/// Testų „namų“ katalogas: `$TMPDIR/mircbsd-test-home-<pid>` (0700).
/// Visi `config_dir()`/`history_path()`/numatytas `log_dir` testuose
/// rodo čia, nepriklausomai nuo `HOME`/`XDG_CONFIG_HOME`.
#[cfg(test)]
pub fn test_home() -> PathBuf {
    static DIR: OnceLock<PathBuf> = OnceLock::new();
    DIR.get_or_init(|| {
        let d = std::env::temp_dir().join(format!("mircbsd-test-home-{}", std::process::id()));
        let _ = std::fs::create_dir_all(&d);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(&d, std::fs::Permissions::from_mode(0o700));
        }
        d
    })
    .clone()
}

fn home_dir() -> PathBuf {
    env_home()
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

/// Config failo bool: tos pačios reikšmės kaip `/set` (true/false/1/0/yes/
/// no/on/off, be raidžių dydžio). Anksčiau „True“ ar „on“ tyliai reiškė
/// false — `tls=True` jungdavosi BE TLS. Neatpažinta reikšmė palieka
/// numatytąją ir duoda įspėjimą.
fn parse_config_bool(key: &str, v: &str, current: bool, warnings: &mut Vec<String>) -> bool {
    match parse_bool_value(v) {
        (b, true) => b,
        _ => {
            warnings.push(format!(
                "Neteisinga {key} reikšmė: {v} (laukta true/false; paliekama {current})"
            ));
            current
        }
    }
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
    #[cfg(unix)]
    let readable_by_others = {
        use std::os::unix::fs::PermissionsExt;
        file.metadata()
            .map(|m| m.permissions().mode() & 0o077 != 0)
            .unwrap_or(false)
    };

    let reader = BufReader::new(file);
    for line in reader.lines() {
        let line = match line {
            Ok(line) => line,
            Err(err) => {
                warnings.push(format!("Nepavyko perskaityti konfigūracijos: {err}"));
                break;
            }
        };
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
            "server" => {
                if v.is_empty() || crate::validate::is_valid_server(v) {
                    cfg.server = v.to_string();
                } else {
                    // Anksčiau Client::new tyliai išvalydavo serverį, o pasveikinimas
                    // vis tiek rodė „Serveris: …“ ir jungimasis eidavo į „:portas“.
                    cfg.server.clear();
                    warnings.push(format!("Neteisingas serverio vardas: {v}"));
                }
            }
            "port" => {
                if is_valid_port(v) {
                    cfg.port = v.to_string();
                } else {
                    cfg.port = "6667".into();
                    warnings.push(format!("Neteisingas portas: {v} (numatytas 6667)"));
                }
            }
            "tls" => cfg.tls = parse_config_bool(&key, v, cfg.tls, &mut warnings),
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
            "reconnect" => cfg.reconnect = parse_config_bool(&key, v, cfg.reconnect, &mut warnings),
            "ctcpversion" => {
                cfg.ctcp_version = parse_config_bool(&key, v, cfg.ctcp_version, &mut warnings)
            }
            "notifications" => {
                cfg.notifications = parse_config_bool(&key, v, cfg.notifications, &mut warnings)
            }
            "mouse" => cfg.mouse = parse_config_bool(&key, v, cfg.mouse, &mut warnings),
            "tscolor" => cfg.timestamp_color = v.to_string(),
            "timestamps" => {
                cfg.timestamps = parse_config_bool(&key, v, cfg.timestamps, &mut warnings)
            }
            "ts_format" => cfg.ts_format = v.to_string(),
            "highlight_color" => cfg.highlight_color = v.to_string(),
            "nick_colors" => {
                cfg.nick_colors = parse_config_bool(&key, v, cfg.nick_colors, &mut warnings)
            }
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
            "sasl_external" => {
                cfg.sasl_external = parse_config_bool(&key, v, cfg.sasl_external, &mut warnings)
            }
            "tls_pinning" => {
                cfg.tls_pinning = parse_config_bool(&key, v, cfg.tls_pinning, &mut warnings)
            }
            "tls_pin" => cfg.tls_pin = v.trim().to_ascii_lowercase(),
            "theme" => cfg.theme = v.to_ascii_lowercase(),
            _ => {}
        }
    }

    // Passwords in a group/world-readable file: tell the user (the file is
    // only ever rewritten 0600 by `/set`, never chmod-ed behind their back).
    #[cfg(unix)]
    if readable_by_others
        && (!cfg.server_pass.is_empty() || !cfg.sasl_pass.is_empty() || !cfg.nick_serv.is_empty())
    {
        warnings.push(format!(
            "{} su slaptažodžiais skaitomas kitiems naudotojams — chmod 600 {}",
            path.display(),
            path.display()
        ));
    }

    (cfg, warnings)
}

fn user_config_dir() -> Option<PathBuf> {
    // Testuose XDG_CONFIG_HOME ignoruojamas — tik izoliuotas test_home().
    #[cfg(not(test))]
    if let Ok(xdg) = std::env::var("XDG_CONFIG_HOME") {
        if !xdg.is_empty() {
            return Some(PathBuf::from(xdg));
        }
    }
    env_home().map(|h| PathBuf::from(h).join(".config"))
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
        if let Some(home) = env_home() {
            return PathBuf::from(home)
                .join(rest)
                .to_string_lossy()
                .into_owned();
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
            ("complete", &b"\t"[..]),
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
                    // Bytes, not `&s[..]`: a non-ASCII letter after `\x` is not
                    // a char boundary and slicing the str would panic.
                    let hex_bytes = &bytes[i + 2..i + 4];
                    let hex = String::from_utf8_lossy(hex_bytes);
                    if !hex_bytes.iter().all(u8::is_ascii_hexdigit) {
                        return Err(format!("nevalidus \\xXX: \\x{hex}"));
                    }
                    let v = u8::from_str_radix(&hex, 16)
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

#[cfg(test)]
#[path = "config_tests.rs"]
mod more_tests;

#[cfg(test)]
mod tests {
    use super::{load_config, unescape_key_seq};
    use std::io::Write;

    #[test]
    fn config_read_error_reports_warning_and_stops() {
        let path = std::env::temp_dir().join(format!(
            "mircbsd-config-{}-{:?}-{}",
            std::process::id(),
            std::thread::current().id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .expect("system clock")
                .as_nanos()
        ));
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&path)
            .expect("create test config");
        file.write_all(b"nick=first\n\xff\nnick=second\n")
            .expect("write test config");
        drop(file);

        let (config, warnings) = load_config(&path);
        std::fs::remove_file(&path).expect("remove test config");

        assert_eq!(config.nick, "first");
        assert!(warnings
            .iter()
            .any(|warning| warning.contains("perskaityti")));
    }

    #[cfg(unix)]
    #[test]
    fn readable_config_with_passwords_warns() {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!(
            "mircbsd-config-perm-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        let secret = dir.join("secret");
        std::fs::write(&secret, "nick=a\nsasl=hunter2\n").unwrap();
        std::fs::set_permissions(&secret, std::fs::Permissions::from_mode(0o644)).unwrap();
        let (_, warns) = load_config(&secret);
        assert!(warns.iter().any(|w| w.contains("chmod 600")), "{warns:?}");

        // 0600, or no passwords at all: no warning.
        std::fs::set_permissions(&secret, std::fs::Permissions::from_mode(0o600)).unwrap();
        assert!(load_config(&secret).1.is_empty());
        let plain = dir.join("plain");
        std::fs::write(&plain, "nick=a\n").unwrap();
        std::fs::set_permissions(&plain, std::fs::Permissions::from_mode(0o644)).unwrap();
        assert!(load_config(&plain).1.is_empty());
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn key_escape_with_non_ascii_after_x_is_an_error_not_a_panic() {
        // `\xaé` sliced a str in the middle of "é" → panic at startup / `/keys`.
        for bad in ["\\xaé", "\\xé", "\\xžž", "\\x"] {
            assert!(unescape_key_seq(bad).is_err(), "{bad:?}");
        }
        assert_eq!(unescape_key_seq("\\x1bé").unwrap(), "\x1bé".as_bytes());
    }

    fn load_str(text: &str) -> (super::Config, Vec<String>) {
        let path = std::env::temp_dir().join(format!(
            "mircbsd-config-b-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        std::fs::write(&path, text).expect("write test config");
        let out = load_config(&path);
        std::fs::remove_file(&path).expect("remove test config");
        out
    }

    #[test]
    fn config_bools_match_set_syntax() {
        let (c, w) = load_str("tls=True\nmouse=off\nreconnect=ON\ntimestamps=No\n");
        assert!(c.tls, "tls=True turi įjungti TLS");
        assert!(!c.mouse && c.reconnect && !c.timestamps);
        assert!(w.is_empty(), "{w:?}");
        // Neatpažinta reikšmė — įspėjimas, numatytoji lieka.
        let (c, w) = load_str("reconnect=taip\n");
        assert!(c.reconnect);
        assert!(w.iter().any(|x| x.contains("reconnect")), "{w:?}");
    }

    #[test]
    fn invalid_server_in_config_warns() {
        let (c, w) = load_str("server=bad server!\n");
        assert!(c.server.is_empty());
        assert!(w.iter().any(|x| x.contains("serverio")), "{w:?}");
        let (c, w) = load_str("server=irc.libera.chat\n");
        assert_eq!(c.server, "irc.libera.chat");
        assert!(w.is_empty());
    }
}
