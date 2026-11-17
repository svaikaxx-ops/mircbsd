use super::*;
use crate::client::types::MsgType;
use crate::input::testutil::*;
use chrono::Utc;

fn quiet_net(c: &Client) {
    let mut st = c.mu.lock();
    st.reconnect = false;
    st.server = "127.0.0.1".into();
    st.port = "1".into();
    st.mouse = false;
}

#[test]
fn ts_format_accepts_clocks_only() {
    assert!(is_valid_ts_format("15:04"));
    assert!(is_valid_ts_format("15:04:05"));
    assert!(is_valid_ts_format("2006-01-02 15:04"));
    assert!(!is_valid_ts_format(""));
    assert!(!is_valid_ts_format("15:04:05 MST"));
    assert!(!is_valid_ts_format("15:04  05"));
    assert!(!is_valid_ts_format("15:04\x1b"));
}

#[test]
fn escape_key_seq_matches_keys_syntax() {
    assert_eq!(escape_key_seq("\u{1b}S"), "\\eS");
    assert_eq!(escape_key_seq(" \t"), "\\x20\\t");
    assert_eq!(escape_key_seq("\\"), "\\\\");
}

#[test]
fn plain_text_and_commands() {
    let (c, q) = connected();
    open_active(&c, "#chan");
    c.handle_input("labas");
    assert_eq!(sent(&q), vec!["PRIVMSG #chan :labas"]);
    assert!(texts(&c, "#chan").iter().any(|t| t == "labas"));

    c.handle_input("/me moja");
    assert!(sent(&q).iter().any(|l| l.contains("ACTION moja")));
    c.handle_input("/nick");
    assert!(server_texts(&c)
        .iter()
        .any(|l| l.contains("Dabartinis slapyvardis")));
}

#[test]
fn join_msg_query_notice_kick_mode() {
    let (c, q) = connected();
    open_active(&c, "#chan");
    c.handle_cmd("join", "libera secret");
    assert!(sent(&q).iter().any(|l| l == "JOIN #libera secret"));
    c.handle_cmd("join", "#chan bad key");
    assert!(server_texts(&c).iter().any(|l| l.contains("kanalo raktas")));

    c.handle_cmd("msg", "bob labas tau");
    assert!(sent(&q).iter().any(|l| l == "PRIVMSG bob :labas tau"));
    c.handle_cmd("query", "bob");
    assert_eq!(c.active_name(), "bob");
    assert!(c.mu.lock().active_chan().unwrap().is_private);

    c.handle_cmd("j", "#chan");
    // switch back
    c.handle_cmd("window", "2");
    c.handle_cmd("notice", "bob tyliai");
    assert!(sent(&q).iter().any(|l| l == "NOTICE bob :tyliai"));
    c.handle_cmd("kick", "bob per daug");
    // may not be on a channel after window switch — go back
    {
        let st = c.mu.lock();
        let i = st.find_ch("#chan").unwrap();
        drop(st);
        c.switch_ch(i);
    }
    c.handle_cmd("kick", "bob per daug");
    assert!(sent(&q).iter().any(|l| l == "KICK #chan bob :per daug"));
    c.handle_cmd("op", "bob");
    assert!(sent(&q).iter().any(|l| l == "MODE #chan +o bob"));
    c.handle_cmd("mode", "+m");
    assert!(sent(&q).iter().any(|l| l == "MODE #chan +m"));
    c.handle_cmd("mode", "#chan +i");
    assert!(sent(&q).iter().any(|l| l == "MODE #chan +i"));
    c.handle_cmd("topic", "nauja");
    assert!(sent(&q).iter().any(|l| l == "TOPIC #chan :nauja"));
    c.handle_cmd("whois", "bob");
    assert!(sent(&q).iter().any(|l| l == "WHOIS bob"));
    assert_eq!(c.mu.lock().whois_chan, "#chan");
    c.handle_cmd("invite", "bob");
    assert!(sent(&q).iter().any(|l| l.starts_with("INVITE bob ")));
    c.handle_cmd("cycle", "");
    let lines = sent(&q);
    assert!(lines.iter().any(|l| l.starts_with("PART #chan :cycle")));
    assert!(lines.iter().any(|l| l == "JOIN #chan"));
    c.handle_cmd("part", "iki");
    assert!(sent(&q).iter().any(|l| l == "PART #chan :iki"));
    c.handle_cmd("raw", "PING lag");
    assert!(sent(&q).iter().any(|l| l == "PING lag"));
    c.handle_cmd("list", "#libera");
    assert!(sent(&q).iter().any(|l| l == "LIST #libera"));
    c.handle_cmd("w", "#chan");
    assert!(sent(&q).iter().any(|l| l == "WHO #chan"));
    assert_eq!(c.mu.lock().who_chan, "#chan");
    c.handle_cmd("names", "");
    assert!(sent(&q).iter().any(|l| l == "NAMES #chan"));
    c.handle_cmd("time", "");
    assert!(sent(&q).iter().any(|l| l == "TIME"));
}

