//! IRC validation, casemapping, and wildcard helpers ported from `irc.go` / `ui.go`.

/// Default channel type prefixes when the server did not send CHANTYPES (`standartinėsChanTypes`).
pub const STANDARTINES_CHAN_TYPES: &str = "#&!+";

/// Default status prefix signs when PREFIX is not passed (`defaultPrefixSigns`).
pub const DEFAULT_PREFIX_SIGNS: &str = "~&@%+";

const MAX_MODE_PARAMS: usize = 4;

const NICK_EXTRA: &str = "_-[]\\`^{}|";

fn is_letter(r: char) -> bool {
    r.is_alphabetic()
}

fn contains_rune(haystack: &str, r: char) -> bool {
    haystack.contains(r)
}

// ---- Casemapping (`lowerIRC`, `asciiFold`, `foldEqualIRC`, `nickEqual`, `normNick`) ----

/// RFC 1459 casemapping (A–Z and `[]\^` ↔ `{}|~`).
pub fn lower_irc(r: char) -> char {
    match r {
        '[' => '{',
        ']' => '}',
        '\\' => '|',
        '^' => '~',
        _ if r.is_ascii_uppercase() => r.to_ascii_lowercase(),
        _ => r,
    }
}

/// ASCII-only case fold (bytes `A`–`Z` only; non-ASCII unchanged).
pub fn ascii_fold(s: &str) -> String {
    if !s.bytes().any(|b| b.is_ascii_uppercase()) {
        return s.to_string();
    }
    let mut b: Vec<u8> = s.bytes().collect();
    for byte in &mut b {
        if (*byte).is_ascii_uppercase() {
            *byte = byte.to_ascii_lowercase();
        }
    }
    String::from_utf8(b).unwrap_or_else(|_| s.to_string())
}

/// Nick equality without allocating folded strings.
pub fn fold_equal_irc(a: &str, b: &str, ascii: bool) -> bool {
    if ascii {
        if a.len() != b.len() {
            return false;
        }
        for (ca, cb) in a.bytes().zip(b.bytes()) {
            let mut ca = ca;
            let mut cb = cb;
            if ca.is_ascii_uppercase() {
                ca += b'a' - b'A';
            }
            if cb.is_ascii_uppercase() {
                cb += b'a' - b'A';
            }
            if ca != cb {
                return false;
            }
        }
        return true;
    }
    let mut ai = 0;
    let mut bi = 0;
    loop {
        let (ra, sa) = next_rune(a, ai);
        let (rb, sb) = next_rune(b, bi);
        if sa == 0 && sb == 0 {
            return true;
        }
        if sa == 0 || sb == 0 {
            return false;
        }
        if lower_irc(ra) != lower_irc(rb) {
            return false;
        }
        ai += sa;
        bi += sb;
    }
}

fn next_rune(s: &str, i: usize) -> (char, usize) {
    match s[i..].chars().next() {
        None => ('\0', 0),
        Some(c) => (c, c.len_utf8()),
    }
}

/// Compare nicks using server `CASEMAPPING` (`""` / `rfc1459` → RFC 1459; `ascii` → ASCII).
pub fn nick_equal(a: &str, b: &str, cm: &str) -> bool {
    if a.is_empty() || b.is_empty() {
        return a == b;
    }
    fold_equal_irc(a, b, cm == "ascii")
}

/// Strip one leading status prefix sign when present (`stripMode`).
pub fn strip_mode<'a>(nick: &'a str, signs: &str) -> &'a str {
    let signs = if signs.is_empty() {
        DEFAULT_PREFIX_SIGNS
    } else {
        signs
    };
    let Some(first) = nick.chars().next() else {
        return nick;
    };
    if contains_rune(signs, first) {
        let first_len = first.len_utf8();
        &nick[first_len..]
    } else {
        nick
    }
}

/// Normalized nick key for sets (`normNick`).
pub fn norm_nick(nick: &str, sign: &str, cm: &str) -> String {
    let bare = strip_mode(nick, sign);
    if cm == "ascii" {
        ascii_fold(bare)
    } else {
        bare.chars().map(lower_irc).collect()
    }
}

// ---- Wildcards (`wildEq`, `wildcardMatch`) ----

