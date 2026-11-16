//! Keyboard handling (`handleKey`, `handleSearchKey`, `handleMouse`, paste).
//!
//! [`Client::handle_key`] gets one chunk of raw terminal bytes — a single key
//! (`"\x1b[A"`, `"\r"`, `"a"`), a few typed characters, or pasted text. Split
//! escape sequences are reassembled before this point by
//! [`KeyBuffer`](super::KeyBuffer); [`esc_complete`] tells when a sequence is
//! finished.

use std::sync::Arc;
use std::time::{Duration, Instant};

use chrono::Utc;

use crate::client::session::{split_for_privmsg, term_write};
use crate::client::types::{new_msg, Client, MsgType, State, SERVER_WINDOW};
use crate::config::key_actions;
use crate::sanitize::is_control_rune;
use crate::ui::CHAN_LIST_WIDTH;

use super::decode_rune;
use super::sensitive::is_sensitive_history;

/// How long after the last pasted chunk more paste data may still arrive; an
/// Enter inside this window is part of the paste, not a confirmation.
const PASTE_WINDOW: Duration = Duration::from_millis(30);
/// After scrolling DOWN, history freezing is suspended this long so streaming
/// messages cannot push the view back up while the user heads for the bottom.
const CHASE_WINDOW: Duration = Duration::from_millis(1500);
/// Most lines a paste buffer collects.
const MAX_PASTE_LINES: usize = 200;
/// Input history size.
const HISTORY_MAX: usize = 1000;
/// Longest search query (bytes).
const MAX_SEARCH_QUERY: usize = 500;

/// Is the byte sequence (starting with ESC) COMPLETE? `false` = wait for more.
/// A lone ESC is complete (the user pressed Esc).
pub fn esc_complete(b: &[u8]) -> bool {
    if b.is_empty() {
        return false;
    }
    if b[0] != 0x1b {
        return true;
    }
    if b.len() == 1 {
        return true;
    }
    match b[1] {
        // CSI: "\x1b[<params><final>", the final byte is 0x40–0x7E.
        b'[' => {
            if b.len() == 2 {
                return false;
            }
            (0x40..=0x7e).contains(&b[b.len() - 1])
        }
        // SS3: "\x1bOX" — one character after the "O".
        b'O' => b.len() >= 3,
        // "\x1bX" — Alt+X (two bytes).
        _ => b.len() >= 2,
    }
}

/// Split one paste chunk into COMPLETE lines plus a possibly unfinished tail
/// (a chunk often ends mid-line; the rest arrives in the next read).
pub fn split_paste_lines(raw: &str) -> (Vec<String>, String) {
    let raw = raw.replace("\r\n", "\n").replace('\r', "\n");
    if !raw.is_empty() && !raw.ends_with('\n') {
        return match raw.rfind('\n') {
            None => (Vec::new(), raw),
            Some(i) => (
                raw[..i].split('\n').map(String::from).collect(),
                raw[i + 1..].to_string(),
            ),
        };
    }
    (
        raw.trim_end_matches('\n')
            .split('\n')
            .map(String::from)
            .collect(),
        String::new(),
    )
}

/// Debug rendering of one byte for `/keys debug`.
fn debug_byte(x: u8) -> String {
    if (32..127).contains(&x) {
        format!("{}('{}')", x, x as char)
    } else {
        format!("0x{x:02x}")
    }
}

/// Word start before `pos` (skips spaces, then the word).
fn word_left(input: &[char], mut pos: usize) -> usize {
    pos = pos.min(input.len());
    while pos > 0 && input[pos - 1] == ' ' {
        pos -= 1;
    }
    while pos > 0 && input[pos - 1] != ' ' {
        pos -= 1;
    }
    pos
}

/// Word end after `pos` (skips spaces, then the word).
fn word_right(input: &[char], mut pos: usize) -> usize {
    while pos < input.len() && input[pos] == ' ' {
        pos += 1;
    }
    while pos < input.len() && input[pos] != ' ' {
        pos += 1;
    }
    pos
}

impl State {
    /// Append the continuation of a paste to the buffer and extend the
    /// collection window (`collectPaste`).
    pub(crate) fn collect_paste(&mut self, chunk: &str) {
        let chunk = if self.paste_partial.is_empty() {
            chunk.to_string()
        } else {
            format!("{}{}", self.paste_partial, chunk)
        };
        let (lines, partial) = split_paste_lines(&chunk);
        let mut added = 0;
        for l in lines {
            if l.is_empty() {
                continue;
            }
            if self.paste_buf.len() >= MAX_PASTE_LINES {
                break;
            }
            self.paste_buf.push(l);
            added += 1;
        }
        if added > 0 || !partial.is_empty() {
            self.paste_collect_until = Some(Instant::now() + PASTE_WINDOW);
        }
        self.paste_partial = partial;
    }

