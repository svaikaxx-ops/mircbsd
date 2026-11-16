//! Messages and text rendering ported from `ui.go`:
//! mIRC formatting → ANSI, word wrapping by terminal cells, URL
//! highlighting, per-message render lines, JSON export and day separators.

use chrono::{DateTime, Datelike, Local, Timelike, Utc};
use serde::Serialize;

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
                    lines.push(cur.concat());
                    cur = rest;
                    cur_w = cur.iter().map(|c| cluster_width(c)).sum();
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

// ---- Go time layout formatting ----

const SHORT_MONTHS: [&str; 12] = [
    "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
];
const LONG_MONTHS: [&str; 12] = [
    "January",
    "February",
    "March",
    "April",
    "May",
    "June",
    "July",
    "August",
    "September",
    "October",
    "November",
    "December",
];
const SHORT_DAYS: [&str; 7] = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const LONG_DAYS: [&str; 7] = [
    "Sunday",
    "Monday",
    "Tuesday",
    "Wednesday",
    "Thursday",
    "Friday",
    "Saturday",
];

fn starts_with_lower(s: &[u8]) -> bool {
    matches!(s.first(), Some(c) if c.is_ascii_lowercase())
}

fn is_digit_at(s: &[u8], i: usize) -> bool {
    matches!(s.get(i), Some(c) if c.is_ascii_digit())
}

