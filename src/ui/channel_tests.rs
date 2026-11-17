use super::*;

fn ch() -> Channel {
    Channel::new("#t", 0)
}

#[test]
fn new_defaults() {
    let c = ch();
    assert_eq!(c.max_messages, DEFAULT_MAX_MESSAGES);
    assert_eq!(c.rendered_gen, -1);
    assert_eq!(c.unread_from, -1);
}

#[test]
fn strip_and_prefix() {
    assert_eq!(strip_mode("@bob", "~&@%+"), "bob");
    assert_eq!(strip_mode("bob", "~&@%+"), "bob");
    assert_eq!(strip_mode("@bob", ""), "@bob");
    assert_eq!(strip_mode("", "@"), "");
    assert_eq!(mode_prefix("+bob", "~&@%+"), "+");
    assert_eq!(mode_prefix("bob", "~&@%+"), "");
    assert_eq!(strip_mode_default("~x"), "x");
    // Non-ASCII first char is never a sign.
    assert_eq!(strip_mode("Žilvinas", "~&@%+"), "Žilvinas");
}

#[test]
fn sorting_by_status_then_name() {
    let mut v: Vec<String> = ["bob", "+carol", "@dave", "Alice", "~zed", "%eve"]
        .iter()
        .map(|s| s.to_string())
        .collect();
    sort_nicks(&mut v, None);
    assert_eq!(v, ["~zed", "@dave", "%eve", "+carol", "Alice", "bob"]);
    assert!(nick_less("@a", "+a", "~&@%+"));
    assert!(!nick_less("b", "A", "~&@%+"));
    assert!(nick_less("", "a", "~&@%+"));
}

#[test]
fn add_remove_rename() {
    let mut c = ch();
    c.add_nick("bob", None);
    c.add_nick("@alice", None);
    c.add_nick("carol", None);
    assert_eq!(c.nicks, ["@alice", "bob", "carol"]);
    assert!(c.has_nick("BOB"));
    assert!(c.has_nick("@bob"));
    assert!(!c.has_nick("dave"));
    // Duplicate (different case) ignored.
    c.add_nick("Bob", None);
    assert_eq!(c.nicks.len(), 3);
    // NAMES brings a prefix for an existing nick.
    c.add_nick("@bob", None);
    assert_eq!(c.nicks, ["@alice", "@bob", "carol"]);
    // A prefix-less re-add keeps the existing prefix.
    c.add_nick("bob", None);
    assert_eq!(c.nicks, ["@alice", "@bob", "carol"]);

    c.rename_nick("bob", "zed", None);
    assert_eq!(c.nicks, ["@alice", "@zed", "carol"]);
    assert!(!c.has_nick("bob"));
    assert!(c.has_nick("zed"));
    c.add_nick("bob", None);
    assert_eq!(c.nicks.len(), 4);

    c.remove_nick("ZED", None);
    assert_eq!(c.nicks, ["@alice", "bob", "carol"]);
    assert!(!c.has_nick("zed"));
    c.remove_nick("nobody", None);
    assert_eq!(c.nicks.len(), 3);
}

#[test]
fn set_mode_plus_minus() {
    let mut c = ch();
    c.add_nick("bob", None);
    c.add_nick("alice", None);
    c.set_mode("bob", "+o", None, None);
    assert_eq!(c.nicks, ["@bob", "alice"]);
    c.set_mode("alice", "+v", None, None);
    assert_eq!(c.nicks, ["@bob", "+alice"]);
    c.set_mode("bob", "-o", None, None);
    assert_eq!(c.nicks, ["+alice", "bob"]);
    // "+ov" applies each letter in order (last add wins on one nick).
    c.set_mode("bob", "+ov", None, None);
    assert_eq!(c.nicks, ["+alice", "+bob"]);
    // Unknown mode letter ignored.
    c.set_mode("bob", "+x", None, None);
    assert_eq!(c.nicks, ["+alice", "+bob"]);
    // Custom PREFIX (e.g. only ops).
    c.set_mode("alice", "+o", Some("@+"), Some("ov"));
    assert_eq!(c.nicks[0], "@alice");
}

#[test]
fn casemapping_rfc1459_vs_ascii() {
    let mut c = ch();
    c.add_nick("[a]", None);
    c.add_nick("{a}", None);
    // RFC 1459: "[a]" ≡ "{a}".
    assert_eq!(c.nicks.len(), 1);

    let mut c = ch();
    c.casemapping = "ascii".into();
    c.add_nick("[a]", None);
    c.add_nick("{a}", None);
    assert_eq!(c.nicks.len(), 2);
    c.remove_nick("{a}", None);
    assert_eq!(c.nicks, ["[a]"]);
}

