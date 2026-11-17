//! In-window search (`doSearch`, `searchForward`), `/search all` and
//! `/logsearch` (port of the search helpers in `input.go`).
//!
//! `do_search` walks the active window backwards from the previous hit;
//! `search_forward` ("n" on an empty input line) walks forwards. Both scroll
//! so the hit's last rendered line sits at the bottom of the message area.

use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};

use crate::client::types::{Client, State};
use crate::sanitize::{sanitize_path, sanitize_text};
use crate::ui::{msg_width, strip_irc_format};

/// Most hits `/search all` prints into `*server*`.
const MAX_SEARCH_ALL: usize = 50;
/// Most hits `/logsearch` prints, across every file.
const MAX_LOG_HITS: usize = 500;
/// A log file larger than this is skipped.
const MAX_LOG_FILE: u64 = 10 * 1024 * 1024;
/// A single log line longer than this aborts that file (Go scanner buffer).
const MAX_LOG_LINE: usize = 64 * 1024;

/// Escape `filepath.Glob` metacharacters so the pattern matches the literal
/// (`globQuote`).
pub fn glob_quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        if matches!(c, '\\' | '[' | ']' | '*' | '?') {
            out.push('\\');
        }
        out.push(c);
    }
    out
}

/// Message body of a log line: everything after the first `]` timestamp
/// marker, when there is one. Searching the whole line would match the clock.
fn log_body(line: &str) -> &str {
    match line.find(']') {
        Some(i) if i > 0 && line.len() > i + 2 => line[i + 1..].trim(),
        _ => line,
    }
}

impl Client {
    /// Search the active window backwards (`doSearch`).
    ///
    /// The same query continues from the previous hit. A miss leaves search
    /// mode open (`search_result_idx = -1`).
    pub fn do_search(&self, query: &str) {
        let query_low = query.to_lowercase();
        let mut st = self.mu.lock();
        let idx = st.active_idx;
        if st.channels.get(idx).is_none() {
            return;
        }
        let n = st.channels[idx].messages.len();
        if n == 0 {
            st.search_result_idx = -1;
            return;
        }
        let start_from = if query == st.last_search_query && st.search_result_idx > 0 {
            (st.search_result_idx as usize - 1).min(n - 1)
        } else {
            n - 1
        };
        let found = {
            let msgs = &st.channels[idx].messages;
            let mut hit = None;
            for i in (0..=start_from).rev() {
                let m = &msgs[i];
                let hay = format!("{} {}", strip_irc_format(&m.text), m.nick).to_lowercase();
                if hay.contains(&query_low) {
                    hit = Some(i);
                    break;
                }
            }
            hit
        };
        if let Some(i) = found {
            scroll_to_message(self, &mut st, idx, i);
            st.search_result_idx = i as i64;
            st.last_search_query = query.to_string();
            st.search_mode = false;
        } else {
            st.search_result_idx = -1;
        }
    }

    /// Next hit downwards ("n" with an empty input line) — `searchForward`.
    pub fn search_forward(&self) {
        let mut st = self.mu.lock();
        if st.active_chan().is_none() || st.last_search_query.is_empty() || st.search_mode {
            return;
        }
        let q = st.last_search_query.to_lowercase();
        let idx = st.active_idx;
        let n = st.channels[idx].messages.len();
        let start = if st.search_result_idx >= 0 {
            st.search_result_idx as usize + 1
        } else {
            0
        };
        if start >= n {
            return;
        }
        let found = {
            let msgs = &st.channels[idx].messages;
            let mut hit = None;
            for (i, m) in msgs.iter().enumerate().skip(start) {
                let hay = format!("{} {}", strip_irc_format(&m.text), m.nick).to_lowercase();
                if hay.contains(&q) {
                    hit = Some(i);
                    break;
                }
            }
            hit
        };
        if let Some(i) = found {
            scroll_to_message(self, &mut st, idx, i);
            st.search_result_idx = i as i64;
        }
    }

