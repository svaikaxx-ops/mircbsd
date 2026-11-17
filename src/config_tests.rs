//! Konfigūracijos skaitymo, `/set` įrašymo ir keys failo testai.
//! Visi keliai — po izoliuotu `test_home()`, ne tikru `$HOME`.

use super::*;
use crate::test_util::{config_dir_lock, TempDir};

fn load_str(body: &str) -> (Config, Vec<String>) {
    let d = TempDir::new("cfg");
    let p = d.join("config");
    std::fs::write(&p, body).unwrap();
    // 0600: kitaip config su slaptažodžiais duoda „chmod 600“ įspėjimą.
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o600)).unwrap();
    }
    load_config(&p)
}

#[test]
fn test_home_isolates_every_config_path() {
    let home = test_home();
    assert!(home.starts_with(std::env::temp_dir()), "{home:?}");
    if let Ok(real) = std::env::var("HOME") {
        if !real.is_empty() && !std::env::temp_dir().starts_with(&real) {
            assert!(!home.starts_with(&real), "testų home po tikru $HOME");
        }
    }
    for p in [
        config_dir(),
        config_path(),
        history_path(),
        ignore_path(),
        monitor_path(),
        keys_path(),
    ] {
        assert!(p.unwrap().starts_with(&home));
    }
    assert!(Path::new(&default_config().log_dir).starts_with(&home));
    assert!(Path::new(&expand_home_path("~/x.pem")).starts_with(&home));
}

#[test]
fn missing_file_yields_defaults_without_warnings() {
    let d = TempDir::new("cfg-missing");
    let (c, w) = load_config(&d.join("nera"));
    assert!(w.is_empty());
    assert_eq!(c.nick, "mircbsd");
    assert_eq!(c.port, "6667");
    assert_eq!(c.max_messages, 5000);
    assert!(c.reconnect && c.mouse && c.timestamps && c.nick_colors);
    assert!(!c.tls);
}

#[test]
fn comments_blank_and_garbage_lines_are_skipped() {
    let (c, w) = load_str(
        "# komentaras\n\n   \nbe lygybės ženklo\n=be rakto\nnežinomas=1\n  NICK = Jonas  \n",
    );
    assert!(w.is_empty(), "{w:?}");
    assert_eq!(c.nick, "Jonas");
}

#[test]
fn quotes_are_stripped_only_when_matching() {
    let (c, _) = load_str("realname=\"Vardas Pavardė\"\nnickserv='slaptas'\nperform=\"x\n");
    assert_eq!(c.real_name, "Vardas Pavardė");
    assert_eq!(c.nick_serv, "slaptas");
    assert_eq!(c.perform, vec!["\"x"]);
    let (c, _) = load_str("realname=\"\nnickserv=\"'\n");
    assert_eq!(c.real_name, "\"");
    assert_eq!(c.nick_serv, "\"'");
}

#[test]
fn value_may_contain_equals_sign() {
    let (c, _) = load_str("password=a=b=c\n");
    assert_eq!(c.server_pass, "a=b=c");
}

#[test]
fn invalid_port_warns_and_falls_back() {
    for bad in ["0", "65536", "abc", "-1", "", "6667x"] {
        let (c, w) = load_str(&format!("port=6697\nport={bad}\n"));
        assert_eq!(c.port, "6667", "{bad}");
        assert_eq!(w.len(), 1, "{bad}: {w:?}");
        assert!(w[0].contains("portas"), "{w:?}");
    }
    let (c, w) = load_str("port=65535\n");
    assert_eq!(c.port, "65535");
    assert!(w.is_empty());
}

#[test]
fn invalid_nick_warns_and_keeps_default() {
    for bad in ["has space", "#chan", "a,b", "a:b", &"x".repeat(31)] {
        let (c, w) = load_str(&format!("nick={bad}\n"));
        assert_eq!(c.nick, "mircbsd", "{bad}");
        assert_eq!(w.len(), 1, "{bad}");
    }
    // Teisingi priimami (įsk. RFC simbolius ir unikodo raides).
    for good in ["[bot]", "a_b-c", "Jonas", "Žygis"] {
        let (c, w) = load_str(&format!("nick={good}\n"));
        assert_eq!(c.nick, good);
        assert!(w.is_empty());
    }
}

#[test]
fn lists_are_split_and_trimmed() {
    let (c, _) = load_str(
        "autojoin= #a , ,#b,\nperform=MODE me +x ; ;PRIVMSG x :y\nhighlight_words=, labas ,vardas\n",
    );
    assert_eq!(c.auto_join, vec!["#a", "#b"]);
    assert_eq!(c.perform, vec!["MODE me +x", "PRIVMSG x :y"]);
    assert_eq!(c.highlight_words, vec!["labas", "vardas"]);
}

