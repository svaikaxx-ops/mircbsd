//! Laiko pagalbinės funkcijos — vienintelė vieta, kuri žino apie laiko
//! biblioteką (`jiff`). Likęs kodas naudoja tik šiuos tipus/funkcijas, todėl
//! bibliotekos keitimas ateityje paliestų tik šį failą.
//!
//! - [`Ts`] — momentas laike (UTC), saugomas žinutėse.
//! - [`LocalTime`] — momentas vietinėje laiko juostoje (rodymui ekrane,
//!   žurnalams). Laiko juosta — `TZ` arba `/etc/localtime` (kaip libc).

use jiff::tz::TimeZone;

pub use jiff::SignedDuration as Duration;
pub use jiff::Timestamp as Ts;
pub use jiff::ZonedDateTime as LocalTime;

/// Dabartinis momentas.
pub fn now() -> Ts {
    Ts::now()
}

/// Momentas vietinėje laiko juostoje.
pub fn local(ts: Ts) -> LocalTime {
    ts.to_zoned_datetime(TimeZone::system())
}

/// Dabartinis vietinis laikas.
pub fn local_now() -> LocalTime {
    local(now())
}

/// IRCv3 `server-time` (RFC 3339, pvz. `2026-11-17T10:00:00.123Z`).
pub fn parse_rfc3339(raw: &str) -> Option<Ts> {
    raw.parse::<Ts>().ok()
}

/// Unix sekundės → momentas (`None`, jei už ribų).
pub fn from_unix(secs: i64) -> Option<Ts> {
    Ts::from_second(secs).ok()
}

/// Momentas → Unix sekundės.
pub fn unix(ts: Ts) -> i64 {
    ts.as_second()
}

/// `n` minučių trukmė.
pub fn minutes(n: i64) -> Duration {
    Duration::from_mins(n)
}

/// `n` parų trukmė (24 h).
pub fn days(n: i64) -> Duration {
    Duration::from_hours(24 * n)
}

/// Ar `y-m-d` yra egzistuojanti kalendorinė data.
pub fn valid_date(y: i32, m: u32, d: u32) -> bool {
    match (i16::try_from(y), i8::try_from(m), i8::try_from(d)) {
        (Ok(y), Ok(m), Ok(d)) => jiff::civil::Date::new(y, m, d).is_ok(),
        _ => false,
    }
}

/// Ar du momentai vietiniu laiku patenka į skirtingas paras.
pub fn changed_local_day(a: Ts, b: Ts) -> bool {
    local(a).date() != local(b).date()
}

/// Vietinis sieninis laikas iš kalendorinių laukų (DST spragoje — ankstesnis
/// galimas momentas). Naudojama testuose.
pub fn local_at(y: i16, m: i8, d: i8, h: i8, mi: i8, s: i8, nanos: i32) -> LocalTime {
    jiff::civil::date(y, m, d)
        .at(h, mi, s, nanos)
        .to_zoned_datetime(TimeZone::system())
        .expect("teisinga vietinė data")
}

/// UTC momentas iš kalendorinių laukų (testams ir konstantoms).
pub fn utc(y: i16, m: i8, d: i8, h: i8, mi: i8, s: i8) -> Ts {
    jiff::civil::date(y, m, d)
        .at(h, mi, s, 0)
        .to_zoned_datetime(TimeZone::UTC)
        .expect("teisinga UTC data")
        .timestamp()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rfc3339_server_time() {
        let t = parse_rfc3339("2026-11-17T10:20:30.123Z").unwrap();
        assert_eq!(unix(t), unix(utc(2026, 11, 17, 10, 20, 30)));
        let t = parse_rfc3339("2026-11-17T12:20:30+02:00").unwrap();
        assert_eq!(unix(t), unix(utc(2026, 11, 17, 10, 20, 30)));
        assert!(parse_rfc3339("vakar").is_none());
        assert!(parse_rfc3339("").is_none());
    }

    #[test]
    fn dates_and_durations() {
        assert!(valid_date(2024, 2, 29));
        assert!(!valid_date(2023, 2, 29));
        assert!(!valid_date(2026, 13, 1));
        assert!(!valid_date(100_000, 1, 1));
        let t = utc(2026, 1, 1, 0, 0, 0);
        assert_eq!(unix(t + days(1)) - unix(t), 86_400);
        assert_eq!(unix(t + minutes(5)) - unix(t), 300);
        assert_eq!(from_unix(unix(t)), Some(t));
    }
}
