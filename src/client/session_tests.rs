//! Sesijos pagalbinių funkcijų ir išsaugojimo per `Client` testai.
//! Bendri config failai — tik po `config_dir_lock()` ir izoliuotu home.

use super::*;
use crate::config::default_config;
use crate::test_util::{config_dir_lock, Rng};
use std::sync::Arc;

fn client() -> Arc<Client> {
    let mut cfg = default_config();
    cfg.server = "irc.example.org".into();
    cfg.nick = "me".into();
    Client::new(&cfg)
}

#[test]
fn split_for_privmsg_property() {
    let mut r = Rng::new(0x5917);
    for _ in 0..3_000 {
        let target = ["#c", "nick", "#ilgas-kanalo-pavadinimas-ąčę"][r.below(3)];
        let mut text = r.interesting_string(700);
        if r.below(2) == 0 {
            text = "ž".repeat(r.below(900));
        }
        let parts = split_for_privmsg(target, &text);
        assert!(!parts.is_empty());
        assert_eq!(parts.concat(), text, "tekstas neprarandamas");
        for p in &parts {
            let line = format!("PRIVMSG {target} :{p}\r\n");
            assert!(line.len() <= MAX_LINE_BYTES, "{} B", line.len());
        }
        if !text.is_empty() {
            assert!(parts.iter().all(|p| !p.is_empty()), "{parts:?}");
        }
    }
}

#[test]
fn channel_log_file_dates_are_real() {
    assert!(is_channel_log_file("#k-2024-02-29.log", "#k"));
    assert!(!is_channel_log_file("#k-2023-02-29.log", "#k"));
    assert!(!is_channel_log_file("#k-2024-1-01.log", "#k"));
    assert!(!is_channel_log_file("#k-２０２４-01-01.log", "#k"));
    assert!(!is_channel_log_file("#k-.log", "#k"));
    assert!(!is_channel_log_file("", "#k"));
    // Ne ASCII skaitmenys / daugiabaičiai — be panikos.
    assert!(!is_channel_log_file("#k-2024-0ą-01.log", "#k"));
}

#[test]
fn history_round_trips_through_client() {
    let _g = config_dir_lock();
    let p = history_path().unwrap();
    let _ = std::fs::remove_file(&p);
    let c = client();
    c.mu.lock().history = vec!["/join #a".into(), "labas ąčę".into()];
    c.save_history();
    let c2 = client();
    c2.load_history();
    assert_eq!(c2.mu.lock().history, vec!["/join #a", "labas ąčę"]);
    // Tuščia istorija neperrašo esamo failo.
    client().save_history();
    assert!(std::fs::read_to_string(&p).unwrap().contains("labas"));
    let _ = std::fs::remove_file(&p);
}

#[test]
fn ignore_and_monitor_round_trip_through_client() {
    let _g = config_dir_lock();
    let ip = ignore_path().unwrap();
    let mp = monitor_path().unwrap();
    let c = client();
    c.mu.lock().ignore_list = vec!["spam*".into(), "bot".into()];
    c.save_ignore();
    let c2 = client();
    c2.load_ignore();
    assert_eq!(c2.mu.lock().ignore_list, vec!["spam*", "bot"]);
    crate::persist::save_monitor(&mp, &["gerasis".into(), "b@d".into()]).unwrap();
    c2.load_monitor();
    assert_eq!(c2.mu.lock().monitor_list, vec!["gerasis"]);
    let _ = std::fs::remove_file(ip);
    let _ = std::fs::remove_file(mp);
}

#[test]
fn unreadable_history_reports_error_instead_of_wiping() {
    let _g = config_dir_lock();
    let p = history_path().unwrap();
    crate::persist::ensure_parent_dir(&p).unwrap();
    std::fs::write(&p, b"\xff\xfe\n").unwrap();
    let c = client();
    c.mu.lock().history = vec!["senas".into()];
    c.load_history();
    assert_eq!(
        c.mu.lock().history,
        vec!["senas"],
        "klaida — atmintis nepaliesta"
    );
    let st = c.mu.lock();
    let i = st.find_ch(crate::client::types::SERVER_WINDOW).unwrap();
    assert!(st.channels[i]
        .messages
        .iter()
        .any(|m| m.text.contains("Istorija neperskaityta")));
    drop(st);
    let _ = std::fs::remove_file(&p);
}

#[test]
fn load_keys_reads_isolated_keys_file() {
    let _g = config_dir_lock();
    let p = keys_path().unwrap();
    crate::persist::ensure_parent_dir(&p).unwrap();
    std::fs::write(&p, "search = \\eS\nblogas = \\x01\n").unwrap();
    let c = client();
    let warns = c.load_keys();
    assert_eq!(warns.len(), 1);
    assert_eq!(
        c.mu.lock().custom_binds.get("\x1bS").map(String::as_str),
        Some("search")
    );
    std::fs::remove_file(&p).unwrap();
    assert!(c.load_keys().is_empty());
    assert!(c.mu.lock().custom_binds.is_empty());
}