#[test]
fn maxmessages_rejects_non_positive_and_garbage() {
    for (v, want) in [
        ("200", 200),
        ("0", 5000),
        ("-5", 5000),
        ("x", 5000),
        ("1e3", 5000),
    ] {
        let (c, _) = load_str(&format!("maxmessages={v}\n"));
        assert_eq!(c.max_messages, want, "{v}");
    }
}

#[test]
fn home_relative_paths_expand_into_home() {
    let (c, _) = load_str("logdir=~/logai\ntls_cert=~/c.pem\ntls_key=/abs/k.pem\n");
    assert_eq!(Path::new(&c.log_dir), test_home().join("logai"));
    assert_eq!(Path::new(&c.tls_cert), test_home().join("c.pem"));
    assert_eq!(c.tls_key, "/abs/k.pem");
    // „~vartotojas/“ neplečiamas.
    assert_eq!(expand_home_path("~kitas/x"), "~kitas/x");
}

#[test]
fn tls_pin_and_theme_are_lowercased() {
    let (c, _) = load_str("tls_pin= AB:CD \ntheme=LIGHT\n");
    assert_eq!(c.tls_pin, "ab:cd");
    assert_eq!(c.theme, "light");
}

#[test]
fn show_joins_is_validated() {
    let (c, w) = load_str("show_joins=SMART\n");
    assert_eq!(c.show_joins, "smart");
    assert!(w.is_empty(), "{w:?}");
    let (c, w) = load_str("show_joins=kartais\n");
    assert_eq!(c.show_joins, "all");
    assert!(w.iter().any(|l| l.contains("show_joins")), "{w:?}");
    assert_eq!(crate::config::default_config().show_joins, "all");
}

#[test]
fn config_bools_accept_every_set_token() {
    // `/set` priima on/off/yes/no/1/0/true/false — config skaitytojas turi
    // suprasti VISUS tuos pačius žodžius, kitaip `/set mouse on` po
    // perkrovimo tyliai tampa false.
    for (v, want) in [
        ("true", true),
        ("1", true),
        ("yes", true),
        ("on", true),
        ("TRUE", true),
        ("false", false),
        ("0", false),
        ("no", false),
        ("off", false),
    ] {
        let (c, _) = load_str(&format!(
            "tls={v}\nmouse={v}\nreconnect={v}\nnotifications={v}\nctcpversion={v}\n\
             timestamps={v}\nnick_colors={v}\nsasl_external={v}\ntls_pinning={v}\n"
        ));
        let got = [
            c.tls,
            c.mouse,
            c.reconnect,
            c.notifications,
            c.ctcp_version,
            c.timestamps,
            c.nick_colors,
            c.sasl_external,
            c.tls_pinning,
        ];
        assert!(got.iter().all(|b| *b == want), "{v}: {got:?}");
    }
}

#[test]
fn unrecognized_bool_keeps_default_and_warns() {
    let (c, w) = load_str("mouse=bet kas\ntls=taip\n");
    assert!(c.mouse, "numatytas mouse=true lieka");
    assert!(!c.tls, "numatytas tls=false lieka");
    assert_eq!(w.len(), 2, "{w:?}");
}

#[test]
fn every_string_key_is_read() {
    let (c, w) = load_str(
        "server=irc.libera.chat\nrealname=R\nnickserv=N\npassword=P\nsasl=S\n\
         tscolor=blue\nts_format=15:04:05\nhighlight_color=red\n",
    );
    assert!(w.is_empty());
    assert_eq!(c.server, "irc.libera.chat");
    assert_eq!(
        (
            c.real_name.as_str(),
            c.nick_serv.as_str(),
            c.server_pass.as_str(),
            c.sasl_pass.as_str()
        ),
        ("R", "N", "P", "S")
    );
    assert_eq!(c.timestamp_color, "blue");
    assert_eq!(c.ts_format, "15:04:05");
    assert_eq!(c.highlight_color, "red");
}

// ---- /set įrašymas ----

fn read_config() -> String {
    std::fs::read_to_string(config_path().unwrap()).unwrap_or_default()
}

fn write_config(s: &str) {
    let p = config_path().unwrap();
    crate::persist::ensure_parent_dir(&p).unwrap();
    std::fs::write(p, s).unwrap();
}

#[test]
fn save_setting_replaces_in_place_and_preserves_rest() {
    let _g = config_dir_lock();
    write_config("# antraštė\nnick=me\n  Mouse = true\n# mouse=komentaras\nport=6697");
    save_config_setting("mouse", "false").unwrap();
    assert_eq!(
        read_config(),
        "# antraštė\nnick=me\n  Mouse =false\n# mouse=komentaras\nport=6697\n"
    );
    // Naujas raktas pridedamas gale.
    save_config_setting("theme", "light").unwrap();
    assert!(read_config().ends_with("port=6697\ntheme=light\n"));
    let _ = std::fs::remove_file(config_path().unwrap());
}

