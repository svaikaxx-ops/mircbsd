use super::*;
use crate::client::send::{new_write_queue, WriteQueue};
use crate::config::default_config;
use std::sync::Arc;

fn setup() -> (Arc<Client>, WriteQueue) {
    let mut cfg = default_config();
    cfg.server = "irc.example.org".into();
    cfg.nick = "me".into();
    let c = Client::new(&cfg);
    let (h, q) = new_write_queue();
    c.mu.lock().write = Some(h);
    (c, q)
}

fn sent(q: &WriteQueue) -> Vec<String> {
    let mut v = Vec::new();
    while let Ok(l) = q.rx.try_recv() {
        v.push(l);
    }
    v
}

fn texts(c: &Client, chan: &str) -> Vec<String> {
    let st = c.mu.lock();
    let i = st.find_ch(chan).expect("channel");
    st.channels[i]
        .messages
        .iter()
        .map(|m| m.text.clone())
        .collect()
}

#[test]
fn ping_pong() {
    let (c, q) = setup();
    c.parse_irc("PING :abc");
    assert_eq!(sent(&q), vec!["PONG :abc"]);
}

#[test]
fn join_names_quit_flow() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #rust");
    assert_eq!(c.active_name(), "#rust");
    c.parse_irc(":srv 353 me = #rust :@op bob");
    c.parse_irc(":bob!u@h QUIT :bye");
    {
        let st = c.mu.lock();
        let i = st.find_ch("#rust").unwrap();
        assert_eq!(st.channels[i].nicks, vec!["@op", "me"]);
    }
    assert!(texts(&c, "#rust").contains(&"bye".to_string()));
}

#[test]
fn names_does_not_create_windows() {
    let (c, _q) = setup();
    c.parse_irc(":srv 353 me = #ghost :a b c");
    c.parse_irc(":srv 332 me #ghost :topic");
    c.parse_irc(":x!u@h MODE #ghost +o y");
    assert!(c.find_ch("#ghost").is_none());
}

#[test]
fn mode_prefix_applies_to_right_nick() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #c");
    c.parse_irc(":srv 353 me = #c :bob me");
    c.apply_isupport(&["CHANMODES=beI,k,l,imnpst", "PREFIX=(ov)@+"]);
    // +l takes a parameter (C), then +o takes bob.
    c.parse_irc(":op!u@h MODE #c +lo 30 bob");
    let st = c.mu.lock();
    let i = st.find_ch("#c").unwrap();
    assert!(
        st.channels[i].nicks.contains(&"@bob".to_string()),
        "{:?}",
        st.channels[i].nicks
    );
}

#[test]
fn nick_change_updates_self_and_channels() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #c");
    c.parse_irc(":me!u@h NICK :me2");
    assert_eq!(c.my_nick(), "me2");
    let st = c.mu.lock();
    let i = st.find_ch("#c").unwrap();
    assert_eq!(st.channels[i].nicks, vec!["me2"]);
}

#[test]
fn privmsg_pm_highlight_and_urls() {
    let (c, _q) = setup();
    c.parse_irc(":bob!u@h PRIVMSG me :see https://example.org/x.");
    let st = c.mu.lock();
    let i = st.find_ch("bob").unwrap();
    assert!(st.channels[i].is_private);
    assert!(st.channels[i].messages[0].highlight);
    assert_eq!(st.last_urls, vec!["https://example.org/x"]);
}

#[test]
fn ctcp_version_and_ping_reply() {
    let (c, q) = setup();
    c.parse_irc(":bob!u@h PRIVMSG me :\x01VERSION\x01");
    let long = "x".repeat(200);
    c.parse_irc(&format!(":bob!u@h PRIVMSG me :\x01PING {long}\x01"));
    let s = sent(&q);
    assert!(s[0].starts_with("NOTICE bob :\x01VERSION mircBSD 0.4.8"));
    assert!(s[1].len() < 100, "{}", s[1].len());
}

#[test]
fn echo_message_is_suppressed() {
    let (c, _q) = setup();
    c.mu.lock().active_caps.insert("echo-message".into());
    c.parse_irc(":me!u@h PRIVMSG #c :hi");
    assert!(c.find_ch("#c").is_none());
}

