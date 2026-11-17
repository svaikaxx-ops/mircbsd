//! Messages and text rendering ported from `ui.go`:
//! mIRC formatting → ANSI, word wrapping by terminal cells, URL
//! highlighting, per-message render lines, JSON export and day separators.

use chrono::{DateTime, Datelike, Local, Utc};
use serde::Serialize;

#[path = "time_format.rs"]
mod time_format;
pub use time_format::format_go_time;

use crate::sanitize::{is_control_rune, sanitize_text};
use crate::width::{cluster_width, rune_clusters, string_width};

use super::theme::{
    hl_bg_code, mirc_colors, nick_color, timestamps_enabled, ts_color_code, ts_format, ANSI_BOLD,
    ANSI_CYAN, ANSI_DARK_GRAY, ANSI_GREEN, ANSI_ITALIC, ANSI_RED, ANSI_RESET, ANSI_REVERSE,
    ANSI_UNDER,
};

// ---- mIRC colour / formatting codes ----

/// Converts mIRC formatting (`^B ^C ^O ^V ^_ ^]`) to ANSI (`renderIRCFormat`).
///
/// `^C` handling: up to two digits of foreground, optional `,` and up to two
/// digits of background. The background is parsed but IGNORED; a colour
/// number ≥ 16 or a bare `^C` resets. Other control characters (e.g. ESC)
/// are dropped. A reset is always appended.
pub fn render_irc_format(text: &str) -> String {
    let runes: Vec<char> = text.chars().collect();
    let colors = mirc_colors();
    let mut b = String::with_capacity(text.len() + 16);
    let mut i = 0;
    while i < runes.len() {
        let r = runes[i];
        match r {
            '\x02' => {
                b.push_str(ANSI_BOLD);
                i += 1;
            }
            '\x1d' => {
                b.push_str(ANSI_ITALIC);
                i += 1;
            }
            '\x1f' => {
                b.push_str(ANSI_UNDER);
                i += 1;
            }
            '\x16' => {
                b.push_str(ANSI_REVERSE);
                i += 1;
            }
            '\x0f' => {
                b.push_str(ANSI_RESET);
                i += 1;
            }
            '\x03' => {
                i += 1;
                let mut fg: i32 = -1;
                // Foreground (1–2 digits).
                if i < runes.len() && runes[i].is_ascii_digit() {
                    let mut num = runes[i] as i32 - '0' as i32;
                    i += 1;
                    if i < runes.len() && runes[i].is_ascii_digit() {
                        num = num * 10 + (runes[i] as i32 - '0' as i32);
                        i += 1;
                    }
                    if num < 16 {
                        fg = num;
                    }
                }
                // Background — consumed, but ignored.
                if i < runes.len() && runes[i] == ',' {
                    i += 1;
                    if i < runes.len() && runes[i].is_ascii_digit() {
                        i += 1;
                        if i < runes.len() && runes[i].is_ascii_digit() {
                            i += 1;
                        }
                    }
                }
                if fg >= 0 {
                    b.push_str(colors[fg as usize]);
                } else {
                    // ^C without a colour = reset.
                    b.push_str(ANSI_RESET);
                }
            }
            _ => {
                // Control characters from the network (e.g. ESC) — never printed.
                if !is_control_rune(r) {
                    b.push(r);
                }
                i += 1;
            }
        }
    }
    b.push_str(ANSI_RESET);
    b
}

/// Removes all IRC formatting codes (`stripIRCFormat`): plain text.
pub fn strip_irc_format(text: &str) -> String {
    let runes: Vec<char> = text.chars().collect();
    let mut b = String::with_capacity(text.len());
    let mut i = 0;
    while i < runes.len() {
        let r = runes[i];
        match r {
            '\x02' | '\x1d' | '\x1f' | '\x16' | '\x0f' => i += 1,
            '\x03' => {
                i += 1;
                // Skip colour digits.
                let mut k = 0;
                while k < 2 && i < runes.len() && runes[i].is_ascii_digit() {
                    i += 1;
                    k += 1;
                }
                if i < runes.len() && runes[i] == ',' {
                    i += 1;
                    let mut k = 0;
                    while k < 2 && i < runes.len() && runes[i].is_ascii_digit() {
                        i += 1;
                        k += 1;
                    }
                }
            }
            _ => {
                b.push(r);
                i += 1;
            }
        }
    }
    b
}

// ---- Message ----

