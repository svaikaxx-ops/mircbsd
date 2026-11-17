use super::*;
use crate::ui::message::{Message, MsgType};
use std::time::Duration;

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
    assert_eq!(msg_width(100, false, SidebarMode::On), 100 - 18 - 2);
    assert_eq!(msg_width(100, true, SidebarMode::On), 100 - 18 - 2 - 17);
    // Narrow: the channel list shrinks and the nick list is hidden.
    assert_eq!(msg_width(50, true, SidebarMode::On), 50 - 12 - 2);
    assert_eq!(msg_width(20, true, SidebarMode::On), 10);
    assert_eq!(msg_area_top(false, false), 2);
    assert_eq!(msg_area_top(true, true), 4);
}

#[test]
fn responsive_layout() {
    // 80 columns still has room for the nick list (43-cell messages).
    let l = layout(80, true, SidebarMode::On);
    assert_eq!((l.chan_w, l.nick_w, l.msg_w), (18, 16, 43));
    // Below 77 the nick list would squeeze messages under 40 cells.
    assert_eq!(layout(76, true, SidebarMode::On).nick_w, 0);
    assert_eq!(layout(76, true, SidebarMode::On).msg_w, 76 - 18 - 2);
    // Query / server windows never show it.
    assert_eq!(layout(200, false, SidebarMode::On).nick_w, 0);
    assert_eq!(chan_list_width(59, SidebarMode::On), CHAN_LIST_WIDTH_NARROW);
    assert_eq!(chan_list_width(60, SidebarMode::On), CHAN_LIST_WIDTH);
}

#[test]
fn status_bar_keeps_the_window_name() {
    let full = status_left("me", "irc.example.org [TLS]", "#rust", 120);
    assert_eq!(full, " mircBSD | me @ irc.example.org [TLS] | #rust ");
    // Narrow: the brand goes first, then the server — "| #rust " stays.
    let mid = status_left("me", "irc.example.org [TLS]", "#rust", 40);
    assert_eq!(mid, " me @ irc.example.org [TLS] | #rust ");
    let small = status_left("me", "irc.example.org [TLS]", "#rust", 20);
    assert_eq!(small, " me | #rust ");
    assert!(status_left("me", "", "*server*", 80).contains("[NEPRISIJUNGTA] | *server*"));
    // The hint shrinks before the left part is clipped.
    assert_eq!(status_right_for(&full, 120), STATUS_RIGHT);
    assert_eq!(status_right_for(&full, 60), STATUS_RIGHT_SHORT);
    assert_eq!(status_right_for(&full, 46), "");
}

#[test]
fn input_window_counts_cells_and_follows_the_cursor() {
    let ascii: Vec<char> = "abcdef".chars().collect();
    assert_eq!(input_window(&ascii, 6, 10), (0, 6));
    // Cursor at the end of a long line: the tail is shown, cursor fits.
    let long: Vec<char> = "x".repeat(30).chars().collect();
    let (s, e) = input_window(&long, 30, 10);
    assert_eq!((s, e), (21, 30));
    // Cursor at the START of a long line: the head is shown (the old code
    // always showed the tail and put the cursor on the wrong character).
    assert_eq!(input_window(&long, 0, 10), (0, 10));
    // Wide characters take two cells each.
    let wide: Vec<char> = "你好世界你好".chars().collect();
    let (s, e) = input_window(&wide, 6, 7);
    let cells: i32 = wide[s..6].iter().map(|&r| rune_width(r)).sum();
    assert!(cells < 7, "cursor must stay inside the 7-cell field");
    assert_eq!(e, 6);
}

#[test]
fn too_small_frame_explains_the_minimum() {
    let f = too_small_frame(30, 5).unwrap();
    assert!(f.contains("per mažas"));
    assert!(f.contains("40×8"));
    assert!(too_small_frame(0, 0).is_none());
}