    /// Replace the input with a history line, cursor at the end.
    fn load_history_line(&mut self, idx: usize) {
        self.input = self.history[idx].chars().collect();
        self.input_pos = self.input.len();
    }

    /// ↑ with text in the input: previous history line (`historyUp`).
    pub(crate) fn history_up(&mut self) {
        if self.history.is_empty() {
            return;
        }
        let last = self.history.len() - 1;
        if self.history_idx < 0 || self.history_idx as usize > last {
            self.history_idx = last as i64;
        } else if self.history_idx > 0 {
            self.history_idx -= 1;
        }
        self.tab_candidates.clear();
        self.load_history_line(self.history_idx as usize);
    }

    /// ↓ with text in the input: next history line / empty (`historyDown`).
    pub(crate) fn history_down(&mut self) {
        if self.history_idx == -1 {
            return;
        }
        self.history_idx += 1;
        self.tab_candidates.clear();
        if self.history_idx as usize >= self.history.len() {
            self.history_idx = -1;
            self.input.clear();
            self.input_pos = 0;
        } else {
            self.load_history_line(self.history_idx as usize);
        }
    }

    /// The user went on typing / navigating after a Tab completion: take the
    /// completion as accepted. The `" [i/N]"` cycling hint sitting right
    /// after the cursor is dropped (it must never be sent) and the candidate
    /// list is forgotten.
    pub(crate) fn accept_tab_completion(&mut self) {
        if self.tab_candidates.is_empty() {
            return;
        }
        let total = self.tab_candidates.len();
        if total > 1 {
            let hint: Vec<char> = format!(" [{}/{}]", self.tab_idx + 1, total)
                .chars()
                .collect();
            let pos = self.input_pos.min(self.input.len());
            if self.input[pos..] == hint[..] {
                self.input.truncate(pos);
            }
        }
        self.tab_candidates.clear();
    }
}

impl Client {
    /// Handle one chunk of keyboard bytes (`handleKey`).
    pub fn handle_key(self: &Arc<Self>, input: &[u8]) {
        let mut b: &[u8] = input;

        // Raw mode clears ISIG, so Ctrl+C arrives as ETX (0x03), not SIGINT.
        if b == [0x03] {
            self.stop();
            return;
        }

        // ---- /keys debug: show the raw bytes, `q` leaves the mode ----
        let keys_debug = self.mu.lock().keys_debug;
        if keys_debug {
            let parts: Vec<String> = b.iter().map(|&x| debug_byte(x)).collect();
            self.server_msg(&format!("[KEYS] {} baitai: {}", b.len(), parts.join(" ")));
            if b.len() == 1 && b[0] == b'q' {
                self.mu.lock().keys_debug = false;
                self.server_msg("[KEYS] debug išjungtas");
            }
            return;
        }

        // ---- paste confirmation mode ----
        let (has_paste, collecting) = {
            let st = self.mu.lock();
            let has = !st.paste_buf.is_empty();
            let collecting = has && st.paste_collect_until.map_or(false, |t| Instant::now() < t);
            (has, collecting)
        };
        if has_paste {
            if collecting {
                // The paste is still streaming in (~64 B chunks): keep
                // collecting lines; an Enter is not a confirmation yet.
                self.mu.lock().collect_paste(&String::from_utf8_lossy(b));
                self.queue_redraw();
                return;
            }
            if b.len() == 1 {
                match b[0] {
                    b'\r' | b'\n' => self.send_paste(),
                    0x1b => {
                        self.mu.lock().paste_buf.clear();
                        self.server_msg("Paste atšauktas");
                    }
                    _ => {}
                }
            }
            self.queue_redraw();
            return;
        }

        // ---- multi-line paste detection ----
        let nl_count = b.iter().filter(|&&x| x == b'\n' || x == b'\r').count();
        if nl_count > 1 {
            let (lines, partial) = split_paste_lines(&String::from_utf8_lossy(b));
            let nonempty: Vec<String> = lines.into_iter().filter(|l| !l.is_empty()).collect();
            if nonempty.len() > 1 {
                let total = nonempty.len();
                {
                    let mut st = self.mu.lock();
                    st.paste_buf = nonempty;
                    st.paste_partial = partial;
                    st.paste_collect_until = Some(Instant::now() + PASTE_WINDOW);
                }
                self.server_msg(&format!(
                    "⚠ Paste: {total} eilutės. Enter=siųsti, Esc=atšaukti"
                ));
                self.queue_redraw();
                return;
            }
        }

        // ---- search mode (Ctrl+R) ----
        if self.mu.lock().search_mode {
            self.handle_search_key(b);
            return;
        }

        // ---- user key remapping (`keys` file) ----
        // A received sequence that the user rebound is replaced by the
        // canonical action sequence and then handled normally. A cycle
        // (sequence → action → same sequence) is cut after 3 rounds.
        for _ in 0..3 {
            let key = String::from_utf8_lossy(b).into_owned();
            let act = self.mu.lock().custom_binds.get(&key).cloned();
            let Some(act) = act else { break };
            let Some(&canon) = key_actions().get(act.as_str()) else {
                break;
            };
            if canon == b {
                break;
            }
            b = canon;
        }

        // ---- search result navigation: "n" next (down), "p" previous ----
        // ONLY with an EMPTY input line (with text they stay letters).
        let (empty_input, last_query) = {
            let st = self.mu.lock();
            (st.input.is_empty(), st.last_search_query.clone())
        };
        if empty_input && !last_query.is_empty() && b.len() == 1 && (b[0] == b'n' || b[0] == b'p') {
            if b[0] == b'n' {
                self.search_forward();
            } else {
                self.do_search(&last_query);
            }
            return;
        }

        // Anything but Tab (and mouse reports) accepts a pending completion.
        let is_mouse = b.starts_with(b"\x1b[<");
        if !is_mouse && b != b"\t" {
            self.mu.lock().accept_tab_completion();
        }

        // ---- Ctrl+arrows: nick list scroll (↑↓), word navigation (←→) ----
        // (Ctrl+Home/End channel scrolling was removed in v0.4.5: the list
        // follows the active channel — see `switch_ch`.)
        if b.len() >= 6
            && b[0] == 0x1b
            && b[1] == b'['
            && b[2] == b'1'
            && b[3] == b';'
            && b[4] == b'5'
        {
            match b[5] {
                b'A' => {
                    let mut st = self.mu.lock();
                    if st.nick_scroll_off > 0 {
                        st.nick_scroll_off -= 1;
                    }
                }
                b'B' => self.mu.lock().nick_scroll_off += 1,
                b'C' => {
                    let mut st = self.mu.lock();
                    st.input_pos = word_right(&st.input, st.input_pos);
                }
                b'D' => {
                    let mut st = self.mu.lock();
                    st.input_pos = word_left(&st.input, st.input_pos);
                }
                _ => {}
            }
            if matches!(b[5], b'A' | b'B' | b'C' | b'D') {
                self.queue_redraw();
            }
            return;
        }

        // ---- CSI sequences ----
        if b.len() >= 3 && b[0] == 0x1b && b[1] == b'[' {
            self.handle_csi(b);
            return;
        }

        // ---- SS3: F1–F4 ----
        if b.len() == 3 && b[0] == 0x1b && b[1] == b'O' {
            match b[2] {
                b'P' => self.switch_ch(0),
                b'Q' => self.switch_ch(1),
                b'R' => self.switch_ch(2),
                b'S' => self.switch_ch(3),
                _ => {}
            }
            return;
        }

        // ---- Alt+1..Alt+9: window by number ----
        if b.len() == 2 && b[0] == 0x1b && (b'1'..=b'9').contains(&b[1]) {
            self.switch_ch((b[1] - b'1') as usize);
            return;
        }

        // ---- a lone ESC ----
        if b.len() == 1 && b[0] == 0x1b {
            self.mu.lock().tab_candidates.clear();
            return;
        }

        // ---- typed text and control characters ----
        self.edit_input(b);
    }

