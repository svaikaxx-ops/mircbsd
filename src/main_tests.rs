//! Žurnalų rašytojo testai (binarinis modulis). Katalogai — `$TMPDIR`
//! (ne `$HOME`), kiekvienas testas savo.

use super::*;
use mircbsd::ui::Message;

struct Tmp(std::path::PathBuf);

impl Tmp {
    fn new(tag: &str) -> Self {
        static SEQ: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
        let d = std::env::temp_dir().join(format!(
            "mircbsd-main-{tag}-{}-{}",
            std::process::id(),
            SEQ.fetch_add(1, Ordering::Relaxed)
        ));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        Tmp(d)
    }
    fn s(&self) -> String {
        self.0.to_string_lossy().into_owned()
    }
}

impl Drop for Tmp {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn entry(dir: &str, chan: &str, kind: MsgType, nick: &str, text: &str) -> LogEntry {
    LogEntry {
        log_dir: dir.into(),
        server: "irc.example.org".into(),
        chan_name: chan.into(),
        msg: Message::new(
            mircbsd::timeutil::utc(2026, 3, 5, 12, 0, 0),
            nick,
            text,
            kind,
            false,
        ),
    }
}

#[test]
fn log_line_per_message_type() {
    let body = |k, t: &str| {
        let l = format_log_line(&entry("", "#c", k, "jonas", t));
        // „[YYYY-MM-DD HH:MM:SS] “ prefiksas — 22 simboliai.
        assert!(l.starts_with('[') && l.ends_with('\n'), "{l:?}");
        l[22..].trim_end().to_string()
    };
    assert_eq!(body(MsgType::Chat, "labas"), "<jonas> labas");
    assert_eq!(body(MsgType::Action, "moja"), "* jonas moja");
    assert_eq!(body(MsgType::Join, ""), "--> jonas prisijungė");
    assert_eq!(body(MsgType::Part, "ate"), "<-- jonas išėjo (ate)");
    assert_eq!(body(MsgType::Quit, "q"), "<-- jonas atsijungė (q)");
    assert_eq!(body(MsgType::Nick, "petras"), "*** jonas → petras");
    assert_eq!(body(MsgType::Notice, "n"), "-jonas- n");
    assert_eq!(body(MsgType::Server, "s"), "s");
    assert_eq!(body(MsgType::Error, "e"), "e");
}

#[test]
fn log_line_strips_formatting_and_cannot_inject_lines() {
    let l = format_log_line(&entry(
        "",
        "#c",
        MsgType::Chat,
        "ni\nck\x1b",
        "\x02bold\x02 a\r\n[2026-01-01 00:00:00] <admin> netikra\x1b[2J",
    ));
    assert_eq!(l.matches('\n').count(), 1, "{l:?}");
    assert!(!l.contains('\x1b') && !l.contains('\r') && !l.contains('\x02'));
    assert!(l.contains("<nick> bold a[2026-01-01"));
}

#[test]
fn log_line_property_single_line_no_controls() {
    // Paprastas fiksuotos sėklos LCG (bin modulis neturi lib test_util).
    let mut x: u64 = 0x10_6106;
    let mut next = || {
        x = x
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
        (x >> 33) as u32
    };
    let pool: Vec<char> = "a ž中😀\n\r\t\x1b\x07\x03\x02\u{85}\u{9b}\0[]<>"
        .chars()
        .collect();
    let kinds = [
        MsgType::Chat,
        MsgType::Action,
        MsgType::Notice,
        MsgType::Part,
        MsgType::Server,
    ];
    for _ in 0..5_000 {
        let mut s = String::new();
        for _ in 0..(next() % 40) {
            s.push(pool[next() as usize % pool.len()]);
        }
        let kind = kinds[next() as usize % kinds.len()];
        let l = format_log_line(&entry("", "#c", kind, &s, &s));
        assert_eq!(l.matches('\n').count(), 1, "{s:?} → {l:?}");
        assert!(l.ends_with('\n'));
        assert!(
            !l.trim_end_matches('\n')
                .chars()
                .any(|c| c != '\t' && ((c as u32) < 0x20 || (0x7f..=0x9f).contains(&(c as u32)))),
            "{s:?} → {l:?}"
        );
    }
}

#[test]
fn identify_to_nickserv_is_redacted_but_normal_text_is_not() {
    let l = format_log_line(&entry(
        "",
        "#c",
        MsgType::Chat,
        "x",
        "/msg nickserv identify slaptas",
    ));
    assert!(!l.contains("slaptas"), "{l}");
    let l = format_log_line(&entry("", "#c", MsgType::Chat, "x", "šiandien graži diena"));
    assert!(l.contains("graži diena"));
}

#[test]
fn write_log_entry_creates_private_file_and_appends() {
    let d = Tmp::new("write");
    let mut files = HashMap::new();
    write_log_entry(
        &mut files,
        &entry(&d.s(), "#kanalas", MsgType::Chat, "a", "pirma"),
    )
    .unwrap();
    write_log_entry(
        &mut files,
        &entry(&d.s(), "#kanalas", MsgType::Chat, "b", "antra"),
    )
    .unwrap();
    assert_eq!(files.len(), 1);
    let day = format_go_time(
        &mircbsd::timeutil::local(mircbsd::timeutil::utc(2026, 3, 5, 12, 0, 0)),
        "2006-01-02",
    );
    let p =
        d.0.join("irc.example.org")
            .join(format!("#kanalas-{day}.log"));
    drop(files);
    let text = fs::read_to_string(&p).unwrap();
    assert_eq!(text.lines().count(), 2);
    assert!(text.contains("<a> pirma") && text.contains("<b> antra"));
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = |p: &Path| fs::metadata(p).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode(&p), 0o600);
        assert_eq!(mode(&d.0.join("irc.example.org")), 0o700);
    }
}