/// Message kind (`MsgType`). Order matches the Go iota.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Default)]
pub enum MsgType {
    #[default]
    Chat,
    Action,
    Join,
    Part,
    Quit,
    Nick,
    Notice,
    Server,
    Error,
}

impl MsgType {
    /// Human readable kind for `/exportjson` (Go `String()`).
    pub fn as_str(self) -> &'static str {
        match self {
            MsgType::Chat => "chat",
            MsgType::Action => "action",
            MsgType::Join => "join",
            MsgType::Part => "part",
            MsgType::Quit => "quit",
            MsgType::Nick => "nick",
            MsgType::Notice => "notice",
            MsgType::Server => "server",
            MsgType::Error => "error",
        }
    }
}

impl std::fmt::Display for MsgType {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Message {
    pub ts: DateTime<Utc>,
    pub nick: String,
    pub text: String,
    pub msg_type: MsgType,
    pub highlight: bool,
}

/// Wire shape of a message in `/exportjson` (field order = Go's).
#[derive(Serialize)]
struct Wire {
    ts: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    nick: String,
    #[serde(rename = "type")]
    kind: &'static str,
    #[serde(skip_serializing_if = "String::is_empty")]
    text: String,
    #[serde(skip_serializing_if = "is_false")]
    highlight: bool,
}

fn is_false(b: &bool) -> bool {
    !*b
}

impl Message {
    pub fn new(
        ts: DateTime<Utc>,
        nick: impl Into<String>,
        text: impl Into<String>,
        msg_type: MsgType,
        highlight: bool,
    ) -> Self {
        Message {
            ts,
            nick: nick.into(),
            text: text.into(),
            msg_type,
            highlight,
        }
    }

    fn wire(&self) -> Wire {
        Wire {
            ts: self.ts.format("%Y-%m-%dT%H:%M:%SZ").to_string(),
            nick: sanitize_text(&self.nick),
            kind: self.msg_type.as_str(),
            text: sanitize_text(&strip_irc_format(&self.text)),
            highlight: self.highlight,
        }
    }

    /// `MarshalJSON` (`/exportjson`, v0.4.8): `ts` RFC 3339 (UTC), `text`
    /// SANITISED (no IRC formatting codes, no terminal control characters)
    /// so a file opened elsewhere cannot drive a terminal.
    ///
    /// Output is byte-compatible with Go's `encoding/json` (including the
    /// HTML-safe escaping of `<`, `>`, `&`, U+2028, U+2029).
    pub fn marshal_json(&self) -> String {
        let s = serde_json::to_string(&self.wire()).unwrap_or_else(|_| "{}".to_string());
        go_json_escape(&s)
    }

