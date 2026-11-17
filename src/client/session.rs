//! Session lifecycle and persistence helpers: shutdown (`stop`), history /
//! ignore / monitor / keys loading and saving, `/clearlogs`, render-cache
//! invalidation, PRIVMSG splitting and the URL opener.
//!
//! Go equivalents: `main.go` (`stop`, `openURL`), `config.go`
//! (`saveHistory`, `loadIgnore`, `saveIgnore`, `loadKeys`), `irc.go`
//! (`clearLogs`, `splitForPrivmsg`), `ui.go` (`invalidateRenderCaches`).

use std::collections::HashMap;
use std::io::Write;
use std::path::Path;
use std::process::{Command, Stdio};
use std::sync::atomic::Ordering;
use std::time::{Duration, Instant};

use crate::config::{history_path, ignore_path, keys_path, monitor_path, parse_keys_file};
use crate::persist;
use crate::sanitize::{is_safe_url, sanitize_path};

use super::types::Client;
use super::MAX_LINE_BYTES;

/// Escape sequence that turns SGR 1006 mouse reporting off.
pub const MOUSE_OFF: &str = "\x1b[?1002l\x1b[?1000l\x1b[?1006l";
/// Escape sequence that turns SGR 1006 mouse reporting on.
pub const MOUSE_ON: &str = "\x1b[?1000h\x1b[?1002h\x1b[?1006h";

/// Write raw terminal escape sequences to stdout (best effort).
pub fn term_write(s: &str) {
    let mut out = std::io::stdout();
    let _ = out.write_all(s.as_bytes());
    let _ = out.flush();
}

impl Client {
    // ---- shutdown ----

    /// Begin shutdown (`stop`): idempotent. Turns the mouse mode off, saves
    /// the history, runs the `on_stop` hook (log worker drain), then wakes
    /// every `wait_stop` caller and the UI loop.
    pub fn stop(&self) {
        if self.stop_once.swap(true, Ordering::SeqCst) {
            return;
        }
        let mouse = self.mu.lock().mouse;
        if mouse {
            term_write(MOUSE_OFF);
        }
        self.save_history();
        let hook = self.hooks.read().on_stop.clone();
        if let Some(h) = hook {
            h();
        }
        *self.stop_done.lock() = true;
        self.stop_cv.notify_all();
        // Wake the draw loop so it notices the stop request.
        self.queue_redraw();
    }

    /// Has `stop` completed?
    pub fn is_stopped(&self) -> bool {
        *self.stop_done.lock()
    }

    /// Block until `stop` has completed (`<-stopCh`). `None` waits forever.
    /// Returns whether the client is stopped.
    pub fn wait_stop(&self, timeout: Option<Duration>) -> bool {
        let deadline = timeout.map(|t| Instant::now() + t);
        let mut done = self.stop_done.lock();
        while !*done {
            match deadline {
                Some(d) => {
                    if self.stop_cv.wait_until(&mut done, d).timed_out() {
                        break;
                    }
                }
                None => self.stop_cv.wait(&mut done),
            }
        }
        *done
    }

    // ---- persistence ----

    /// Last 1000 history lines to the history file (atomic, 0600).
    pub fn save_history(&self) {
        let Some(path) = history_path() else { return };
        let lines = self.mu.lock().history.clone();
        if lines.is_empty() {
            // Nothing to save; never replace an existing file with an empty one.
            return;
        }
        if let Err(err) = persist::save_history(&path, &lines) {
            self.err_msg(&format!("Istorija neišsaugota ({}): {err}", path.display()));
        }
    }

    /// Load the history file into memory.
    pub fn load_history(&self) {
        let Some(path) = history_path() else { return };
        match persist::load_history(&path) {
            Ok(lines) => self.mu.lock().history = lines,
            Err(err) => self.err_msg(&format!(
                "Istorija neperskaityta ({}): {err}",
                path.display()
            )),
        }
    }

    /// Persist the ignore list.
    pub fn save_ignore(&self) {
        let Some(path) = ignore_path() else { return };
        let list = self.mu.lock().ignore_list.clone();
        if let Err(err) = persist::save_ignore(&path, &list) {
            self.err_msg(&format!(
                "Ignoravimo sąrašas neišsaugotas ({}): {err}",
                path.display()
            ));
        }
    }

