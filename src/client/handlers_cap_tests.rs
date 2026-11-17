//! IRCv3 CAP derybų būsenų mašina, SASL (PLAIN/EXTERNAL), ir kiti
//! serverio eilučių kraštiniai atvejai.

use super::*;
use crate::client::send::{new_write_queue, WriteQueue};
use crate::config::default_config;
use crate::test_util::Rng;
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

fn server_texts(c: &Client) -> Vec<String> {
    let st = c.mu.lock();
    let i = st.find_ch(SERVER_WINDOW).expect("server window");
    st.channels[i]
        .messages
        .iter()
        .map(|m| m.text.clone())
        .collect()
}

// ---- CAP ----

#[test]
fn cap_ls_without_anything_wanted_ends_negotiation() {
    let (c, q) = setup();
    c.parse_irc(":srv CAP * LS :some-vendor/cap another");
    assert_eq!(sent(&q), vec!["CAP END"]);
}

#[test]
fn cap_ls_302_multiline_accumulates_before_request() {
    let (c, q) = setup();
    c.parse_irc(":srv CAP * LS * :multi-prefix sasl=PLAIN,EXTERNAL");
    c.parse_irc(":srv CAP * LS * :server-time=foo");
    assert!(sent(&q).is_empty());
    c.parse_irc(":srv CAP * LS :echo-message chghost");
    // Be slaptažodžio sasl neprašomas; tvarka — IRCV3_WANTED_CAPS.
    assert_eq!(
        sent(&q),
        vec!["CAP REQ :multi-prefix server-time echo-message chghost"]
    );
}

#[test]
fn cap_names_are_case_insensitive() {
    let (c, q) = setup();
    c.parse_irc(":srv CAP * ls :Server-Time");
    assert_eq!(sent(&q), vec!["CAP REQ :server-time"]);
    c.parse_irc(":srv CAP me ACK :SERVER-TIME");
    assert!(c.has_cap("server-time"));
}

#[test]
fn cap_ack_multiline_waits_for_last() {
    let (c, q) = setup();
    c.parse_irc(":srv CAP * LS :multi-prefix away-notify");
    sent(&q);
    c.parse_irc(":srv CAP me ACK * :multi-prefix");
    assert!(sent(&q).is_empty(), "tęsinys — dar neatsakom");
    c.parse_irc(":srv CAP me ACK :away-notify");
    assert_eq!(sent(&q), vec!["CAP END"]);
    assert!(c.has_cap("multi-prefix") && c.has_cap("away-notify"));
}

#[test]
fn cap_nak_ends_negotiation() {
    let (c, q) = setup();
    c.parse_irc(":srv CAP * LS :server-time");
    sent(&q);
    c.parse_irc(":srv CAP me NAK :server-time");
    assert_eq!(sent(&q), vec!["CAP END"]);
    assert!(!c.has_cap("server-time"));
}

#[test]
fn cap_new_requests_only_missing_and_del_removes() {
    let (c, q) = setup();
    c.parse_irc(":srv CAP * LS :server-time");
    c.parse_irc(":srv CAP me ACK :server-time");
    sent(&q);
    c.parse_irc(":srv CAP me NEW :server-time away-notify");
    assert_eq!(sent(&q), vec!["CAP REQ :away-notify"]);
    c.parse_irc(":srv CAP me ACK :away-notify");
    sent(&q);
    c.parse_irc(":srv CAP me DEL :server-time");
    assert!(!c.has_cap("server-time"));
    assert!(c.has_cap("away-notify"));
}

#[test]
fn cap_list_reply_is_not_echoed_back_to_server() {
    // Serverio „CAP * LIST“ — atsakymas į MŪSŲ užklausą. Atsakius „CAP LIST“
    // serveris vėl atsiunčia sąrašą → begalinis ciklas.
    let (c, q) = setup();
    c.mu.lock().active_caps.insert("server-time".into());
    c.parse_irc(":srv CAP me LIST :server-time");
    assert!(sent(&q).is_empty());
    assert!(server_texts(&c).iter().any(|t| t.contains("server-time")));
}