#[test]
fn nick_in_use_sequence() {
    let (c, q) = setup();
    c.parse_irc(":srv 433 * me :in use");
    c.parse_irc(":srv 433 * me_ :in use");
    c.parse_irc(":srv 433 * me2 :in use");
    c.parse_irc(":srv 433 * me3 :in use");
    assert_eq!(sent(&q), vec!["NICK me_", "NICK me2", "NICK me3"]);
}

#[test]
fn isupport_005_applies() {
    let (c, _q) = setup();
    c.parse_irc(":srv 005 me CHANTYPES=# PREFIX=(ov)@+ CASEMAPPING=ascii :are supported");
    assert!(c.is_channel("#a"));
    assert!(!c.is_channel("&a"));
    assert_eq!(c.casemapping(), "ascii");
}

#[test]
fn cap_ls_requests_wanted_and_sasl_plain() {
    let (c, q) = setup();
    c.mu.lock().sasl_pass = "secret".into();
    c.parse_irc(":srv CAP * LS * :multi-prefix away-notify");
    assert!(sent(&q).is_empty(), "continuation must not answer yet");
    c.parse_irc(":srv CAP * LS :sasl=PLAIN server-time unknown");
    let s = sent(&q);
    assert_eq!(
        s,
        vec!["CAP REQ :multi-prefix away-notify server-time sasl"]
    );

    c.parse_irc(":srv CAP me ACK :multi-prefix sasl");
    assert_eq!(sent(&q), vec!["AUTHENTICATE PLAIN"]);
    c.parse_irc("AUTHENTICATE +");
    assert_eq!(
        sent(&q),
        vec![format!(
            "AUTHENTICATE {}",
            sasl_plain_payload("me", "secret")
        )]
    );
    c.parse_irc(":srv 903 me :ok");
    assert_eq!(sent(&q), vec!["CAP END"]);
    assert!(c.mu.lock().sasl_ok);
}

#[test]
fn unsolicited_sasl_ack_never_leaks_password() {
    let (c, q) = setup();
    c.mu.lock().sasl_pass = "secret".into();
    // We never requested sasl (cap_sasl false) → hostile ACK is ignored.
    c.parse_irc(":srv CAP me ACK :sasl");
    assert_eq!(sent(&q), vec!["CAP END"]);
    // And an AUTHENTICATE challenge without pending gets an empty reply.
    c.parse_irc("AUTHENTICATE +");
    assert_eq!(sent(&q), vec!["AUTHENTICATE +"]);
}

#[test]
fn cap_ack_minus_removes() {
    let (c, q) = setup();
    c.mu.lock().active_caps.insert("sasl".into());
    c.parse_irc(":srv CAP me ACK :-sasl");
    assert!(!c.has_cap("sasl"));
    assert_eq!(sent(&q), vec!["CAP END"]);
}

#[test]
fn welcome_runs_autojoin_and_identify() {
    let (c, q) = setup();
    {
        let mut st = c.mu.lock();
        st.auto_join = vec!["#a".into(), "#b".into()];
        st.nickserv_pass = "pw".into();
        st.monitor_list = vec!["x".into(), "y".into()];
        st.perform = vec!["MODE me +i".into()];
    }
    c.parse_irc(":srv 001 me :Welcome");
    assert_eq!(
        sent(&q),
        vec![
            "PRIVMSG NickServ :IDENTIFY pw",
            "JOIN #a",
            "JOIN #b",
            "MODE me +i",
            "MONITOR + x,y"
        ]
    );
}

#[test]
fn part_self_closes_window_and_kick_message() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #a");
    c.parse_irc(":me!u@h JOIN #b");
    c.parse_irc(":me!u@h PART #a");
    assert!(c.find_ch("#a").is_none());
    c.parse_irc(":op!u@h KICK #b me :bad");
    assert!(c.find_ch("#b").is_some());
    assert!(texts(&c, SERVER_WINDOW)
        .iter()
        .any(|t| t.contains("išmestas")));
}