    /// Renders the message into one or more terminal lines of width `msg_w`
    /// (`renderLines`). `my_nick` is part of the cache key in the caller; the
    /// text rendering itself does not depend on it.
    pub fn render_lines(&self, msg_w: i32, _my_nick: &str) -> Vec<String> {
        // Nick/channel/server text — no terminal codes.
        let nick = sanitize_text(&self.nick);
        // Chat/action/notice text goes through render_irc_format (strips
        // codes); other kinds are printed raw — clean control characters.
        let text = if !matches!(
            self.msg_type,
            MsgType::Chat | MsgType::Action | MsgType::Notice
        ) {
            sanitize_text(&self.text)
        } else {
            self.text.clone()
        };

        let ts_color = ts_color_code();
        let mut ts = String::new();
        let mut ts_len = 0;
        // Timestamp — configurable (timestamps=true|false, ts_format).
        // When disabled: smaller prefix, wider text.
        if timestamps_enabled() {
            let local = self.ts.with_timezone(&Local);
            let formatted = format_go_time(&local, &ts_format());
            ts_len = string_width(&formatted) + 1;
            if ts_color.is_empty() {
                ts = format!("{} ", formatted);
            } else {
                ts = format!("{}{}{} ", ts_color, formatted, ANSI_RESET);
            }
        }

        let (prefix, prefix_len): (String, i32) = match self.msg_type {
            MsgType::Chat => {
                let nc = nick_color(&nick);
                let hl = hl_bg_code();
                let nick_str = format!("<{}> ", nick);
                let w = ts_len + string_width(&nick_str);
                if self.highlight {
                    (
                        format!("{hl}{ANSI_BOLD}{ts}{nc}{nick_str}{ANSI_RESET}{hl}"),
                        w,
                    )
                } else {
                    (format!("{ts}{nc}{nick_str}{ANSI_RESET}"), w)
                }
            }
            MsgType::Action => {
                let nc = nick_color(&nick);
                let pfx = format!("* {} ", nick);
                (format!("{ts}{nc}{pfx}"), ts_len + string_width(&pfx))
            }
            MsgType::Join | MsgType::Part | MsgType::Quit | MsgType::Nick => {
                (format!("{ts}{ANSI_GREEN}"), ts_len)
            }
            MsgType::Notice => {
                let pfx = format!("-{}- ", nick);
                (format!("{ts}{ANSI_CYAN}{pfx}"), ts_len + string_width(&pfx))
            }
            MsgType::Server => (format!("{ts}{ANSI_CYAN}"), ts_len),
            MsgType::Error => (format!("{ts}{ANSI_RED}"), ts_len),
        };

        // Body text by kind.
        let body_plain = match self.msg_type {
            MsgType::Chat | MsgType::Action | MsgType::Notice => strip_irc_format(&text),
            MsgType::Join => format!("--> {} prisijungė {}", nick, text)
                .trim()
                .to_string(),
            MsgType::Part => format!("<-- {} išėjo ({})", nick, text),
            MsgType::Quit => format!("<-- {} atsijungė ({})", nick, text),
            MsgType::Nick => format!("*** {} → {}", nick, text),
            MsgType::Server => text.clone(),
            MsgType::Error => format!("KLAIDA: {}", text),
        };

        let mut body_width = msg_w - prefix_len;
        if body_width < 8 {
            body_width = 8;
        }

        let chunks = wrap_text(&body_plain, body_width);
        let indent = " ".repeat(prefix_len.max(0) as usize);

        let mut lines = Vec::with_capacity(chunks.len());
        for (i, chunk) in chunks.iter().enumerate() {
            // Render with IRC formatting codes.
            let rendered = match self.msg_type {
                MsgType::Chat | MsgType::Action | MsgType::Notice => {
                    highlight_urls(&render_irc_format(&find_chunk(&text, chunk)))
                }
                _ => highlight_urls(chunk),
            };
            let suffix = if self.highlight { ANSI_RESET } else { "" };
            if i == 0 {
                lines.push(format!("{prefix}{rendered}{suffix}{ANSI_RESET}"));
            } else {
                lines.push(format!("{indent}{rendered}{ANSI_RESET}"));
            }
        }
        lines
    }
}

impl Serialize for Message {
    /// Plain serde serialisation with the same shape as [`Message::marshal_json`]
    /// (without Go's HTML-safe escaping).
    fn serialize<S: serde::Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        self.wire().serialize(s)
    }
}

/// `/exportjson`: a message slice as indented JSON (`json.MarshalIndent(msgs,
/// "", "  ")`), including Go's HTML-safe escaping.
pub fn messages_to_json_indent(msgs: &[Message]) -> String {
    let wires: Vec<Wire> = msgs.iter().map(Message::wire).collect();
    let s = serde_json::to_string_pretty(&wires).unwrap_or_else(|_| "[]".to_string());
    go_json_escape(&s)
}

/// Go's `encoding/json` escapes `<`, `>`, `&`, U+2028 and U+2029 inside
/// strings. These characters can only occur inside JSON strings in serde's
/// output, so a global replace is safe.
fn go_json_escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            _ => out.push(c),
        }
    }
    out
}

// ---- Wrapping ----