    /// `/search all <word>`: up to 50 hits from every window, into `*server*`.
    pub fn search_all_channels(&self, query: &str) {
        let q = query.to_lowercase();
        let results = {
            let st = self.mu.lock();
            let mut results = Vec::new();
            for ch in &st.channels {
                for m in &ch.messages {
                    let text_low = strip_irc_format(&m.text).to_lowercase();
                    if text_low.contains(&q) || m.nick.to_lowercase().contains(&q) {
                        let line = sanitize_text(&strip_irc_format(&m.text));
                        let line = line.trim();
                        results.push((
                            ch.name.clone(),
                            format!("<{}> {line}", sanitize_text(&m.nick)),
                        ));
                        if results.len() >= MAX_SEARCH_ALL {
                            break;
                        }
                    }
                }
                if results.len() >= MAX_SEARCH_ALL {
                    break;
                }
            }
            results
        };
        if results.is_empty() {
            self.err_msg(&format!(
                "Paieška „{}“: nieko nerasta",
                sanitize_text(query)
            ));
            return;
        }
        let n = results.len();
        for (name, line) in &results {
            self.server_msg(&format!("{name} {line}"));
        }
        let extra = if n >= MAX_SEARCH_ALL {
            " (rodoma pirmi 50)"
        } else {
            ""
        };
        self.server_msg(&format!(
            "Paieška „{}“: {n} atitikmenų{extra}",
            sanitize_text(query)
        ));
    }

    /// `/logsearch [channel] <word>`: scan log files, print hits into `*server*`.
    pub fn log_search(&self, chan_filter: &str, query: &str) {
        let (log_dir, srv) = {
            let st = self.mu.lock();
            (st.log_dir.clone(), st.server.clone())
        };
        if log_dir.is_empty() {
            self.err_msg("Žurnalai išjungti (nėra log katalogo)");
            return;
        }
        let q = query.to_lowercase();
        let dir = if srv.is_empty() {
            PathBuf::from(&log_dir)
        } else {
            PathBuf::from(&log_dir).join(sanitize_path(&srv))
        };
        let is_dir = std::fs::metadata(&dir).map(|m| m.is_dir()).unwrap_or(false);
        if !is_dir {
            self.err_msg(&format!(
                "Žurnalų katalogas „{}“ nerastas",
                sanitize_text(&dir.display().to_string())
            ));
            return;
        }
        let files = match log_files(&dir, chan_filter) {
            Ok(f) if !f.is_empty() => f,
            _ if chan_filter.is_empty() => {
                self.err_msg("Žurnalų failų nerasta");
                return;
            }
            _ => {
                self.err_msg(&format!(
                    "Žurnalų failų kanalui „{}“ nerasta",
                    sanitize_text(chan_filter)
                ));
                return;
            }
        };

        let mut total = 0usize;
        let mut searched = 0usize;
        let mut skipped = 0usize;
        for path in &files {
            if total >= MAX_LOG_HITS {
                break;
            }
            if self.log_search_file(path, &q, &mut total).is_err() {
                skipped += 1;
            } else {
                searched += 1;
            }
        }
        if total == 0 && skipped == 0 {
            self.err_msg(&format!(
                "Žurnaluose nieko nerasta: {}",
                sanitize_text(query)
            ));
            return;
        }
        if total > 0 {
            self.server_msg(&format!(
                "Žurnalų paieška: {total} atitikmenų (maks. 500), {searched} failuose"
            ));
        }
        if skipped > 0 {
            self.err_msg(&format!("{skipped} failų net įskaitoma — praleista"));
        }
    }