    /// Enter after a paste: send every collected line to the active window.
    fn send_paste(&self) {
        let (lines, active_name) = {
            let mut st = self.mu.lock();
            let lines = std::mem::take(&mut st.paste_buf);
            let name = st.active_chan().map(|c| c.name.clone()).unwrap_or_default();
            (lines, name)
        };
        if !(self.is_connected() && !active_name.is_empty() && active_name != SERVER_WINDOW) {
            return;
        }
        for l in lines {
            if l.is_empty() {
                continue;
            }
            for part in split_for_privmsg(&active_name, &l) {
                self.send(&format!("PRIVMSG {active_name} :{part}"));
                self.add_msg(
                    &active_name,
                    new_msg(Utc::now(), &self.my_nick(), &part, MsgType::Chat),
                );
            }
        }
    }

    /// `ESC [ …` sequences: arrows, Home/End, PgUp/PgDn, F5–F12, mouse.
    fn handle_csi(self: &Arc<Self>, b: &[u8]) {
        // Mouse report (SGR 1006): ESC [ < b ; x ; y M|m
        if b[2] == b'<' {
            self.handle_mouse(b);
            return;
        }
        match b[2] {
            b'A' => {
                let mut st = self.mu.lock();
                if st.input.is_empty() {
                    st.scroll_off += 3;
                    st.chase_until = None; // scrolling up freezes the view again
                    drop(st);
                    self.queue_redraw();
                } else {
                    st.history_up();
                    drop(st);
                    self.queue_redraw();
                }
                return;
            }
            b'B' => {
                let mut st = self.mu.lock();
                if st.input.is_empty() {
                    if st.scroll_off > 0 {
                        st.scroll_off = (st.scroll_off - 3).max(0);
                        st.chase_until = Some(Instant::now() + CHASE_WINDOW);
                    }
                } else {
                    st.history_down();
                }
                drop(st);
                self.queue_redraw();
                return;
            }
            b'5' => {
                let mut st = self.mu.lock();
                st.scroll_off += st.height - 4;
                st.chase_until = None;
                drop(st);
                self.queue_redraw();
                return;
            }
            b'6' => {
                let mut st = self.mu.lock();
                if b.len() >= 4 && b[3] == b';' {
                    // Shift+PgDn — jump to the bottom.
                    st.scroll_off = 0;
                    st.chase_until = None;
                } else {
                    st.scroll_off = (st.scroll_off - (st.height - 4)).max(0);
                    // While scrolling down, skip freezing so streaming
                    // messages do not push the view back.
                    st.chase_until = Some(Instant::now() + CHASE_WINDOW);
                }
                drop(st);
                self.queue_redraw();
                return;
            }
            b'D' => {
                let mut st = self.mu.lock();
                if st.input_pos > 0 {
                    st.input_pos -= 1;
                }
                drop(st);
                self.queue_redraw();
                return;
            }
            b'C' => {
                let mut st = self.mu.lock();
                if st.input_pos < st.input.len() {
                    st.input_pos += 1;
                }
                drop(st);
                self.queue_redraw();
                return;
            }
            b'H' => {
                self.mu.lock().input_pos = 0;
                self.queue_redraw();
                return;
            }
            b'F' => {
                let mut st = self.mu.lock();
                st.input_pos = st.input.len();
                drop(st);
                self.queue_redraw();
                return;
            }
            _ => {}
        }
        // F1–F12 in their `ESC [ nn ~` forms.
        if b.len() >= 4 {
            let idx = match &b[2..] {
                b"11~" => Some(0),
                b"12~" => Some(1),
                b"13~" => Some(2),
                b"14~" => Some(3),
                b"15~" => Some(4),
                b"17~" => Some(5),
                b"18~" => Some(6),
                b"19~" => Some(7),
                b"20~" => Some(8),
                b"21~" => Some(9),
                b"23~" => Some(10),
                b"24~" => Some(11),
                _ => None,
            };
            if let Some(i) = idx {
                self.switch_ch(i);
            }
        }
    }

