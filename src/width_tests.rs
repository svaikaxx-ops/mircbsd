//! Pločio skaičiavimo testai: CJK, emoji, ZWJ, derinamieji ženklai,
//! vėliavos, odos tonai + savybės (nukirpimas/užpildymas niekada neviršija).

use super::*;
use crate::test_util::Rng;

#[test]
fn control_chars_have_zero_width() {
    for c in ['\0', '\x01', '\x1b', '\x7f', '\u{85}', '\u{9f}'] {
        assert_eq!(rune_width(c), 0, "{c:?}");
    }
    assert_eq!(rune_width('\u{a0}'), 1);
}

#[test]
fn east_asian_wide_and_fullwidth() {
    for c in [
        '中',
        '한',
        'あ',
        'カ',
        'Ａ',
        '\u{3000}',
        '\u{20000}',
        '\u{1100}',
    ] {
        assert_eq!(rune_width(c), 2, "{c:?} U+{:04X}", c as u32);
    }
    // Pusės pločio katakana ir lotyniškos — 1.
    for c in ['ｱ', 'a', 'ą', 'Ж', 'λ'] {
        assert_eq!(rune_width(c), 1, "{c:?}");
    }
}

#[test]
fn range_boundaries_are_inclusive() {
    // Kiekvieno intervalo pirmas ir paskutinis taškas viduje, kaimynai — ne.
    for &(lo, hi) in WIDE_RANGES {
        for cp in [lo, hi] {
            if let Some(c) = char::from_u32(cp) {
                assert!(in_ranges(c, WIDE_RANGES), "U+{cp:04X}");
            }
        }
    }
    for w in WIDE_RANGES.windows(2) {
        if w[0].1 + 1 < w[1].0 {
            let gap = char::from_u32(w[0].1 + 1).unwrap();
            assert!(!in_ranges(gap, WIDE_RANGES), "U+{:04X}", gap as u32);
        }
    }
    assert!(!in_ranges('a', &[]));
    assert!(!in_ranges('\0', WIDE_RANGES));
    assert!(!in_ranges('\u{10FFFF}', WIDE_RANGES));
}

#[test]
fn combining_marks_join_previous_cluster() {
    assert_eq!(rune_clusters("e\u{301}x"), vec!["e\u{301}", "x"]);
    assert_eq!(string_width("e\u{301}\u{302}"), 1);
    // Ne lentelėje esantis derinamasis ženklas (Mn) atpažįstamas per kategoriją.
    assert_eq!(rune_width('\u{0bcd}'), 0);
    assert_eq!(string_width("த\u{0bcd}"), 1);
}

#[test]
fn zero_width_format_chars() {
    for c in [
        '\u{200b}', '\u{200c}', '\u{200e}', '\u{2060}', '\u{feff}', '\u{fe0f}',
    ] {
        assert_eq!(rune_width(c), 0, "U+{:04X}", c as u32);
    }
}

#[test]
fn zwj_sequences_are_one_wide_cluster() {
    let family = "👨\u{200d}👩\u{200d}👧\u{200d}👦";
    assert_eq!(rune_clusters(family).len(), 1);
    assert_eq!(string_width(family), 2);
    let rainbow = "🏳\u{fe0f}\u{200d}🌈";
    assert_eq!(rune_clusters(rainbow).len(), 1);
    assert_eq!(string_width(rainbow), 2);
    assert_eq!(string_width(&format!("a{family}b")), 4);
}

#[test]
fn flags_pair_regional_indicators() {
    let lt = "🇱🇹";
    assert_eq!(rune_clusters(lt).len(), 1);
    assert_eq!(string_width(lt), 2);
    assert_eq!(rune_clusters("🇱🇹🇺🇸").len(), 2);
    assert_eq!(string_width("🇱🇹🇺🇸"), 4);
    // Nesuporuotas (nelyginis) rodiklis — atskiras klasteris.
    let odd = rune_clusters("🇱🇹🇺");
    assert_eq!(odd.len(), 2);
    assert_eq!(string_width("🇱🇹🇺"), 4);
    assert!(is_regional_indicator('\u{1f1e6}') && !is_regional_indicator('A'));
}