    /// Load the ignore list.
    pub fn load_ignore(&self) {
        let Some(path) = ignore_path() else { return };
        match persist::load_ignore(&path) {
            Ok(list) => self.mu.lock().ignore_list = list,
            Err(err) => self.err_msg(&format!(
                "Ignoravimo sąrašas neperskaitytas ({}): {err}",
                path.display()
            )),
        }
    }

    /// Load the MONITOR (/notify) list.
    pub fn load_monitor(&self) {
        let Some(path) = monitor_path() else { return };
        match persist::load_monitor(&path) {
            Ok(list) => self.mu.lock().monitor_list = list,
            Err(err) => self.err_msg(&format!(
                "Stebėjimo sąrašas neperskaitytas ({}): {err}",
                path.display()
            )),
        }
    }

    /// (Re)read the `keys` file into `custom_binds`; returns its warnings
    /// (`loadKeys`).
    pub fn load_keys(&self) -> Vec<String> {
        let (binds, warns) = match keys_path() {
            Some(p) => parse_keys_file(&p),
            None => (HashMap::new(), Vec::new()),
        };
        self.mu.lock().custom_binds = binds;
        warns
    }

    // ---- rendering ----

    /// After a theme / colour / timestamp change: cached lines carry the OLD
    /// SGR codes, so drop every channel's render cache and redraw
    /// (`invalidateRenderCaches`).
    pub fn invalidate_render_caches(&self) {
        {
            let mut st = self.mu.lock();
            crate::ui::invalidate_render_caches(st.channels.iter_mut());
        }
        self.queue_redraw();
    }

    // ---- logs ----

    /// `/clearlogs`: delete log files of the current server — all of them
    /// (`chan_name` empty) or only one channel's. Returns the number of
    /// removed files (`clearLogs`).
    pub fn clear_logs(&self, chan_name: &str) -> usize {
        let (dir, server) = {
            let st = self.mu.lock();
            (st.log_dir.clone(), st.server.clone())
        };
        // Let the log writer close its handles first (no writes into deleted files).
        let hook = self.hooks.read().close_logs.clone();
        if let Some(h) = hook {
            h(&server, chan_name);
        }
        if dir.is_empty() || server.is_empty() {
            return 0;
        }
        let srv_dir = Path::new(&dir).join(sanitize_path(&server));
        let Ok(entries) = std::fs::read_dir(&srv_dir) else {
            return 0;
        };
        let file_prefix = sanitize_path(chan_name); // empty = every file of this server
        let mut removed = 0;
        for e in entries.flatten() {
            if !e.file_type().map(|t| t.is_file()).unwrap_or(false) {
                continue;
            }
            let name = e.file_name().to_string_lossy().into_owned();
            let matches = if file_prefix.is_empty() {
                name.strip_suffix(".log")
                    .is_some_and(|stem| !stem.is_empty())
            } else {
                is_channel_log_file(&name, &file_prefix)
            };
            if !matches {
                continue;
            }
            if std::fs::remove_file(e.path()).is_ok() {
                removed += 1;
            }
        }
        removed
    }
}

/// Is `name` EXACTLY the log file of channel `chan_prefix` (sanitized)?
///
/// File names are `<CHANNEL>-YYYY-MM-DD.log` (and the old `<CHANNEL>.log`).
/// The date must be a real `YYYY-MM-DD`, so `#k-2` is never mistaken for
/// `#k` and older years are matched too (`isChannelLogFile`).
pub fn is_channel_log_file(name: &str, chan_prefix: &str) -> bool {
    if name.strip_suffix(".log") == Some(chan_prefix) {
        return true;
    }
    let Some(rest) = name
        .strip_prefix(chan_prefix)
        .and_then(|r| r.strip_prefix('-'))
    else {
        return false;
    };
    let Some(date) = rest.strip_suffix(".log") else {
        return false;
    };
    is_ymd_date(date)
}

