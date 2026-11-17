//! Text and path sanitization ported from `ui.go` and `irc.go`.

const MAX_LINE_BYTES: usize = 512;

/// Terminal control codes that must not appear on screen (from `ui.go` `isControlRune`).
pub fn is_control_rune(r: char) -> bool {
    let u = r as u32;
    (u < 0x20 && r != '\t') || u == 0x7f || (0x80..=0x9f).contains(&u)
}

/// Drops C0 (except tab), DEL, and C1 (0x80–0x9f) from network/UI text (`sanitizeText`).
pub fn sanitize_text(s: &str) -> String {
    if !s.chars().any(is_control_rune) {
        return s.to_string();
    }
    s.chars().filter(|&r| !is_control_rune(r)).collect()
}

/// Makes a server/channel name safe as a directory component (`sanitizePath`).
pub fn sanitize_path(s: &str) -> String {
    if s.is_empty() {
        return String::new();
    }
    let mut out = s.replace(['/', '\\', ':', '*', '?', '"'], "_");
    while out.contains("..") {
        out = out.replace("..", "_");
    }
    if out.trim_matches('.').is_empty() {
        return "_".to_string();
    }
    out
}

/// Channel name safe for display/command targets (`sanitizeChanName`).
pub fn sanitize_chan_name(name: &str) -> String {
    const WS: &[char] = &[' ', '\t', '\r', '\n', '\x0b', '\x0c'];
    if name.contains(WS) {
        return String::new();
    }
    let safe = sanitize_text(name);
    if safe.is_empty() {
        return String::new();
    }
    const WS2: &[char] = &[' ', '\t', '\r', '\n'];
    if safe.contains(WS2) {
        return String::new();
    }
    safe
}

/// Truncate to 510 bytes on UTF-8 boundaries (`truncateIRC`, `maxLineBytes - 2` for CRLF).
pub fn truncate_irc(line: &str) -> String {
    const LIMIT: usize = MAX_LINE_BYTES - 2;
    if line.len() <= LIMIT {
        return line.to_string();
    }
    let mut cut = LIMIT;
    while cut > 0 && !line.is_char_boundary(cut) {
        cut -= 1;
    }
    line[..cut].to_string()
}
