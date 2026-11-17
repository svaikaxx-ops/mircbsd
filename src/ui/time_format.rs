//! Formatting of timestamps using Go time layouts.

use chrono::{DateTime, Datelike, Local, Timelike};

// ---- Go time layout formatting ----

const SHORT_MONTHS: [&str; 12] = [
    "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
];
const LONG_MONTHS: [&str; 12] = [
    "January",
    "February",
    "March",
    "April",
    "May",
    "June",
    "July",
    "August",
    "September",
    "October",
    "November",
    "December",
];
const SHORT_DAYS: [&str; 7] = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const LONG_DAYS: [&str; 7] = [
    "Sunday",
    "Monday",
    "Tuesday",
    "Wednesday",
    "Thursday",
    "Friday",
    "Saturday",
];

fn starts_with_lower(s: &[u8]) -> bool {
    matches!(s.first(), Some(c) if c.is_ascii_lowercase())
}

fn is_digit_at(s: &[u8], i: usize) -> bool {
    matches!(s.get(i), Some(c) if c.is_ascii_digit())
}

/// Formats a time using a Go time layout (`time.Format`), e.g. `15:04`,
/// `2006-01-02 15:04:05`, `Jan _2 3:04PM`. `ts_format` is a Go layout, so
/// the Rust port needs its own formatter (chrono uses strftime).
///
/// Supported elements: year (`2006`, `06`), month (`01`, `1`, `Jan`,
/// `January`), day (`02`, `2`, `_2`, `002`/`__2` day of year), weekday
/// (`Mon`, `Monday`), hour (`15`, `03`, `3`), minute (`04`, `4`), second
/// (`05`, `5`), `PM`/`pm`, fractional seconds (`.000`, `.999`, `,000`),
/// numeric zones (`-0700`, `-07:00`, `-07`, `Z0700`, `Z07:00`, `Z07`, …) and
/// `MST` (rendered as a numeric offset — chrono has no zone abbreviations).
pub fn format_go_time(t: &DateTime<Local>, layout: &str) -> String {
    let l = layout.as_bytes();
    let mut out = String::with_capacity(layout.len() + 8);
    let mut lit_start = 0usize; // start of pending literal run
    let mut i = 0usize;

    let year = t.year();
    let month = t.month() as usize;
    let day = t.day();
    let hour = t.hour();
    let min = t.minute();
    let sec = t.second();
    let nano = t.nanosecond() % 1_000_000_000; // drop leap-second overflow
    let wday = t.weekday().num_days_from_sunday() as usize;
    let yday = t.ordinal();
    let off = t.offset().local_minus_utc();

    macro_rules! flush {
        ($upto:expr) => {
            if lit_start < $upto {
                out.push_str(&layout[lit_start..$upto]);
            }
        };
    }

    while i < l.len() {
        let rest = &l[i..];
        // (token length, kind) — None = literal byte.
        let tok: Option<(usize, Tok)> = match l[i] {
            b'J' => {
                if rest.starts_with(b"Jan") {
                    if rest.starts_with(b"January") {
                        Some((7, Tok::LongMonth))
                    } else if !starts_with_lower(&rest[3..]) {
                        Some((3, Tok::Month))
                    } else {
                        None
                    }
                } else {
                    None
                }
            }
            b'M' => {
                if rest.starts_with(b"Mon") {
                    if rest.starts_with(b"Monday") {
                        Some((6, Tok::LongWeekDay))
                    } else if !starts_with_lower(&rest[3..]) {
                        Some((3, Tok::WeekDay))
                    } else {
                        None
                    }
                } else if rest.starts_with(b"MST") {
                    Some((3, Tok::TzAbbrev))
                } else {
                    None
                }
            }
            b'0' => match rest.get(1) {
                Some(b'1') => Some((2, Tok::ZeroMonth)),
                Some(b'2') => Some((2, Tok::ZeroDay)),
                Some(b'3') => Some((2, Tok::ZeroHour12)),
                Some(b'4') => Some((2, Tok::ZeroMinute)),
                Some(b'5') => Some((2, Tok::ZeroSecond)),
                Some(b'6') => Some((2, Tok::Year)),
                Some(b'0') if rest.get(2) == Some(&b'2') => Some((3, Tok::ZeroYearDay)),
                _ => None,
            },
            b'1' => {
                if rest.get(1) == Some(&b'5') {
                    Some((2, Tok::Hour))
                } else {
                    Some((1, Tok::NumMonth))
                }
            }
            b'2' => {
                if rest.starts_with(b"2006") {
                    Some((4, Tok::LongYear))
                } else {
                    Some((1, Tok::Day))
                }
            }
            b'_' => {
                if rest.get(1) == Some(&b'2') {
                    // "_2006" is a literal underscore followed by the year.
                    if rest.starts_with(b"_2006") {
                        // literal '_' — handled as literal, then "2006".
                        None
                    } else {
                        Some((2, Tok::UnderDay))
                    }
                } else if rest.starts_with(b"__2") {
                    Some((3, Tok::UnderYearDay))
                } else {
                    None
                }
            }
            b'3' => Some((1, Tok::Hour12)),
            b'4' => Some((1, Tok::Minute)),
            b'5' => Some((1, Tok::Second)),
            b'P' => {
                if rest.get(1) == Some(&b'M') {
                    Some((2, Tok::PmUpper))
                } else {
                    None
                }
            }
            b'p' => {
                if rest.get(1) == Some(&b'm') {
                    Some((2, Tok::PmLower))
                } else {
                    None
                }
            }
            b'-' | b'Z' => {
                let iso = l[i] == b'Z';
                let body = &rest[1..];
                if body.starts_with(b"070000") {
                    Some((
                        7,
                        Tok::Tz {
                            colon: false,
                            secs: true,
                            short: false,
                            iso,
                        },
                    ))
                } else if body.starts_with(b"07:00:00") {
                    Some((
                        9,
                        Tok::Tz {
                            colon: true,
                            secs: true,
                            short: false,
                            iso,
                        },
                    ))
                } else if body.starts_with(b"0700") {
                    Some((
                        5,
                        Tok::Tz {
                            colon: false,
                            secs: false,
                            short: false,
                            iso,
                        },
                    ))
                } else if body.starts_with(b"07:00") {
                    Some((
                        6,
                        Tok::Tz {
                            colon: true,
                            secs: false,
                            short: false,
                            iso,
                        },
                    ))
                } else if body.starts_with(b"07") {
                    Some((
                        3,
                        Tok::Tz {
                            colon: false,
                            secs: false,
                            short: true,
                            iso,
                        },
                    ))
                } else {
                    None
                }
            }
            b'.' | b',' => {
                // ,000 / .000 / ,999 / .999 — fractional seconds, only when
                // the run of 0s or 9s is not followed by another digit.
                if matches!(rest.get(1), Some(b'0') | Some(b'9')) {
                    let ch = rest[1];
                    let mut j = 1;
                    while j < rest.len() && rest[j] == ch {
                        j += 1;
                    }
                    if !is_digit_at(rest, j) {
                        Some((j, Tok::Frac(ch == b'9', j - 1)))
                    } else {
                        None
                    }
                } else {
                    None
                }
            }
            _ => None,
        };

        let Some((n, tok)) = tok else {
            i += 1;
            continue;
        };
        flush!(i);
        match tok {
            Tok::LongYear => out.push_str(&format!("{:04}", year)),
            Tok::Year => out.push_str(&format!("{:02}", year.rem_euclid(100))),
            Tok::Month => out.push_str(SHORT_MONTHS[month - 1]),
            Tok::LongMonth => out.push_str(LONG_MONTHS[month - 1]),
            Tok::NumMonth => out.push_str(&month.to_string()),
            Tok::ZeroMonth => out.push_str(&format!("{:02}", month)),
            Tok::WeekDay => out.push_str(SHORT_DAYS[wday]),
            Tok::LongWeekDay => out.push_str(LONG_DAYS[wday]),
            Tok::Day => out.push_str(&day.to_string()),
            Tok::UnderDay => out.push_str(&format!("{:>2}", day)),
            Tok::ZeroDay => out.push_str(&format!("{:02}", day)),
            Tok::UnderYearDay => out.push_str(&format!("{:>3}", yday)),
            Tok::ZeroYearDay => out.push_str(&format!("{:03}", yday)),
            Tok::Hour => out.push_str(&format!("{:02}", hour)),
            Tok::Hour12 => {
                let h = if hour % 12 == 0 { 12 } else { hour % 12 };
                out.push_str(&h.to_string());
            }
            Tok::ZeroHour12 => {
                let h = if hour % 12 == 0 { 12 } else { hour % 12 };
                out.push_str(&format!("{:02}", h));
            }
            Tok::Minute => out.push_str(&min.to_string()),
            Tok::ZeroMinute => out.push_str(&format!("{:02}", min)),
            Tok::Second => out.push_str(&sec.to_string()),
            Tok::ZeroSecond => out.push_str(&format!("{:02}", sec)),
            Tok::PmUpper => out.push_str(if hour >= 12 { "PM" } else { "AM" }),
            Tok::PmLower => out.push_str(if hour >= 12 { "pm" } else { "am" }),
            Tok::TzAbbrev => out.push_str(&fmt_zone_abbrev(off)),
            Tok::Tz {
                colon,
                secs,
                short,
                iso,
            } => {
                if iso && off == 0 {
                    out.push('Z');
                } else {
                    out.push_str(&fmt_offset(off, colon, secs, short));
                }
            }
            Tok::Frac(trim, digits) => {
                let sep = l[i] as char;
                let mut s = format!("{:09}", nano);
                s.truncate(digits.min(9));
                if trim {
                    let t = s.trim_end_matches('0').to_string();
                    if !t.is_empty() {
                        out.push(sep);
                        out.push_str(&t);
                    }
                } else {
                    out.push(sep);
                    out.push_str(&s);
                    // Go pads beyond 9 digits with zeros.
                    for _ in 9..digits {
                        out.push('0');
                    }
                }
            }
        }
        i += n;
        lit_start = i;
    }
    flush!(l.len());
    out
}

