use super::*;
use crate::ui::message::{Message, MsgType};
use chrono::{TimeZone, Utc};

#[test]
fn clamp_status_fits_width() {
    let right = STATUS_RIGHT;
    let left = format!(" mircBSD | nick @ serveris | {} ", "Ž".repeat(80));
    let w = 100;
    let (got, pad) = clamp_status_left(&left, right, w);
    assert!(string_width(&got) + pad + string_width(right) <= w);
    assert_ne!(got, left);
    // Short left is padded to fill the row.
    let (got, pad) = clamp_status_left(" a ", right, w);
    assert_eq!(got, " a ");
    assert_eq!(string_width(&got) + pad + string_width(right), w);
    // Narrow width never panics / goes negative.
    let (got, pad) = clamp_status_left("abc", right, 5);
    assert_eq!((got.as_str(), pad), ("", 0));
}

#[test]
fn widths_and_rows() {
    assert_eq!(msg_width(100, false), 100 - 18 - 2);
    assert_eq!(msg_width(100, true), 100 - 18 - 2 - 17);
    assert_eq!(msg_width(30, true), 10);
    assert_eq!(msg_area_top(false, false), 2);
    assert_eq!(msg_area_top(true, true), 4);
}

#[test]
fn cache_is_incremental_and_inserts_day_separators() {
    let _g = crate::ui::theme::TEST_LOCK.lock();
    let mut ch = Channel::new("#t", 0);
    let t1 = chrono::Local
        .with_ymd_and_hms(2026, 10, 21, 23, 0, 0)
        .unwrap()
        .with_timezone(&Utc);
    let t2 = chrono::Local
        .with_ymd_and_hms(2026, 10, 22, 0, 30, 0)
        .unwrap()
        .with_timezone(&Utc);
    ch.add_msg(Message::new(t1, "a", "one", MsgType::Chat, false));
    update_render_cache(&mut ch, 60, "me");
    assert_eq!(ch.rendered.len(), 1);
    assert_eq!(ch.rendered_msg_count, 1);
    let first = ch.rendered[0].clone();

    ch.add_msg(Message::new(t2, "a", "two", MsgType::Chat, false));
    update_render_cache(&mut ch, 60, "me");
    // old line + separator + new line
    assert_eq!(ch.rendered.len(), 3);
    assert_eq!(ch.rendered[0], first);
    assert!(ch.rendered[1].contains("2026-10-22"));
    assert_eq!(ch.rendered_msg_count, 2);

    // Valid cache → untouched.
    let snapshot = ch.rendered.clone();
    update_render_cache(&mut ch, 60, "me");
    assert_eq!(ch.rendered, snapshot);

    // Width change → full re-render (same result length here).
    update_render_cache(&mut ch, 70, "me");
    assert_eq!(ch.rendered_w, 70);
    assert_eq!(ch.rendered.len(), 3);

    // Theme invalidation → rendered_gen = -1 and empty cache.
    crate::ui::theme::invalidate_render_caches([&mut ch]);
    assert_eq!(ch.rendered_gen, -1);
    assert!(ch.rendered.is_empty());
    update_render_cache(&mut ch, 70, "me");
    assert_eq!(ch.rendered.len(), 3);
}

#[test]
fn history_freeze() {
    let mut ch = Channel::new("#t", 0);
    // First draw: nothing to compensate.
    assert_eq!(apply_history_freeze(&mut ch, false, 100, 10, false), 10);
    assert_eq!(ch.draw_line_count, 100);
    // 5 new lines while scrolled up → offset grows.
    assert_eq!(apply_history_freeze(&mut ch, true, 105, 10, false), 15);
    // Chasing the bottom → no freeze.
    assert_eq!(apply_history_freeze(&mut ch, true, 110, 15, true), 15);
    // At the bottom → never frozen.
    assert_eq!(apply_history_freeze(&mut ch, true, 120, 0, false), 0);
}

fn params<'a>(
    channels: &'a [ChanState],
    nicks: &'a [String],
    lines: &'a [String],
    input: &'a [char],
) -> FrameParams<'a> {
    FrameParams {
        width: 80,
        height: 24,
        channels,
        active_idx: 0,
        active_name: "#rust",
        topic: "hello \x02topic\x02",
        nicks,
        prefix_signs: "~&@%+",
        show_nicks: true,
        lines,
        input,
        input_pos: input.len(),
        scroll_off: 0,
        nick_scroll_off: 0,
        chan_scroll_off: 0,
        connected: true,
        my_nick: "me",
        server: "irc.example.org",
        away: "",
        use_tls: true,
        search_mode: false,
        search_query: "",
    }
}

