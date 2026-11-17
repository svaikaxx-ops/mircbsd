use super::*;
use crate::input::testutil::*;
use crate::ui::Channel;

fn many_channels() -> Arc<Client> {
    let c = client();
    {
        let mut st = c.mu.lock();
        for i in 0..30 {
            st.channels.push(Channel::new(
                format!("#kanalas{}", i % 10 + 10 * (i / 10)),
                10,
            ));
        }
        st.active_idx = 0;
        st.height = 20;
    }
    c
}

#[test]
fn esc_complete_cases() {
    for s in ["\x1b[", "\x1b[1;5", "\x1bO"] {
        assert!(!esc_complete(s.as_bytes()), "{s:?} is incomplete");
    }
    for s in [
        "\x1b",
        "\x1b[1;5H",
        "\x1b[1;5F",
        "\x1b[A",
        "\x1bOP",
        "\x1bX",
        "a",
        "\x1b[0;3;5m",
        "labas\r",
    ] {
        assert!(esc_complete(s.as_bytes()), "{s:?} is complete");
    }
    assert!(!esc_complete(b""));
}

#[test]
fn ctrl_c_stops_the_client() {
    let c = client();
    c.handle_key(b"\x03");
    assert!(c.is_stopped());
}

#[test]
fn typing_editing_and_word_keys() {
    let c = client();
    c.handle_key(b"labas pasaulis");
    assert_eq!(input_text(&c), "labas pasaulis");
    c.handle_key(b"\x17"); // Ctrl+W
    assert_eq!(input_text(&c), "labas ");
    c.handle_key(b"\x01"); // Ctrl+A
    assert_eq!(c.mu.lock().input_pos, 0);
    c.handle_key(b"\x1b[1;5C"); // Ctrl+Right
    assert_eq!(c.mu.lock().input_pos, 5);
    c.handle_key(b"\x1b[1;5D"); // Ctrl+Left
    assert_eq!(c.mu.lock().input_pos, 0);
    c.handle_key(b"\x05"); // Ctrl+E
    c.handle_key(b"\x7f"); // Backspace
    assert_eq!(input_text(&c), "labas");
    c.handle_key(b"\x1b[D\x1b[D"[..3].as_ref()); // Left
    c.handle_key(b"\x0b"); // Ctrl+K
    assert_eq!(input_text(&c), "laba");
    c.handle_key(b"\x15"); // Ctrl+U
    assert_eq!(input_text(&c), "");
    // UTF-8 typing keeps characters whole.
    c.handle_key("žąsis".as_bytes());
    assert_eq!(input_text(&c), "žąsis");
    c.handle_key(b"\x7f");
    assert_eq!(input_text(&c), "žąsi");
}

#[test]
fn c1_control_bytes_are_not_inserted() {
    let c = client();
    c.handle_key(&[0x9b, b'a']);
    assert_eq!(input_text(&c), "a");
}

#[test]
fn enter_submits_records_history_and_skips_secrets() {
    let (c, q) = connected();
    open_active(&c, "#chan");
    c.handle_key(b"sveiki\r");
    assert_eq!(sent(&q), vec!["PRIVMSG #chan :sveiki"]);
    assert_eq!(input_text(&c), "");
    c.handle_key(b"/msg nickserv identify hunter2\r");
    let st = c.mu.lock();
    assert_eq!(
        st.history,
        vec!["sveiki"],
        "password command must not be saved"
    );
}

#[test]
fn history_arrows() {
    let c = client();
    c.mu.lock().history = vec!["a".into(), "b".into()];
    c.handle_key(b"x");
    c.handle_key(b"\x1b[A");
    assert_eq!(input_text(&c), "b");
    c.handle_key(b"\x1b[A");
    assert_eq!(input_text(&c), "a");
    c.handle_key(b"\x1b[B");
    assert_eq!(input_text(&c), "b");
    c.handle_key(b"\x1b[B");
    assert_eq!(input_text(&c), "");
}

#[test]
fn up_arrow_on_empty_line_recalls_history_without_scrolling() {
    let c = client();
    c.mu.lock().height = 24;
    c.mu.lock().history = vec!["/join #a".into(), "/whois bob".into()];
    c.handle_key(b"\x1b[A");
    assert_eq!(input_text(&c), "/whois bob");
    assert_eq!(c.mu.lock().scroll_off, 0, "↑ neturi slinkti žinučių");
    c.handle_key(b"\x1b[A");
    assert_eq!(input_text(&c), "/join #a");
    c.handle_key(b"\x1b[B");
    assert_eq!(input_text(&c), "/whois bob");
    c.handle_key(b"\x1b[B");
    assert_eq!(input_text(&c), "");
    assert_eq!(c.mu.lock().scroll_off, 0);
}