#[test]
fn nick_limit() {
    let mut c = ch();
    for i in 0..(MAX_NICKS_PER_CHANNEL + 50) {
        c.add_nick(&format!("n{:05}", i), None);
    }
    assert_eq!(c.nicks.len(), MAX_NICKS_PER_CHANNEL);
    assert_eq!(c.nick_set.len(), MAX_NICKS_PER_CHANNEL);
    // Sorted.
    assert!(c.nicks.windows(2).all(|w| w[0] <= w[1]));
}

#[test]
fn lazy_set_rebuild_and_clear() {
    let mut c = ch();
    c.nicks = vec!["@bob".into(), "alice".into()];
    // Set not built yet.
    assert!(c.has_nick("bob"));
    c.add_nick("BOB", None);
    assert_eq!(c.nicks.len(), 2);
    assert_eq!(c.nick_set.len(), 2);
    c.clear_nicks();
    assert!(c.nicks.is_empty() && c.nick_set.is_empty());
    assert!(!c.has_nick("bob"));
}

#[test]
fn add_msg_trims_oldest_quarter() {
    let ts = chrono::Utc::now();
    let mut c = Channel::new("#t", 8);
    c.rendered = vec!["x".into()];
    c.rendered_gen = 3;
    c.rendered_msg_count = 1;
    for i in 0..8 {
        c.add_msg(Message::new(
            ts,
            "n",
            format!("m{i}"),
            super::super::message::MsgType::Chat,
            false,
        ));
    }
    assert_eq!(c.messages.len(), 8);
    c.add_msg(Message::new(
        ts,
        "n",
        "m8",
        super::super::message::MsgType::Chat,
        false,
    ));
    // 9 > 8 → drop 8/4 = 2 oldest.
    assert_eq!(c.messages.len(), 7);
    assert_eq!(c.messages[0].text, "m2");
    assert_eq!(c.rendered_gen, -1);
    assert_eq!(c.rendered_msg_count, 0);
    assert!(c.rendered.is_empty());
}

fn msg(text: &str) -> Message {
    Message::new(
        chrono::Utc::now(),
        "n",
        text,
        crate::ui::MsgType::Chat,
        false,
    )
}

#[test]
fn tiny_max_messages_still_bounds_buffer() {
    let mut c = Channel::new("#t", 3);
    for i in 0..20 {
        c.add_msg(msg(&i.to_string()));
    }
    assert!(c.messages.len() <= 3, "{}", c.messages.len());
    assert_eq!(c.messages.last().unwrap().text, "19");
}

#[test]
fn trim_shifts_unread_from() {
    let mut c = Channel::new("#t", 8);
    for i in 0..8 {
        c.add_msg(msg(&i.to_string()));
    }
    // Pirmoji neperskaityta — „6“.
    c.unread_from = 6;
    c.add_msg(msg("8")); // 9 > 8 → nukerpamos 2 seniausios
    assert_eq!(c.messages[c.unread_from as usize].text, "6");
}

fn recount(c: &Channel) -> usize {
    c.messages.iter().map(msg_cost).sum()
}

#[test]
fn msg_bytes_stays_in_step_with_messages() {
    let mut c = Channel::new("#t", 8);
    for i in 0..20 {
        c.add_msg(msg(&"x".repeat(i * 10)));
        assert_eq!(c.msg_bytes, recount(&c), "after add {i}");
    }
    c.insert_msg(2, msg("žyma"));
    assert_eq!(c.msg_bytes, recount(&c));
    c.remove_msg(2);
    assert_eq!(c.msg_bytes, recount(&c));
    c.trim_oldest_bytes(1);
    assert_eq!(c.msg_bytes, recount(&c));
    c.clear_messages();
    assert_eq!(c.msg_bytes, 0);
}

#[test]
fn trim_oldest_bytes_keeps_newest_and_drops_cache() {
    let mut c = Channel::new("#t", 100);
    for i in 0..10 {
        c.add_msg(msg(&i.to_string()));
    }
    c.rendered = vec!["0".into(); 10];
    c.rendered_gen = c.render_gen;
    c.rendered_msg_count = 10;
    c.unread_from = 5;
    let one = msg_cost(&c.messages[0]);
    // Just over one message's worth → two oldest go.
    assert_eq!(c.trim_oldest_bytes(one + 1), 2 * one);
    assert_eq!(c.messages[0].text, "2");
    assert_eq!(c.messages[c.unread_from as usize].text, "5");
    assert_eq!(c.rendered_gen, -1);
    assert_eq!(c.rendered_msg_count, 0);
    assert!(c.rendered.is_empty());
    // Asking for everything still keeps the newest line.
    c.trim_oldest_bytes(usize::MAX);
    assert_eq!(c.messages.len(), 1);
    assert_eq!(c.messages[0].text, "9");
    assert_eq!(c.trim_oldest_bytes(usize::MAX), 0);
}