#[test]
fn cache_is_incremental_and_inserts_day_separators() {
    let _g = crate::ui::theme::TEST_LOCK.lock();
    let mut ch = Channel::new("#t", 0);
    let t1 = crate::timeutil::local_at(2026, 10, 21, 23, 0, 0, 0).timestamp();
    let t2 = crate::timeutil::local_at(2026, 10, 22, 0, 30, 0, 0).timestamp();
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
fn lag_shown_grows_only_past_the_measured_value() {
    let ms = Duration::from_millis;
    // Nieko nematuota ir nelaukiama — nerodoma.
    assert_eq!(lag_shown(None, None), None);
    // Pirmas PING dar < 1 s — dar nerodoma; po 1 s — auga.
    assert_eq!(lag_shown(None, Some(ms(400))), None);
    assert_eq!(lag_shown(None, Some(ms(1500))), Some(ms(1500)));
    // Greitas ryšys: laukimas iki 1 s nerodomas (nemirguliuoja).
    assert_eq!(lag_shown(Some(ms(120)), Some(ms(800))), Some(ms(120)));
    // Laukiama ilgiau už matavimą → augantis skaičius.
    assert_eq!(lag_shown(Some(ms(3000)), Some(ms(2000))), Some(ms(3000)));
    assert_eq!(lag_shown(Some(ms(3000)), Some(ms(7000))), Some(ms(7000)));
    assert_eq!(lag_shown(Some(ms(120)), None), Some(ms(120)));
}

#[test]
fn lag_format() {
    assert_eq!(format_lag(Duration::from_millis(123)), "0.12s");
    assert_eq!(format_lag(Duration::from_millis(5040)), "5.04s");
    assert_eq!(format_lag(Duration::from_millis(12_340)), "12.3s");
    assert_eq!(format_lag(Duration::from_secs(187)), "187s");
}

#[test]
fn lag_segment_yields_before_the_window_name() {
    let srv = "irc.example.org [TLS]";
    // Platu: viskas telpa.
    let (l, seg) = status_left_lag("me", srv, "#rust", Some("0.12s"), 120);
    assert_eq!(l, " mircBSD | me @ irc.example.org [TLS] | #rust ");
    assert_eq!(seg, "| lag 0.12s ");
    // Siauriau: pirma dingsta „mircBSD“, lag lieka.
    let (l, seg) = status_left_lag("me", srv, "#rust", Some("0.12s"), 50);
    assert_eq!(l, " me @ irc.example.org [TLS] | #rust ");
    assert_eq!(seg, "| lag 0.12s ");
    // Dar siauriau: lag dingsta pirmiau nei serveris ir lango vardas.
    let (l, seg) = status_left_lag("me", srv, "#rust", Some("0.12s"), 40);
    assert_eq!(l, " me @ irc.example.org [TLS] | #rust ");
    assert_eq!(seg, "");
    let (l, seg) = status_left_lag("me", srv, "#rust", Some("0.12s"), 20);
    assert_eq!(l, " me | #rust ");
    assert_eq!(seg, "");
    // Niekada neprisijungus — lag nerodomas.
    let (_, seg) = status_left_lag("me", "", "*server*", Some("0.12s"), 120);
    assert_eq!(seg, "");
}

#[test]
fn frame_status_bar_shows_lag_and_warns_when_slow() {
    // Spalvų asertacijos: NO_COLOR testas lygiagrečiai perjungia globalų
    // NO_COLOR — be šio užrakto lag būna „\x1b[27m“ vietoj raudono (FreeBSD CI).
    let _g = crate::ui::theme::TEST_LOCK.lock();
    let channels = vec![ChanState {
        name: "#rust".into(),
        ..Default::default()
    }];
    let nicks: Vec<String> = vec![];
    let lines: Vec<String> = vec![];
    let input: Vec<char> = vec![];
    let status = |p: &FrameParams| -> String {
        let out = draw_frame(p).unwrap().output;
        let start = out.find("\x1b[1;1H").unwrap();
        let end = out[start..].find("\x1b[2;1H").unwrap() + start;
        out[start..end].to_string()
    };
    for (w, hint) in [(80, STATUS_RIGHT_SHORT), (120, STATUS_RIGHT)] {
        let mut p = params(&channels, &nicks, &lines, &input);
        p.width = w;
        p.lag = Some(Duration::from_millis(120));
        let bar = status(&p);
        assert!(bar.contains("| #rust | lag 0.12s "), "{w}: {bar:?}");
        assert!(bar.contains(hint), "{w}: užuomina");
        assert!(!bar.contains(ANSI_BG_RED));
        // Matoma juosta (be CSI sekų) — lygiai w ląstelių.
        let mut visible = String::new();
        let mut it = bar.chars();
        while let Some(ch) = it.next() {
            if ch == '\x1b' {
                for n in it.by_ref() {
                    if ('@'..='~').contains(&n) && n != '[' {
                        break;
                    }
                }
            } else {
                visible.push(ch);
            }
        }
        assert_eq!(string_width(&visible), w, "{w}: {visible:?}");

        // ≥ 5 s — paryškinta (raudonas fonas), po jo juostos spalvos grąžinamos.
        p.lag = Some(Duration::from_millis(6200));
        let bar = status(&p);
        assert!(
            bar.contains(&format!(
                "{ANSI_BG_RED}| lag 6.20s {ANSI_RESET}{ANSI_BG_BLUE}"
            )),
            "{w}: {bar:?}"
        );

        // Atsijungus — lag nerodomas.
        p.connected = false;
        assert!(!status(&p).contains("lag "));
    }
}

#[test]
fn show_joins_filters_rendered_lines_and_rerenders_on_change() {
    let _g = crate::ui::theme::TEST_LOCK.lock();
    let t = crate::timeutil::now();
    let mut ch = Channel::new("#t", 0);
    ch.add_msg(Message::new(t, "bob", "labas", MsgType::Chat, false));
    ch.add_msg(Message::new(t, "bob", "", MsgType::Part, false).quiet());
    ch.add_msg(Message::new(t, "tylus", "", MsgType::Join, false).quiet());
    ch.add_msg(Message::new(t, "op", "išmetė x (y)", MsgType::Server, false).quiet());
    let plain = |ch: &Channel| -> Vec<String> {
        ch.rendered
            .iter()
            .map(|l| crate::ui::theme::strip_colors(l))
            .collect()
    };

    crate::ui::theme::set_show_joins("all");
    update_render_cache(&mut ch, 60, "me");
    assert_eq!(ch.rendered.len(), 4);

    // smart: bob kalbėjo — jo part lieka; tylus join slepiamas; kick lieka.
    crate::ui::theme::set_show_joins("smart");
    crate::ui::theme::invalidate_render_caches([&mut ch]);
    update_render_cache(&mut ch, 60, "me");
    let lines = plain(&ch);
    assert_eq!(lines.len(), 3, "{lines:?}");
    assert!(lines.iter().any(|l| l.contains("bob išėjo")));
    assert!(!lines.iter().any(|l| l.contains("tylus")));
    assert!(lines.iter().any(|l| l.contains("išmetė x")));

    // none: lieka pokalbis ir išmetimas; nauja join eilutė — inkrementaliai.
    crate::ui::theme::set_show_joins("none");
    crate::ui::theme::invalidate_render_caches([&mut ch]);
    update_render_cache(&mut ch, 60, "me");
    assert_eq!(ch.rendered.len(), 2);
    ch.add_msg(Message::new(t, "bob", "", MsgType::Join, false).quiet());
    update_render_cache(&mut ch, 60, "me");
    assert_eq!(ch.rendered.len(), 2);

    crate::ui::theme::set_show_joins("all");
    crate::ui::theme::invalidate_render_caches([&mut ch]);
    update_render_cache(&mut ch, 60, "me");
    assert_eq!(ch.rendered.len(), 5);
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
        connecting: false,
        my_nick: "me",
        server: "irc.example.org",
        away: "",
        use_tls: true,
        search_mode: false,
        search_query: "",
        lag: None,
        sidebar: SidebarMode::On,
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
    assert!(f.output.contains("ISTORIJA: +5"));
    assert!(!f.output.contains("line 49"));
    p.scroll_off = 0;

    // Nick list header carries the count; the status bar names the window.
    let f = draw_frame(&p).unwrap();
    assert!(f.output.contains(" Nick (3)"));
    assert!(f.output.contains("| #rust "));

    // NO_COLOR: no colour SGR left, backgrounds become reverse video.
    {
        let _g = crate::ui::theme::TEST_LOCK.lock();
        crate::ui::theme::set_no_color(true);
        let f = draw_frame(&p).unwrap();
        crate::ui::theme::set_no_color(false);
        for code in ["\x1b[44m", "\x1b[31m", "\x1b[90m", "\x1b[36m"] {
            assert!(!f.output.contains(code), "{code:?} with NO_COLOR");
        }
        assert!(f.output.contains("\x1b[7m"));
        assert!(f.output.contains("1:#rust"));
    }

    // Narrow terminal: nick list hidden, channel list narrower.
    p.width = 50;
    let f = draw_frame(&p).unwrap();
    assert!(!f.output.contains(" Nick ("));
    assert!(f.output.contains("| #rust "));
    p.width = 80;

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

fn input_row(out: &str) -> (String, i32) {
    // Paskutinė įvesties eilutė: tekstas po „> “ (be ANSI) ir kursoriaus stulpelis.
    let start = out.rfind("\x1b[24;1H").unwrap();
    let row = &out[start..];
    let last_move = out.rfind("\x1b[24;").unwrap();
    let tail = &out[last_move..];
    let col: i32 = tail[5..tail.find('H').unwrap()].parse().unwrap();
    let mut plain = String::new();
    let mut it = row[..last_move - start].chars().peekable();
    while let Some(c) = it.next() {
        if c == '\x1b' {
            for d in it.by_ref() {
                if d.is_ascii_alphabetic() {
                    break;
                }
            }
        } else {
            plain.push(c);
        }
    }
    (plain, col)
}

#[test]
fn input_line_wide_chars_fit_and_cursor_follows_home() {
    let channels = vec![ChanState {
        name: "#a".into(),
        ..Default::default()
    }];
    let nicks: Vec<String> = vec![];
    let lines: Vec<String> = vec![];
    // 60 plačių simbolių = 120 ląstelių > 80.
    let input: Vec<char> = "界".repeat(60).chars().collect();
    let mut p = params(&channels, &nicks, &lines, &input);
    let f = draw_frame(&p).unwrap();
    let (shown, col) = input_row(&f.output);
    assert!(
        crate::width::string_width(&shown) <= 80,
        "eilutė per plati: {}",
        crate::width::string_width(&shown)
    );
    assert!(col <= 80);
    // Home: kursorius eilutės pradžioje, rodoma pradžia.
    p.input_pos = 0;
    let f = draw_frame(&p).unwrap();
    let (shown, col) = input_row(&f.output);
    assert_eq!(col, 3, "kursorius turi būti po „> “");
    assert!(shown.starts_with("> 界"), "{shown:?}");
}

// ---- sidebar=auto|on|off (telefonai / Termux) ----

#[test]
fn sidebar_mode_parse_visible_toggle() {
    assert_eq!(SidebarMode::parse(" AUTO "), Some(SidebarMode::Auto));
    assert_eq!(SidebarMode::parse("on"), Some(SidebarMode::On));
    assert_eq!(SidebarMode::parse("no"), Some(SidebarMode::Off));
    assert_eq!(SidebarMode::parse("kartais"), None);
    assert_eq!(SidebarMode::default(), SidebarMode::Auto);
    // auto: slepiama siauresniame nei 80 (telefonas ~72), rodoma nuo 80.
    assert!(!SidebarMode::Auto.visible(79));
    assert!(SidebarMode::Auto.visible(80));
    assert!(SidebarMode::On.visible(40));
    assert!(!SidebarMode::Off.visible(200));
    // Alt+B: priešinga tam, kas matoma.
    assert_eq!(SidebarMode::Auto.toggled(72), SidebarMode::On);
    assert_eq!(SidebarMode::Auto.toggled(120), SidebarMode::Off);
    assert_eq!(SidebarMode::On.toggled(72), SidebarMode::Off);
    assert_eq!(SidebarMode::Off.toggled(120), SidebarMode::On);
}

#[test]
fn hidden_sidebar_gives_messages_the_full_width() {
    assert_eq!(chan_list_width(72, SidebarMode::Auto), 0);
    assert_eq!(chan_list_width(72, SidebarMode::On), CHAN_LIST_WIDTH);
    assert_eq!(chan_list_width(120, SidebarMode::Auto), CHAN_LIST_WIDTH);
    // Be sąrašo: visas plotis minus vienas atsarginis stulpelis (kaip ir su
    // sąrašu) — telefone 72 stulpeliai → 71, o ne 52.
    let l = layout(72, false, SidebarMode::Auto);
    assert_eq!((l.chan_w, l.nick_w, l.msg_w), (0, 0, 71));
    assert_eq!(layout(72, false, SidebarMode::On).msg_w, 72 - 18 - 2);
    // Nick sąrašas grįžta, kai vietos užtenka.
    let l = layout(79, true, SidebarMode::Off);
    assert_eq!((l.chan_w, l.nick_w), (0, NICK_LIST_WIDTH));
    assert_eq!(l.msg_w, 79 - 1 - (NICK_LIST_WIDTH + 1));
}

#[test]
fn activity_marker_lists_other_unread_windows() {
    let mk = |name: &str, unread: i32, highlight: i32| ChanState {
        name: name.into(),
        unread,
        highlight,
        ..Default::default()
    };
    let chans = vec![
        mk("*server*", 1, 0),
        mk("#a", 0, 0),
        mk("#b", 4, 2),
        mk("#c", 3, 0),
    ];
    // Aktyvus (#c) neįtraukiamas; „!“ — paminėjimas.
    assert_eq!(activity_marker(&chans, 3), " [1 !3]");
    assert_eq!(activity_marker(&chans[1..2], 0), "");
    let many: Vec<ChanState> = (0..10).map(|i| mk(&format!("#{i}"), 1, 0)).collect();
    assert_eq!(activity_marker(&many, 0), " [2 3 4 5 6 7 +]");
}

#[test]
fn frame_without_sidebar_starts_messages_at_column_one() {
    let mut channels = vec![
        ChanState {
            name: "*server*".into(),
            ..Default::default()
        },
        ChanState {
            name: "#rust".into(),
            ..Default::default()
        },
        ChanState {
            name: "#kitas".into(),
            unread: 2,
            highlight: 1,
            ..Default::default()
        },
    ];
    let nicks: Vec<String> = vec![];
    let lines = vec!["\x1b[0mpirma eilute".to_string()];
    let input: Vec<char> = vec![];
    let mut p = params(&channels, &nicks, &lines, &input);
    p.width = 72;
    p.active_idx = 1;
    p.show_nicks = false;
    p.sidebar = SidebarMode::Auto;
    let f = draw_frame(&p).unwrap();
    let top = f.msg_area_top;
    // Pranešimas — 1 stulpelyje; kanalų sąrašo ir jo „│“ nėra.
    assert!(
        f.output.contains(&format!(
            "\x1b[{top};1H\x1b[71X\x1b[{top};1H\x1b[0mpirma eilute"
        )),
        "{:?}",
        f.output
    );
    assert!(!f.output.contains("1:*server*"));
    assert!(!f.output.contains('│'));
    assert!(!f.output.contains('┴'));
    // Aktyvumas — statuso juostoje prie lango vardo.
    assert!(f.output.contains("| #rust [!3] "), "{:?}", f.output);

    // Tas pats su sidebar=on: sąrašas grįžta, žymos nebėra.
    p.sidebar = SidebarMode::On;
    let f = draw_frame(&p).unwrap();
    assert!(f.output.contains("1:*server*"));
    assert!(f.output.contains('│'));
    assert!(!f.output.contains("[!3]"));
    channels.clear();
}
