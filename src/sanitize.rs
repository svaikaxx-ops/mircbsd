//! Text and path sanitization ported from `ui.go` and `irc.go`.

const MAX_LINE_BYTES: usize = 512;

/// Terminal control codes that must not appear on screen (from `ui.go` `isControlRune`).
///
/// Besides C0 (except tab), DEL and C1 this also drops the Unicode bidi
/// embeddings/overrides (U+202A–U+202E) and isolates (U+2066–U+2069): on a
/// bidi-capable terminal an unterminated RLO from a nick or message reorders
/// the rest of the row, so network text could disguise who said what.
pub fn is_control_rune(r: char) -> bool {
    let u = r as u32;
    (u < 0x20 && r != '\t') || u == 0x7f || (0x80..=0x9f).contains(&u) || is_bidi_control(u)
}

/// Bidi embedding/override/isolate controls (not the harmless LRM/RLM marks).
fn is_bidi_control(u: u32) -> bool {
    (0x202A..=0x202E).contains(&u) || (0x2066..=0x2069).contains(&u)
}

/// Can this URL be handed to an external opener (`/open`) as is?
///
/// The user sees the SANITISED text, the opener gets the raw string, so a
/// URL with control, bidi or invisible (zero-width, soft hyphen, BOM)
/// characters would open something other than what was shown. A leading
/// `-` would be parsed as an option by the opener.
pub fn is_safe_url(url: &str) -> bool {
    !url.is_empty()
        && !url.starts_with('-')
        && !url.chars().any(|r| {
            let u = r as u32;
            r.is_whitespace()
                || is_control_rune(r)
                || u == 0x00AD
                || (0x200B..=0x200F).contains(&u)
                || (0x2060..=0x2064).contains(&u)
                || u == 0xFEFF
        })
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bidi_overrides_and_isolates_are_dropped() {
        // RLO/LRO/embeddings/PDF and the isolates reorder the rest of the
        // terminal row (nick panel included) on bidi-capable terminals —
        // display spoofing ("<admin> " that is really someone else).
        let s =
            "a\u{202A}b\u{202B}c\u{202C}d\u{202D}e\u{202E}f\u{2066}g\u{2067}h\u{2068}i\u{2069}j";
        assert_eq!(sanitize_text(s), "abcdefghij");
        assert_eq!(sanitize_chan_name("#x\u{202E}y"), "#xy");
        // Chat text goes through render_irc_format, which shares the filter.
        assert!(!crate::ui::render_irc_format("a\u{202E}b").contains('\u{202E}'));
        // Plain marks (LRM/RLM) do not reorder anything outside — kept.
        assert_eq!(sanitize_text("a\u{200E}b\u{200F}"), "a\u{200E}b\u{200F}");
        // Regular RTL text stays intact.
        assert_eq!(sanitize_text("שלום"), "שלום");
    }

    #[test]
    fn c0_c1_and_del_are_dropped() {
        assert_eq!(
            sanitize_text("a\x1b]52;c;ZXZpbA==\x07b\u{9b}c\x7fd\te"),
            "a]52;c;ZXZpbA==bcd\te"
        );
    }
}