#[test]
fn write_log_entry_without_dir_is_noop() {
    let mut files = HashMap::new();
    write_log_entry(&mut files, &entry("", "#c", MsgType::Chat, "a", "b")).unwrap();
    assert!(files.is_empty());
}

#[test]
fn hostile_channel_and_server_names_stay_inside_log_dir() {
    let d = Tmp::new("trav");
    let mut files = HashMap::new();
    let mut e = entry(&d.s(), "../../pabėgau", MsgType::Chat, "a", "x");
    e.server = "../srv".into();
    write_log_entry(&mut files, &e).unwrap();
    drop(files);
    // Visi sukurti failai — log_dir viduje, nieko šalia jo.
    let parent = d.0.parent().unwrap();
    assert!(!parent.join("pabėgau").exists());
    let mut found = Vec::new();
    for sub in fs::read_dir(&d.0).unwrap() {
        let sub = sub.unwrap().path();
        assert!(sub.is_dir(), "{sub:?}");
        for f in fs::read_dir(&sub).unwrap() {
            found.push(f.unwrap().path());
        }
    }
    assert_eq!(found.len(), 1, "{found:?}");
    assert!(found[0].starts_with(&d.0));
}

#[test]
fn day_rollover_replaces_open_handle_and_close_filters() {
    let d = Tmp::new("roll");
    let mut files = HashMap::new();
    let mut e = entry(&d.s(), "#a", MsgType::Chat, "a", "x");
    write_log_entry(&mut files, &e).unwrap();
    e.msg.ts = mircbsd::timeutil::utc(2026, 3, 7, 12, 0, 0);
    write_log_entry(&mut files, &e).unwrap();
    assert_eq!(files.len(), 1, "senos dienos rankena uždaryta");
    write_log_entry(&mut files, &entry(&d.s(), "#b", MsgType::Chat, "a", "x")).unwrap();
    let mut other = entry(&d.s(), "#a", MsgType::Chat, "a", "x");
    other.server = "irc.example.org.kitas".into();
    write_log_entry(&mut files, &other).unwrap();
    assert_eq!(files.len(), 3);
    // Tik #a šiame serveryje (ne panašaus pavadinimo serveris).
    close_log_files(&mut files, "irc.example.org", "#a");
    assert_eq!(files.len(), 2);
    close_log_files(&mut files, "irc.example.org", "");
    assert_eq!(files.len(), 1);
    assert!(files
        .keys()
        .all(|k| k.starts_with("irc.example.org.kitas/")));
    close_log_files(&mut files, "", "");
    assert!(files.is_empty());
}

#[test]
fn clip_runes_counts_chars_not_bytes() {
    assert_eq!(clip_runes("ąčę", 5, true), "ąčę");
    assert_eq!(clip_runes("ąčęėį", 3, true), "ąčę…");
    assert_eq!(clip_runes("ąčęėį", 3, false), "ąčę");
    assert_eq!(clip_runes("", 0, true), "");
    assert_eq!(clip_runes("a", 0, true), "…");
}
