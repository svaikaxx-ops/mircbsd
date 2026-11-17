//! Kanalų raktai: /join be ryšio ar prieš 001, grįžimas po pakartotinio
//! prisijungimo, MODE +k/-k, 324, /part, autojoin, JOIN eilučių biudžetas.

use crate::client::chan_keys::join_lines;
use crate::client::Client;
use crate::input::testutil::*;

fn welcome(c: &Client) {
    let me = c.my_nick();
    c.parse_irc(&format!(":srv 001 {me} :Welcome"));
}

fn all_texts(c: &Client) -> Vec<String> {
    c.mu.lock()
        .channels
        .iter()
        .flat_map(|ch| ch.messages.iter().map(|m| m.text.clone()))
        .collect()
}

#[test]
fn key_of_offline_join_is_used_after_welcome() {
    let (c, q) = connected();
    c.mu.lock().connected = false;
    c.handle_cmd("join", "#slapta Raktas1");
    c.mu.lock().connected = true;
    welcome(&c);
    assert_eq!(sent(&q), vec!["JOIN #slapta Raktas1"]);
}

#[test]
fn key_of_join_before_welcome_is_used() {
    let (c, q) = connected();
    c.mu.lock().registered = false;
    c.handle_cmd("join", "#anksti Raktas2");
    assert!(sent(&q).is_empty());
    welcome(&c);
    assert_eq!(sent(&q), vec!["JOIN #anksti Raktas2"]);
}

#[test]
fn reconnect_rejoins_with_remembered_keys() {
    let (c, q) = connected();
    let me = c.my_nick();
    c.handle_cmd("join", "#a Raktas3");
    c.parse_irc(&format!(":{me}!u@h JOIN #a"));
    c.parse_irc(&format!(":{me}!u@h JOIN #b"));
    // Raktas sužinotas iš MODE (kitas operatorius uždėjo +k).
    c.parse_irc(&format!(":{me}!u@h JOIN #c"));
    c.parse_irc(":op!u@h MODE #c +lk 10 Raktas4");
    sent(&q);
    c.mu.lock().note_disconnect();
    welcome(&c);
    // Kanalai su raktais pirmi, raktai sulygiuoti.
    assert_eq!(sent(&q), vec!["JOIN #a,#c,#b Raktas3,Raktas4"]);
}

#[test]
fn mode_key_is_masked_and_minus_k_forgets_it() {
    let (c, q) = connected();
    let me = c.my_nick();
    c.parse_irc(&format!(":{me}!u@h JOIN #m"));
    c.parse_irc(":op!u@h MODE #m +k Slapt4s");
    assert!(
        !all_texts(&c).iter().any(|t| t.contains("Slapt4s")),
        "raktas parodytas: {:?}",
        all_texts(&c)
    );
    assert!(texts(&c, "#m").iter().any(|t| t == "Mode #m: +k ***"));
    assert_eq!(c.mu.lock().chan_key("#M"), Some("Slapt4s"));

    c.parse_irc(":op!u@h MODE #m -k Slapt4s");
    assert!(!all_texts(&c).iter().any(|t| t.contains("Slapt4s")));
    assert_eq!(c.mu.lock().chan_key("#m"), None);
    sent(&q);
    c.mu.lock().note_disconnect();
    welcome(&c);
    assert_eq!(sent(&q), vec!["JOIN #m"]);
}

#[test]
fn channel_mode_is_324_stores_and_masks_key() {
    let (c, _q) = connected();
    let me = c.my_nick();
    c.parse_irc(&format!(":{me}!u@h JOIN #r"));
    c.parse_irc(&format!(":srv 324 {me} #r +nlk 5 Raktas5"));
    assert_eq!(c.mu.lock().chan_key("#r"), Some("Raktas5"));
    assert!(!all_texts(&c).iter().any(|t| t.contains("Raktas5")));
    assert!(texts(&c, "#r")
        .iter()
        .any(|t| t == "Kanalo #r režimai: +nlk 5 ***"));
    // „*“ (paslėptas ne operatoriams) įsiminto rakto neperrašo.
    c.parse_irc(&format!(":srv 324 {me} #r +k *"));
    assert_eq!(c.mu.lock().chan_key("#r"), Some("Raktas5"));
}

