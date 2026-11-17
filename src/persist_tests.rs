//! Išsaugojimo/įkėlimo testai laikinuose kataloguose (ne tikrame $HOME).

use super::*;
use crate::test_util::{Rng, TempDir};

fn v(items: &[&str]) -> Vec<String> {
    items.iter().map(|s| s.to_string()).collect()
}

#[test]
fn missing_files_load_as_empty() {
    let d = TempDir::new("persist-missing");
    let p = d.join("nera");
    assert!(load_lines(&p).unwrap().is_empty());
    assert!(load_history(&p).unwrap().is_empty());
    assert!(load_ignore(&p).unwrap().is_empty());
    assert!(load_monitor(&p).unwrap().is_empty());
}

#[test]
fn lines_round_trip_and_skip_empty() {
    let d = TempDir::new("persist-lines");
    let p = d.join("a");
    let data = v(&["pirma", "  su tarpais  ", "ąčę 中文 😀", "/msg x :y"]);
    save_lines(&p, &data).unwrap();
    assert_eq!(load_lines(&p).unwrap(), data);
    std::fs::write(&p, "a\n\n\nb\r\n").unwrap();
    // `lines()` nuima CRLF, tuščios praleidžiamos.
    assert_eq!(load_lines(&p).unwrap(), v(&["a", "b"]));
}

#[test]
fn history_keeps_only_last_1000() {
    let d = TempDir::new("persist-hist");
    let p = d.join("history");
    let lines: Vec<String> = (0..1500).map(|i| format!("eilutė {i}")).collect();
    save_history(&p, &lines).unwrap();
    let back = load_history(&p).unwrap();
    assert_eq!(back.len(), 1000);
    assert_eq!(back[0], "eilutė 500");
    assert_eq!(back[999], "eilutė 1499");
}

#[test]
fn ignore_list_is_trimmed() {
    let d = TempDir::new("persist-ign");
    let p = d.join("ignore");
    save_ignore(&p, &v(&["bob", "*!*@spam.example"])).unwrap();
    assert_eq!(load_ignore(&p).unwrap(), v(&["bob", "*!*@spam.example"]));
    std::fs::write(&p, "  alice  \n\t\n bob\n").unwrap();
    assert_eq!(load_ignore(&p).unwrap(), v(&["alice", "bob"]));
}

#[test]
fn monitor_caps_at_100_and_drops_invalid_nicks() {
    let d = TempDir::new("persist-mon");
    let p = d.join("notify");
    let many: Vec<String> = (0..150).map(|i| format!("n{i}")).collect();
    save_monitor(&p, &many).unwrap();
    let back = load_monitor(&p).unwrap();
    assert_eq!(back.len(), 100);
    assert_eq!(back[99], "n99");
    std::fs::write(&p, "gerasis\nb@d\nsu tarpu\n#kanalas\n  [ok]  \n").unwrap();
    assert_eq!(load_monitor(&p).unwrap(), v(&["gerasis", "[ok]"]));
}

#[test]
fn atomic_write_creates_private_parent_dirs() {
    let d = TempDir::new("persist-dirs");
    let p = d.join("x/y/z/file");
    atomic_write(&p, b"duomenys").unwrap();
    assert_eq!(std::fs::read(&p).unwrap(), b"duomenys");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = |p: &Path| std::fs::metadata(p).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode(&d.join("x/y/z")), 0o700);
        assert_eq!(mode(&p), 0o600);
    }
    // Perrašymas nepalieka laikinų failų.
    atomic_write(&p, b"antri").unwrap();
    let names: Vec<_> = std::fs::read_dir(d.join("x/y/z"))
        .unwrap()
        .map(|e| e.unwrap().file_name())
        .collect();
    assert_eq!(names, vec![std::ffi::OsString::from("file")]);
}

#[test]
fn atomic_write_failure_leaves_no_temp_file() {
    let d = TempDir::new("persist-fail");
    // Tikslas — katalogas: rename nepavyks, laikinas failas turi dingti.
    let target = d.join("katalogas");
    std::fs::create_dir(&target).unwrap();
    std::fs::write(target.join("viduje"), b"x").unwrap();
    assert!(atomic_write(&target, b"y").is_err());
    let names: Vec<_> = std::fs::read_dir(d.path())
        .unwrap()
        .map(|e| e.unwrap().file_name().into_string().unwrap())
        .collect();
    assert_eq!(names, vec!["katalogas".to_string()], "{names:?}");
}

#[test]
fn ensure_parent_dir_accepts_bare_file_name() {
    ensure_parent_dir(Path::new("tik-vardas")).unwrap();
}

#[test]
fn load_lines_reports_invalid_utf8() {
    let d = TempDir::new("persist-utf8");
    let p = d.join("blogas");
    std::fs::write(&p, b"gera\n\xff\xfe\n").unwrap();
    assert!(load_lines(&p).is_err());
    assert!(load_ignore(&p).is_err());
    assert!(load_monitor(&p).is_err());
}

#[test]
fn random_lists_round_trip() {
    let d = TempDir::new("persist-rand");
    let p = d.join("r");
    let mut r = Rng::new(0x9e75);
    for _ in 0..200 {
        let list: Vec<String> = (0..r.below(20))
            .map(|_| r.interesting_string(30))
            .map(|s| s.replace(['\n', '\r'], ""))
            .filter(|s| !s.is_empty())
            .collect();
        save_lines(&p, &list).unwrap();
        assert_eq!(load_lines(&p).unwrap(), list);
    }
}