/// Iterative `*` / `?` matching (pattern and subject must already be same case).
pub fn wild_eq(p: &str, s: &str) -> bool {
    let pb = p.as_bytes();
    let sb = s.as_bytes();
    let mut pi = 0usize;
    let mut si = 0usize;
    let mut star: isize = -1;
    let mut si_save = 0usize;
    while si < sb.len() {
        if pi < pb.len() && pb[pi] == b'*' {
            star = pi as isize;
            si_save = si;
            pi += 1;
        } else if pi < pb.len() && (pb[pi] == b'?' || pb[pi] == sb[si]) {
            pi += 1;
            si += 1;
        } else if star != -1 {
            pi = (star + 1) as usize;
            si_save += 1;
            si = si_save;
        } else {
            return false;
        }
    }
    while pi < pb.len() && pb[pi] == b'*' {
        pi += 1;
    }
    pi == pb.len()
}

/// Simple `*` / `?` matching, case-insensitive (`wildcardMatch`).
pub fn wildcard_match(pattern: &str, s: &str) -> bool {
    wild_eq(&pattern.to_lowercase(), &s.to_lowercase())
}

// ---- Validators (from `irc.go`) ----

/// PRIVMSG/NOTICE target (nick or channel).
pub fn is_valid_target(t: &str) -> bool {
    if t.is_empty() || t.len() > 100 {
        return false;
    }
    for r in t.chars() {
        let ok = matches!(r, 'a'..='z' | 'A'..='Z' | '0'..='9')
            || matches!(r, '.' | '-' | '_' | '#' | '&' | '!' | '+')
            || is_letter(r);
        if !ok {
            return false;
        }
    }
    true
}

/// Channel name using default CHANTYPES.
pub fn is_valid_channel(ch: &str) -> bool {
    is_valid_channel_for(ch, STANDARTINES_CHAN_TYPES)
}

/// Channel name for a given CHANTYPES string.
pub fn is_valid_channel_for(ch: &str, chantypes: &str) -> bool {
    if ch.is_empty() || ch.len() > 50 {
        return false;
    }
    let chantypes = if chantypes.is_empty() {
        STANDARTINES_CHAN_TYPES
    } else {
        chantypes
    };
    let first = ch.as_bytes().first().copied().unwrap_or(0) as char;
    if !contains_rune(chantypes, first) {
        return false;
    }
    for (i, r) in ch.chars().enumerate() {
        let ok = matches!(r, 'a'..='z' | 'A'..='Z' | '0'..='9')
            || matches!(r, '.' | '-' | '_')
            || i == 0
            || is_letter(r);
        if !ok {
            return false;
        }
    }
    ch.len() > 1
}

/// JOIN channel key parameter.
pub fn is_valid_channel_key(key: &str) -> bool {
    if key.is_empty() {
        return true;
    }
    if key.len() > 64 {
        return false;
    }
    for r in key.chars() {
        if matches!(r, ' ' | '\t' | '\r' | '\n' | '\x0b' | '\x0c') {
            return false;
        }
        let u = r as u32;
        if u < 0x20 || u == 0x7f || (0x80..=0x9f).contains(&u) {
            return false;
        }
    }
    true
}

/// MODE command target (channel, nick, or `*server*` mask).
pub fn is_valid_mode_target(t: &str) -> bool {
    if t.is_empty() || t.len() > 100 {
        return false;
    }
    if let Some(rest) = t.strip_prefix('*') {
        return is_valid_mask_name(rest);
    }
    for r in t.chars() {
        let ok = matches!(r, 'a'..='z' | 'A'..='Z' | '0'..='9')
            || matches!(r, '.' | '-' | '_' | '#' | '&' | '!' | '+')
            || contains_rune(NICK_EXTRA, r)
            || is_letter(r);
        if !ok {
            return false;
        }
    }
    true
}

/// Server mask body (without leading `*`).
pub fn is_valid_mask_name(s: &str) -> bool {
    if s.is_empty() || s.len() > 100 {
        return false;
    }
    for r in s.chars() {
        let ok = matches!(r, 'a'..='z' | 'A'..='Z' | '0'..='9')
            || matches!(r, '.' | '-' | '_' | '*')
            || is_letter(r);
        if !ok {
            return false;
        }
    }
    true
}

/// MODE parameters after the target.
pub fn is_valid_mode_spec(fields: &[&str]) -> bool {
    if fields.len() > MAX_MODE_PARAMS {
        return false;
    }
    for f in fields {
        if f.is_empty() {
            return false;
        }
        if f.contains('\r') || f.contains('\n') {
            return false;
        }
    }
    true
}

/// WHO command target.
pub fn is_valid_who_target(t: &str) -> bool {
    if t.is_empty() || t.len() > 100 {
        return false;
    }
    if t == "*" {
        return true;
    }
    if let Some(mask) = t.strip_prefix('*') {
        return is_valid_mask_name(mask);
    }
    is_valid_mode_target(t)
}

