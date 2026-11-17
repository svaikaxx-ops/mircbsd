//! Valymo funkcijų testai: kiekviena valdymo simbolių klasė + savybės.

use super::*;
use crate::test_util::Rng;

/// Unikodo „Cc“ kategorija = C0 + DEL + C1 — tai, ko terminalas neturi gauti.
fn is_cc(c: char) -> bool {
    let u = c as u32;
    u < 0x20 || (0x7f..=0x9f).contains(&u)
}

#[test]
fn every_c0_except_tab_is_control() {
    for u in 0u32..0x20 {
        let c = char::from_u32(u).unwrap();
        assert_eq!(is_control_rune(c), c != '\t', "U+{u:04X}");
    }
}

#[test]
fn del_and_every_c1_are_control() {
    for u in 0x7fu32..=0x9f {
        assert!(is_control_rune(char::from_u32(u).unwrap()), "U+{u:04X}");
    }
}

#[test]
fn printable_neighbours_are_not_control() {
    for c in [' ', '~', '\u{a0}', '\u{a1}', 'ą', '中', '😀', '\u{200d}'] {
        assert!(!is_control_rune(c), "{c:?}");
    }
}

#[test]
fn sanitize_strips_terminal_escape_introducers() {
    // CSI (ESC [ ir 8 bitų 0x9b), OSC (ESC ] ... BEL), DCS (0x90), ST (0x9c).
    assert_eq!(sanitize_text("a\x1b[2Jb"), "a[2Jb");
    assert_eq!(sanitize_text("a\u{9b}31mb"), "a31mb");
    assert_eq!(sanitize_text("\x1b]52;c;ZXZpbA==\x07x"), "]52;c;ZXZpbA==x");
    assert_eq!(sanitize_text("\u{90}q\u{9c}"), "q");
    assert_eq!(sanitize_text("cr\rlf\nbs\x08nul\0"), "crlfbsnul");
    assert_eq!(sanitize_text("tab\tkeeps"), "tab\tkeeps");
}

#[test]
fn sanitize_is_identity_on_clean_text() {
    for s in ["", "labas", "ąčęėįšųūž", "中文 😀 👨‍👩‍👧", "e\u{301}"] {
        assert_eq!(sanitize_text(s), s);
    }
}

#[test]
fn sanitize_text_property() {
    let mut r = Rng::new(0x5a17);
    for _ in 0..30_000 {
        let s = r.interesting_string(40);
        let out = sanitize_text(&s);
        assert!(
            !out.chars().any(|c| is_cc(c) && c != '\t'),
            "{s:?} → {out:?}"
        );
        // Visi ne valdymo simboliai išlieka ta pačia tvarka.
        let kept: String = s.chars().filter(|c| !is_control_rune(*c)).collect();
        assert_eq!(out, kept);
        // Idempotentiška.
        assert_eq!(sanitize_text(&out), out);
    }
}

#[test]
fn sanitize_path_blocks_traversal_and_separators() {
    assert_eq!(sanitize_path(""), "");
    assert_eq!(sanitize_path("."), "_");
    assert_eq!(sanitize_path(".."), "_");
    assert_eq!(sanitize_path("...."), "__");
    assert_eq!(sanitize_path("../../etc/passwd"), "____etc_passwd");
    assert_eq!(sanitize_path("a\\b:c*d?e\"f"), "a_b_c_d_e_f");
    assert_eq!(sanitize_path("#kanalas"), "#kanalas");
    assert_eq!(sanitize_path("irc.libera.chat"), "irc.libera.chat");
}

#[test]
fn sanitize_path_property() {
    let mut r = Rng::new(0x9a7);
    for _ in 0..30_000 {
        let s = r.interesting_string(16);
        let out = sanitize_path(&s);
        if s.is_empty() {
            assert!(out.is_empty());
            continue;
        }
        assert!(!out.is_empty(), "{s:?}");
        assert!(!out.contains('/') && !out.contains('\\'), "{s:?} → {out:?}");
        assert!(!out.contains(".."), "{s:?} → {out:?}");
        assert!(out != "." && out != "..", "{s:?}");
        let joined = std::path::Path::new("/base").join(&out);
        assert_eq!(
            joined.parent(),
            Some(std::path::Path::new("/base")),
            "{out:?}"
        );
    }
}

#[test]
fn chan_name_rejects_whitespace_and_strips_controls() {
    assert_eq!(sanitize_chan_name("#ok"), "#ok");
    assert_eq!(sanitize_chan_name("#a b"), "");
    for ws in ['\t', '\r', '\n', '\x0b', '\x0c'] {
        assert_eq!(sanitize_chan_name(&format!("#a{ws}b")), "", "{ws:?}");
    }
    assert_eq!(sanitize_chan_name("#a\x07b\x1b"), "#ab");
    assert_eq!(sanitize_chan_name("#a\u{85}b"), "#ab");
    assert_eq!(sanitize_chan_name("\x01\x02"), "");
    assert_eq!(sanitize_chan_name(""), "");
}

#[test]
fn chan_name_property() {
    let mut r = Rng::new(0xc4a7);
    for _ in 0..30_000 {
        let s = r.interesting_string(12);
        let out = sanitize_chan_name(&s);
        assert!(!out.chars().any(is_cc), "{s:?} → {out:?}");
        assert!(!out.contains(' '), "{s:?} → {out:?}");
    }
}

#[test]
fn truncate_irc_limits_and_boundaries() {
    assert_eq!(truncate_irc("abc"), "abc");
    let exact = "a".repeat(510);
    assert_eq!(truncate_irc(&exact), exact);
    assert_eq!(truncate_irc(&"a".repeat(511)).len(), 510);
    // 509 ASCII + „ą“ (2 baitai) — pjauti tarp simbolių, ne per vidurį.
    let s = format!("{}ą", "a".repeat(509));
    assert_eq!(truncate_irc(&s), "a".repeat(509));
}

#[test]
fn truncate_irc_property() {
    let mut r = Rng::new(0x510);
    for _ in 0..3_000 {
        let s = r.interesting_string(600);
        let out = truncate_irc(&s);
        assert!(out.len() <= 510);
        assert!(s.starts_with(&out));
        if s.len() <= 510 {
            assert_eq!(out, s);
        } else {
            // Ne daugiau nei 3 baitai prarasti dėl UTF-8 ribos.
            assert!(out.len() >= 507, "{}", out.len());
        }
    }
}