#[test]
fn page_keys_scroll() {
    let c = client();
    c.mu.lock().height = 24;
    c.handle_key(b"\x1b[5~"); // PgUp
    assert_eq!(c.mu.lock().scroll_off, 20);
    c.handle_key(b"\x1b[5~");
    c.handle_key(b"\x1b[6~"); // PgDn
    assert_eq!(c.mu.lock().scroll_off, 20);
    assert!(c.mu.lock().chase_until.is_some());
    c.handle_key(b"\x1b[6;2~"); // Shift+PgDn
    let st = c.mu.lock();
    assert_eq!(st.scroll_off, 0);
    assert!(st.chase_until.is_none());
}

#[test]
fn ctrl_arrows_scroll_nicks() {
    let c = many_channels();
    c.handle_key(b"\x1b[1;5B");
    assert_eq!(c.mu.lock().nick_scroll_off, 1);
    c.handle_key(b"\x1b[1;5A");
    assert_eq!(c.mu.lock().nick_scroll_off, 0);
    c.handle_key(b"\x1b[1;5A");
    assert_eq!(c.mu.lock().nick_scroll_off, 0);
}

#[test]
fn home_end_do_not_scroll_channels() {
    let c = many_channels();
    let target = {
        let mut st = c.mu.lock();
        st.chan_scroll_off = st.channels.len() as i32;
        st.clamp_chan_scroll();
        st.chan_scroll_off
    };
    for k in [
        &b"\x1b[1;5H"[..],
        b"\x1b[1;5F",
        b"\x1b[H",
        b"\x1b[F",
        b"\x1b[5;5~",
        b"\x1b[6;5~",
    ] {
        c.handle_key(k);
    }
    assert_eq!(c.mu.lock().chan_scroll_off, target);
    set_input(&c, "labas pasaulis");
    c.handle_key(b"\x1b[H");
    assert_eq!(c.mu.lock().input_pos, 0);
    c.handle_key(b"\x1b[F");
    assert_eq!(c.mu.lock().input_pos, "labas pasaulis".chars().count());
}

#[test]
fn window_switching_keys() {
    let c = client();
    for n in ["#a", "#b", "#c", "#d", "#e", "#f"] {
        let mut st = c.mu.lock();
        c.ensure_ch(&mut st, n);
        st.height = 30;
    }
    c.handle_key(b"\x0e"); // Ctrl+N
    assert_eq!(c.mu.lock().active_idx, 1);
    c.handle_key(b"\x10"); // Ctrl+P
    c.handle_key(b"\x10"); // wraps to the last
    assert_eq!(c.mu.lock().active_idx, 6);
    c.handle_key(b"\x1b3"); // Alt+3
    assert_eq!(c.mu.lock().active_idx, 2);
    c.handle_key(b"\x1bOS"); // F4
    assert_eq!(c.mu.lock().active_idx, 3);
    c.handle_key(b"\x1b[15~"); // F5
    assert_eq!(c.mu.lock().active_idx, 4);
    c.handle_key(b"\x1b[17~"); // F6
    assert_eq!(c.mu.lock().active_idx, 5);
    c.handle_key(b"\x1b[11~"); // F1 (CSI form)
    assert_eq!(c.mu.lock().active_idx, 0);
    c.handle_key(b"\x1b[24~"); // F12 → out of range, ignored
    assert_eq!(c.mu.lock().active_idx, 0);
}

#[test]
fn ctrl_r_enters_search_and_esc_leaves() {
    let c = client();
    c.handle_key(b"\x12");
    assert!(c.mu.lock().search_mode);
    c.handle_key(b"ab");
    assert_eq!(c.mu.lock().search_query, "ab");
    c.handle_key(b"\x7f");
    assert_eq!(c.mu.lock().search_query, "a");
    c.handle_key(b"\x1b");
    let st = c.mu.lock();
    assert!(!st.search_mode);
    assert!(st.search_query.is_empty());
}