    /// Read one log file. `Err` means "skipped" (unreadable or over the size cap).
    fn log_search_file(&self, path: &Path, q: &str, total: &mut usize) -> Result<(), ()> {
        let meta = std::fs::metadata(path).map_err(|_| ())?;
        if !meta.is_file() || meta.len() > MAX_LOG_FILE {
            return Err(());
        }
        let file = std::fs::File::open(path).map_err(|_| ())?;
        let mut reader = BufReader::new(file);
        let base = path
            .file_name()
            .map(|s| s.to_string_lossy().into_owned())
            .unwrap_or_default();
        loop {
            if *total >= MAX_LOG_HITS {
                break;
            }
            let mut buf = Vec::new();
            let n = reader.read_until(b'\n', &mut buf).map_err(|_| ())?;
            if n == 0 {
                break;
            }
            if buf.len() > MAX_LOG_LINE {
                return Err(());
            }
            if buf.last() == Some(&b'\n') {
                buf.pop();
            }
            if buf.last() == Some(&b'\r') {
                buf.pop();
            }
            let line = String::from_utf8_lossy(&buf);
            let body = log_body(&line);
            if body.to_lowercase().contains(q) {
                self.server_msg(&format!("{base}> {body}"));
                *total += 1;
            }
        }
        Ok(())
    }
}

/// Log files in `dir`. With a channel filter, only `<sanitized>-*.log`
/// (the `globQuote`d pattern `input.go` hands to `filepath.Glob`).
fn log_files(dir: &Path, chan_filter: &str) -> Result<Vec<PathBuf>, ()> {
    let prefix = if chan_filter.is_empty() {
        None
    } else {
        // Quote metacharacters the same way Go does, then match the literal
        // name the quotes protect (`[` must not become a character class).
        // `glob_quote` is what Go passes to `filepath.Glob`. Matching the
        // literal sanitized name is the same set of files, and `[` stays a
        // literal instead of a character class.
        let literal = sanitize_path(chan_filter);
        Some(format!("{literal}-"))
    };
    let mut files = Vec::new();
    for entry in std::fs::read_dir(dir).map_err(|_| ())?.flatten() {
        let name = entry.file_name().to_string_lossy().into_owned();
        if !name.ends_with(".log") {
            continue;
        }
        if entry.file_type().map(|t| t.is_dir()).unwrap_or(true) {
            continue;
        }
        if let Some(p) = &prefix {
            if !name.starts_with(p) {
                continue;
            }
        }
        files.push(entry.path());
    }
    files.sort();
    Ok(files)
}

