//! User input: keys, slash commands, Tab completion, search (port of
//! `input.go`). Everything is implemented as methods on [`Client`], like the
//! Go methods on `*Client`.
//!
//! * [`keys`] — `handle_key`, mouse (SGR 1006), paste, key remapping
//! * [`commands`] — `handle_input` / `handle_cmd` (every slash command), `/set`
//! * [`tab`] — `do_tab` autocomplete (commands, nicks, channels)
//! * [`search`] — Ctrl+R / `/search`, `/search all`, `/logsearch`
//! * [`sensitive`] — password detection for the history and the logs
//!
//! Feeding keys: the terminal must already be in raw mode. Either run
//! [`Client::read_keys`] on a thread (it reads stdin, buffers split escape
//! sequences and stops the client on EOF), or push byte chunks into
//! [`KeyBuffer::feed`] / [`Client::handle_key`] yourself.
//!
//! Locking: `Client::mu` is not reentrant — these modules take the lock for
//! state changes, drop it, and only then call `add_msg`, `server_msg`,
//! `err_msg`, `send`, `switch_ch` (which lock again).

pub mod commands;
pub mod keys;
pub mod search;
pub mod sensitive;
pub mod tab;

use std::io::Read;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::Arc;

use crate::client::Client;

pub use commands::is_valid_ts_format;
pub use keys::{esc_complete, split_paste_lines};
pub use sensitive::{is_sensitive_history, is_sensitive_log_line};

/// Read size of the keyboard thread (Go: `make([]byte, 64)`).
const READ_CHUNK: usize = 64;
/// An unfinished escape sequence longer than this is delivered as is.
const MAX_PENDING_ESC: usize = 24;

/// First whitespace-separated field, or `""` (`firstField`).
pub fn first_field(s: &str) -> &str {
    s.split_whitespace().next().unwrap_or("")
}

/// Decode one rune from raw key bytes. Invalid UTF-8 degrades to the single
/// byte as a Latin-1 rune (`decodeRune`).
pub fn decode_rune(b: &[u8]) -> (char, usize) {
    if b.is_empty() {
        return ('\0', 1);
    }
    let n = utf8_len(b[0]);
    if n > 1 && b.len() >= n {
        if let Ok(s) = std::str::from_utf8(&b[..n]) {
            if let Some(c) = s.chars().next() {
                return (c, n);
            }
        }
    }
    (b[0] as char, 1)
}

/// Total length of the UTF-8 sequence introduced by `lead` (1 for ASCII and
/// for bytes that cannot start a sequence).
fn utf8_len(lead: u8) -> usize {
    match lead {
        0xC2..=0xDF => 2,
        0xE0..=0xEF => 3,
        0xF0..=0xF4 => 4,
        _ => 1,
    }
}

/// Split one read into individual key events, plus a trailing unfinished
/// escape sequence (if any). A chunk with more than one newline is a paste
/// and is returned whole.
fn split_key_events(b: &[u8]) -> (Vec<Vec<u8>>, Vec<u8>) {
    let newlines = b.iter().filter(|&&x| x == b'\n' || x == b'\r').count();
    if newlines > 1 {
        return (vec![b.to_vec()], Vec::new());
    }
    let mut keys = Vec::new();
    let mut i = 0;
    while i < b.len() {
        let n = key_len(&b[i..]);
        if n == 0 {
            break;
        }
        // Extra bytes may follow a finished sequence (`\x1b[D!`). Judge
        // completeness on the span itself, not on the tail of the read.
        if b[i] == 0x1b && !esc_complete(&b[i..i + n]) {
            break;
        }
        keys.push(b[i..i + n].to_vec());
        i += n;
    }
    (keys, b[i..].to_vec())
}

/// Byte length of the key that starts at `b`: one rune, or one escape sequence.
fn key_len(b: &[u8]) -> usize {
    if b.is_empty() {
        return 0;
    }
    if b[0] != 0x1b {
        return decode_rune(b).1;
    }
    if b.len() == 1 {
        return 1;
    }
    match b[1] {
        b'[' => {
            for (k, &x) in b.iter().enumerate().skip(2) {
                if (0x40..=0x7e).contains(&x) {
                    return k + 1;
                }
            }
            b.len()
        }
        b'O' => 3.min(b.len()),
        _ => 2.min(b.len()),
    }
}