/// Strict `YYYY-MM-DD` with a real calendar date.
fn is_ymd_date(d: &str) -> bool {
    let b = d.as_bytes();
    if b.len() != 10 || b[4] != b'-' || b[7] != b'-' {
        return false;
    }
    let digits = |r: std::ops::Range<usize>| b[r].iter().all(|c| c.is_ascii_digit());
    if !(digits(0..4) && digits(5..7) && digits(8..10)) {
        return false;
    }
    let (y, m, day) = (
        d[0..4].parse::<i32>().unwrap_or(0),
        d[5..7].parse::<u32>().unwrap_or(0),
        d[8..10].parse::<u32>().unwrap_or(0),
    );
    crate::timeutil::valid_date(y, m, day)
}

/// Split `text` into parts that each fit a `PRIVMSG <target> :` line within
/// 512 bytes. Splits by BYTES (Lithuanian letters are 2 bytes) but never in
/// the middle of a UTF-8 character (`splitForPrivmsg`).
pub fn split_for_privmsg(target: &str, text: &str) -> Vec<String> {
    // 45 = extra headers added by servers / bouncers.
    let header = "PRIVMSG ".len() + target.len() + " :".len();
    let budget = MAX_LINE_BYTES.saturating_sub(header + 2 + 45).max(100);
    let mut parts = Vec::new();
    let mut rest = text;
    while rest.len() > budget {
        let mut cut = budget;
        while cut > 0 && !rest.is_char_boundary(cut) {
            cut -= 1;
        }
        if cut == 0 {
            break;
        }
        parts.push(rest[..cut].to_string());
        rest = &rest[cut..];
    }
    parts.push(rest.to_string());
    parts
}

/// Look `name` up in `$PATH` (`exec.LookPath`).
fn look_path(name: &str) -> Option<std::path::PathBuf> {
    let path = std::env::var_os("PATH")?;
    for dir in std::env::split_paths(&path) {
        let cand = dir.join(name);
        if let Ok(md) = std::fs::metadata(&cand) {
            if !md.is_file() {
                continue;
            }
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                if md.permissions().mode() & 0o111 == 0 {
                    continue;
                }
            }
            return Some(cand);
        }
    }
    None
}

/// Open `url` in the default browser (`openURL`). Debian: xdg-open, gio,
/// sensible-browser, …; the child is detached with its stdio on /dev/null so
/// it cannot scribble over the TUI.
pub fn open_url(url: &str) -> Result<(), String> {
    // Never let a URL be parsed as an option by the opener, and never open
    // one with hidden characters (it would differ from what was shown).
    if !is_safe_url(url) {
        return Err("netinkamas URL".into());
    }
    let termux = is_termux();
    let candidates = opener_candidates(std::env::consts::OS, termux);
    let mut tried = Vec::new();
    for (name, args) in &candidates {
        tried.push(*name);
        let Some(path) = look_path(name) else {
            continue;
        };
        let child = Command::new(path)
            .args(args)
            .arg(url)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn();
        if let Ok(mut child) = child {
            // Reap the child so it does not linger as a zombie.
            std::thread::spawn(move || {
                let _ = child.wait();
            });
            return Ok(());
        }
    }
    let hint = if termux {
        "Termux: pkg install termux-tools"
    } else {
        "Debian: apt install xdg-utils"
    };
    Err(format!(
        "naršyklės įrankis nerastas (bandė: {}). {hint}",
        tried.join(", ")
    ))
}

/// Ar veikiama Termux (Android) aplinkoje: Termux nustato `TERMUX_VERSION`
/// ir `PREFIX=/data/data/com.termux/files/usr`.
pub fn is_termux() -> bool {
    cfg!(target_os = "android")
        || std::env::var_os("TERMUX_VERSION").is_some()
        || std::env::var("PREFIX").is_ok_and(|p| p.contains("com.termux"))
}