/// Formats a time using a Go time layout (`time.Format`), e.g. `15:04`,
/// `2006-01-02 15:04:05`, `Jan _2 3:04PM`. `ts_format` is a Go layout, so
/// the Rust port needs its own formatter (chrono uses strftime).
///
/// Supported elements: year (`2006`, `06`), month (`01`, `1`, `Jan`,
/// `January`), day (`02`, `2`, `_2`, `002`/`__2` day of year), weekday
/// (`Mon`, `Monday`), hour (`15`, `03`, `3`), minute (`04`, `4`), second
/// (`05`, `5`), `PM`/`pm`, fractional seconds (`.000`, `.999`, `,000`),
/// numeric zones (`-0700`, `-07:00`, `-07`, `Z0700`, `Z07:00`, `Z07`, …) and
/// `MST` (rendered as a numeric offset — chrono has no zone abbreviations).
pub fn format_go_time(t: &DateTime<Local>, layout: &str) -> String {
    let l = layout.as_bytes();
    let mut out = String::with_capacity(layout.len() + 8);
    let mut lit_start = 0usize; // start of pending literal run
    let mut i = 0usize;

    let year = t.year();
    let month = t.month() as usize;
    let day = t.day();
    let hour = t.hour();
    let min = t.minute();
    let sec = t.second();
    let nano = t.nanosecond() % 1_000_000_000; // drop leap-second overflow
    let wday = t.weekday().num_days_from_sunday() as usize;
    let yday = t.ordinal();
    let off = t.offset().local_minus_utc();

    macro_rules! flush {
        ($upto:expr) => {
            if lit_start < $upto {
                out.push_str(&layout[lit_start..$upto]);
            }
        };
    }

    while i < l.len() {
        let rest = &l[i..];
        // (token length, kind) — None = literal byte.
        let tok: Option<(usize, Tok)> = match l[i] {
            b'J' => {
                if rest.starts_with(b"Jan") {
                    if rest.starts_with(b"January") {
                        Some((7, Tok::LongMonth))
                    } else if !starts_with_lower(&rest[3..]) {
                        Some((3, Tok::Month))
                    } else {
                        None
                    }
                } else {
                    None
                }
            }
            b'M' => {
                if rest.starts_with(b"Mon") {
                    if rest.starts_with(b"Monday") {
                        Some((6, Tok::LongWeekDay))
                    } else if !starts_with_lower(&rest[3..]) {
                        Some((3, Tok::WeekDay))
                    } else {
                        None
                    }
                } else if rest.starts_with(b"MST") {
                    Some((3, Tok::TzAbbrev))
                } else {
                    None
                }
            }
            b'0' => match rest.get(1) {
                Some(b'1') => Some((2, Tok::ZeroMonth)),
                Some(b'2') => Some((2, Tok::ZeroDay)),
                Some(b'3') => Some((2, Tok::ZeroHour12)),
                Some(b'4') => Some((2, Tok::ZeroMinute)),
                Some(b'5') => Some((2, Tok::ZeroSecond)),
                Some(b'6') => Some((2, Tok::Year)),
                Some(b'0') if rest.get(2) == Some(&b'2') => Some((3, Tok::ZeroYearDay)),
                _ => None,
            },
            b'1' => {
                if rest.get(1) == Some(&b'5') {
                    Some((2, Tok::Hour))
                } else {
                    Some((1, Tok::NumMonth))
                }
            }
            b'2' => {
                if rest.starts_with(b"2006") {
                    Some((4, Tok::LongYear))
                } else {
                    Some((1, Tok::Day))
                }
            }
            b'_' => {
                if rest.get(1) == Some(&b'2') {
                    // "_2006" is a literal underscore followed by the year.
                    if rest.starts_with(b"_2006") {
                        // literal '_' — handled as literal, then "2006".
                        None
                    } else {
                        Some((2, Tok::UnderDay))
                    }
                } else if rest.starts_with(b"__2") {
                    Some((3, Tok::UnderYearDay))
                } else {
                    None
                }
            }
            b'3' => Some((1, Tok::Hour12)),
            b'4' => Some((1, Tok::Minute)),
            b'5' => Some((1, Tok::Second)),
            b'P' => {
                if rest.get(1) == Some(&b'M') {
                    Some((2, Tok::PmUpper))
                } else {
                    None
                }
            }
            b'p' => {
                if rest.get(1) == Some(&b'm') {
                    Some((2, Tok::PmLower))
                } else {
                    None
                }
            }
            b'-' | b'Z' => {
                let iso = l[i] == b'Z';
                let body = &rest[1..];
                if body.starts_with(b"070000") {
                    Some((
                        7,
                        Tok::Tz {
                            colon: false,
                            secs: true,
                            short: false,
                            iso,
                        },
                    ))
                } else if body.starts_with(b"07:00:00") {
                    Some((
                        9,
                        Tok::Tz {
                            colon: true,
                            secs: true,
                            short: false,
                            iso,
                        },
                    ))
                } else if body.starts_with(b"0700") {
                    Some((
                        5,
                        Tok::Tz {
                            colon: false,
                            secs: false,
                            short: false,
                            iso,
                        },
                    ))
                } else if body.starts_with(b"07:00") {
                    Some((
                        6,
                        Tok::Tz {
                            colon: true,
                            secs: false,
                            short: false,
                            iso,
                        },
                    ))
                } else if body.starts_with(b"07") {
                    Some((
                        3,
                        Tok::Tz {
                            colon: false,
                            secs: false,
                            short: true,
                            iso,
                        },
                    ))
                } else {
                    None
                }
            }
            b'.' | b',' => {
                // ,000 / .000 / ,999 / .999 — fractional seconds, only when
                // the run of 0s or 9s is not followed by another digit.
                if matches!(rest.get(1), Some(b'0') | Some(b'9')) {
                    let ch = rest[1];
                    let mut j = 1;
                    while j < rest.len() && rest[j] == ch {
                        j += 1;
                    }
                    if !is_digit_at(rest, j) {
                        Some((j, Tok::Frac(ch == b'9', j - 1)))
                    } else {
                        None
                    }
                } else {
                    None
                }
            }
            _ => None,
        };

        let Some((n, tok)) = tok else {
            i += 1;
            continue;
        };
        flush!(i);
        match tok {
            Tok::LongYear => out.push_str(&format!("{:04}", year)),
            Tok::Year => out.push_str(&format!("{:02}", year.rem_euclid(100))),
            Tok::Month => out.push_str(SHORT_MONTHS[month - 1]),
            Tok::LongMonth => out.push_str(LONG_MONTHS[month - 1]),
            Tok::NumMonth => out.push_str(&month.to_string()),
            Tok::ZeroMonth => out.push_str(&format!("{:02}", month)),
            Tok::WeekDay => out.push_str(SHORT_DAYS[wday]),
            Tok::LongWeekDay => out.push_str(LONG_DAYS[wday]),
            Tok::Day => out.push_str(&day.to_string()),
            Tok::UnderDay => out.push_str(&format!("{:>2}", day)),
            Tok::ZeroDay => out.push_str(&format!("{:02}", day)),
            Tok::UnderYearDay => out.push_str(&format!("{:>3}", yday)),
            Tok::ZeroYearDay => out.push_str(&format!("{:03}", yday)),
            Tok::Hour => out.push_str(&format!("{:02}", hour)),
            Tok::Hour12 => {
                let h = if hour % 12 == 0 { 12 } else { hour % 12 };
                out.push_str(&h.to_string());
            }
            Tok::ZeroHour12 => {
                let h = if hour % 12 == 0 { 12 } else { hour % 12 };
                out.push_str(&format!("{:02}", h));
            }
            Tok::Minute => out.push_str(&min.to_string()),
            Tok::ZeroMinute => out.push_str(&format!("{:02}", min)),
            Tok::Second => out.push_str(&sec.to_string()),
            Tok::ZeroSecond => out.push_str(&format!("{:02}", sec)),
            Tok::PmUpper => out.push_str(if hour >= 12 { "PM" } else { "AM" }),
            Tok::PmLower => out.push_str(if hour >= 12 { "pm" } else { "am" }),
            Tok::TzAbbrev => out.push_str(&fmt_zone_abbrev(off)),
            Tok::Tz {
                colon,
                secs,
                short,
                iso,
            } => {
                if iso && off == 0 {
                    out.push('Z');
                } else {
                    out.push_str(&fmt_offset(off, colon, secs, short));
                }
            }
            Tok::Frac(trim, digits) => {
                let sep = l[i] as char;
                let mut s = format!("{:09}", nano);
                s.truncate(digits.min(9));
                if trim {
                    let t = s.trim_end_matches('0').to_string();
                    if !t.is_empty() {
                        out.push(sep);
                        out.push_str(&t);
                    }
                } else {
                    out.push(sep);
                    out.push_str(&s);
                    // Go pads beyond 9 digits with zeros.
                    for _ in 9..digits {
                        out.push('0');
                    }
                }
            }
        }
        i += n;
        lit_start = i;
    }
    flush!(l.len());
    out
}

