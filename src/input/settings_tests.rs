//! `/set` per `Client`: patikra, gyvas pritaikymas ir įrašas į config.
//! Keičiami tik kliento lygio raktai — globalios temos/laiko būsenos
//! nekeičiam (kiti lygiagretūs testai nuo jos priklauso); globaliems
//! raktams tikrinamas tik atmetimas.

use super::testutil::*;
use crate::config::{config_path, load_config};
use crate::test_util::config_dir_lock;

fn config_text() -> String {
    std::fs::read_to_string(config_path().unwrap()).unwrap_or_default()
}

fn fresh_config() {
    let _ = std::fs::remove_file(config_path().unwrap());
}

#[test]
fn set_client_bools_apply_live_and_persist() {
    let _g = config_dir_lock();
    fresh_config();
    let c = client();
    c.mu.lock().mouse = false;
    for (cmd, key) in [
        ("notifications", "notifications"),
        ("ctcpversion", "ctcpversion"),
        ("reconnect", "reconnect"),
    ] {
        c.handle_input(&format!("/set {cmd} off"));
        c.handle_input(&format!("/set {cmd} ON"));
        assert!(
            config_text().contains(&format!("{key}=ON")),
            "{}",
            config_text()
        );
    }
    {
        let st = c.mu.lock();
        assert!(st.notify && st.ctcp_version && st.reconnect);
    }
    c.handle_input("/set reconnect no");
    assert!(!c.mu.lock().reconnect);
    let (cfg, _) = load_config(&config_path().unwrap());
    assert!(!cfg.reconnect && cfg.notifications && cfg.ctcp_version);
    assert!(server_texts(&c)
        .iter()
        .any(|t| t == "nustatymas: reconnect = no"));
    fresh_config();
}

#[test]
fn set_maxmessages_range_and_live_apply() {
    let _g = config_dir_lock();
    fresh_config();
    let c = client();
    open_active(&c, "#a");
    for bad in ["99", "100001", "abc", "-5", "1e3"] {
        c.handle_input(&format!("/set maxmessages {bad}"));
    }
    assert_eq!(config_text(), "", "atmestos reikšmės neįrašomos");
    assert_eq!(
        server_texts(&c)
            .iter()
            .filter(|t| t.contains("100–100000"))
            .count(),
        5
    );
    c.handle_input("/set maxmessages 250");
    {
        let st = c.mu.lock();
        assert_eq!(st.max_messages, 250);
        assert!(st.channels.iter().all(|ch| ch.max_messages == 250));
    }
    assert_eq!(config_text(), "maxmessages=250\n");
    fresh_config();
}

#[test]
fn set_rejects_invalid_values_for_global_keys_without_writing() {
    let _g = config_dir_lock();
    fresh_config();
    let c = client();
    for cmd in [
        "/set timestamps taip",
        "/set nick_colors maybe",
        "/set mouse 2",
        "/set ts_format %H:%M",
        "/set ts_format 15:04\x1b[2J",
        "/set tscolor violetinė",
        "/set highlight_color rainbow",
        "/set theme solarized",
    ] {
        c.handle_input(cmd);
    }
    assert_eq!(config_text(), "");
    let errs = server_texts(&c);
    assert!(errs.iter().filter(|t| t.contains("true/false")).count() >= 3);
    assert!(errs.iter().any(|t| t.contains("Nevalidus laikmatis")));
    assert!(errs.iter().any(|t| t.contains("Nežinoma spalva")));
    assert!(errs.iter().any(|t| t.contains("Nežinoma tema")));
    // Klaidos pranešime terminalo sekos išvalytos.
    assert!(!errs.iter().any(|t| t.contains('\x1b')));
    fresh_config();
}

#[test]
fn set_unknown_or_forbidden_key() {
    let _g = config_dir_lock();
    fresh_config();
    let c = client();
    c.handle_input("/set server evil.example");
    c.handle_input("/set password x");
    assert_eq!(config_text(), "");
    assert_eq!(
        server_texts(&c)
            .iter()
            .filter(|t| t.contains("Nežinomas arba nekeičiamas raktas"))
            .count(),
        2
    );
}

#[test]
fn set_without_value_shows_and_without_args_lists() {
    let c = client();
    c.mu.lock().reconnect = false;
    c.handle_input("/set reconnect");
    assert!(server_texts(&c).iter().any(|t| t == "reconnect = false"));
    c.handle_input("/set");
    let t = server_texts(&c);
    for k in crate::config::SETTABLE_KEYS {
        assert!(t.iter().any(|l| l.starts_with(&format!("{k} = "))), "{k}");
    }
}