/// Number of trailing bytes of `b` that form an INCOMPLETE UTF-8 sequence
/// (a multi-byte character split between two reads).
fn incomplete_utf8_tail(b: &[u8]) -> usize {
    for k in 1..=3.min(b.len()) {
        let x = b[b.len() - k];
        if (0x80..=0xBF).contains(&x) {
            continue; // continuation byte — keep looking for its lead
        }
        return if x >= 0xC0 && utf8_len(x) > k { k } else { 0 };
    }
    0
}

/// Reassembles key sequences that a terminal delivered in pieces
/// (`readKeys` buffering).
///
/// A read can end in the middle of a control sequence (`"\x1b["` followed by
/// `"1;5H"` in the next read) or in the middle of a multi-byte character.
/// Without buffering the first piece would be lost (ESC is the cancel key) —
/// a classic "key does not work" cause.
#[derive(Debug, Default)]
pub struct KeyBuffer {
    pending: Vec<u8>,
}

impl KeyBuffer {
    pub fn new() -> Self {
        Self::default()
    }

    /// Bytes waiting for their continuation.
    pub fn pending(&self) -> &[u8] {
        &self.pending
    }

    /// Put an unfinished tail back (a key split off the end of a read).
    pub fn stash(&mut self, data: &[u8]) {
        self.pending = data.to_vec();
    }

    /// Feed one read; returns the complete chunk to hand to
    /// [`Client::handle_key`], or `None` when more bytes are needed.
    pub fn feed(&mut self, data: &[u8]) -> Option<Vec<u8>> {
        let mut b = std::mem::take(&mut self.pending);
        b.extend_from_slice(data);
        if b.is_empty() {
            return None;
        }
        if !esc_complete(&b) && b.len() < MAX_PENDING_ESC {
            // Unfinished control sequence — wait for the rest.
            self.pending = b;
            return None;
        }
        if b[0] != 0x1b {
            // Do not cut a multi-byte character in half.
            let tail = incomplete_utf8_tail(&b);
            if tail > 0 {
                self.pending = b.split_off(b.len() - tail);
                if b.is_empty() {
                    return None;
                }
            }
        }
        Some(b)
    }
}

impl Client {
    /// Keyboard loop (`readKeys`): reads stdin until it closes, then stops
    /// the client. Blocks — run it on its own thread.
    pub fn read_keys(self: &Arc<Self>) {
        let stdin = std::io::stdin();
        let mut lock = stdin.lock();
        self.read_keys_from(&mut lock);
    }

    /// [`read_keys`](Self::read_keys) over any byte source (testable).
    pub fn read_keys_from<R: Read>(self: &Arc<Self>, r: &mut R) {
        let mut buf = [0u8; READ_CHUNK];
        let mut kb = KeyBuffer::new();
        loop {
            match r.read(&mut buf) {
                // stdin closed / broken — the keyboard is gone, stop cleanly.
                Ok(0) => {
                    self.stop();
                    return;
                }
                Ok(n) => {
                    if let Some(chunk) = kb.feed(&buf[..n]) {
                        // One read can hold several keys (`hi` + Left + `!`).
                        // A multi-line paste stays one chunk so paste detection
                        // still sees every newline. A trailing unfinished
                        // escape is put back and completed by the next read.
                        let (keys, rest) = split_key_events(&chunk);
                        if !rest.is_empty() {
                            let mut pending = rest;
                            pending.extend_from_slice(kb.pending());
                            kb.stash(&pending);
                        }
                        for key in keys {
                            self.handle_key_guarded(&key);
                            if self.is_stopped() {
                                return;
                            }
                        }
                    }
                    if self.is_stopped() {
                        return;
                    }
                }
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
                Err(_) => {
                    self.stop();
                    return;
                }
            }
        }
    }

    /// `handle_key` that turns a panic into a clean shutdown (the terminal
    /// must not stay in raw mode with a dead keyboard — `recoverPanic`).
    pub fn handle_key_guarded(self: &Arc<Self>, b: &[u8]) {
        if catch_unwind(AssertUnwindSafe(|| self.handle_key(b))).is_err() {
            eprint!("\r\nmircBSD: panic (keyboard input)\r\n");
            self.stop();
        }
    }

    /// Install the `perform` hook: `perform=` lines run through
    /// [`Client::handle_input`] after registration (`/commands` and plain
    /// text, like Go `handleInput`). Holds only a weak reference.
    pub fn install_input_hooks(self: &Arc<Self>) {
        let weak = Arc::downgrade(self);
        self.hooks.write().perform = Some(Arc::new(move |line: &str| {
            if let Some(c) = weak.upgrade() {
                c.handle_input(line);
            }
        }));
    }
}