#[test]
fn away_notify_announces_once() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #a");
    c.parse_irc(":srv 353 me = #a :bob");
    c.parse_irc(":bob!u@h AWAY :lunch");
    c.parse_irc(":bob!u@h AWAY :lunch");
    c.parse_irc(":bob!u@h AWAY");
    let t = texts(&c, "#a");
    assert_eq!(t.iter().filter(|x| x.contains("yra away")).count(), 1);
    assert_eq!(t.iter().filter(|x| x.contains("grįžo")).count(), 1);
}

#[test]
fn monitor_numerics_track_state() {
    let (c, _q) = setup();
    c.mu.lock().monitor_list = vec!["bob".into(), "al".into()];
    c.parse_irc(":srv 730 me :Bob!u@h,al!u@h");
    assert!(c.mu.lock().monitor_online.contains(&c.mon_key("bob")));
    c.parse_irc(":srv 731 me :bob!u@h");
    assert!(!c.mu.lock().monitor_online.contains(&c.mon_key("bob")));
}

#[test]
fn server_time_tag_sets_timestamp() {
    let (c, _q) = setup();
    c.parse_irc("@time=2020-01-02T03:04:05.000Z :bob!u@h PRIVMSG me :hi");
    let st = c.mu.lock();
    let i = st.find_ch("bob").unwrap();
    let ts = st.channels[i].messages[0].ts;
    assert_eq!(
        ts.with_timezone(&chrono::Utc)
            .format("%Y-%m-%d")
            .to_string(),
        "2020-01-02"
    );
}

#[test]
fn whois_and_unknown_numeric_render() {
    let (c, _q) = setup();
    c.parse_irc(":srv 311 me bob u h * :Real Name");
    c.parse_irc(":srv 999 me foo bar");
    let t = texts(&c, SERVER_WINDOW);
    assert!(t.iter().any(|x| x.contains("[whois] bob (u@h)")));
    assert!(t.iter().any(|x| x == "[999] foo bar"));
}

#[test]
fn helpers() {
    assert!(contains_nick("hello Me!", "me"));
    assert!(!contains_nick("game", "me"));
    assert!(!contains_nick("x", ""));
    assert_eq!(
        extract_urls("a http://x.org, b ftp://y.z) c"),
        vec!["http://x.org", "ftp://y.z"]
    );
    assert_eq!(cut_utf8("ééé", 3), "é");
}

#[test]
fn urls_with_hidden_characters_are_not_collected() {
    // /urls shows the sanitized text, /open would hand the RAW string to the
    // browser: a URL with ESC/C1/bidi/zero-width characters opens something
    // other than what the user saw.
    for evil in [
        "https://example.org/\u{202E}gpj.exe",
        "https://exa\u{200B}mple.org/",
        "https://example.org/\x1b]52;c;eA==\x07",
        "https://example.org/\u{9b}31m",
        "https://example.org/\u{FEFF}",
    ] {
        assert!(
            extract_urls(&format!("see {evil} now")).is_empty(),
            "{evil:?} must be rejected"
        );
    }
    // Ordinary (also IDN) URLs still work.
    assert_eq!(
        extract_urls("https://žalgiris.lt/a?b=c#d"),
        vec!["https://žalgiris.lt/a?b=c#d"]
    );
}

#[test]
fn ctcp_flood_replies_are_rate_limited() {
    // Every CTCP VERSION/PING used to get a NOTICE: a CTCP flood from many
    // nicks filled the 256-line send queue (5 lines/s), dropping the user's
    // own messages, and could get us killed for excess flood.
    let (c, q) = setup();
    for i in 0..30 {
        c.parse_irc(&format!(":bot{i}!u@h PRIVMSG #c :\x01VERSION\x01"));
        c.parse_irc(&format!(":bot{i}!u@h PRIVMSG #c :\x01PING {i}\x01"));
    }
    let n = sent(&q).len();
    assert!(n > 0, "the first CTCPs are still answered");
    assert!(n <= 5, "{n} CTCP replies queued");
}

