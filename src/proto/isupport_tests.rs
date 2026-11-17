//! ISUPPORT (005) kraštiniai atvejai.

use super::*;
use crate::test_util::Rng;

#[test]
fn defaults_before_005() {
    let i = Isup::default();
    assert_eq!(i.nick_len_limit(), 30);
    assert_eq!(i.channel_len_limit(), 50);
    assert_eq!(i.topic_len_limit(), 390);
    assert_eq!(i.kick_len_limit(), 0);
    assert_eq!(i.chan_mode_groups(), parse_chan_modes(DEFAULT_CHAN_MODES));
}

#[test]
fn limits_apply_and_bad_values_keep_previous() {
    let mut i = Isup::default();
    i.apply(&[
        "NICKLEN=16",
        "CHANNELLEN=64",
        "TOPICLEN=307",
        "KICKLEN=255",
        "MODES=4",
    ]);
    assert_eq!(
        (
            i.nick_len_limit(),
            i.channel_len_limit(),
            i.topic_len_limit(),
            i.kick_len_limit(),
            i.limits.modes
        ),
        (16, 64, 307, 255, 4)
    );
    // Neigiami/šiukšlės/tušti — palieka ankstesnę reikšmę.
    i.apply(&[
        "NICKLEN=-1",
        "CHANNELLEN=x",
        "TOPICLEN=",
        "KICKLEN=99999999999",
        "MODES",
    ]);
    assert_eq!(i.limits.nick_len, 16);
    assert_eq!(i.limits.channel_len, 64);
    assert_eq!(i.limits.topic_len, 307);
    assert_eq!(i.limits.kick_len, 255);
    assert_eq!(i.limits.modes, 4);
    // NICKLEN=0 — „neskelbta“: naudojamas numatytas 30.
    i.apply(&["NICKLEN=0"]);
    assert_eq!(i.nick_len_limit(), 30);
}

#[test]
fn monitor_bad_value_disables() {
    let mut i = Isup::default();
    i.apply(&["MONITOR=100"]);
    assert_eq!(i.limits.monitor, 100);
    i.apply(&["MONITOR=bad"]);
    assert_eq!(i.limits.monitor, 0);
}

#[test]
fn prefix_variants() {
    let mut i = Isup::default();
    i.apply(&["PREFIX=(qaohv)~&@%+"]);
    assert_eq!(
        (i.prefix_modes.as_str(), i.prefix_signs.as_str()),
        ("qaohv", "~&@%+")
    );
    // Nesutampantis ilgis, tuščias, be „(“ — ignoruojama.
    for bad in [
        "PREFIX=(ov)@",
        "PREFIX=()",
        "PREFIX=ov@+",
        "PREFIX=(ov",
        "PREFIX=(o)ąž",
    ] {
        let mut j = Isup::default();
        j.apply(&[bad]);
        assert_eq!(j.prefix_modes, "qaohv", "{bad}");
    }
    // Unikodo ženklai skaičiuojami simboliais, ne baitais.
    let mut j = Isup::default();
    j.apply(&["PREFIX=(o)★"]);
    assert_eq!(j.prefix_signs, "★");
}

#[test]
fn chantypes_variants() {
    let mut i = Isup::default();
    i.apply(&["CHANTYPES=#"]);
    assert!(i.is_channel("#a") && !i.is_channel("&a"));
    i.apply(&["CHANTYPES="]);
    assert_eq!(i.chantypes, "#", "tuščia reikšmė nekeičia");
    i.apply(&["CHANTYPES=#&!+"]);
    assert!(i.is_channel("!abc") && i.is_channel("+x"));
}

#[test]
fn casemapping_whitelist() {
    let mut i = Isup::default();
    for ok in [
        "ascii",
        "rfc1459",
        "strict-rfc1459",
        "rfc8265",
        "rfc7613",
        "utf8",
    ] {
        i.apply(&[format!("CASEMAPPING={ok}")]);
        assert_eq!(i.casemapping, ok);
    }
    // Nežinoma reikšmė — lieka ankstesnė.
    i.apply(&["CASEMAPPING=ascii"]);
    i.apply(&["CASEMAPPING=kazkas-naujo"]);
    assert_eq!(i.casemapping, "ascii");
    i.apply(&["CASEMAPPING=RFC8265"]);
    assert_eq!(i.casemapping, "rfc8265");
}

#[test]
fn unknown_and_negated_tokens_are_ignored() {
    let mut i = Isup::default();
    let before = i.clone();
    i.apply(&["-NICKLEN", "EXCEPTS", "WHOX", "=", "", "NICKLENX=5"]);
    assert_eq!(i, before);
}

#[test]
fn chanmodes_extra_groups_are_ignored() {
    let g = parse_chan_modes("a,b,c,d,e,f");
    assert_eq!(
        (g.a, g.b, g.c, g.d),
        ("a".into(), "b".into(), "c".into(), "d".into())
    );
}

#[test]
fn takes_param_matrix() {
    let g = parse_chan_modes("beI,k,l,imnpst");
    for (m, add, want) in [
        ('b', true, true),
        ('b', false, true),
        ('k', true, true),
        ('k', false, true),
        ('l', true, true),
        ('l', false, false),
        ('m', true, false),
        ('m', false, false),
        ('o', true, true),
        ('v', false, true),
        ('z', true, false),
    ] {
        assert_eq!(g.takes_param(m, add, "ov"), want, "{m} {add}");
    }
}

#[test]
fn random_tokens_never_panic() {
    let mut r = Rng::new(0x005);
    const KEYS: &[&str] = &[
        "PREFIX=",
        "CHANTYPES=",
        "CHANMODES=",
        "CASEMAPPING=",
        "NETWORK=",
        "NICKLEN=",
        "MONITOR=",
        "MODES=",
        "",
    ];
    for _ in 0..20_000 {
        let toks: Vec<String> = (0..r.below(4))
            .map(|_| format!("{}{}", r.pick(KEYS), r.interesting_string(8)))
            .collect();
        let mut i = Isup::default();
        i.apply(&toks);
        let g = i.chan_mode_groups();
        let _ = g.takes_param('o', true, &i.prefix_modes);
        let _ = i.is_channel(&r.interesting_string(3));
        assert_eq!(
            i.prefix_modes.chars().count(),
            i.prefix_signs.chars().count()
        );
    }
}