    /// Typed characters and control keys (Enter, Tab, Backspace, Ctrl+…).
    fn edit_input(self: &Arc<Self>, b: &[u8]) {
        let mut i = 0;
        while i < b.len() {
            let (r, size) = decode_rune(&b[i..]);
            i += size;
            match r {
                '\r' | '\n' => {
                    let text = {
                        let mut st = self.mu.lock();
                        let text = st.input.iter().collect::<String>().trim().to_string();
                        st.input.clear();
                        st.input_pos = 0;
                        st.tab_candidates.clear();
                        text
                    };
                    if !text.is_empty() {
                        // Password commands (IDENTIFY, …) are not kept in the history.
                        if !is_sensitive_history(&text) {
                            let mut st = self.mu.lock();
                            st.history.push(text.clone());
                            if st.history.len() > HISTORY_MAX {
                                let excess = st.history.len() - HISTORY_MAX;
                                st.history.drain(..excess);
                            }
                            st.history_idx = -1;
                        }
                        self.handle_input(&text);
                    }
                    self.queue_redraw();
                }
                '\t' => {
                    self.do_tab();
                    return;
                }
                '\u{7f}' | '\u{8}' => {
                    let mut st = self.mu.lock();
                    if st.input_pos > 0 && st.input_pos <= st.input.len() {
                        let p = st.input_pos - 1;
                        st.input.remove(p);
                        st.input_pos = p;
                    }
                    st.tab_candidates.clear();
                    drop(st);
                    self.queue_redraw();
                }
                '\u{e}' => {
                    // Ctrl+N — next window
                    let idx = {
                        let st = self.mu.lock();
                        (st.active_idx + 1) % st.channels.len().max(1)
                    };
                    self.switch_ch(idx);
                }
                '\u{10}' => {
                    // Ctrl+P — previous window
                    let idx = {
                        let st = self.mu.lock();
                        if st.active_idx == 0 {
                            st.channels.len().saturating_sub(1)
                        } else {
                            st.active_idx - 1
                        }
                    };
                    self.switch_ch(idx);
                }
                '\u{c}' => {
                    // Ctrl+L — repaint the screen
                    term_write("\x1b[2J\x1b[H");
                    self.queue_redraw();
                }
                '\u{12}' => {
                    // Ctrl+R — search
                    let mut st = self.mu.lock();
                    st.search_mode = true;
                    st.search_query.clear();
                    drop(st);
                    self.queue_redraw();
                }
                '\u{15}' => {
                    // Ctrl+U — clear the line
                    let mut st = self.mu.lock();
                    st.input.clear();
                    st.input_pos = 0;
                    st.history_idx = -1;
                    st.tab_candidates.clear();
                    drop(st);
                    self.queue_redraw();
                }
                '\u{1}' => {
                    // Ctrl+A — start of line
                    let mut st = self.mu.lock();
                    st.input_pos = 0;
                    st.tab_candidates.clear();
                    drop(st);
                    self.queue_redraw();
                }
                '\u{5}' => {
                    // Ctrl+E — end of line
                    let mut st = self.mu.lock();
                    st.input_pos = st.input.len();
                    st.tab_candidates.clear();
                    drop(st);
                    self.queue_redraw();
                }
                '\u{b}' => {
                    // Ctrl+K — delete from the cursor to the end
                    let mut st = self.mu.lock();
                    let pos = st.input_pos.min(st.input.len());
                    st.input.truncate(pos);
                    st.tab_candidates.clear();
                    drop(st);
                    self.queue_redraw();
                }
                '\u{17}' => {
                    // Ctrl+W — delete the word before the cursor
                    let mut st = self.mu.lock();
                    if st.input_pos > 0 {
                        let end = st.input_pos.min(st.input.len());
                        let pos = word_left(&st.input, end);
                        st.input.drain(pos..end);
                        st.input_pos = pos;
                    }
                    st.tab_candidates.clear();
                    drop(st);
                    self.queue_redraw();
                }
                _ => {
                    if r as u32 >= 32 && !is_control_rune(r) {
                        let mut st = self.mu.lock();
                        let pos = st.input_pos.min(st.input.len());
                        st.input.insert(pos, r);
                        st.input_pos = pos + 1;
                        st.tab_candidates.clear();
                        drop(st);
                        self.queue_redraw();
                    }
                }
            }
        }
    }