fn setup_other_server() -> (Arc<Client>, WriteQueue) {
    // Config credentials belong to irc.example.org; the user then did
    // `/connect other.example.net …`.
    let (c, q) = setup();
    {
        let mut st = c.mu.lock();
        st.server = "other.example.net".into();
        st.sasl_pass = "saslsecret".into();
        st.nickserv_pass = "nssecret".into();
        st.server_pass = "srvsecret".into();
        st.perform = vec!["PRIVMSG NickServ :IDENTIFY performsecret".into()];
    }
    (c, q)
}

#[test]
fn config_credentials_never_go_to_another_server() {
    let (c, q) = setup_other_server();
    c.parse_irc(":srv CAP * LS :sasl=PLAIN");
    c.parse_irc(":srv CAP me ACK :sasl");
    c.parse_irc("AUTHENTICATE +");
    c.parse_irc(":srv 001 me :Welcome");
    let s = sent(&q);
    assert!(
        !s.iter().any(|l| l.contains("secret")
            || l.contains(&sasl_plain_payload("me", "saslsecret"))
            || l == "AUTHENTICATE PLAIN"),
        "{s:?}"
    );
    assert!(s.contains(&"CAP END".to_string()), "{s:?}");
}

#[test]
fn config_credentials_still_used_for_the_configured_server() {
    let (c, q) = setup_other_server();
    c.mu.lock().server = "IRC.Example.ORG".into();
    c.parse_irc(":srv 001 me :Welcome");
    assert!(sent(&q).contains(&"PRIVMSG NickServ :IDENTIFY nssecret".to_string()));
}

#[test]
fn hostile_server_cannot_grow_monitor_and_cap_sets_without_bound() {
    let (c, _q) = setup();
    c.mu.lock().monitor_list = vec!["bob".into()];
    // 730 for nicks we never asked to watch: not stored (memory DoS).
    for i in 0..50 {
        c.parse_irc(&format!(":srv 730 me :x{i}a!u@h,x{i}b!u@h"));
    }
    c.parse_irc(":srv 730 me :BOB!u@h");
    assert_eq!(c.mu.lock().monitor_online.len(), 1);

    // An endless CAP LS continuation must not buffer unbounded names.
    for i in 0..3000 {
        c.parse_irc(&format!(":srv CAP * LS * :cap{i}a cap{i}b cap{i}c"));
    }
    assert!(c.mu.lock().cap_available.len() <= 1024);

    // ACK of capabilities we never requested does not enable them.
    c.parse_irc(":srv CAP me ACK :evil-cap another server-time");
    let st = c.mu.lock();
    assert!(!st.active_caps.contains("evil-cap"));
    assert!(!st.active_caps.contains("another"));
    assert!(st.active_caps.contains("server-time"));
}

#[test]
fn nick_in_use_after_registration_does_not_rename() {
    let (c, q) = setup();
    c.parse_irc(":srv 001 me :Welcome");
    sent(&q);
    c.parse_irc(":srv 433 me taken :Nickname is already in use");
    assert_eq!(c.my_nick(), "me");
    assert!(sent(&q).is_empty(), "po 001 433 negali siųsti NICK me_");
}

#[test]
fn nick_in_use_during_registration_tries_alternative() {
    let (c, q) = setup();
    c.parse_irc(":srv 433 * me :Nickname is already in use");
    assert_eq!(c.my_nick(), "me_");
    assert_eq!(sent(&q), vec!["NICK me_"]);
}

#[test]
fn welcome_adopts_server_assigned_nick() {
    let (c, _q) = setup();
    c.mu.lock().nick = "verylongnick".into();
    c.parse_irc(":srv 001 verylong :Welcome");
    assert_eq!(c.my_nick(), "verylong");
}

