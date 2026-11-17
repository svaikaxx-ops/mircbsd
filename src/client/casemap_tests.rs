//! Unicode CASEMAPPING (rfc8265 / rfc7613 / utf8): visos vardų lankstymo
//! vietos (langai, nick'ų rinkiniai, ignore, MONITOR, Tab) turi naudoti TĄ
//! PATĮ algoritmą.

use super::*;
use crate::client::send::{new_write_queue, WriteQueue};
use crate::config::default_config;
use crate::validate::{fold_name, nick_equal, norm_nick};
use std::sync::Arc;

fn setup() -> (Arc<Client>, WriteQueue) {
    let mut cfg = default_config();
    cfg.server = "irc.example.org".into();
    cfg.nick = "me".into();
    cfg.log_dir = String::new();
    let c = Client::new(&cfg);
    let (h, q) = new_write_queue();
    {
        let mut st = c.mu.lock();
        st.write = Some(h);
        st.connected = true;
        st.registered = true;
    }
    (c, q)
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
fn unicode_casemappings_fold_lithuanian_letters() {
    for cm in ["rfc8265", "RFC8265", "rfc7613", "utf8", "utf-8"] {
        assert!(nick_equal("#Žalgiris", "#žalgiris", cm), "{cm}");
        assert!(nick_equal("ĄŽUOLAS", "ąžuolas", cm), "{cm}");
        assert_eq!(fold_name("#ŽALGIRIS", cm), "#žalgiris", "{cm}");
        // PRECIS neturi rfc1459 skliaustų atvaizdavimo.
        assert!(!nick_equal("[a]", "{a}", cm), "{cm}");
    }
    // Senos reikšmės nepakito: rfc1459 ir nežinomos — tik ASCII + []\^.
    for cm in ["rfc1459", "", "kažkas-naujo"] {
        assert!(!nick_equal("#Žalgiris", "#žalgiris", cm), "{cm}");
        assert!(nick_equal("[a]^", "{A}~", cm), "{cm}");
    }
    // strict-rfc1459: be ^/~.
    assert!(nick_equal("[a]\\", "{a}|", "strict-rfc1459"));
    assert!(!nick_equal("a^", "a~", "strict-rfc1459"));
}

#[test]
fn equality_and_set_keys_use_the_same_fold() {
    let names = [
        "#Žalgiris",
        "#žalgiris",
        "#ŽALGIRIS",
        "[a]^",
        "{a}~",
        "{A}^",
        "Bob",
        "bob",
        "İstanbul",
        "i̇stanbul",
        "ΣΟΦΙΑ",
        "σοφια",
    ];
    for cm in ["ascii", "rfc1459", "strict-rfc1459", "rfc8265", "utf8", "x"] {
        for a in names {
            for b in names {
                assert_eq!(
                    nick_equal(a, b, cm),
                    fold_name(a, cm) == fold_name(b, cm),
                    "{a:?} vs {b:?} under {cm}"
                );
                assert_eq!(
                    nick_equal(a, b, cm),
                    norm_nick(a, "", cm) == norm_nick(b, "", cm),
                    "{a:?} vs {b:?} under {cm}"
                );
            }
        }
    }
}

#[test]
fn rfc8265_server_keeps_one_window_per_channel() {
    let (c, _q) = setup();
    c.parse_irc(":srv 005 me CASEMAPPING=rfc8265 :are supported");
    c.parse_irc(":me!u@h JOIN #Žalgiris");
    c.parse_irc(":jonas!u@h PRIVMSG #žalgiris :labas");
    c.parse_irc(":Ąžuolas!u@h PRIVMSG me :sveikas");
    c.parse_irc(":ąŽUOLAS!u@h PRIVMSG me :vėl");
    let st = c.mu.lock();
    let names: Vec<&str> = st.channels.iter().map(|ch| ch.name.as_str()).collect();
    assert_eq!(names, vec![SERVER_WINDOW, "#Žalgiris", "Ąžuolas"]);
    drop(st);
    assert!(texts(&c, "#ŽALGIRIS").contains(&"labas".to_string()));
    assert_eq!(texts(&c, "Ąžuolas").len(), 2);
}

#[test]
fn casemapping_change_rebuilds_existing_nick_sets() {
    let (c, _q) = setup();
    // Langas atidarytas PRIEŠ 005 (rfc1459 raktai) …
    c.parse_irc(":me!u@h JOIN #a");
    c.parse_irc(":srv 353 me = #a :me @Žygis");
    // … tada serveris praneša Unicode casemapping.
    c.parse_irc(":srv 005 me CASEMAPPING=rfc8265 :are supported");
    c.parse_irc(":žygis!u@h QUIT :bye");
    let st = c.mu.lock();
    let i = st.find_ch("#a").unwrap();
    assert_eq!(st.channels[i].nicks, vec!["me".to_string()]);
    assert_eq!(st.channels[i].casemapping, "rfc8265");
}

#[test]
fn ignore_monitor_and_away_use_unicode_fold() {
    let (c, q) = setup();
    c.parse_irc(":srv 005 me CASEMAPPING=rfc8265 :are supported");
    c.parse_irc(":me!u@h JOIN #a");
    {
        let mut st = c.mu.lock();
        st.ignore_list = vec!["Žygis".into(), "*!*@Ąžuolas.lt".into()];
        st.monitor_list = vec!["jūratė".into()];
    }
    c.parse_irc(":žYGIS!u@h PRIVMSG #a :šlamštas");
    c.parse_irc(":kitas!u@ąžuolas.lt PRIVMSG #a :šlamštas");
    assert!(!texts(&c, "#a").iter().any(|t| t == "šlamštas"));

    c.parse_irc(":srv 730 me :JŪRATĖ!u@h");
    assert!(c.mu.lock().monitor_online.contains(&c.mon_key("Jūratė")));
    sent(&q);

    // AWAY throttle raktas — tas pats lankstymas.
    c.parse_irc(":srv 353 me = #a :Ėglė");
    c.parse_irc(":Ėglė!u@h AWAY :pietūs");
    c.parse_irc(":ėglė!u@h AWAY :pietūs");
    let n = texts(&c, "#a")
        .iter()
        .filter(|t| t.contains("yra away"))
        .count();
    assert_eq!(n, 1);
}

#[test]
fn tab_completion_dedups_by_server_fold() {
    let (c, _q) = setup();
    c.parse_irc(":srv 005 me CASEMAPPING=rfc8265 :are supported");
    c.parse_irc(":me!u@h JOIN #a");
    c.parse_irc(":srv 353 me = #a :me Žygis");
    // Tas pats žmogus kalbėjo kita raidžių forma.
    c.parse_irc(":žygis!u@h PRIVMSG #a :labas");
    {
        let mut st = c.mu.lock();
        st.input = "žy".chars().collect();
        st.input_pos = st.input.len();
    }
    c.do_tab();
    let cands = c.mu.lock().tab_candidates.clone();
    assert_eq!(cands, vec!["Žygis".to_string()]);
}

fn sent(q: &WriteQueue) -> Vec<String> {
    let mut v = Vec::new();
    while let Ok(l) = q.rx.try_recv() {
        v.push(l);
    }
    v
}