/// Wraps text to `width` terminal CELLS (not runes) — `wrapText`.
///
/// CJK ideographs take 2 cells, combining marks 0. The text is split into
/// grapheme clusters so an accented letter or emoji is never cut in half.
pub fn wrap_text(text: &str, width: i32) -> Vec<String> {
    let width = if width <= 0 { 1 } else { width };
    let clusters = rune_clusters(text);
    if clusters.is_empty() {
        return vec![String::new()];
    }
    let mut lines: Vec<String> = Vec::new();
    let mut cur: Vec<String> = Vec::with_capacity(width as usize);
    let mut cur_w: i32 = 0;
    for cl in clusters {
        let cw = cluster_width(&cl);
        if cur_w + cw > width {
            // Line full — look for a break point (a space).
            let cut = cur.iter().rposition(|c| c == " ");
            match cut {
                None => {
                    // No space: hard-break by width.
                    if cur.is_empty() {
                        // A single cluster (e.g. 2 cells) wider than the
                        // width — print it anyway so text is not lost.
                        lines.push(cl);
                        cur.clear();
                        cur_w = 0;
                        continue;
                    }
                    lines.push(cur.concat());
                    cur.clear();
                    // Cluster starts the new line.
                    cur.push(cl);
                    cur_w = cw;
                    continue;
                }
                Some(cut) => {
                    // Break at the last space: what follows it is the next line.
                    let rest = cur.split_off(cut + 1);
                    cur.truncate(cut);
                    // Tarpas eilutės pradžioje (po kieto lūžio) — tuščios
                    // eilutės nekuriam.
                    if !cur.is_empty() {
                        lines.push(cur.concat());
                    }
                    cur = rest;
                    cur_w = cur.iter().map(|c| cluster_width(c)).sum();
                    // Likutis po tarpo + platus klasteris vis dar gali netilpti
                    // (pvz. „b界“ pločiui 2) — tada likutis tampa atskira eilute.
                    if !cur.is_empty() && cur_w + cw > width {
                        lines.push(cur.concat());
                        cur.clear();
                        cur_w = 0;
                    }
                    cur.push(cl);
                    cur_w += cw;
                    continue;
                }
            }
        }
        cur.push(cl);
        cur_w += cw;
    }
    if !cur.is_empty() || lines.is_empty() {
        lines.push(cur.concat());
    }
    lines
}

// ---- URLs ----