#[test]
fn reconnect_rejoins_channels_and_clears_stale_nicks() {
    let (c, q) = setup();
    c.parse_irc(":srv 001 me :Welcome");
    c.parse_irc(":me!u@h JOIN #a");
    c.parse_irc(":srv 353 me = #a :me bob");
    c.parse_irc(":me!u@h JOIN #b");
    c.parse_irc(":op!u@h KICK #b me :lauk");
    c.parse_irc(":bob!u@h PRIVMSG me :labas");
    sent(&q);
    c.mu.lock().note_disconnect();
    {
        let st = c.mu.lock();
        let i = st.find_ch("#a").unwrap();
        assert!(st.channels[i].nicks.is_empty(), "seni nick'ai liko");
    }
    c.parse_irc(":srv 001 me :Welcome");
    let out = sent(&q);
    assert!(out.contains(&"JOIN #a".to_string()), "{out:?}");
    assert!(
        !out.iter().any(|l| l == "JOIN #b"),
        "išmestas kanalas: {out:?}"
    );
    assert!(!out.iter().any(|l| l.contains("bob")), "{out:?}");
    // Antras 001 be atsijungimo nekartoja JOIN.
    c.parse_irc(":srv 001 me :Welcome");
    assert!(!sent(&q).iter().any(|l| l.starts_with("JOIN")));
}

#[test]
fn kick_of_self_clears_nick_list() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #k");
    c.parse_irc(":srv 353 me = #k :me bob");
    c.parse_irc(":op!u@h KICK #k me :lauk");
    let st = c.mu.lock();
    let i = st.find_ch("#k").unwrap();
    assert!(
        st.channels[i].nicks.is_empty(),
        "{:?}",
        st.channels[i].nicks
    );
}

#[test]
fn contains_nick_checks_every_occurrence() {
    assert!(contains_nick("bobby, bob?", "bob"));
    assert!(contains_nick("žžbob bob", "bob"));
    assert!(!contains_nick("bobby bobas", "bob"));
}

#[test]
fn channel_notice_goes_to_channel_window() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #n");
    c.parse_irc(":ChanServ!s@s NOTICE #n :kanalo pranešimas");
    c.parse_irc(":op!s@s NOTICE @#n :tik opams");
    c.parse_irc(":srv NOTICE me :asmeninis");
    c.parse_irc(":x!s@s NOTICE #kitas :be lango");
    let t = texts(&c, "#n");
    assert!(t.contains(&"kanalo pranešimas".to_string()), "{t:?}");
    assert!(t.contains(&"tik opams".to_string()), "{t:?}");
    let s = texts(&c, SERVER_WINDOW);
    assert!(s.contains(&"asmeninis".to_string()));
    assert!(s.contains(&"be lango".to_string()));
    assert!(c.find_ch("#kitas").is_none());
}

#[test]
fn nick_change_renames_private_window() {
    let (c, _q) = setup();
    c.parse_irc(":alice!u@h PRIVMSG me :labas");
    c.parse_irc(":alice!u@h NICK alisa");
    assert!(c.find_ch("alice").is_none());
    c.parse_irc(":alisa!u@h PRIVMSG me :vėl");
    let t = texts(&c, "alisa");
    assert_eq!(t.len(), 3, "{t:?}"); // labas, NICK, vėl — vienas langas
}

#[test]
fn action_highlight_uses_word_match_and_highlight_words() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #a");
    c.mu.lock().highlight_words = vec!["rust".into()];
    c.parse_irc(":x!u@h PRIVMSG #a :\x01ACTION meets meme\x01");
    c.parse_irc(":x!u@h PRIVMSG #a :\x01ACTION loves Rust\x01");
    let st = c.mu.lock();
    let i = st.find_ch("#a").unwrap();
    let hl: Vec<bool> = st.channels[i]
        .messages
        .iter()
        .filter(|m| m.msg_type == MsgType::Action)
        .map(|m| m.highlight)
        .collect();
    assert_eq!(hl, vec![false, true]);
}

#[test]
fn join_new_window_does_not_inherit_scroll_and_rejoin_keeps_focus() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #a");
    c.mu.lock().scroll_off = 7; // vartotojas pasislinko #a
    c.parse_irc(":me!u@h JOIN #b");
    assert_eq!(c.active_name(), "#b");
    assert_eq!(c.mu.lock().scroll_off, 0, "#b paveldėjo #a slinkimą");
    // Grįžus į #a — jo slinkimas atstatytas.
    let a = c.find_ch("#a").unwrap();
    c.switch_ch(a);
    assert_eq!(c.mu.lock().scroll_off, 7);
    // Serverio JOIN į jau atidarytą langą (rejoin) fokuso nevagia.
    c.parse_irc(":me!u@h JOIN #b");
    assert_eq!(c.active_name(), "#a");
}

