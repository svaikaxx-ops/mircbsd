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

#[test]
fn wrap_text_never_exceeds_width_mixed_scripts() {
    // Deterministinis „atsitiktinis“ tekstas: tarpai, lietuviškos raidės,
    // platūs CJK, emoji, jungiamieji ženklai.
    let alphabet = [" ", "a", "ž", "界", "😀", "e\u{301}", " ", "b"];
    let mut seed: u32 = 12345;
    for _ in 0..400 {
        let mut s = String::new();
        let len = (seed % 40) as usize + 1;
        for _ in 0..len {
            seed = seed.wrapping_mul(1_103_515_245).wrapping_add(12345);
            s.push_str(alphabet[(seed >> 16) as usize % alphabet.len()]);
        }
        for width in 1..12 {
            let lines = wrap_text(&s, width);
            for l in &lines {
                let w = crate::width::string_width(l);
                let single = crate::width::rune_clusters(l).len() == 1;
                assert!(w <= width || single, "{s:?} w={width}: {l:?} ({w})");
            }
            // Nieko neprarandama (išskyrus tarpus lūžio vietose).
            assert_eq!(
                lines.concat().replace(' ', ""),
                s.replace(' ', ""),
                "{s:?} w={width}"
            );
        }
    }
}