#[test]
fn frame_basic_structure() {
    let channels = vec![
        ChanState {
            name: "#rust".into(),
            ..Default::default()
        },
        ChanState {
            name: "bob".into(),
            unread: 3,
            highlight: 1,
            is_private: true,
        },
        ChanState {
            name: "#go".into(),
            unread: 7,
            ..Default::default()
        },
    ];
    let nicks: Vec<String> = vec!["@op".into(), "+v".into(), "plain".into()];
    let lines: Vec<String> = (0..50).map(|i| format!("line {i}")).collect();
    let input: Vec<char> = "hi\x1b there".chars().collect();
    let mut p = params(&channels, &nicks, &lines, &input);
    let f = draw_frame(&p).unwrap();
    assert_eq!(f.msg_area_top, 3); // status + topic
    let o = &f.output;
    assert!(o.starts_with(ANSI_HIDE_CUR));
    assert!(o.ends_with(ANSI_SHOW_CUR));
    assert!(o.contains("\x1b]0;mircBSD — #rust (10)\x07"));
    assert!(o.contains("[TLS]"));
    assert!(o.contains("hello topic"));
    assert!(o.contains("1:#rust"));
    assert!(o.contains("2:>bob"));
    assert!(o.contains("!1"));
    assert!(o.contains("3:#go"));
    assert!(o.contains("line 49"));
    assert!(!o.contains("line 0"));
    assert!(o.contains(" Nick"));
    // ESC in the input line is stripped.
    assert!(o.contains("hi there"));
    assert!(!o.contains("hi\x1b there"));

    // Scrolled: banner is present and view shifted.
    p.scroll_off = 5;
    let f = draw_frame(&p).unwrap();
    assert!(f.output.contains("SCROLL (+5"));
    assert!(!f.output.contains("line 49"));

    // Too small / bad active idx → None.
    p.width = 39;
    assert!(draw_frame(&p).is_none());
    p.width = 80;
    p.active_idx = 9;
    assert!(draw_frame(&p).is_none());
}

#[test]
fn frame_disconnected_search_and_away() {
    let channels = vec![ChanState {
        name: "#a".into(),
        ..Default::default()
    }];
    let nicks: Vec<String> = vec![];
    let lines: Vec<String> = vec![];
    let input: Vec<char> = vec![];
    let mut p = params(&channels, &nicks, &lines, &input);
    p.connected = false;
    p.server = "";
    p.topic = "";
    p.search_mode = true;
    p.search_query = "q\x1b";
    p.away = "gone";
    p.show_nicks = false;
    p.width = 120;
    let f = draw_frame(&p).unwrap();
    assert_eq!(f.msg_area_top, 3); // status + search
    assert!(f.output.contains("[NEPRISIJUNGTA]"));
    assert!(f.output.contains("Paieška: q "));
    assert!(f.output.contains("[AWAY] "));
    assert!(!f.output.contains(" Nick"));
}

#[test]
fn frame_long_input_keeps_cursor_in_bounds() {
    let channels = vec![ChanState {
        name: "#a".into(),
        ..Default::default()
    }];
    let nicks: Vec<String> = vec![];
    let lines: Vec<String> = vec![];
    let input: Vec<char> = "x".repeat(500).chars().collect();
    let p = params(&channels, &nicks, &lines, &input);
    let f = draw_frame(&p).unwrap();
    // Cursor placed at row h, column ≤ w.
    let last_move = f.output.rfind("\x1b[24;").unwrap();
    let tail = &f.output[last_move..];
    let col: i32 = tail[5..tail.find('H').unwrap()].parse().unwrap();
    assert!((1..=80).contains(&col));
}

#[test]
fn many_channels_scroll_arrows() {
    let channels: Vec<ChanState> = (0..60)
        .map(|i| ChanState {
            name: format!("#c{i}"),
            ..Default::default()
        })
        .collect();
    let nicks: Vec<String> = vec![];
    let lines: Vec<String> = vec![];
    let input: Vec<char> = vec![];
    let mut p = params(&channels, &nicks, &lines, &input);
    p.topic = "";
    p.chan_scroll_off = 10;
    let f = draw_frame(&p).unwrap();
    assert!(f.output.contains('▲'));
    assert!(f.output.contains('▼'));
    // Out-of-range scroll is clamped, never panics.
    p.chan_scroll_off = 10_000;
    let f = draw_frame(&p).unwrap();
    assert!(f.output.contains("#c59"));
    p.chan_scroll_off = -5;
    assert!(draw_frame(&p).is_some());
}