/// Wraps URLs in ANSI colour (`highlightURLs`).
///
/// The text is scanned byte by byte: ANSI sequences are skipped untouched
/// (so URLs inside coloured text are still found) and every URL is wrapped
/// exactly ONCE.
pub fn highlight_urls(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out: Vec<u8> = Vec::with_capacity(bytes.len() + 32);
    let mut i = 0;
    while i < bytes.len() {
        // ANSI sequence (ESC [ ... final) — copied unchanged.
        if bytes[i] == 0x1b {
            let j = skip_ansi_seq(bytes, i);
            out.extend_from_slice(&bytes[i..j]);
            i = j;
            continue;
        }
        // URL?
        if has_url_prefix(&bytes[i..]) {
            let mut j = i;
            while j < bytes.len() && !is_url_boundary(bytes[j]) {
                j += 1;
            }
            // TrimRight(".,!?)") — all ASCII, so byte trimming is safe.
            let mut core_end = j;
            while core_end > i && matches!(bytes[core_end - 1], b'.' | b',' | b'!' | b'?' | b')') {
                core_end -= 1;
            }
            if core_end > i {
                out.extend_from_slice(ANSI_CYAN.as_bytes());
                out.extend_from_slice(ANSI_BOLD.as_bytes());
                out.extend_from_slice(&bytes[i..core_end]);
                out.extend_from_slice(ANSI_RESET.as_bytes());
                // Trailing punctuation — not wrapped.
                out.extend_from_slice(&bytes[core_end..j]);
                i = j;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    match String::from_utf8(out) {
        Ok(s) => s,
        Err(e) => String::from_utf8_lossy(e.as_bytes()).into_owned(),
    }
}

/// Does the text start with a URL scheme (`hasURLPrefix`)?
fn has_url_prefix(s: &[u8]) -> bool {
    s.starts_with(b"http://") || s.starts_with(b"https://") || s.starts_with(b"ftp://")
}

/// Does this byte end a URL (`isURLBoundary`)?
fn is_url_boundary(c: u8) -> bool {
    matches!(
        c,
        b' ' | b'\t' | b'\n' | b'\r' | b'<' | b'>' | b'"' | b'\'' | 0x1b
    )
}

/// Index past the ANSI sequence at `i` (`skipANSISeq`). Clamped to the
/// slice length (the Go version could slice out of range on a trailing ESC).
fn skip_ansi_seq(s: &[u8], i: usize) -> usize {
    let mut j = i + 1;
    if j < s.len() && s[j] == b'[' {
        j += 1;
        while j < s.len() && !is_ansi_terminator(s[j]) {
            j += 1;
        }
        if j < s.len() {
            j += 1;
        }
        return j;
    }
    // Not a CSI sequence — 2 bytes.
    (i + 2).min(s.len())
}

/// Final byte of an ANSI sequence (`isANSITerminator`).
fn is_ansi_terminator(c: u8) -> bool {
    (0x40..=0x7e).contains(&c)
}

// ---- Chunk mapping (wrapped plain text → original with codes) ----

/// Finds the part of the ORIGINAL text (with formatting codes) that
/// corresponds to a wrapped PLAIN chunk (`findChunk`). On failure returns
/// the plain chunk.
///
/// The search runs in the plain text, then boundaries are mapped back via
/// [`plain_to_original_bounds`], so formatting codes survive wrapping.
/// The plain BYTE index is converted to a rune index first — with non-ASCII
/// text (Lithuanian letters, emoji) a byte index would drift the boundary.
pub fn find_chunk(original: &str, plain_chunk: &str) -> String {
    let plain = strip_irc_format(original);
    let trimmed = plain_chunk.trim();
    if trimmed.is_empty() {
        return plain_chunk.to_string();
    }
    // Whole original (single line) — return it.
    if plain.chars().count() == trimmed.chars().count() {
        return original.to_string();
    }
    let Some(plain_idx) = plain.find(trimmed) else {
        return plain_chunk.to_string();
    };
    let (start, end) = plain_to_original_bounds(
        original,
        plain[..plain_idx].chars().count(),
        trimmed.chars().count(),
    );
    if start < 0 || end < 0 {
        return plain_chunk.to_string();
    }
    let (start, end) = (start as usize, end as usize);
    if start > end
        || end > original.len()
        || !original.is_char_boundary(start)
        || !original.is_char_boundary(end)
    {
        return plain_chunk.to_string();
    }
    original[start..end].to_string()
}

/// Converts indices in the PLAIN text into byte indices in the original text
/// with IRC formatting codes (`plainToOriginalBounds`).
///
/// `plain_idx` — character index without formatting codes; `plain_len` —
/// number of characters. Returns `[start, end)` byte indices, or `(-1, -1)`
/// when the range is not found.
pub fn plain_to_original_bounds(
    original: &str,
    plain_idx: usize,
    plain_len: usize,
) -> (isize, isize) {
    let mut plain_seen: usize = 0;
    let mut start_byte: isize = -1;
    let mut end_byte: isize = -1;
    let mut byte_pos: isize = 0;
    // In colour parameters (digits after ^C).
    let mut in_color = false;
    // Byte position where a formatting code started: if the plain run starts
    // right after a code, the bound must START at the code (otherwise the
    // colour code is detached from its text).
    let mut code_start: isize = -1;
    // Byte position where the last formatting code ended.
    let mut last_code_end: isize = -1;

    for r in original.chars() {
        let rlen = r.len_utf8() as isize;
        let mut is_code = false;
        match r {
            '\x02' | '\x1d' | '\x1f' | '\x16' | '\x0f' => {
                is_code = true;
                in_color = false;
            }
            '\x03' => {
                is_code = true;
                in_color = true;
            }
            _ => {
                if in_color {
                    if r.is_ascii_digit() || r == ',' {
                        // Colour parameter — the code continues.
                        is_code = true;
                    } else {
                        in_color = false;
                    }
                }
            }
        }

        if is_code {
            if code_start < 0 {
                code_start = byte_pos;
            }
            byte_pos += rlen;
            last_code_end = byte_pos;
            continue;
        }

        // Ordinary character — counts towards the plain text.
        if plain_seen == plain_idx && start_byte < 0 {
            if code_start >= 0 && last_code_end == byte_pos {
                // Plain run begins after a formatting code — include the
                // code so the colour stays with its text.
                start_byte = code_start;
            } else {
                start_byte = byte_pos;
            }
        }
        if plain_seen == plain_idx + plain_len {
            end_byte = byte_pos;
            return (start_byte, end_byte);
        }
        plain_seen += 1;
        byte_pos += rlen;
        last_code_end = -1;
        code_start = -1;
    }
    if start_byte >= 0 && end_byte < 0 {
        end_byte = original.len() as isize;
    }
    (start_byte, end_byte)
}

// ---- Day separators ----

/// Day-change marker line (`daySepLine`, v0.4.7).
pub fn day_sep_line(t: &DateTime<Utc>) -> String {
    let local = t.with_timezone(&Local);
    format!(
        "{}── {} ──{}",
        ANSI_DARK_GRAY,
        format_go_time(&local, "2006-01-02"),
        ANSI_RESET
    )
}

/// Do two message times fall on different DAYS (local time)? (`changedDay`)
pub fn changed_day(a: &DateTime<Utc>, b: &DateTime<Utc>) -> bool {
    let a = a.with_timezone(&Local);
    let b = b.with_timezone(&Local);
    a.year() != b.year() || a.ordinal() != b.ordinal()
}

#[cfg(test)]
#[path = "message_tests.rs"]
mod tests;
