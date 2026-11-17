//! IRC eilutės analizatoriaus kraštiniai atvejai + „fuzz“ ciklai.

use super::*;
use crate::test_util::Rng;

#[test]
fn every_tag_escape_is_unescaped() {
    let l = parse_line(r"@k=a\:b\sc\\d\re\nf PING").unwrap();
    assert_eq!(l.tags["k"], "a;b c\\d\re\nf");
}

#[test]
fn unknown_escape_drops_backslash_and_trailing_backslash_vanishes() {
    assert_eq!(unescape_tag_value(r"a\bc"), "abc");
    assert_eq!(unescape_tag_value(r"abc\"), "abc");
    assert_eq!(unescape_tag_value(r"\\\"), "\\");
    assert_eq!(unescape_tag_value(""), "");
}

#[test]
fn tag_without_value_empty_key_and_empty_segments() {
    let l = parse_line("@a;=x;;b=;+draft/reply=id1;vendor.org/k=v CMD").unwrap();
    assert_eq!(l.tags.len(), 4, "{:?}", l.tags);
    assert_eq!(l.tags["a"], "");
    assert_eq!(l.tags["b"], "");
    assert_eq!(l.tags["+draft/reply"], "id1");
    assert_eq!(l.tags["vendor.org/k"], "v");
    assert!(!l.tags.contains_key(""));
}

#[test]
fn duplicate_tag_last_wins() {
    let t = parse_tags("k=1;k=2");
    assert_eq!(t["k"], "2");
}

#[test]
fn tag_value_may_contain_equals() {
    let t = parse_tags("k=a=b");
    assert_eq!(t["k"], "a=b");
}

#[test]
fn trailing_keeps_spaces_and_colons() {
    let l = parse_line(":n!u@h PRIVMSG #c ::-) a  b : c ").unwrap();
    assert_eq!(l.params, vec!["#c", ":-) a  b : c "]);
}

#[test]
fn colon_inside_middle_param_is_not_trailing() {
    let l = parse_line("CMD a:b :c d").unwrap();
    assert_eq!(l.params, vec!["a:b", "c d"]);
}

#[test]
fn trailing_space_without_param() {
    let l = parse_line("PING x ").unwrap();
    assert_eq!(l.params, vec!["x"]);
}

#[test]
fn empty_prefix_is_accepted() {
    let l = parse_line(": PRIVMSG x :y").unwrap();
    assert_eq!(l.prefix, "");
    assert_eq!(l.from_nick(), "");
    assert_eq!(l.cmd, "PRIVMSG");
}

#[test]
fn tags_then_prefix_without_command_is_rejected() {
    assert!(parse_line("@a=b :pfx").is_none());
    assert!(parse_line("@a=b").is_none());
    assert!(parse_line("@").is_none());
    assert!(parse_line(":").is_none());
}

#[test]
fn numeric_and_lowercase_commands_kept_verbatim() {
    assert_eq!(parse_line(":srv 001 me :hi").unwrap().cmd, "001");
    assert_eq!(parse_line("privmsg a b").unwrap().cmd, "privmsg");
}

#[test]
fn utf8_everywhere() {
    let l = parse_line("@žyma=ąčę :Žygis!ū@šaltinis PRIVMSG #kanalas :labas 中文 😀").unwrap();
    assert_eq!(l.tags["žyma"], "ąčę");
    assert_eq!(l.from_nick(), "Žygis");
    assert_eq!(l.param(1), "labas 中文 😀");
}

#[test]
fn lines_longer_than_512_bytes_parse_completely() {
    // IRCv3 žymos nepatenka į 512 baitų biudžetą — ilgos eilutės turi būti
    // išanalizuotos visos (skaitymo buferis atmeta tik > MAX_READ_BUFFER).
    let tag = "x".repeat(4000);
    let body = "ą".repeat(400); // 800 baitų
    let line = format!("@msgid={tag};time=2024-01-02T03:04:05Z :n!u@h PRIVMSG #c :{body}");
    assert!(line.len() > 4500);
    let l = parse_line(&line).unwrap();
    assert_eq!(l.tags["msgid"].len(), 4000);
    assert!(l.server_time().is_some());
    assert_eq!(l.param(1), body);
}

#[test]
fn many_params() {
    let line = format!("CMD {}:end", "p ".repeat(30));
    let l = parse_line(&line).unwrap();
    assert_eq!(l.params.len(), 31);
    assert_eq!(l.params[30], "end");
}

#[test]
fn server_time_variants() {
    let t = |v: &str| server_time(&parse_tags(&format!("time={v}")));
    let a = t("2024-03-01T10:00:00Z").unwrap();
    let b = t("2024-03-01T12:00:00+02:00").unwrap();
    assert_eq!(a, b, "laiko juosta konvertuojama į UTC");
    assert!(t("2024-03-01T10:00:00.123456Z").is_some());
    assert!(t("2024-13-01T10:00:00Z").is_none());
    assert!(t("2024-03-01 10:00:00").is_none());
    assert!(t("").is_none());
    assert!(server_time(&HashMap::new()).is_none());
}

#[test]
fn from_nick_variants() {
    assert_eq!(from_nick("a!b@c"), "a");
    assert_eq!(from_nick("!b@c"), "");
    assert_eq!(from_nick("a!b!c"), "a");
    assert_eq!(from_nick(""), "");
}

// ---- savybių („fuzz“) testai ----

/// Atsitiktinės įvestys iš IRC sintaksei pavojingų simbolių.
fn irc_noise(r: &mut Rng) -> String {
    const TOK: &[&str] = &[
        "@", ":", " ", "  ", ";", "=", "\\", "\\s", "a", "B", "1", "ą", "中", "😀", "\r", "\n",
        "\0", "!", "PRIVMSG", "#c",
    ];
    let n = r.below(24);
    (0..n).map(|_| *r.pick(TOK)).collect()
}

#[test]
fn parse_never_panics_on_arbitrary_input() {
    let mut r = Rng::new(0x1ec0_1ec0);
    for i in 0..50_000 {
        let s = if i % 2 == 0 {
            irc_noise(&mut r)
        } else {
            String::from_utf8_lossy(&r.bytes(80)).into_owned()
        };
        if let Some(l) = parse_line(&s) {
            // Invariantai: komanda be tarpų; ne paskutiniai parametrai be
            // tarpų ir neprasideda „:“.
            assert!(!l.cmd.contains(' '), "{s:?}");
            if let Some((_last, mid)) = l.params.split_last() {
                for p in mid {
                    assert!(!p.contains(' ') && !p.starts_with(':'), "{s:?} → {p:?}");
                }
            }
            let _ = l.server_time();
            let _ = l.from_nick();
        }
    }
}

#[test]
fn structured_lines_round_trip() {
    let mut r = Rng::new(42);
    let word = |r: &mut Rng| -> String {
        const C: &[char] = &['a', 'z', '0', '#', 'ą', '中', '-', '[', '!', '@', '.'];
        let n = 1 + r.below(8);
        (0..n).map(|_| *r.pick(C)).collect()
    };
    for _ in 0..5_000 {
        let prefix = if r.below(2) == 0 {
            word(&mut r)
        } else {
            String::new()
        };
        // Komanda negali prasidėti „@“ (tai būtų žymų blokas).
        let cmd = format!("C{}", word(&mut r));
        let mids: Vec<String> = (0..r.below(5)).map(|_| word(&mut r)).collect();
        let trailing = (r.below(2) == 0).then(|| {
            let mut t = r.interesting_string(20);
            t.retain(|c| c != '\r' && c != '\n');
            t
        });
        let mut line = String::new();
        if !prefix.is_empty() {
            line.push_str(&format!(":{prefix} "));
        }
        line.push_str(&cmd);
        for m in &mids {
            line.push(' ');
            line.push_str(m);
        }
        if let Some(t) = &trailing {
            line.push_str(" :");
            line.push_str(t);
        }
        let l = parse_line(&line).unwrap_or_else(|| panic!("{line:?}"));
        assert_eq!(l.prefix, prefix, "{line:?}");
        assert_eq!(l.cmd, cmd, "{line:?}");
        let mut want = mids.clone();
        if let Some(t) = trailing {
            want.push(t);
        }
        assert_eq!(l.params, want, "{line:?}");
    }
}