#[test]
fn skin_tone_modifier_attaches_to_emoji() {
    let thumbs = "👍\u{1f3fd}";
    assert_eq!(rune_clusters(thumbs).len(), 1);
    assert_eq!(string_width(thumbs), 2);
    assert_eq!(rune_clusters("👍\u{1f3fd}x"), vec![thumbs, "x"]);
}

#[test]
fn keycap_sequence_is_one_cluster() {
    let k = "1\u{fe0f}\u{20e3}";
    assert_eq!(rune_clusters(k).len(), 1);
}

#[test]
fn cluster_width_of_empty_is_zero() {
    assert_eq!(cluster_width(""), 0);
    assert_eq!(clusters_width(&[]), 0);
    assert!(rune_clusters("").is_empty());
}

#[test]
fn truncate_width_edges() {
    assert_eq!(truncate_width("abc", 0), "");
    assert_eq!(truncate_width("abc", -3), "");
    assert_eq!(truncate_width("abc", 3), "abc");
    assert_eq!(truncate_width("abcd", 3), "ab…");
    assert_eq!(truncate_width("中中", 3), "中…");
    assert_eq!(truncate_width("中中", 2), "…");
    assert_eq!(truncate_width("中", 1), "…");
    // Klasteris neperkertamas: e+akūtas lieka kartu.
    assert_eq!(truncate_width("e\u{301}e\u{301}e\u{301}", 2), "e\u{301}…");
}

#[test]
fn pad_and_fit_edges() {
    assert_eq!(pad_right("ab", 4), "ab  ");
    assert_eq!(pad_right("abcdef", 4), "abcdef");
    assert_eq!(pad_right("中", 3), "中 ");
    assert_eq!(fit_width("abc", 0), "");
    assert_eq!(fit_width("abcdef", 4), "abc…");
    assert_eq!(fit_width("中", 4), "中  ");
}

#[test]
fn truncate_and_fit_property() {
    let mut r = Rng::new(0x3d1d);
    for _ in 0..20_000 {
        let s = r.interesting_string(24);
        let max = r.below(30) as i32;
        let t = truncate_width(&s, max);
        assert!(string_width(&t) <= max.max(0), "{s:?} max={max} → {t:?}");
        if max <= 0 {
            assert_eq!(t, "");
        } else if string_width(&s) <= max {
            assert_eq!(t, s);
        } else {
            assert!(t.ends_with('…'), "{s:?} → {t:?}");
            // Prieš „…“ — originalo pradžia (klasteriai neperpjauti).
            let body = t.strip_suffix('…').unwrap();
            assert!(s.starts_with(body), "{s:?} → {t:?}");
        }
        let f = fit_width(&s, max);
        assert_eq!(string_width(&f), max.max(0), "fit {s:?} {max} → {f:?}");
    }
}

#[test]
fn string_width_is_additive_over_clusters() {
    let mut r = Rng::new(0xadd);
    for _ in 0..20_000 {
        let s = r.interesting_string(20);
        let cl = rune_clusters(&s);
        assert_eq!(cl.concat(), s, "klasteriai nepraranda simbolių");
        assert!(cl.iter().all(|c| !c.is_empty()));
        assert_eq!(string_width(&s), clusters_width(&cl));
        // Klasteris niekada platesnis už jo simbolių sumą (be ZWJ — ≤ 2).
        for c in &cl {
            let w = cluster_width(c);
            let sum: i32 = c.chars().map(rune_width).sum();
            assert!((0..=sum.max(2)).contains(&w), "{c:?} → {w}");
            if !c.contains('\u{200d}') {
                assert!(w <= 2, "{c:?} → {w}");
            }
        }
        assert!(string_width(&s) >= 0);
    }
}

#[test]
fn zwj_between_non_emoji_is_not_one_glyph() {
    // Terminalas „a‍中“ rodo 3 ląstelėmis — anksčiau skaičiuota 2, todėl
    // priešiška žinutė su daug tokių porų perpildydavo eilutę.
    assert_eq!(string_width("a\u{200d}中"), 3);
    assert_eq!(string_width("文\u{200d} "), 3);
    assert_eq!(string_width(&"a\u{200d}中".repeat(10)), 30);
    assert_eq!(string_width("\u{200d}"), 0);
    // Tikros emoji ZWJ sekos lieka 2.
    assert_eq!(string_width("❤\u{fe0f}\u{200d}🔥"), 2);
    assert_eq!(string_width("🧑\u{200d}💻"), 2);
}