#[test]
fn sasl_external_full_flow_sends_no_password() {
    let (c, q) = setup();
    {
        let mut st = c.mu.lock();
        st.use_tls = true;
        st.sasl_external = true;
        st.tls_cert_path = "/x/cert.pem".into();
        st.sasl_pass = "neturi-nutekėti".into();
    }
    c.parse_irc(":srv CAP * LS :sasl=EXTERNAL");
    assert_eq!(sent(&q), vec!["CAP REQ :sasl"]);
    c.parse_irc(":srv CAP me ACK :sasl");
    assert_eq!(sent(&q), vec!["AUTHENTICATE EXTERNAL"]);
    c.parse_irc("AUTHENTICATE +");
    assert_eq!(sent(&q), vec!["AUTHENTICATE +"]);
    c.parse_irc(":srv 900 me me!u@h me :You are now logged in as me");
    c.parse_irc(":srv 903 me :SASL authentication successful");
    assert_eq!(sent(&q), vec!["CAP END"]);
    assert!(c.mu.lock().sasl_ok && !c.mu.lock().sasl_pending);
}

#[test]
fn sasl_external_without_tls_warns_and_falls_back_to_plain() {
    let (c, q) = setup();
    {
        let mut st = c.mu.lock();
        st.sasl_external = true;
        st.tls_cert_path = "/x/cert.pem".into();
        st.sasl_pass = "pw".into();
    }
    c.parse_irc(":srv CAP * LS :sasl");
    assert_eq!(sent(&q), vec!["CAP REQ :sasl"]);
    assert_eq!(c.mu.lock().sasl_mech, "PLAIN");
    assert!(server_texts(&c).iter().any(|t| t == SASL_EXTERNAL_WARN));
}

#[test]
fn sasl_failure_ends_cap_and_reports() {
    let (c, q) = setup();
    c.mu.lock().sasl_pass = "blogas".into();
    c.parse_irc(":srv CAP * LS :sasl");
    c.parse_irc(":srv CAP me ACK :sasl");
    c.parse_irc("AUTHENTICATE +");
    sent(&q);
    c.parse_irc(":srv 904 me :SASL authentication failed");
    assert_eq!(sent(&q), vec!["CAP END"]);
    let st = c.mu.lock();
    assert!(!st.sasl_ok && !st.sasl_pending);
    drop(st);
    assert!(server_texts(&c)
        .iter()
        .any(|t| t.contains("SASL klaida (904)")));
    // Vėlesnis iššūkis po nesėkmės slaptažodžio nebeišsiunčia.
    c.parse_irc("AUTHENTICATE +");
    assert_eq!(sent(&q), vec!["AUTHENTICATE +"]);
}

#[test]
fn sasl_long_credentials_are_chunked() {
    let (c, q) = setup();
    c.mu.lock().sasl_pass = "p".repeat(600);
    c.parse_irc(":srv CAP * LS :sasl");
    c.parse_irc(":srv CAP me ACK :sasl");
    sent(&q);
    c.parse_irc("AUTHENTICATE +");
    let s = sent(&q);
    assert_eq!(s.len(), 3, "{s:?}");
    assert_eq!(s[0].len(), "AUTHENTICATE ".len() + 400);
    assert!(s.iter().all(|l| l.starts_with("AUTHENTICATE ")));
    let joined: String = s.iter().map(|l| &l["AUTHENTICATE ".len()..]).collect();
    assert_eq!(joined, sasl_plain_payload("me", &"p".repeat(600)));
}

#[test]
fn authenticate_with_payload_other_than_plus_is_ignored() {
    let (c, q) = setup();
    c.mu.lock().sasl_pass = "pw".into();
    c.parse_irc(":srv CAP * LS :sasl");
    c.parse_irc(":srv CAP me ACK :sasl");
    sent(&q);
    c.parse_irc("AUTHENTICATE Zm9v");
    assert!(sent(&q).is_empty());
}

// ---- kiti ----

#[test]
fn highlight_finds_later_whole_word_occurrence() {
    // Pirmas atitikmuo žodžio viduje („welcome“), antras — atskiras žodis.
    assert!(contains_nick("welcome me", "me"));
    assert!(contains_nick("Ann: annoying, ann", "ann"));
    assert!(!contains_nick("welcome", "me"));
    assert!(!contains_nick("mememe", "me"));
    assert!(contains_nick("(me)", "me"));
    // „[“ — nick simbolis, „[me]“ yra kitas nick.
    assert!(!contains_nick("[me]", "me"));
}