#[test]
fn search_prompt_enter_runs_search() {
    let c = client();
    c.add_msg(
        "*server*",
        new_msg(Utc::now(), "bob", "rasta cia", MsgType::Chat),
    );
    c.handle_key(b"\x12");
    c.handle_key(b"rasta");
    c.handle_key(b"\r");
    let st = c.mu.lock();
    assert!(!st.search_mode);
    assert_eq!(st.last_search_query, "rasta");
    assert!(st.search_result_idx >= 0);
}

#[test]
fn search_n_p_only_with_empty_input() {
    let c = client();
    c.mu.lock().last_search_query = "zzz".into();
    c.handle_key(b"n");
    // Empty input + last query: "n" navigates, nothing typed.
    assert_eq!(input_text(&c), "");
    set_input(&c, "a");
    c.handle_key(b"n");
    assert_eq!(input_text(&c), "an");
}

#[test]
fn custom_binds_remap_to_canonical_sequence() {
    let c = client();
    // Bind Alt+S ("\x1bs") to the Ctrl+R search action.
    c.mu.lock()
        .custom_binds
        .insert("\x1bs".into(), "search".into());
    c.handle_key(b"\x1bs");
    assert!(c.mu.lock().search_mode);
    // A bind whose action is unknown is ignored (falls through).
    c.mu.lock().search_mode = false;
    c.mu.lock()
        .custom_binds
        .insert("\x1bx".into(), "nonsense".into());
    c.handle_key(b"\x1bx");
    assert!(!c.mu.lock().search_mode);
    // chan-next via a custom key.
    {
        let mut st = c.mu.lock();
        c.ensure_ch(&mut st, "#a");
        st.height = 30;
        st.custom_binds.insert("\x1bn".into(), "chan-next".into());
    }
    c.handle_key(b"\x1bn");
    assert_eq!(c.mu.lock().active_idx, 1);
}

#[test]
fn keys_debug_reports_bytes_and_q_exits() {
    let c = client();
    c.mu.lock().keys_debug = true;
    c.handle_key(b"\x1b[A");
    c.handle_key(b"q");
    assert!(!c.mu.lock().keys_debug);
    let t = server_texts(&c);
    assert!(
        t.iter()
            .any(|l| l.contains("3 baitai: 0x1b 91('[') 65('A')")),
        "{t:?}"
    );
    assert!(t.iter().any(|l| l.contains("debug išjungtas")));
}

#[test]
fn paste_split_across_reads_is_sent_after_the_window() {
    let (c, q) = connected();
    open_active(&c, "#chan");
    c.handle_key(b"pirmoji\nantroji\n");
    std::thread::sleep(Duration::from_millis(10));
    c.handle_key(b"trecija-pus");
    std::thread::sleep(Duration::from_millis(10));
    c.handle_key(b"e\n");
    std::thread::sleep(Duration::from_millis(60));
    c.handle_key(b"\r"); // confirm
    let msgs: Vec<String> = sent(&q)
        .into_iter()
        .filter_map(|l| l.strip_prefix("PRIVMSG #chan :").map(String::from))
        .collect();
    assert_eq!(msgs, vec!["pirmoji", "antroji", "trecija-puse"]);
    assert!(c.mu.lock().paste_buf.is_empty());
}

#[test]
fn paste_enter_inside_window_is_ignored_and_esc_cancels() {
    let (c, q) = connected();
    open_active(&c, "#chan");
    c.handle_key(b"a\nb\n");
    c.handle_key(b"\r"); // inside the collection window: not a confirmation
    assert!(sent(&q).is_empty());
    std::thread::sleep(Duration::from_millis(60));
    c.handle_key(b"\x1b");
    assert!(c.mu.lock().paste_buf.is_empty());
    assert!(sent(&q).is_empty());
    assert!(server_texts(&c).iter().any(|t| t == "Paste atšauktas"));
}

#[test]
fn split_paste_lines_handles_tails() {
    assert_eq!(
        split_paste_lines("a\nb\n"),
        (vec!["a".to_string(), "b".to_string()], String::new())
    );
    assert_eq!(
        split_paste_lines("a\nb"),
        (vec!["a".to_string()], "b".to_string())
    );
    assert_eq!(split_paste_lines("abc"), (vec![], "abc".to_string()));
    assert_eq!(split_paste_lines("a\r\nb\r\n").0, vec!["a", "b"]);
}