/// URL atidarymo įrankiai pagal OS, tikrinimo tvarka. Termux'e pirmas —
/// `termux-open-url` (termux-tools, atidaro Android naršyklėje).
pub fn opener_candidates(os: &str, termux: bool) -> Vec<(&'static str, Vec<&'static str>)> {
    let mut v: Vec<(&'static str, Vec<&'static str>)> = match os {
        "macos" => vec![("open", vec![])],
        "windows" => vec![("rundll32", vec!["url.dll,FileProtocolHandler"])],
        _ => vec![
            ("xdg-open", vec![]),
            ("gio", vec!["open"]),
            ("sensible-browser", vec![]),
            ("x-www-browser", vec![]),
            ("www-browser", vec![]),
            ("firefox", vec![]),
        ],
    };
    if termux || os == "android" {
        v.insert(0, ("termux-open-url", vec![]));
    }
    v
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::default_config;

    #[test]
    fn termux_opener_goes_first() {
        fn names(v: Vec<(&'static str, Vec<&'static str>)>) -> Vec<&'static str> {
            v.into_iter().map(|(n, _)| n).collect()
        }
        let linux = names(opener_candidates("linux", false));
        assert_eq!(linux[0], "xdg-open");
        assert!(!linux.contains(&"termux-open-url"));
        assert_eq!(
            names(opener_candidates("linux", true))[0],
            "termux-open-url"
        );
        assert_eq!(
            names(opener_candidates("android", false))[0],
            "termux-open-url"
        );
        assert_eq!(names(opener_candidates("macos", false)), vec!["open"]);
    }

    #[test]
    fn channel_log_file_is_exact() {
        assert!(is_channel_log_file("#k-2026-10-21.log", "#k"));
        assert!(is_channel_log_file("#k-2025-03-15.log", "#k"));
        assert!(is_channel_log_file("#k.log", "#k"));
        // Another channel whose name merely starts with "#k-2".
        assert!(!is_channel_log_file("#k-2-2026-10-21.log", "#k"));
        assert!(!is_channel_log_file("#k-2026-13-45.log", "#k"));
        assert!(!is_channel_log_file("#k-2026-10-21.txt", "#k"));
        assert!(!is_channel_log_file("#kk-2026-10-21.log", "#k"));
    }

    #[test]
    fn split_privmsg_respects_budget_and_utf8() {
        assert_eq!(split_for_privmsg("#c", "short"), vec!["short"]);
        let text = "ž".repeat(600); // 1200 bytes
        let parts = split_for_privmsg("#c", &text);
        assert!(parts.len() >= 3);
        for p in &parts {
            assert!("PRIVMSG #c :".len() + p.len() + 2 <= MAX_LINE_BYTES);
        }
        assert_eq!(parts.concat(), text);
    }

    #[test]
    fn stop_is_idempotent_and_wakes_waiters() {
        let c = Client::new(&default_config());
        assert!(!c.is_stopped());
        assert!(!c.wait_stop(Some(Duration::from_millis(10))));
        let c2 = c.clone();
        let h = std::thread::spawn(move || c2.wait_stop(Some(Duration::from_secs(5))));
        std::thread::sleep(Duration::from_millis(20));
        c.stop();
        c.stop();
        assert!(h.join().unwrap());
        assert!(c.is_stopped());
    }

    #[test]
    fn clear_logs_removes_only_matching_files() {
        let dir = std::env::temp_dir().join(format!("mircbsd-clearlogs-{}", std::process::id()));
        let srv = dir.join("irc.example.org");
        std::fs::create_dir_all(&srv).unwrap();
        for f in [
            "#k-2026-10-21.log",
            "#k-2-2026-10-21.log",
            "#z-2026-10-21.log",
            "#k-2026-10-21.json",
            "notes.txt",
            ".log",
        ] {
            std::fs::write(srv.join(f), "x").unwrap();
        }
        #[cfg(unix)]
        std::os::unix::fs::symlink(srv.join("notes.txt"), srv.join("linked.log")).unwrap();
        let mut cfg = default_config();
        cfg.server = "irc.example.org".into();
        cfg.log_dir = dir.to_string_lossy().into_owned();
        let c = Client::new(&cfg);
        assert_eq!(c.clear_logs("#k"), 1);
        assert!(srv.join("#k-2-2026-10-21.log").exists());
        assert_eq!(c.clear_logs(""), 2);
        assert_eq!(c.clear_logs(""), 0);
        for f in ["#k-2026-10-21.json", "notes.txt", ".log"] {
            assert!(srv.join(f).exists(), "{f} must be preserved");
        }
        #[cfg(unix)]
        assert!(srv.join("linked.log").is_symlink());
        let _ = std::fs::remove_dir_all(&dir);
    }
}

#[cfg(test)]
#[path = "session_tests.rs"]
mod more_tests;