/// Put message `i`'s last rendered line at the bottom of the viewport.
/// Caller holds `Client::mu`.
fn scroll_to_message(c: &Client, st: &mut State, ch_idx: usize, i: usize) {
    let name = st.channels[ch_idx].name.clone();
    // Same geometry as the frame (responsive layout: the nick list may be
    // hidden on a narrow terminal).
    let msg_w = msg_width(st.width, c.is_channel(&name));
    let my_nick = st.nick.clone();
    let ch = &st.channels[ch_idx];
    let found_count = ch.messages[i].render_lines(msg_w, &my_nick).len() as i32;
    let mut first_line = 0i32;
    for m in &ch.messages[..i] {
        first_line += m.render_lines(msg_w, &my_nick).len() as i32;
    }
    let mut total = first_line + found_count;
    let mut prev_ts = ch.messages[i].ts;
    for m in &ch.messages[i + 1..] {
        total += m.render_lines(msg_w, &my_nick).len() as i32;
        // Paros skirtiklis piešiamas kaip atskira eilutė (update_render_cache)
        // — be jo rastoji žinutė atsidurdavo tiek eilučių per žemai/už ekrano.
        if crate::ui::changed_day(&prev_ts, &m.ts) {
            total += 1;
        }
        prev_ts = m.ts;
    }
    let mut off = total - (first_line + found_count);
    if off < 0 {
        off = 0;
    }
    st.scroll_off = off;
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::client::types::{new_msg, MsgType};
    use crate::input::testutil::*;

    #[test]
    fn glob_quote_escapes_metacharacters() {
        assert_eq!(glob_quote("[chan]"), "\\[chan\\]");
        assert_eq!(glob_quote("a*b?c\\d"), "a\\*b\\?c\\\\d");
        assert_eq!(glob_quote("#libera"), "#libera");
    }

    #[test]
    fn log_body_skips_the_timestamp() {
        assert_eq!(log_body("[10:00:00] <bob> hi"), "<bob> hi");
        assert_eq!(log_body("no bracket"), "no bracket");
        assert_eq!(log_body("]"), "]");
    }

    #[test]
    fn search_walks_backward_then_forward() {
        let c = client();
        c.mu.lock().width = 80;
        c.add_msg(
            "*server*",
            new_msg(crate::timeutil::now(), "a", "one alpha", MsgType::Chat),
        );
        c.add_msg(
            "*server*",
            new_msg(crate::timeutil::now(), "b", "two", MsgType::Chat),
        );
        c.add_msg(
            "*server*",
            new_msg(crate::timeutil::now(), "c", "three alpha", MsgType::Chat),
        );
        c.do_search("alpha");
        assert_eq!(c.mu.lock().search_result_idx, 2);
        assert!(!c.mu.lock().search_mode);
        c.do_search("alpha");
        assert_eq!(c.mu.lock().search_result_idx, 0);
        c.search_forward();
        assert_eq!(c.mu.lock().search_result_idx, 2);
        c.do_search("missing");
        assert_eq!(c.mu.lock().search_result_idx, -1);
    }

    #[test]
    fn search_scroll_counts_day_separators() {
        let c = client();
        c.mu.lock().width = 80;
        let t0 = crate::timeutil::now() - crate::timeutil::days(10);
        c.add_msg("*server*", new_msg(t0, "a", "rasta", MsgType::Chat));
        for d in 1..=3 {
            let t = t0 + crate::timeutil::days(2 * d);
            c.add_msg("*server*", new_msg(t, "b", "kita", MsgType::Chat));
        }
        c.do_search("rasta");
        // 3 žinutės po rastosios + 3 paros skirtikliai.
        assert_eq!(c.mu.lock().scroll_off, 6);
    }

    #[test]
    fn search_all_reports_hits_and_a_cap() {
        let c = client();
        open_active(&c, "#chan");
        c.add_msg(
            "#chan",
            new_msg(crate::timeutil::now(), "bob", "raktas cia", MsgType::Chat),
        );
        c.add_msg(
            "*server*",
            new_msg(crate::timeutil::now(), "", "kitas", MsgType::Server),
        );
        c.search_all_channels("raktas");
        let t = server_texts(&c);
        assert!(
            t.iter()
                .any(|l| l.contains("#chan") && l.contains("raktas")),
            "{t:?}"
        );
        assert!(t.iter().any(|l| l.contains("1 atitikmenų")), "{t:?}");
        c.search_all_channels("nėra-tokio");
        assert!(server_texts(&c).iter().any(|l| l.contains("nieko nerasta")));
    }

    #[test]
    fn log_search_reads_channel_files_and_skips_timestamps() {
        let dir = std::env::temp_dir().join(format!("mircbsd-logsearch-{}", std::process::id()));
        let srv = dir.join("irc.example.org");
        std::fs::create_dir_all(&srv).unwrap();
        std::fs::write(
            srv.join("#k-2026-10-21.log"),
            "[10:00:00] <bob> slaptas žodis\n[11:00:00] <a> kitas\n",
        )
        .unwrap();
        std::fs::write(srv.join("#z-2026-10-21.log"), "[10:00:00] <a> nieko\n").unwrap();
        let c = client();
        c.mu.lock().log_dir = dir.to_string_lossy().into_owned();
        c.log_search("#k", "10:00");
        assert!(
            server_texts(&c).iter().any(|l| l.contains("nieko nerasta")),
            "the clock must not match"
        );
        c.log_search("#k", "slaptas");
        let t = server_texts(&c);
        assert!(
            t.iter()
                .any(|l| l.contains("#k-2026-10-21.log>") && l.contains("slaptas")),
            "{t:?}"
        );
        assert!(t.iter().any(|l| l.contains("1 atitikmenų")), "{t:?}");
        assert!(!t.iter().any(|l| l.contains("#z-")));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn log_search_without_a_directory_explains() {
        let c = client();
        c.log_search("", "x");
        assert!(server_texts(&c)
            .iter()
            .any(|l| l.contains("Žurnalai išjungti")));
    }
}