#[derive(Debug, Clone)]
enum Tok {
    LongYear,
    Year,
    Month,
    LongMonth,
    NumMonth,
    ZeroMonth,
    WeekDay,
    LongWeekDay,
    Day,
    UnderDay,
    ZeroDay,
    UnderYearDay,
    ZeroYearDay,
    Hour,
    Hour12,
    ZeroHour12,
    Minute,
    ZeroMinute,
    Second,
    ZeroSecond,
    PmUpper,
    PmLower,
    /// `MST` — rendered numerically (no zone abbreviation database).
    TzAbbrev,
    /// Numeric zone: `-0700`, `-07:00`, `-07`, `-070000`, `-07:00:00` and
    /// their `Z…` (ISO 8601, "Z" for UTC) variants.
    Tz {
        colon: bool,
        secs: bool,
        short: bool,
        iso: bool,
    },
    /// (trim trailing zeros, digits)
    Frac(bool, usize),
}

/// `MST` fallback: Go prints the short numeric form (`-07` / `-0730`) when a
/// zone has no abbreviation.
fn fmt_zone_abbrev(off: i32) -> String {
    let sign = if off < 0 { '-' } else { '+' };
    let a = off.abs();
    let (h, m) = (a / 3600, (a % 3600) / 60);
    if m == 0 {
        format!("{}{:02}", sign, h)
    } else {
        format!("{}{:02}{:02}", sign, h, m)
    }
}

/// Numeric zone offset in the requested shape.
fn fmt_offset(off: i32, colon: bool, secs: bool, short: bool) -> String {
    let sign = if off < 0 { '-' } else { '+' };
    let a = off.abs();
    let (h, m, s) = (a / 3600, (a % 3600) / 60, a % 60);
    if short {
        return format!("{}{:02}", sign, h);
    }
    match (colon, secs) {
        (true, true) => format!("{}{:02}:{:02}:{:02}", sign, h, m, s),
        (false, true) => format!("{}{:02}{:02}{:02}", sign, h, m, s),
        (true, false) => format!("{}{:02}:{:02}", sign, h, m),
        (false, false) => format!("{}{:02}{:02}", sign, h, m),
    }
}

#[cfg(test)]
#[path = "time_format_tests.rs"]
mod tests;