#[cfg(test)]
pub(crate) mod testutil {
    use std::sync::Arc;

    use crate::client::send::{new_write_queue, WriteQueue};
    use crate::client::Client;
    use crate::config::default_config;

    /// Disconnected client with a quiet log dir.
    pub fn client() -> Arc<Client> {
        let mut cfg = default_config();
        cfg.server = "irc.example.org".into();
        cfg.nick = "me".into();
        cfg.log_dir = String::new();
        Client::new(&cfg)
    }

    /// Client that looks connected, with its write queue exposed.
    pub fn connected() -> (Arc<Client>, WriteQueue) {
        let c = client();
        let (h, q) = new_write_queue();
        {
            let mut st = c.mu.lock();
            st.write = Some(h);
            st.connected = true;
            st.registered = true;
        }
        (c, q)
    }

    /// Drain the lines queued for the server.
    pub fn sent(q: &WriteQueue) -> Vec<String> {
        let mut v = Vec::new();
        while let Ok(l) = q.rx.try_recv() {
            v.push(l);
        }
        v
    }

    /// Message texts of window `chan`.
    pub fn texts(c: &Client, chan: &str) -> Vec<String> {
        let st = c.mu.lock();
        let i = st.find_ch(chan).expect("window must exist");
        st.channels[i]
            .messages
            .iter()
            .map(|m| m.text.clone())
            .collect()
    }

    /// Texts of the `*server*` window.
    pub fn server_texts(c: &Client) -> Vec<String> {
        c.mu.lock().channels[0]
            .messages
            .iter()
            .map(|m| m.text.clone())
            .collect()
    }

    /// Put `text` into the input line with the cursor at the end.
    pub fn set_input(c: &Client, text: &str) {
        let mut st = c.mu.lock();
        st.input = text.chars().collect();
        st.input_pos = st.input.len();
    }

    pub fn input_text(c: &Client) -> String {
        c.mu.lock().input.iter().collect()
    }

    /// Open window `name` and make it active.
    pub fn open_active(c: &Client, name: &str) {
        let mut st = c.mu.lock();
        let i = c.ensure_ch(&mut st, name).expect("ensure_ch");
        st.active_idx = i;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn first_field_is_safe_on_blank() {
        assert_eq!(first_field("   "), "");
        assert_eq!(first_field(" foo bar "), "foo");
    }

    #[test]
    fn decode_rune_handles_utf8_and_garbage() {
        assert_eq!(decode_rune("ž".as_bytes()), ('ž', 2));
        assert_eq!(decode_rune(b"a"), ('a', 1));
        // Invalid sequence → the first byte as a Latin-1 rune.
        assert_eq!(decode_rune(&[0xC3]), ('\u{c3}', 1));
        assert_eq!(decode_rune(&[0xFF, b'a']), ('\u{ff}', 1));
    }

    #[test]
    fn key_buffer_joins_split_escape_sequences() {
        let mut kb = KeyBuffer::new();
        assert_eq!(kb.feed(b"\x1b["), None);
        assert_eq!(kb.feed(b"1;5"), None);
        assert_eq!(kb.feed(b"H").as_deref(), Some(&b"\x1b[1;5H"[..]));
        assert!(kb.pending().is_empty());
        // A lone ESC is a complete key.
        assert_eq!(kb.feed(b"\x1b").as_deref(), Some(&b"\x1b"[..]));
        assert_eq!(kb.feed(b"abc").as_deref(), Some(&b"abc"[..]));
    }

    #[test]
    fn key_buffer_does_not_cut_utf8() {
        let mut kb = KeyBuffer::new();
        let bytes = "až".as_bytes(); // 61 C5 BE
        assert_eq!(kb.feed(&bytes[..2]).as_deref(), Some(&b"a"[..]));
        assert_eq!(kb.pending(), &[0xC5]);
        assert_eq!(kb.feed(&bytes[2..]).as_deref(), Some("ž".as_bytes()));
    }

    #[test]
    fn read_keys_types_text_and_stops_on_eof() {
        let c = testutil::client();
        let mut src: &[u8] = b"hi\x1b[D!";
        c.read_keys_from(&mut src);
        assert!(c.is_stopped());
        assert_eq!(testutil::input_text(&c), "h!i");
    }
}