#[derive(Debug, Clone)]
enum Tok {
    LongYear,
    Year,
    Month,
    LongMonth,
    NumMonth,
    ZeroMonth,
    WeekDay,
    LongWeekDay,
    Day,
    UnderDay,
    ZeroDay,
    UnderYearDay,
    ZeroYearDay,
    Hour,
    Hour12,
    ZeroHour12,
    Minute,
    ZeroMinute,
    Second,
    ZeroSecond,
    PmUpper,
    PmLower,
    /// `MST` — rendered numerically (no zone abbreviation database).
    TzAbbrev,
    /// Numeric zone: `-0700`, `-07:00`, `-07`, `-070000`, `-07:00:00` and
    /// their `Z…` (ISO 8601, "Z" for UTC) variants.
    Tz {
        colon: bool,
        secs: bool,
        short: bool,
        iso: bool,
    },
    /// (trim trailing zeros, digits)
    Frac(bool, usize),
}

/// `MST` fallback: Go prints the short numeric form (`-07` / `-0730`) when a
/// zone has no abbreviation.
fn fmt_zone_abbrev(off: i32) -> String {
    let sign = if off < 0 { '-' } else { '+' };
    let a = off.abs();
    let (h, m) = (a / 3600, (a % 3600) / 60);
    if m == 0 {
        format!("{}{:02}", sign, h)
    } else {
        format!("{}{:02}{:02}", sign, h, m)
    }
}

