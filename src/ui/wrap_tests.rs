//! Apvyniojimo ir eilučių atvaizdavimo savybių testai: jokia eilutė
//! neviršija pločio, tekstas neprarandamas, niekas nepanikuoja.

use super::*;
use crate::test_util::Rng;
use chrono::Utc;

fn visible(s: &str) -> String {
    // ESC [ ... galutinis baitas — pašalinam (tik CSI, kitų render nekuria).
    let mut out = String::new();
    let mut it = s.chars().peekable();
    while let Some(c) = it.next() {
        if c == '\x1b' {
            if it.peek() == Some(&'[') {
                it.next();
                for n in it.by_ref() {
                    if ('@'..='~').contains(&n) {
                        break;
                    }
                }
            }
        } else {
            out.push(c);
        }
    }
    out
}

fn no_spaces(s: &str) -> String {
    s.chars().filter(|c| *c != ' ').collect()
}

#[test]
fn wrap_space_at_line_start_does_not_overflow() {
    // Tarpas po kieto lūžio atsiduria eilutės pradžioje; kai tada
    // nebetelpa platus simbolis, likutis + simbolis viršydavo plotį.
    let lines = wrap_text("abcde fghi中", 5);
    for l in &lines {
        assert!(string_width(l) <= 5, "{lines:?}");
    }
    assert!(lines.iter().all(|l| !l.is_empty()), "{lines:?}");
    assert_eq!(no_spaces(&lines.concat()), "abcdefghi中");
}

#[test]
fn wrap_wide_chars_with_spaces() {
    let lines = wrap_text("中文 中文中文 中", 6);
    for l in &lines {
        assert!(string_width(l) <= 6, "{lines:?}");
    }
    assert_eq!(no_spaces(&lines.concat()), "中文中文中文中");
}

#[test]
fn wrap_cluster_wider_than_width_is_kept() {
    assert_eq!(wrap_text("中", 1), vec!["中"]);
    assert_eq!(wrap_text("a中b", 1), vec!["a", "中", "b"]);
}

#[test]
fn wrap_never_splits_clusters() {
    let fam = "👨\u{200d}👩\u{200d}👧";
    let text = format!("{fam}{fam}{fam} e\u{301}e\u{301}");
    for w in 1..8 {
        for l in wrap_text(&text, w) {
            for cl in rune_clusters(&l) {
                assert!(
                    cl == fam || cl == "e\u{301}" || cl == " " || cl == "e\u{301} ",
                    "w={w} {l:?} → {cl:?}"
                );
            }
        }
    }
}

#[test]
fn wrap_property_width_and_content() {
    let mut r = Rng::new(0x77a9);
    for _ in 0..20_000 {
        let mut text = r.interesting_string(60);
        // Apvyniojamas jau išvalytas tekstas (render_lines eina per
        // strip_irc_format/sanitize_text) — valdymo simbolių nebūna.
        text.retain(|c| !crate::sanitize::is_control_rune(c) && c != '\t');
        let w = 1 + r.below(20) as i32;
        let lines = wrap_text(&text, w);
        assert!(!lines.is_empty());
        for l in &lines {
            let lw = string_width(l);
            // Vienintelė išimtis: vienas klasteris platesnis už plotį.
            assert!(
                lw <= w || rune_clusters(l).len() == 1,
                "w={w} {text:?} → {lines:?}"
            );
        }
        // Prarandami tik tarpai lūžių vietose.
        assert_eq!(
            no_spaces(&lines.concat()),
            no_spaces(&text),
            "{text:?} w={w}"
        );
    }
}

#[test]
fn render_lines_never_exceed_message_width() {
    let mut r = Rng::new(0x4e4d);
    let kinds = [
        MsgType::Chat,
        MsgType::Action,
        MsgType::Notice,
        MsgType::Server,
        MsgType::Error,
        MsgType::Join,
        MsgType::Part,
        MsgType::Quit,
        MsgType::Nick,
    ];
    for _ in 0..4_000 {
        let text = r.interesting_string(150);
        let nick: String = (0..1 + r.below(10))
            .map(|_| *r.pick(&['a', 'ž', 'Z', '_']))
            .collect();
        let kind = *r.pick(&kinds);
        let m = Message::new(Utc::now(), &nick, &text, kind, r.below(4) == 0);
        // Plotis su atsarga prefiksui (laikas + <nick> + body ≥ 8).
        let w = 30 + r.below(90) as i32;
        let lines = m.render_lines(w, "me");
        assert!(!lines.is_empty());
        for l in &lines {
            let v = visible(l);
            assert!(
                !v.chars()
                    .any(|c| (c as u32) < 0x20 || (0x7f..=0x9f).contains(&(c as u32))),
                "valdymo simbolis ekrane: {text:?} → {v:?}"
            );
            assert!(string_width(&v) <= w, "w={w} {kind:?} {text:?} → {v:?}");
        }
    }
}

#[test]
fn tab_is_rendered_as_space() {
    // Tab plotis skaičiuojamas 0, o terminalas šoka iki 8 ląstelių.
    let m = Message::new(Utc::now(), "n\tk", "a\tb", MsgType::Chat, false);
    for l in m.render_lines(40, "me") {
        assert!(!l.contains('\t'), "{l:?}");
        assert!(visible(&l).contains("a b"), "{l:?}");
    }
    let m = Message::new(Utc::now(), "n", "x\ty", MsgType::Server, false);
    assert!(visible(&m.render_lines(40, "me")[0]).contains("x y"));
}