#[test]
fn away_clear_close_window_quit() {
    let (c, q) = connected();
    quiet_net(&c);
    // quiet_net cleared the server name but left the connection flag.
    open_active(&c, "#chan");
    c.handle_cmd("away", "pietūs");
    assert_eq!(c.mu.lock().away, "pietūs");
    assert!(sent(&q).iter().any(|l| l == "AWAY :pietūs"));
    c.handle_cmd("back", "");
    assert!(c.mu.lock().away.is_empty());
    assert!(sent(&q).iter().any(|l| l == "AWAY"));

    c.add_msg("#chan", new_msg(Utc::now(), "a", "x", MsgType::Chat));
    c.handle_cmd("clear", "");
    assert!(texts(&c, "#chan").is_empty());

    c.handle_cmd("window", "");
    assert!(server_texts(&c).iter().any(|l| l.contains("#chan")));
    c.handle_cmd("close", "#chan");
    assert!(c.find_ch("#chan").is_none());
    assert!(sent(&q).iter().any(|l| l == "PART #chan :Langas uždarytas"));

    q.mark_done();
    c.handle_cmd("quit", "viso");
    assert!(c.is_stopped());
    assert!(sent(&q).iter().any(|l| l == "QUIT :viso"));
}

#[test]
fn validation_errors_do_not_send() {
    let (c, q) = connected();
    c.handle_cmd("connect", "not a server 1");
    c.handle_cmd("connect", "irc.example.org 0");
    c.handle_cmd("connect", "irc.example.org 6667 bad nick");
    c.handle_cmd("nick", "a b");
    c.handle_cmd("join", "");
    c.handle_cmd("msg", "onlynick");
    c.handle_cmd("kick", "");
    c.handle_cmd("mode", "");
    c.handle_cmd("nope", "");
    c.handle_cmd("who", "bad target!!");
    let t = server_texts(&c);
    assert!(t.iter().any(|l| l.contains("Nežinoma komanda: /nope")));
    assert!(t.iter().any(|l| l.contains("porto numeris")));
    assert!(sent(&q).is_empty());
}

