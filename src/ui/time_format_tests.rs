//! Go laiko maketų formatavimo testai (`ts_format`).

use super::*;
use crate::test_util::Rng;

/// Go etaloninis laikas: Mon Jan 2 15:04:05.123456789 2006 (vietinis).
fn reference() -> LocalTime {
    crate::timeutil::local_at(2006, 1, 2, 15, 4, 5, 123_456_789)
}

#[test]
fn reference_time_formats_to_its_own_layout() {
    // Go maketas — tai etaloninio laiko užrašas, todėl jį suformatavus
    // gaunamas pats maketas.
    let t = reference();
    for layout in [
        "2006-01-02 15:04:05",
        "Jan 2 15:04",
        "January Monday",
        "Mon, 02 Jan 2006",
        "1/2 3:4:5",
        "03:04 PM",
        "06",
        "15h04m05s",
    ] {
        assert_eq!(format_go_time(&t, layout), layout, "{layout}");
    }
}

#[test]
fn twelve_hour_and_am_pm() {
    let am = crate::timeutil::local_at(2024, 5, 6, 0, 7, 0, 0);
    assert_eq!(format_go_time(&am, "3:04 PM pm"), "12:07 AM am");
    assert_eq!(format_go_time(&am, "03"), "12");
    let noon = crate::timeutil::local_at(2024, 5, 6, 12, 0, 0, 0);
    assert_eq!(format_go_time(&noon, "3PM"), "12PM");
    let pm = crate::timeutil::local_at(2024, 5, 6, 23, 59, 9, 0);
    assert_eq!(format_go_time(&pm, "03:04:5 pm"), "11:59:9 pm");
}

#[test]
fn padded_days_and_year_day() {
    let t = crate::timeutil::local_at(2024, 2, 5, 1, 2, 3, 0);
    assert_eq!(format_go_time(&t, "_2|02|2"), " 5|05|5");
    assert_eq!(format_go_time(&t, "002|__2"), "036| 36");
    // „_2006“ — pabraukimas ir metai, ne „_2“ diena.
    assert_eq!(format_go_time(&t, "_2006"), "_2024");
}

#[test]
fn fractional_seconds() {
    let t = reference();
    assert_eq!(format_go_time(&t, "05.000"), "05.123");
    assert_eq!(format_go_time(&t, "05,000000"), "05,123456");
    assert_eq!(format_go_time(&t, "05.999999999"), "05.123456789");
    let whole = crate::timeutil::local_at(2024, 1, 1, 0, 0, 7, 0);
    // .999 nukerpa nulius, o visiškai nulinė trupmena dingsta su skirtuku.
    assert_eq!(format_go_time(&whole, "5.999"), "7");
    assert_eq!(format_go_time(&whole, "5.000"), "7.000");
    // Daugiau nei 9 skaitmenys — papildoma nuliais.
    assert_eq!(format_go_time(&t, ".0000000000"), ".1234567890");
    // Po nulių einantis skaitmuo — ne trupmena, o literalai + žetonai.
    assert_eq!(format_go_time(&t, ".01"), ".01");
}

#[test]
fn zone_tokens_match_local_offset() {
    let t = reference();
    let off = t.offset().seconds();
    let sign = if off < 0 { '-' } else { '+' };
    let (h, m) = (off.abs() / 3600, (off.abs() % 3600) / 60);
    assert_eq!(format_go_time(&t, "-0700"), format!("{sign}{h:02}{m:02}"));
    assert_eq!(format_go_time(&t, "-07:00"), format!("{sign}{h:02}:{m:02}"));
    assert_eq!(format_go_time(&t, "-07"), format!("{sign}{h:02}"));
    assert_eq!(
        format_go_time(&t, "-07:00:00"),
        format!("{sign}{h:02}:{m:02}:00")
    );
    let z = format_go_time(&t, "Z07:00");
    if off == 0 {
        assert_eq!(z, "Z");
    } else {
        assert_eq!(z, format!("{sign}{h:02}:{m:02}"));
    }
    assert!(format_go_time(&t, "MST").starts_with(sign));
}

#[test]
fn offset_helpers() {
    assert_eq!(
        fmt_offset(-(7 * 3600 + 30 * 60), true, false, false),
        "-07:30"
    );
    assert_eq!(fmt_offset(3 * 3600 + 5, false, true, false), "+030005");
    assert_eq!(fmt_offset(5 * 3600 + 45 * 60, false, false, true), "+05");
    assert_eq!(fmt_zone_abbrev(0), "+00");
    assert_eq!(fmt_zone_abbrev(-(9 * 3600 + 30 * 60)), "-0930");
}

#[test]
fn literals_and_non_tokens_pass_through() {
    let t = reference();
    assert_eq!(format_go_time(&t, ""), "");
    // „Jana“ — mažoji raidė po „Jan“ = ne žetonas; „JanX“ — žetonas.
    assert_eq!(format_go_time(&t, "Jana"), "Jana");
    assert_eq!(format_go_time(&t, "JanX"), "JanX");
    assert_eq!(format_go_time(&t, "Mona"), "Mona");
    assert_eq!(format_go_time(&t, "[laikas] ąčę"), "[laikas] ąčę");
    assert_eq!(format_go_time(&t, "P p Z -"), "P p Z -");
}

#[test]
fn random_layouts_never_panic() {
    let mut r = Rng::new(0x7f0a);
    let t = reference();
    const TOK: &[&str] = &[
        "2006",
        "06",
        "01",
        "1",
        "Jan",
        "January",
        "02",
        "2",
        "_2",
        "__2",
        "002",
        "Mon",
        "Monday",
        "15",
        "03",
        "3",
        "04",
        "4",
        "05",
        "5",
        "PM",
        "pm",
        "MST",
        "-0700",
        "-07:00",
        "Z07",
        "Z070000",
        "-07:00:00",
        ".000",
        ",999",
        ".9",
        "_",
        "-",
        "Z",
        ".",
        ",",
        "ą",
        "中",
        " ",
    ];
    for _ in 0..20_000 {
        let layout: String = (0..r.below(10))
            .map(|_| {
                if r.below(5) == 0 {
                    r.interesting_char().to_string()
                } else {
                    r.pick(TOK).to_string()
                }
            })
            .collect();
        let _ = format_go_time(&t, &layout);
    }
}