/// Numeric zone offset in the requested shape.
fn fmt_offset(off: i32, colon: bool, secs: bool, short: bool) -> String {
    let sign = if off < 0 { '-' } else { '+' };
    let a = off.abs();
    let (h, m, s) = (a / 3600, (a % 3600) / 60, a % 60);
    if short {
        return format!("{}{:02}", sign, h);
    }
    match (colon, secs) {
        (true, true) => format!("{}{:02}:{:02}:{:02}", sign, h, m, s),
        (false, true) => format!("{}{:02}{:02}{:02}", sign, h, m, s),
        (true, false) => format!("{}{:02}:{:02}", sign, h, m),
        (false, false) => format!("{}{:02}{:02}", sign, h, m),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    fn strip_ansi_for_test(s: &str) -> String {
        let mut out = String::new();
        let mut it = s.chars().peekable();
        while let Some(c) = it.next() {
            if c == '\x1b' {
                for n in it.by_ref() {
                    if ('@'..='~').contains(&n) && n != '[' {
                        break;
                    }
                }
            } else {
                out.push(c);
            }
        }
        out
    }

    fn plain_lines(m: &Message, w: i32) -> Vec<String> {
        m.render_lines(w, "me")
    }

    #[test]
    fn strip_removes_codes() {
        assert_eq!(
            strip_irc_format("\x02bold\x02 \x0304,12red\x03 x"),
            "bold red x"
        );
        assert_eq!(strip_irc_format("\x031,2a\x0f"), "a");
        assert_eq!(strip_irc_format("\x03123"), "3");
    }

    #[test]
    fn render_irc_basic() {
        assert_eq!(render_irc_format("hi"), "hi\x1b[0m");
        assert_eq!(render_irc_format("\x02b"), "\x1b[1mb\x1b[0m");
        assert_eq!(render_irc_format("\x034x"), "\x1b[91mx\x1b[0m");
        // Background ignored.
        assert_eq!(render_irc_format("\x034,12x"), "\x1b[91mx\x1b[0m");
        // ≥16 and bare ^C reset.
        assert_eq!(render_irc_format("\x0399x"), "\x1b[0mx\x1b[0m");
        assert_eq!(render_irc_format("\x03x"), "\x1b[0mx\x1b[0m");
        // ESC dropped.
        assert_eq!(render_irc_format("a\x1b]0;evil\x07b"), "a]0;evilb\x1b[0m");
    }

    #[test]
    fn wrap_basic() {
        assert_eq!(wrap_text("", 10), vec![""]);
        // A space that overflows a full line breaks at the PREVIOUS space (Go behaviour).
        assert_eq!(wrap_text("hello world foo", 11), vec!["hello", "world foo"]);
        assert_eq!(wrap_text("abcdefghij", 4), vec!["abcd", "efgh", "ij"]);
        assert_eq!(wrap_text("abc", 0), vec!["a", "b", "c"]);
    }

    #[test]
    fn wrap_wide_chars() {
        // Each ideograph is 2 cells.
        let lines = wrap_text("世界世界世界", 4);
        assert_eq!(lines, vec!["世界", "世界", "世界"]);
        for l in lines {
            assert!(string_width(&l) <= 4);
        }
    }

    #[test]
    fn wrap_preserves_all_text() {
        let text = "Lorem ipsum dolor sit amet, consectetur adipiscing elit";
        let joined = wrap_text(text, 12).join(" ");
        assert_eq!(joined, text);
    }

    #[test]
    fn urls_wrapped_once_and_punct_excluded() {
        let got = highlight_urls("see https://a.b/c, ok");
        assert_eq!(
            got,
            format!(
                "see {}{}https://a.b/c{}, ok",
                ANSI_CYAN, ANSI_BOLD, ANSI_RESET
            )
        );
        // URL after an ANSI code.
        let got = highlight_urls("\x1b[31mhttp://x.y\x1b[0m");
        assert!(got.contains(&format!(
            "{}{}http://x.y{}",
            ANSI_CYAN, ANSI_BOLD, ANSI_RESET
        )));
        // Trailing ESC does not panic.
        let _ = highlight_urls("abc\x1b");
        // Non-ASCII survives.
        assert_eq!(highlight_urls("žąsis"), "žąsis");
    }

    #[test]
    fn find_chunk_keeps_codes_and_handles_utf8() {
        let orig = "\x034begalingai\x03 šaunus žmogus";
        let plain = strip_irc_format(orig);
        let lines = wrap_text(&plain, 10);
        assert!(lines.len() > 1);
        let first = find_chunk(orig, &lines[0]);
        assert!(first.starts_with("\x034begalingai"));
        // Concatenating plain versions of all chunks reconstructs the text.
        let rebuilt: Vec<String> = lines
            .iter()
            .map(|l| strip_irc_format(&find_chunk(orig, l)))
            .collect();
        assert_eq!(rebuilt.join(" "), plain);
    }

    #[test]
    fn bounds_code_included_at_start() {
        let orig = "ab\x034cd";
        // plain = "abcd"; chunk "cd" starts after the colour code.
        let (s, e) = plain_to_original_bounds(orig, 2, 2);
        assert_eq!(&orig[s as usize..e as usize], "\x034cd");
        let (s, e) = plain_to_original_bounds(orig, 9, 1);
        assert_eq!((s, e), (-1, -1));
    }

    #[test]
    fn json_marshal_matches_go_shape() {
        let ts = Utc.with_ymd_and_hms(2026, 10, 21, 12, 30, 5).unwrap();
        let m = Message::new(ts, "al\x1bice", "\x02<hi>\x02 & bye", MsgType::Chat, true);
        assert_eq!(
            m.marshal_json(),
            r#"{"ts":"2026-10-21T12:30:05Z","nick":"alice","type":"chat","text":"\u003chi\u003e \u0026 bye","highlight":true}"#
        );
        let m = Message::new(ts, "", "", MsgType::Error, false);
        assert_eq!(
            m.marshal_json(),
            r#"{"ts":"2026-10-21T12:30:05Z","type":"error"}"#
        );
        let s = messages_to_json_indent(&[m]);
        assert!(s.starts_with("[\n  {\n    \"ts\""));
        assert_eq!(messages_to_json_indent(&[]), "[]");
    }

    #[test]
    fn go_time_layouts() {
        let t = Local.with_ymd_and_hms(2026, 3, 5, 14, 7, 9).unwrap();
        assert_eq!(format_go_time(&t, "15:04"), "14:07");
        assert_eq!(format_go_time(&t, "15:04:05"), "14:07:09");
        assert_eq!(format_go_time(&t, "2006-01-02"), "2026-03-05");
        assert_eq!(format_go_time(&t, "Jan _2 3:04PM"), "Mar  5 2:07PM");
        assert_eq!(
            format_go_time(&t, "Mon Monday January 06"),
            "Thu Thursday March 26"
        );
        assert_eq!(format_go_time(&t, "[%H] %"), "[%H] %");
        assert_eq!(format_go_time(&t, "15h04"), "14h07");
        assert_eq!(format_go_time(&t, "05.000"), "09.000");
        assert_eq!(format_go_time(&t, "Januarys"), "Marchs");
        assert_eq!(format_go_time(&t, "Jane"), "Jane");
        assert_eq!(format_go_time(&t, ""), "");
    }

    #[test]
    fn changed_day_and_sep() {
        let a = Local
            .with_ymd_and_hms(2026, 10, 21, 23, 59, 30)
            .unwrap()
            .with_timezone(&Utc);
        let b = Local
            .with_ymd_and_hms(2026, 10, 22, 0, 0, 5)
            .unwrap()
            .with_timezone(&Utc);
        let c = Local
            .with_ymd_and_hms(2026, 10, 21, 0, 1, 0)
            .unwrap()
            .with_timezone(&Utc);
        assert!(changed_day(&a, &b));
        assert!(!changed_day(&a, &c));
        assert_eq!(
            day_sep_line(&b),
            format!("{}── 2026-10-22 ──{}", ANSI_DARK_GRAY, ANSI_RESET)
        );
    }

    #[test]
    fn render_lines_chat_wraps_and_indents() {
        let _g = super::super::theme::TEST_LOCK.lock();
        super::super::theme::set_timestamps(false, "");
        super::super::theme::set_nick_colors(false);
        let ts = Utc.with_ymd_and_hms(2026, 10, 21, 12, 0, 0).unwrap();
        let m = Message::new(
            ts,
            "bob",
            "aaaa bbbb cccc dddd eeee ffff",
            MsgType::Chat,
            false,
        );
        // prefix "<bob> " = 6 cells; msg_w 20 → body width 14.
        let lines = plain_lines(&m, 20);
        assert_eq!(lines.len(), 3);
        assert!(lines[0].contains("<bob> "));
        assert!(lines[1].starts_with("      "));
        super::super::theme::set_timestamps(true, "15:04");
        super::super::theme::set_nick_colors(true);
    }

    #[test]
    fn render_lines_min_body_width_and_join() {
        let _g = super::super::theme::TEST_LOCK.lock();
        super::super::theme::set_timestamps(false, "");
        let ts = Utc.with_ymd_and_hms(2026, 10, 21, 12, 0, 0).unwrap();
        let m = Message::new(ts, "x\x1b", "", MsgType::Join, false);
        let lines = m.render_lines(5, "me");
        // msg_w 5 → body width clamped to 8 → "--> x prisijungė" wraps.
        assert!(lines.len() > 1);
        let plain: Vec<String> = lines.iter().map(|l| strip_ansi_for_test(l)).collect();
        // A word longer than the body is hard-broken; nothing is lost.
        assert_eq!(plain.concat().replace(' ', ""), "-->xprisijungė");
        super::super::theme::set_timestamps(true, "15:04");
    }
}