#[test]
fn mouse_wheel_and_clicks() {
    let c = client();
    {
        let mut st = c.mu.lock();
        st.msg_area_top = 3;
        // Go's test client uses height 40; with height 0 a click scrolls
        // the channel list and the next click lands on the wrong row.
        st.height = 40;
        st.channels = vec![
            Channel::new("#a", 100),
            Channel::new("#b", 100),
            Channel::new("#c", 100),
        ];
        st.mouse = true;
    }
    c.handle_mouse(b"\x1b[<64;20;10M");
    assert_eq!(c.mu.lock().scroll_off, 3);
    c.handle_mouse(b"\x1b[<65;20;10M");
    assert_eq!(c.mu.lock().scroll_off, 0);
    for (seq, want) in [
        ("\x1b[<0;5;3m", 0usize),
        ("\x1b[<0;12;4m", 1),
        ("\x1b[<0;8;5m", 2),
    ] {
        c.handle_mouse(seq.as_bytes());
        assert_eq!(c.mu.lock().active_idx, want, "{seq:?}");
    }
    // Click in the message area does not switch.
    c.handle_mouse(b"\x1b[<0;40;10m");
    assert_eq!(c.mu.lock().active_idx, 2);
    // Mouse off: nothing happens.
    c.mu.lock().mouse = false;
    c.handle_mouse(b"\x1b[<64;20;10M");
    assert_eq!(c.mu.lock().scroll_off, 0);
    // Garbage must not panic.
    c.mu.lock().mouse = true;
    for g in [
        "\x1b[<64;20",
        "\x1b[<9999;9999;9999M",
        "\x1b[<x;y;zM",
        "\x1b[<",
        "\x1b[<;;M",
        "\x1b[<99999999999999999999;1;1M",
    ] {
        c.handle_mouse(g.as_bytes());
    }
}

#[test]
fn mouse_report_through_handle_key() {
    let c = client();
    c.mu.lock().mouse = true;
    c.handle_key(b"\x1b[<64;20;10M");
    assert_eq!(c.mu.lock().scroll_off, 3);
}

#[test]
fn tab_in_middle_keeps_text_after_cursor() {
    let c = client();
    open_active(&c, "#chan");
    {
        let mut st = c.mu.lock();
        let ch = st.active_chan_mut().unwrap();
        ch.add_nick("Janina", None);
        ch.add_nick("Jonas", None);
    }
    set_input(&c, "sveika Ja kaip sekasi");
    c.mu.lock().input_pos = "sveika Ja".chars().count();
    c.handle_key(b"\t");
    assert_eq!(input_text(&c), "sveika Janina kaip sekasi");
    c.handle_key(b"!");
    assert_eq!(input_text(&c), "sveika Janina! kaip sekasi");

    // Keli kandidatai: užuomina „[i/N]“ pašalinama, uodega lieka.
    set_input(&c, "J tekstas");
    c.mu.lock().input_pos = 1;
    c.handle_key(b"\t");
    assert!(input_text(&c).ends_with(" tekstas"), "{}", input_text(&c));
    c.handle_key(b"x");
    let got = input_text(&c);
    assert!(
        !got.contains("[1/2]") && got.ends_with(" tekstas"),
        "{got:?}"
    );
}

#[test]
fn delete_home_end_csi_variants() {
    let c = client();
    set_input(&c, "abc");
    c.handle_key(b"\x1b[1~");
    assert_eq!(c.mu.lock().input_pos, 0);
    c.handle_key(b"\x1b[3~");
    assert_eq!(input_text(&c), "bc");
    c.handle_key(b"\x1b[4~");
    assert_eq!(c.mu.lock().input_pos, 2);
    c.handle_key(b"\x1b[3~"); // kursorius gale — nieko
    assert_eq!(input_text(&c), "bc");
    c.handle_key(b"\x1bOH");
    assert_eq!(c.mu.lock().input_pos, 0);
    c.handle_key(b"\x1bOF");
    assert_eq!(c.mu.lock().input_pos, 2);
}

#[test]
fn typing_ends_search_navigation_so_n_can_start_a_line() {
    let c = client();
    c.mu.lock().last_search_query = "zzz".into();
    c.handle_key(b"\x1b[5~"); // PgUp — navigacija išlieka
    assert_eq!(c.mu.lock().last_search_query, "zzz");
    c.handle_key(b"a");
    c.handle_key(b"\x7f"); // tuščia įvestis vėl
    c.handle_key(b"n");
    c.handle_key(b"ice");
    assert_eq!(input_text(&c), "nice");
}