    /// Keys while the Ctrl+R search prompt is open (`handleSearchKey`).
    pub fn handle_search_key(self: &Arc<Self>, b: &[u8]) {
        if b.len() == 1 {
            match b[0] {
                0x1b => {
                    let mut st = self.mu.lock();
                    st.search_mode = false;
                    st.search_query.clear();
                    drop(st);
                    self.queue_redraw();
                    return;
                }
                b'\r' | b'\n' => {
                    let q = self.mu.lock().search_query.clone();
                    if !q.is_empty() {
                        self.do_search(&q);
                    } else {
                        self.mu.lock().search_mode = false;
                    }
                    self.queue_redraw();
                    return;
                }
                0x7f => {
                    let mut st = self.mu.lock();
                    st.search_query.pop();
                    drop(st);
                    self.queue_redraw();
                    return;
                }
                _ => {}
            }
        }
        // Add the typed characters to the query.
        let mut i = 0;
        while i < b.len() {
            let (r, size) = decode_rune(&b[i..]);
            i += size;
            if r as u32 >= 32 && !is_control_rune(r) {
                let mut st = self.mu.lock();
                if st.search_query.len() < MAX_SEARCH_QUERY {
                    st.search_query.push(r);
                }
            }
        }
        self.queue_redraw();
    }