#[test]
fn kick_of_self_keeps_window_and_drops_own_nick() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #k");
    c.parse_irc(":srv 353 me = #k :me @op bob");
    c.parse_irc(":op!u@h KICK #k Me :lauk");
    {
        let st = c.mu.lock();
        let i = st.find_ch("#k").expect("langas lieka");
        // Išmestas — nebe kanale: nick sąrašas išvalomas (nebeatnaujinamas).
        assert!(
            st.channels[i].nicks.is_empty(),
            "{:?}",
            st.channels[i].nicks
        );
    }
    assert!(server_texts(&c)
        .iter()
        .any(|t| t.contains("išmestas iš #k (lauk)")));
}

#[test]
fn kick_of_other_is_shown_in_channel() {
    let (c, _q) = setup();
    c.parse_irc(":me!u@h JOIN #k");
    c.parse_irc(":srv 353 me = #k :me bob");
    c.parse_irc(":op!u@h KICK #k bob :spam");
    let st = c.mu.lock();
    let i = st.find_ch("#k").unwrap();
    assert!(!st.channels[i].nicks.iter().any(|n| n == "bob"));
    assert!(st.channels[i]
        .messages
        .iter()
        .any(|m| m.text == "išmetė bob (spam)"));
}

#[test]
fn kick_for_unknown_channel_creates_nothing() {
    let (c, _q) = setup();
    c.parse_irc(":op!u@h KICK #niekur me :x");
    assert!(c.find_ch("#niekur").is_none());
}

#[test]
fn nick_in_use_gives_up_after_three_attempts() {
    let (c, q) = setup();
    for _ in 0..5 {
        c.parse_irc(":srv 433 * x :in use");
    }
    assert_eq!(sent(&q), vec!["NICK me_", "NICK me2", "NICK me3"]);
    assert!(server_texts(&c)
        .iter()
        .any(|t| t.contains("Paskutinis bandymas")));
    // 001 atstato skaitiklį.
    c.parse_irc(":srv 001 me3 :Welcome");
    assert_eq!(c.mu.lock().nick_attempts, 0);
}

#[test]
fn error_throttled_raises_reconnect_delay() {
    let (c, _q) = setup();
    c.parse_irc("ERROR :Closing Link: (Throttled: Reconnecting too fast)");
    assert_eq!(c.mu.lock().reconnect_delay, Duration::from_secs(120));
}

#[test]
fn parse_irc_never_panics_on_hostile_lines() {
    let (c, q) = setup();
    c.parse_irc(":me!u@h JOIN #a");
    let mut r = Rng::new(0x11fe);
    const CMDS: &[&str] = &[
        "PRIVMSG",
        "NOTICE",
        "JOIN",
        "PART",
        "KICK",
        "QUIT",
        "NICK",
        "MODE",
        "TOPIC",
        "CAP",
        "AUTHENTICATE",
        "001",
        "005",
        "353",
        "366",
        "332",
        "433",
        "730",
        "731",
        "311",
        "PING",
        "ERROR",
        "AWAY",
        "ACCOUNT",
        "CHGHOST",
        "INVITE",
        "900",
        "903",
        "904",
        "999",
    ];
    for _ in 0..20_000 {
        let n = r.below(5);
        let mut line = String::new();
        if r.below(3) == 0 {
            line.push_str("@time=");
            line.push_str(&r.interesting_string(6));
            line.push(' ');
        }
        if r.below(4) != 0 {
            line.push(':');
            line.push_str(r.pick::<&str>(&["me!u@h", "x!y@z", "", "srv", "!@"]));
            line.push(' ');
        }
        line.push_str(r.pick::<&str>(CMDS));
        for _ in 0..n {
            line.push(' ');
            line.push_str(
                r.pick::<&str>(&["#a", "me", "*", "+o", "-v", "LS", "ACK", "=", "#b", ""]),
            );
        }
        line.push_str(" :");
        line.push_str(&r.interesting_string(30));
        c.parse_irc(&line);
        sent(&q);
    }
}