#[test]
fn save_setting_creates_private_file_when_missing() {
    let _g = config_dir_lock();
    let p = config_path().unwrap();
    let _ = std::fs::remove_file(&p);
    save_config_setting("timestamps", "false").unwrap();
    assert_eq!(read_config(), "timestamps=false\n");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            std::fs::metadata(&p).unwrap().permissions().mode() & 0o777,
            0o600
        );
    }
    let _ = std::fs::remove_file(&p);
}

#[test]
fn set_value_survives_reload() {
    // Kiekvienas `/set` priimamas bool žodis turi po perkrovimo duoti tą
    // pačią reikšmę.
    let _g = config_dir_lock();
    for v in ["on", "off", "yes", "no", "1", "0", "true", "false"] {
        let _ = std::fs::remove_file(config_path().unwrap());
        save_config_setting("mouse", v).unwrap();
        let (c, _) = load_config(&config_path().unwrap());
        assert_eq!(c.mouse, parse_bool_value(v).0, "/set mouse {v}");
    }
    let _ = std::fs::remove_file(config_path().unwrap());
}

#[test]
fn parse_bool_value_is_tri_state() {
    assert_eq!(parse_bool_value(" ON "), (true, true));
    assert_eq!(parse_bool_value("No"), (false, true));
    assert_eq!(parse_bool_value("taip"), (false, false));
    assert_eq!(parse_bool_value(""), (false, false));
}

#[test]
fn settable_keys_are_case_insensitive() {
    assert!(is_settable("MOUSE"));
    assert!(is_settable("ts_format"));
    assert!(!is_settable("server"));
    assert!(!is_settable("password"));
}

// ---- keys failas ----

#[test]
fn unescape_key_sequences() {
    assert_eq!(unescape_key_seq(r"\e[1;5A").unwrap(), b"\x1b[1;5A");
    assert_eq!(unescape_key_seq(r"\E\n\r\t\\").unwrap(), b"\x1b\n\r\t\\");
    assert_eq!(unescape_key_seq(r"\x12\x7F").unwrap(), b"\x12\x7f");
    assert_eq!(unescape_key_seq("plain").unwrap(), b"plain");
    assert!(unescape_key_seq(r"\").is_err());
    assert!(unescape_key_seq(r"\x1").is_err());
    assert!(unescape_key_seq(r"\xZZ").is_err());
    assert!(unescape_key_seq(r"\q").is_err());
    // \x + daugiabaitis UTF-8 — klaida, ne panika (baitų riba).
    assert!(unescape_key_seq("\\xą").is_err());
    assert!(unescape_key_seq("\\x1ą").is_err());
}

#[test]
fn unescape_key_seq_never_panics() {
    let mut r = crate::test_util::Rng::new(0x6b65_7973);
    for _ in 0..20_000 {
        let mut s = r.interesting_string(12);
        if r.below(2) == 0 {
            s.insert_str(0, "\\x");
        }
        let _ = unescape_key_seq(&s);
    }
}

#[test]
fn keys_file_parsing_and_warnings() {
    let d = TempDir::new("keys");
    let p = d.join("keys");
    std::fs::write(
        &p,
        "# komentaras\n\
         search = \\eS\n\
         be lygybės\n\
         nezinomas = \\x01\n\
         complete = \\q\n\
         chan-next = \\eS\n\
         chan-prev = \\x01\\x02\\x03\\x04\\x05\\x06\\x07\\x08\\x09\\x0a\\x0b\\x0c\\x0d\\x0e\\x0f\\x10\\x11\n\
         Word-Del = \\x17\n",
    )
    .unwrap();
    let (binds, warns) = parse_keys_file(&p);
    assert_eq!(binds.len(), 2, "{binds:?}");
    assert_eq!(binds.get("\x1bS").map(String::as_str), Some("search"));
    assert_eq!(binds.get("\x17").map(String::as_str), Some("word-del"));
    assert_eq!(warns.len(), 5, "{warns:?}");
    assert!(warns[0].starts_with("keys:3:"));
    assert!(warns[1].contains("nežinomas veiksmas"));
    assert!(warns[2].contains("nežinoma seka"));
    assert!(warns[3].contains("jau perrašyta"));
    assert!(warns[4].contains("per ilga"));
    // Trūkstamas failas — nei susiejimų, nei įspėjimų.
    let (b, w) = parse_keys_file(&d.join("nera"));
    assert!(b.is_empty() && w.is_empty());
}

#[test]
fn key_action_names_are_sorted_and_complete() {
    let names = key_action_names();
    let mut sorted = names.clone();
    sorted.sort();
    assert_eq!(names, sorted);
    assert_eq!(names.len(), key_actions().len());
    assert!(names.contains(&"search".to_string()));
}