    /// SGR 1006 mouse report `ESC [ < b ; x ; y M|m` (`handleMouse`): the
    /// wheel scrolls, releasing a click in the channel column switches window.
    pub fn handle_mouse(&self, b: &[u8]) {
        if !self.mu.lock().mouse {
            return;
        }
        let s = String::from_utf8_lossy(b);
        let Some(rest) = s.strip_prefix("\x1b[<") else {
            return;
        };
        let Some(end) = s.chars().last() else { return };
        if end != 'M' && end != 'm' {
            return; // unfinished sequence
        }
        let Some(body) = rest.strip_suffix(end) else {
            return;
        };
        // Split from the right in case other characters are glued on.
        let Some(i) = body.rfind(';') else { return };
        let xy: Vec<&str> = body[..i].split(';').collect();
        if xy.len() != 2 {
            return;
        }
        let (Ok(btn), Ok(x), Ok(y)) = (
            xy[0].parse::<i64>(),
            xy[1].parse::<i64>(),
            body[i + 1..].parse::<i64>(),
        ) else {
            return;
        };
        match () {
            _ if btn == 64 => {
                // wheel up
                self.mu.lock().scroll_off += 3;
                self.queue_redraw();
            }
            _ if btn == 65 => {
                // wheel down
                let mut st = self.mu.lock();
                st.scroll_off = (st.scroll_off - 3).max(0);
                if st.scroll_off > 0 {
                    st.chase_until = Some(Instant::now() + CHASE_WINDOW);
                }
                drop(st);
                self.queue_redraw();
            }
            _ if end == 'm' => {
                // released click
                let (top, n_chan, chan_scroll) = {
                    let mut st = self.mu.lock();
                    // Clamp the STORED offset first (a terminal resize may have
                    // reduced the visible rows since it last changed).
                    st.clamp_chan_scroll();
                    (
                        st.msg_area_top as i64,
                        st.channels.len() as i64,
                        st.chan_scroll_off as i64,
                    )
                };
                let idx = y - top + chan_scroll;
                if x <= CHAN_LIST_WIDTH as i64 && idx >= 0 && idx < n_chan {
                    self.switch_ch(idx as usize);
                }
            }
            _ => {}
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::input::testutil::*;
    use crate::ui::Channel;

    fn many_channels() -> Arc<Client> {
        let c = client();
        {
            let mut st = c.mu.lock();
            for i in 0..30 {
                st.channels.push(Channel::new(
                    format!("#kanalas{}", i % 10 + 10 * (i / 10)),
                    10,
                ));
            }
            st.active_idx = 0;
            st.height = 20;
        }
        c
    }

    #[test]
    fn esc_complete_cases() {
        for s in ["\x1b[", "\x1b[1;5", "\x1bO"] {
            assert!(!esc_complete(s.as_bytes()), "{s:?} is incomplete");
        }
        for s in [
            "\x1b",
            "\x1b[1;5H",
            "\x1b[1;5F",
            "\x1b[A",
            "\x1bOP",
            "\x1bX",
            "a",
            "\x1b[0;3;5m",
            "labas\r",
        ] {
            assert!(esc_complete(s.as_bytes()), "{s:?} is complete");
        }
        assert!(!esc_complete(b""));
    }

    #[test]
    fn ctrl_c_stops_the_client() {
        let c = client();
        c.handle_key(b"\x03");
        assert!(c.is_stopped());
    }

    #[test]
    fn typing_editing_and_word_keys() {
        let c = client();
        c.handle_key(b"labas pasaulis");
        assert_eq!(input_text(&c), "labas pasaulis");
        c.handle_key(b"\x17"); // Ctrl+W
        assert_eq!(input_text(&c), "labas ");
        c.handle_key(b"\x01"); // Ctrl+A
        assert_eq!(c.mu.lock().input_pos, 0);
        c.handle_key(b"\x1b[1;5C"); // Ctrl+Right
        assert_eq!(c.mu.lock().input_pos, 5);
        c.handle_key(b"\x1b[1;5D"); // Ctrl+Left
        assert_eq!(c.mu.lock().input_pos, 0);
        c.handle_key(b"\x05"); // Ctrl+E
        c.handle_key(b"\x7f"); // Backspace
        assert_eq!(input_text(&c), "labas");
        c.handle_key(b"\x1b[D\x1b[D"[..3].as_ref()); // Left
        c.handle_key(b"\x0b"); // Ctrl+K
        assert_eq!(input_text(&c), "laba");
        c.handle_key(b"\x15"); // Ctrl+U
        assert_eq!(input_text(&c), "");
        // UTF-8 typing keeps characters whole.
        c.handle_key("žąsis".as_bytes());
        assert_eq!(input_text(&c), "žąsis");
        c.handle_key(b"\x7f");
        assert_eq!(input_text(&c), "žąsi");
    }

    #[test]
    fn c1_control_bytes_are_not_inserted() {
        let c = client();
        c.handle_key(&[0x9b, b'a']);
        assert_eq!(input_text(&c), "a");
    }

    #[test]
    fn enter_submits_records_history_and_skips_secrets() {
        let (c, q) = connected();
        open_active(&c, "#chan");
        c.handle_key(b"sveiki\r");
        assert_eq!(sent(&q), vec!["PRIVMSG #chan :sveiki"]);
        assert_eq!(input_text(&c), "");
        c.handle_key(b"/msg nickserv identify hunter2\r");
        let st = c.mu.lock();
        assert_eq!(
            st.history,
            vec!["sveiki"],
            "password command must not be saved"
        );
    }

    #[test]
    fn history_arrows() {
        let c = client();
        c.mu.lock().history = vec!["a".into(), "b".into()];
        c.handle_key(b"x");
        c.handle_key(b"\x1b[A");
        assert_eq!(input_text(&c), "b");
        c.handle_key(b"\x1b[A");
        assert_eq!(input_text(&c), "a");
        c.handle_key(b"\x1b[B");
        assert_eq!(input_text(&c), "b");
        c.handle_key(b"\x1b[B");
        assert_eq!(input_text(&c), "");
    }

    #[test]
    fn arrows_scroll_with_empty_input() {
        let c = client();
        c.mu.lock().height = 24;
        c.handle_key(b"\x1b[A");
        assert_eq!(c.mu.lock().scroll_off, 3);
        c.handle_key(b"\x1b[5~"); // PgUp
        assert_eq!(c.mu.lock().scroll_off, 3 + 20);
        c.handle_key(b"\x1b[6~"); // PgDn
        assert_eq!(c.mu.lock().scroll_off, 3);
        assert!(c.mu.lock().chase_until.is_some());
        c.handle_key(b"\x1b[6;2~"); // Shift+PgDn
        let st = c.mu.lock();
        assert_eq!(st.scroll_off, 0);
        assert!(st.chase_until.is_none());
    }

    #[test]
    fn ctrl_arrows_scroll_nicks() {
        let c = many_channels();
        c.handle_key(b"\x1b[1;5B");
        assert_eq!(c.mu.lock().nick_scroll_off, 1);
        c.handle_key(b"\x1b[1;5A");
        assert_eq!(c.mu.lock().nick_scroll_off, 0);
        c.handle_key(b"\x1b[1;5A");
        assert_eq!(c.mu.lock().nick_scroll_off, 0);
    }

    #[test]
    fn home_end_do_not_scroll_channels() {
        let c = many_channels();
        let target = {
            let mut st = c.mu.lock();
            st.chan_scroll_off = st.channels.len() as i32;
            st.clamp_chan_scroll();
            st.chan_scroll_off
        };
        for k in [
            &b"\x1b[1;5H"[..],
            b"\x1b[1;5F",
            b"\x1b[H",
            b"\x1b[F",
            b"\x1b[5;5~",
            b"\x1b[6;5~",
        ] {
            c.handle_key(k);
        }
        assert_eq!(c.mu.lock().chan_scroll_off, target);
        set_input(&c, "labas pasaulis");
        c.handle_key(b"\x1b[H");
        assert_eq!(c.mu.lock().input_pos, 0);
        c.handle_key(b"\x1b[F");
        assert_eq!(c.mu.lock().input_pos, "labas pasaulis".chars().count());
    }

    #[test]
    fn window_switching_keys() {
        let c = client();
        for n in ["#a", "#b", "#c", "#d", "#e", "#f"] {
            let mut st = c.mu.lock();
            c.ensure_ch(&mut st, n);
            st.height = 30;
        }
        c.handle_key(b"\x0e"); // Ctrl+N
        assert_eq!(c.mu.lock().active_idx, 1);
        c.handle_key(b"\x10"); // Ctrl+P
        c.handle_key(b"\x10"); // wraps to the last
        assert_eq!(c.mu.lock().active_idx, 6);
        c.handle_key(b"\x1b3"); // Alt+3
        assert_eq!(c.mu.lock().active_idx, 2);
        c.handle_key(b"\x1bOS"); // F4
        assert_eq!(c.mu.lock().active_idx, 3);
        c.handle_key(b"\x1b[15~"); // F5
        assert_eq!(c.mu.lock().active_idx, 4);
        c.handle_key(b"\x1b[17~"); // F6
        assert_eq!(c.mu.lock().active_idx, 5);
        c.handle_key(b"\x1b[11~"); // F1 (CSI form)
        assert_eq!(c.mu.lock().active_idx, 0);
        c.handle_key(b"\x1b[24~"); // F12 → out of range, ignored
        assert_eq!(c.mu.lock().active_idx, 0);
    }

    #[test]
    fn ctrl_r_enters_search_and_esc_leaves() {
        let c = client();
        c.handle_key(b"\x12");
        assert!(c.mu.lock().search_mode);
        c.handle_key(b"ab");
        assert_eq!(c.mu.lock().search_query, "ab");
        c.handle_key(b"\x7f");
        assert_eq!(c.mu.lock().search_query, "a");
        c.handle_key(b"\x1b");
        let st = c.mu.lock();
        assert!(!st.search_mode);
        assert!(st.search_query.is_empty());
    }

    #[test]
    fn search_prompt_enter_runs_search() {
        let c = client();
        c.add_msg(
            "*server*",
            new_msg(Utc::now(), "bob", "rasta cia", MsgType::Chat),
        );
        c.handle_key(b"\x12");
        c.handle_key(b"rasta");
        c.handle_key(b"\r");
        let st = c.mu.lock();
        assert!(!st.search_mode);
        assert_eq!(st.last_search_query, "rasta");
        assert!(st.search_result_idx >= 0);
    }

    #[test]
    fn search_n_p_only_with_empty_input() {
        let c = client();
        c.mu.lock().last_search_query = "zzz".into();
        c.handle_key(b"n");
        // Empty input + last query: "n" navigates, nothing typed.
        assert_eq!(input_text(&c), "");
        set_input(&c, "a");
        c.handle_key(b"n");
        assert_eq!(input_text(&c), "an");
    }

    #[test]
    fn custom_binds_remap_to_canonical_sequence() {
        let c = client();
        // Bind Alt+S ("\x1bs") to the Ctrl+R search action.
        c.mu.lock()
            .custom_binds
            .insert("\x1bs".into(), "search".into());
        c.handle_key(b"\x1bs");
        assert!(c.mu.lock().search_mode);
        // A bind whose action is unknown is ignored (falls through).
        c.mu.lock().search_mode = false;
        c.mu.lock()
            .custom_binds
            .insert("\x1bx".into(), "nonsense".into());
        c.handle_key(b"\x1bx");
        assert!(!c.mu.lock().search_mode);
        // chan-next via a custom key.
        {
            let mut st = c.mu.lock();
            c.ensure_ch(&mut st, "#a");
            st.height = 30;
            st.custom_binds.insert("\x1bn".into(), "chan-next".into());
        }
        c.handle_key(b"\x1bn");
        assert_eq!(c.mu.lock().active_idx, 1);
    }

    #[test]
    fn keys_debug_reports_bytes_and_q_exits() {
        let c = client();
        c.mu.lock().keys_debug = true;
        c.handle_key(b"\x1b[A");
        c.handle_key(b"q");
        assert!(!c.mu.lock().keys_debug);
        let t = server_texts(&c);
        assert!(
            t.iter()
                .any(|l| l.contains("3 baitai: 0x1b 91('[') 65('A')")),
            "{t:?}"
        );
        assert!(t.iter().any(|l| l.contains("debug išjungtas")));
    }

    #[test]
    fn paste_split_across_reads_is_sent_after_the_window() {
        let (c, q) = connected();
        open_active(&c, "#chan");
        c.handle_key(b"pirmoji\nantroji\n");
        std::thread::sleep(Duration::from_millis(10));
        c.handle_key(b"trecija-pus");
        std::thread::sleep(Duration::from_millis(10));
        c.handle_key(b"e\n");
        std::thread::sleep(Duration::from_millis(60));
        c.handle_key(b"\r"); // confirm
        let msgs: Vec<String> = sent(&q)
            .into_iter()
            .filter_map(|l| l.strip_prefix("PRIVMSG #chan :").map(String::from))
            .collect();
        assert_eq!(msgs, vec!["pirmoji", "antroji", "trecija-puse"]);
        assert!(c.mu.lock().paste_buf.is_empty());
    }

    #[test]
    fn paste_enter_inside_window_is_ignored_and_esc_cancels() {
        let (c, q) = connected();
        open_active(&c, "#chan");
        c.handle_key(b"a\nb\n");
        c.handle_key(b"\r"); // inside the collection window: not a confirmation
        assert!(sent(&q).is_empty());
        std::thread::sleep(Duration::from_millis(60));
        c.handle_key(b"\x1b");
        assert!(c.mu.lock().paste_buf.is_empty());
        assert!(sent(&q).is_empty());
        assert!(server_texts(&c).iter().any(|t| t == "Paste atšauktas"));
    }

    #[test]
    fn split_paste_lines_handles_tails() {
        assert_eq!(
            split_paste_lines("a\nb\n"),
            (vec!["a".to_string(), "b".to_string()], String::new())
        );
        assert_eq!(
            split_paste_lines("a\nb"),
            (vec!["a".to_string()], "b".to_string())
        );
        assert_eq!(split_paste_lines("abc"), (vec![], "abc".to_string()));
        assert_eq!(split_paste_lines("a\r\nb\r\n").0, vec!["a", "b"]);
    }

    #[test]
    fn mouse_wheel_and_clicks() {
        let c = client();
        {
            let mut st = c.mu.lock();
            st.msg_area_top = 3;
            // Go's test client uses height 40; with height 0 a click scrolls
            // the channel list and the next click lands on the wrong row.
            st.height = 40;
            st.channels = vec![
                Channel::new("#a", 100),
                Channel::new("#b", 100),
                Channel::new("#c", 100),
            ];
            st.mouse = true;
        }
        c.handle_mouse(b"\x1b[<64;20;10M");
        assert_eq!(c.mu.lock().scroll_off, 3);
        c.handle_mouse(b"\x1b[<65;20;10M");
        assert_eq!(c.mu.lock().scroll_off, 0);
        for (seq, want) in [
            ("\x1b[<0;5;3m", 0usize),
            ("\x1b[<0;12;4m", 1),
            ("\x1b[<0;8;5m", 2),
        ] {
            c.handle_mouse(seq.as_bytes());
            assert_eq!(c.mu.lock().active_idx, want, "{seq:?}");
        }
        // Click in the message area does not switch.
        c.handle_mouse(b"\x1b[<0;40;10m");
        assert_eq!(c.mu.lock().active_idx, 2);
        // Mouse off: nothing happens.
        c.mu.lock().mouse = false;
        c.handle_mouse(b"\x1b[<64;20;10M");
        assert_eq!(c.mu.lock().scroll_off, 0);
        // Garbage must not panic.
        c.mu.lock().mouse = true;
        for g in [
            "\x1b[<64;20",
            "\x1b[<9999;9999;9999M",
            "\x1b[<x;y;zM",
            "\x1b[<",
            "\x1b[<;;M",
            "\x1b[<99999999999999999999;1;1M",
        ] {
            c.handle_mouse(g.as_bytes());
        }
    }

    #[test]
    fn mouse_report_through_handle_key() {
        let c = client();
        c.mu.lock().mouse = true;
        c.handle_key(b"\x1b[<64;20;10M");
        assert_eq!(c.mu.lock().scroll_off, 3);
    }
}