/// LIST command filter.
pub fn is_valid_list_filter(f: &str) -> bool {
    if f.is_empty() {
        return true;
    }
    if f.len() > 64 {
        return false;
    }
    for r in f.chars() {
        let ok = matches!(r, 'a'..='z' | 'A'..='Z' | '0'..='9')
            || matches!(r, '.' | '-' | '_' | '#' | '&' | '!' | '+' | '*')
            || is_letter(r);
        if !ok {
            return false;
        }
    }
    true
}

/// Server hostname or IPv6 literal (not bracketed host:port form).
pub fn is_valid_server(server: &str) -> bool {
    if server.is_empty() || server.len() > 100 {
        return false;
    }
    if server.contains("..") {
        return false;
    }
    if server.contains(':') {
        return is_valid_ipv6(server);
    }
    for r in server.chars() {
        let ok = matches!(r, 'a'..='z' | 'A'..='Z' | '0'..='9') || matches!(r, '.' | '-' | '_');
        if !ok {
            return false;
        }
    }
    true
}

fn split_ipv6_groups(s: &str) -> Vec<String> {
    if s.is_empty() {
        return Vec::new();
    }
    s.split(':')
        .filter(|g| !g.is_empty())
        .map(ToString::to_string)
        .collect()
}

/// IPv6 address form (no `[]`, no zone ID `%`).
pub fn is_valid_ipv6(s: &str) -> bool {
    if s.contains(['[', ']', '%']) {
        return false;
    }
    if s.matches("::").count() > 1 {
        return false;
    }
    let groups = if let Some(i) = s.find("::") {
        let mut groups = split_ipv6_groups(&s[..i]);
        groups.extend(split_ipv6_groups(&s[i + 2..]));
        if groups.len() > 7 {
            return false;
        }
        groups
    } else {
        let groups = split_ipv6_groups(s);
        if groups.len() != 8 {
            return false;
        }
        groups
    };
    for g in &groups {
        if g.is_empty() || g.len() > 4 {
            return false;
        }
        for r in g.chars() {
            if !r.is_ascii_hexdigit() {
                return false;
            }
        }
    }
    true
}

/// IRC nick (RFC symbols + Unicode letters, max 30 runes).
pub fn is_valid_nick(nick: &str) -> bool {
    if nick.is_empty() {
        return false;
    }
    if nick.chars().count() > 30 {
        return false;
    }
    for r in nick.chars() {
        let ok = matches!(r, 'a'..='z' | 'A'..='Z' | '0'..='9')
            || contains_rune(NICK_EXTRA, r)
            || is_letter(r);
        if !ok {
            return false;
        }
    }
    true
}

/// TCP port string `1`–`65535`.
pub fn is_valid_port(port: &str) -> bool {
    if port.is_empty() || port.len() > 5 {
        return false;
    }
    for r in port.chars() {
        if !r.is_ascii_digit() {
            return false;
        }
    }
    matches!(port.parse::<u32>(), Ok(n) if (1..=65535).contains(&n))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn valid_nick_accepts_lithuanian_and_rfc_symbols() {
        let valid = [
            "jonas",
            "Žilvinas",
            "ąžuolas",
            "nick[1]",
            "|pipe",
            &"ą".repeat(30),
        ];
        for n in valid {
            assert!(is_valid_nick(n), "is_valid_nick({n:?}) should be true");
        }
    }

    #[test]
    fn valid_nick_rejects_length_and_injection() {
        assert!(!is_valid_nick(&"a".repeat(31)));
        assert!(!is_valid_nick(&"ą".repeat(31)));
        assert!(is_valid_nick(&"ą".repeat(16)));
        for n in ["jo nas", "jo\rnas", "jo:nas", ""] {
            assert!(!is_valid_nick(n), "is_valid_nick({n:?}) should be false");
        }
    }

    #[test]
    fn casemapping_ascii_vs_rfc1459() {
        assert!(nick_equal("Bob", "bob", "ascii"));
        assert!(!nick_equal("Žilvinas", "žilvinas", "ascii"));
        assert!(nick_equal("[a]", "{a}", "rfc1459"));
        assert!(!nick_equal("[a]", "{a}", "ascii"));
        assert_eq!(norm_nick("[a]", "@", "ascii"), "[a]");
        assert_eq!(norm_nick("{a}", "@", "ascii"), "{a}");
        assert_eq!(norm_nick("Bob", "", "ascii"), "bob");
        assert_eq!(norm_nick("[a]", "", ""), "{a}");
    }

    #[test]
    fn valid_channel_requires_name_after_prefix() {
        assert!(is_valid_channel("#test"));
        assert!(!is_valid_channel("#"));
    }
}