#[test]
fn search_set_notify_urls_keys() {
    let c = client();
    quiet_net(&c);
    c.add_msg(
        "*server*",
        new_msg(Utc::now(), "bob", "rasta cia", MsgType::Chat),
    );
    c.handle_cmd("search", "rasta");
    assert_eq!(c.mu.lock().last_search_query, "rasta");
    c.handle_cmd("search", "");
    assert!(c.mu.lock().search_mode);
    c.handle_cmd("search", "all rasta");
    assert!(server_texts(&c).iter().any(|l| l.contains("atitikmenų")));

    c.handle_cmd("set", "");
    assert!(server_texts(&c).iter().any(|l| l.starts_with("mouse = ")));
    c.handle_cmd("set", "theme purple");
    assert!(server_texts(&c).iter().any(|l| l.contains("Nežinoma tema")));
    c.handle_cmd("set", "mouse gal");
    assert!(server_texts(&c).iter().any(|l| l.contains("true/false")));
    c.handle_cmd("set", "nope 1");
    assert!(server_texts(&c)
        .iter()
        .any(|l| l.contains("Nežinomas arba nekeičiamas")));

    c.mu.lock().monitor_list = vec!["bob".into()];
    c.mu.lock().monitor_online.insert(c.mon_key("bob"));
    c.handle_cmd("notify", "list");
    assert!(server_texts(&c).iter().any(|l| l.contains("bob — online")));
    c.handle_cmd("notify", "del nobody");
    assert!(server_texts(&c).iter().any(|l| l.contains("niekos")));

    c.mu.lock().ignore_list = vec!["Bad".into()];
    c.handle_cmd("ignores", "");
    assert!(server_texts(&c).iter().any(|l| l.contains("Bad")));

    c.mu.lock().last_urls = vec!["https://example.org/a".into()];
    c.handle_cmd("urls", "");
    assert!(server_texts(&c)
        .iter()
        .any(|l| l.contains("1: https://example.org/a")));
    c.handle_cmd("open", "9");
    assert!(server_texts(&c).iter().any(|l| l.contains("/open [n]")));

    c.handle_cmd("keys", "debug");
    assert!(c.mu.lock().keys_debug);
    c.handle_cmd("help", "");
    assert!(server_texts(&c)
        .iter()
        .any(|l| l.contains(&format!("mircBSD v{}", crate::client::VERSION))));
}

#[test]
fn export_and_clearlogs_use_the_log_dir() {
    let dir = std::env::temp_dir().join(format!("mircbsd-export-{}", std::process::id()));
    let srv = dir.join("irc.example.org");
    std::fs::create_dir_all(&srv).unwrap();
    std::fs::write(srv.join("#k-2026-10-21.log"), "x").unwrap();
    let c = client();
    {
        let mut st = c.mu.lock();
        st.log_dir = dir.to_string_lossy().into_owned();
        st.server = "irc.example.org".into();
    }
    open_active(&c, "#k");
    c.add_msg("#k", new_msg(Utc::now(), "bob", "eksportas", MsgType::Chat));
    c.handle_cmd("exportjson", "#k");
    let t = server_texts(&c);
    assert!(
        t.iter()
            .any(|l| l.contains("Eksportas:") && l.contains(".json")),
        "{t:?}"
    );
    c.handle_cmd("clearlogs", "#k");
    assert!(server_texts(&c).iter().any(|l| l.contains("Išvalyta 1")));
    assert!(!srv.join("#k-2026-10-21.log").exists());
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn nickserv_export_redacts_credentials() {
    let dir = std::env::temp_dir().join(format!("mircbsd-export-secret-{}", std::process::id()));
    std::fs::create_dir(&dir).unwrap();
    let c = client();
    {
        let mut st = c.mu.lock();
        st.log_dir = dir.to_string_lossy().into_owned();
        st.server = "irc.example.org".into();
    }
    open_active(&c, "NickServ");
    c.add_msg(
        "NickServ",
        new_msg(
            Utc::now(),
            "me",
            "REGISTER secret user@example.org",
            MsgType::Chat,
        ),
    );
    c.add_msg("NickServ", new_msg(Utc::now(), "me", "HELP", MsgType::Chat));
    c.handle_cmd("exportjson", "NickServ");
    let path = dir.join("irc.example.org").join(format!(
        "NickServ-{}.json",
        chrono::Local::now().format("%Y-%m-%d")
    ));
    let contents = std::fs::read_to_string(path).unwrap();
    assert!(!contents.contains("secret"));
    assert!(contents.contains("slaptažodis paslėptas"));
    assert!(contents.contains("HELP"));
    std::fs::remove_dir_all(dir).unwrap();
}

#[test]
fn reconnect_announces_without_looping() {
    let c = client();
    quiet_net(&c);
    c.handle_cmd("reconnect", "");
    assert!(server_texts(&c)
        .iter()
        .any(|l| l.contains("Jungiamasi iš naujo")));
}

#[test]
fn server_window_refuses_chat() {
    let c = client();
    c.handle_input("labas");
    assert!(server_texts(&c).iter().any(|l| l.contains("/join")));
}