#[test]
fn part_and_close_forget_the_key() {
    let (c, q) = connected();
    let me = c.my_nick();
    c.handle_cmd("join", "#p Raktas6");
    c.parse_irc(&format!(":{me}!u@h JOIN #p"));
    open_active(&c, "#p");
    c.handle_cmd("part", "");
    assert_eq!(c.mu.lock().chan_key("#p"), None);
    c.parse_irc(&format!(":{me}!u@h PART #p"));

    // /part be ryšio: kanalas neberejoin'inamas, raktas pamirštas.
    c.handle_cmd("join", "#x Raktas7");
    c.parse_irc(&format!(":{me}!u@h JOIN #x"));
    c.mu.lock().note_disconnect();
    c.mu.lock().connected = false;
    open_active(&c, "#x");
    c.handle_cmd("part", "");
    assert_eq!(c.mu.lock().chan_key("#x"), None);
    c.mu.lock().connected = true;
    sent(&q);
    welcome(&c);
    assert!(sent(&q).is_empty());

    c.handle_cmd("join", "#y Raktas8");
    c.handle_cmd("close", "#y");
    assert_eq!(c.mu.lock().chan_key("#y"), None);
}

#[test]
fn server_part_of_self_forgets_the_key() {
    let (c, _q) = connected();
    let me = c.my_nick();
    c.handle_cmd("join", "#s Raktas9");
    c.parse_irc(&format!(":{me}!u@h JOIN #s"));
    assert_eq!(c.mu.lock().chan_key("#s"), Some("Raktas9"));
    c.parse_irc(&format!(":{me}!u@h PART #S :bye"));
    assert_eq!(c.mu.lock().chan_key("#s"), None);
}

#[test]
fn join_without_key_reuses_remembered_key() {
    let (c, q) = connected();
    let me = c.my_nick();
    c.parse_irc(&format!(":{me}!u@h JOIN #k"));
    c.parse_irc(":op!u@h MODE #k +k Raktas10");
    c.parse_irc(":op!u@h KICK #k me :lauk");
    sent(&q);
    c.handle_cmd("join", "#k");
    assert_eq!(sent(&q), vec!["JOIN #k Raktas10"]);
}

#[test]
fn autojoin_keys_go_first() {
    let (c, q) = connected();
    c.mu.lock().auto_join = vec!["#b".into(), "#a Raktas11".into(), "bad!name".into()];
    welcome(&c);
    assert_eq!(sent(&q), vec!["JOIN #a,#b Raktas11"]);
    assert!(server_texts(&c).iter().any(|t| t.contains("autojoin")));
}

#[test]
fn keys_with_list_separators_are_rejected() {
    let (c, q) = connected();
    c.handle_cmd("join", "#z a,b");
    c.handle_cmd("join", "#z :ab");
    assert!(sent(&q).is_empty());
    assert_eq!(c.mu.lock().chan_key("#z"), None);
}

#[test]
fn join_lines_keep_lists_aligned_within_budget() {
    let mut entries: Vec<(String, Option<String>)> = Vec::new();
    for i in 0..60 {
        let chan = format!("#kanalas-numeris-{i:03}");
        let key = (i % 3 == 0).then(|| format!("Raktas{i:03}"));
        entries.push((chan, key));
    }
    let lines = join_lines(&entries);
    assert!(lines.len() > 1, "{lines:?}");
    let mut seen = Vec::new();
    let mut keyed_done = false;
    for l in &lines {
        assert!(l.len() <= 510, "{} baitų: {l}", l.len());
        let mut parts = l.split(' ');
        assert_eq!(parts.next(), Some("JOIN"));
        let chans: Vec<&str> = parts.next().unwrap().split(',').collect();
        let keys: Vec<&str> = parts.next().map_or(Vec::new(), |k| k.split(',').collect());
        assert!(parts.next().is_none());
        // Raktai priklauso PIRMIESIEMS kanalams.
        for (i, ch) in chans.iter().enumerate() {
            let want = entries.iter().find(|(c, _)| c == ch).unwrap().1.as_deref();
            assert_eq!(keys.get(i).copied(), want, "{l}");
        }
        if keys.len() < chans.len() {
            keyed_done = true;
        } else {
            assert!(!keyed_done, "kanalas su raktu po kanalų be rakto: {l}");
        }
        seen.extend(chans.iter().map(|s| s.to_string()));
    }
    seen.sort();
    let mut want: Vec<String> = entries.iter().map(|(c, _)| c.clone()).collect();
    want.sort();
    assert_eq!(seen, want);
    assert!(join_lines(&[]).is_empty());
}