#[test]
fn closing_active_window_restores_next_windows_scroll() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #a");
    c.parse_irc(":me!u@h JOIN #b");
    let a = c.find_ch("#a").unwrap();
    c.mu.lock().channels[a].scroll_off = 0;
    c.mu.lock().scroll_off = 9; // #b aktyvus, pasislinkęs
    c.parse_irc(":me!u@h PART #b");
    assert_eq!(c.mu.lock().scroll_off, 0);
}

#[test]
fn only_one_unread_separator_per_window() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #a");
    c.parse_irc(":me!u@h JOIN #b"); // aktyvus #b
    let a = c.find_ch("#a").unwrap();
    let b = c.find_ch("#b").unwrap();
    for round in 0..3 {
        c.parse_irc(&format!(":x!u@h PRIVMSG #a :senas {round}"));
        c.switch_ch(b);
        c.parse_irc(&format!(":x!u@h PRIVMSG #a :naujas {round}"));
        c.switch_ch(a);
    }
    let n = texts(&c, "#a")
        .iter()
        .filter(|t| t.as_str() == "── naujos žinutės ──")
        .count();
    assert_eq!(n, 1);
    let t = texts(&c, "#a");
    let pos = t.iter().position(|t| t == "── naujos žinutės ──").unwrap();
    assert_eq!(t[pos + 1], "naujas 2");
}

#[test]
fn statusmsg_privmsg_goes_to_channel_and_user_mode_is_shown() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #s");
    c.parse_irc(":op!u@h PRIVMSG @#s :tik opams");
    assert!(c.find_ch("@#s").is_none(), "atidarytas langas „@#s“");
    assert!(texts(&c, "#s").contains(&"tik opams".to_string()));
    c.parse_irc(":me MODE me :+iw");
    assert!(texts(&c, SERVER_WINDOW).iter().any(|t| t == "Mode me: +iw"));
}

#[test]
fn erroneous_nick_during_registration_retries_with_fallback() {
    let (c, q) = setup();
    c.parse_irc(":srv 432 * me :Erroneous Nickname");
    assert_eq!(sent(&q), vec!["NICK mircbsd"]);
    assert_eq!(c.my_nick(), "mircbsd");
    // Atmestas ir atsarginis — aiški klaida, be begalinio ciklo.
    c.parse_irc(":srv 432 * mircbsd :Erroneous Nickname");
    assert!(sent(&q).is_empty());
    assert!(texts(&c, SERVER_WINDOW)
        .iter()
        .any(|t| t.contains("registracija sustojo")));
    // Po registracijos 432 — tik klaida, nick nekeičiamas.
    c.parse_irc(":srv 001 mircbsd :Welcome");
    sent(&q);
    c.parse_irc(":srv 432 mircbsd bad! :Erroneous Nickname");
    assert!(sent(&q).is_empty());
    assert_eq!(c.my_nick(), "mircbsd");
}

#[test]
fn window_lookup_uses_server_casemapping() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #a[b]");
    // rfc1459: [] ≡ {} — serveris atsiunčia kita forma, langas tas pats.
    c.parse_irc(":x!u@h PRIVMSG #A{B} :labas");
    assert!(c.find_ch("#a{b}").is_some());
    assert_eq!(c.mu.lock().channels.len(), 2, "atsirado antras langas");
    assert!(texts(&c, "#a[b]").contains(&"labas".to_string()));
    // Privatus langas: nick „Bob^“ ≡ „bob~“.
    c.parse_irc(":Bob^!u@h PRIVMSG me :sveikas");
    c.parse_irc(":bob~!u@h PRIVMSG me :vėl");
    assert_eq!(texts(&c, "Bob^").len(), 2);

    // ascii casemapping: [] ir {} — skirtingi.
    c.parse_irc(":srv 005 me CASEMAPPING=ascii :are supported");
    assert!(c.find_ch("#a{b}").is_none());
    assert!(c.find_ch("#A[B]").is_some());
}
