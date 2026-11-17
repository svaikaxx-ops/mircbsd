//! ANSI codes, colour/theme configuration ported from `ui.go`.
//!
//! The Go code kept the configurable look-and-feel (`tsColorCode`,
//! `hlBgCode`, `nickColoring`, `nickColors`, `timestampsEnabled`, `tsFormat`,
//! `themeDarkMode`) in package-level variables. They are process-wide
//! globals here too, guarded by `parking_lot::RwLock`.
//!
//! `nickColors`, `nickColoring` and `themeDarkMode` are kept in ONE lock
//! (`NICK_STATE`) because `nickColor` must see a consistent pair
//! (coloring flag + palette), exactly like the Go `themeMu`.

use std::borrow::Cow;
use std::sync::atomic::{AtomicBool, AtomicU8, Ordering};

use parking_lot::RwLock;

use super::channel::Channel;

// ---- ANSI ----

pub const ANSI_RESET: &str = "\x1b[0m";
pub const ANSI_BOLD: &str = "\x1b[1m";
pub const ANSI_ITALIC: &str = "\x1b[3m";
pub const ANSI_UNDER: &str = "\x1b[4m";
pub const ANSI_REVERSE: &str = "\x1b[7m";
pub const ANSI_RED: &str = "\x1b[31m";
pub const ANSI_GREEN: &str = "\x1b[32m";
pub const ANSI_YELLOW: &str = "\x1b[33m";
pub const ANSI_BLUE: &str = "\x1b[34m";
pub const ANSI_MAGENTA: &str = "\x1b[35m";
pub const ANSI_CYAN: &str = "\x1b[36m";
pub const ANSI_WHITE: &str = "\x1b[37m";
pub const ANSI_BG_BLUE: &str = "\x1b[44m";
pub const ANSI_BG_RED: &str = "\x1b[41m";
pub const ANSI_DARK_GRAY: &str = "\x1b[90m";
pub const ANSI_HIDE_CUR: &str = "\x1b[?25l";
pub const ANSI_SHOW_CUR: &str = "\x1b[?25h";

/// Cursor positioning escape (`moveTo`): 1-based row/column.
pub fn move_to(row: i32, col: i32) -> String {
    format!("\x1b[{};{}H", row, col)
}

// ---- Configurable globals ----

/// Timestamp colour code (`tsColorCode`). Empty = no colour.
static TS_COLOR_CODE: RwLock<&'static str> = RwLock::new(ANSI_DARK_GRAY);
/// Highlight background code (`hlBgCode`).
static HL_BG_CODE: RwLock<&'static str> = RwLock::new(ANSI_BG_RED);

/// Last configured colour NAMES (not codes) — for the `/set` listing, which
/// must show the keyword the user entered ("gray"), not the derived code.
static TS_COLOR_NAME: RwLock<&'static str> = RwLock::new("gray");
static HL_COLOR_NAME: RwLock<Cow<'static, str>> = RwLock::new(Cow::Borrowed("red"));

/// `timestampsEnabled` / `tsFormat` (Go time layout, default `15:04`).
static TIMESTAMPS_ENABLED: RwLock<bool> = RwLock::new(true);
static TS_FORMAT: RwLock<Cow<'static, str>> = RwLock::new(Cow::Borrowed("15:04"));

/// Default (dark background) nick palette (`nickColorsDark`).
pub const NICK_COLORS_DARK: [&str; 24] = [
    "\x1b[31m",
    "\x1b[32m",
    "\x1b[33m",
    "\x1b[34m",
    "\x1b[35m",
    "\x1b[36m",
    "\x1b[91m",
    "\x1b[92m",
    "\x1b[93m",
    "\x1b[94m",
    "\x1b[95m",
    "\x1b[96m",
    "\x1b[38;5;208m",
    "\x1b[38;5;130m",
    "\x1b[38;5;46m",
    "\x1b[38;5;51m",
    "\x1b[38;5;201m",
    "\x1b[38;5;226m",
    "\x1b[38;5;39m",
    "\x1b[38;5;165m",
    "\x1b[38;5;82m",
    "\x1b[38;5;214m",
    "\x1b[38;5;105m",
    "\x1b[38;5;45m",
];

/// Light-background palette (`nickColorsLight`, theme=light, v0.4.8):
/// bright colours (91–96, 226 …) are unreadable on white, so the palette
/// switches to DARK 256-colour entries.
pub const NICK_COLORS_LIGHT: [&str; 24] = [
    "\x1b[38;5;124m",
    "\x1b[38;5;28m",
    "\x1b[38;5;94m",
    "\x1b[38;5;26m",
    "\x1b[38;5;126m",
    "\x1b[38;5;30m",
    "\x1b[38;5;88m",
    "\x1b[38;5;22m",
    "\x1b[38;5;97m",
    "\x1b[38;5;18m",
    "\x1b[38;5;61m",
    "\x1b[38;5;52m",
    "\x1b[38;5;131m",
    "\x1b[38;5;59m",
    "\x1b[38;5;23m",
    "\x1b[38;5;132m",
    "\x1b[38;5;127m",
    "\x1b[38;5;67m",
    "\x1b[38;5;101m",
    "\x1b[38;5;63m",
    "\x1b[38;5;133m",
    "\x1b[38;5;24m",
    "\x1b[38;5;95m",
    "\x1b[38;5;62m",
];

/// mIRC colour numbers 0–15 → ANSI (`mircColors`).
pub const MIRC_COLORS: [&str; 16] = [
    "\x1b[97m", // 0  white
    "\x1b[30m", // 1  black
    "\x1b[34m", // 2  blue
    "\x1b[32m", // 3  green
    "\x1b[91m", // 4  light red
    "\x1b[31m", // 5  brown/dark red
    "\x1b[35m", // 6  purple
    "\x1b[33m", // 7  orange
    "\x1b[93m", // 8  yellow
    "\x1b[92m", // 9  light green
    "\x1b[36m", // 10 teal
    "\x1b[96m", // 11 light cyan
    "\x1b[94m", // 12 light blue
    "\x1b[95m", // 13 pink
    "\x1b[90m", // 14 dark gray
    "\x1b[37m", // 15 light gray
];

/// mIRC table for a light background: white (0), yellow (8), light cyan
/// (11) and light grey (15) are invisible on white and map to darker tones.
pub const MIRC_COLORS_LIGHT: [&str; 16] = [
    "\x1b[90m", // 0  white → dark grey
    "\x1b[30m", // 1  black
    "\x1b[34m", // 2  blue
    "\x1b[32m", // 3  green
    "\x1b[91m", // 4  light red
    "\x1b[31m", // 5  brown/dark red
    "\x1b[35m", // 6  purple
    "\x1b[33m", // 7  orange
    "\x1b[33m", // 8  yellow → dark yellow
    "\x1b[32m", // 9  light green → green
    "\x1b[36m", // 10 teal
    "\x1b[36m", // 11 light cyan → teal
    "\x1b[94m", // 12 light blue
    "\x1b[95m", // 13 pink
    "\x1b[90m", // 14 dark gray
    "\x1b[90m", // 15 light gray → dark grey
];

/// The mIRC colour table (`mircColors`) for the active theme. On the dark
/// theme black (1) would be invisible, so it is shown dark grey.
pub fn mirc_colors() -> &'static [&'static str; 16] {
    static DARK: [&str; 16] = {
        let mut t = MIRC_COLORS;
        t[1] = "\x1b[90m";
        t
    };
    if theme_dark_mode() {
        &DARK
    } else {
        &MIRC_COLORS_LIGHT
    }
}

/// Nick colouring state — one lock (Go `themeMu`) so `nick_color` always
/// sees a consistent flag + palette pair.
struct NickState {
    /// `nickColoring`
    coloring: bool,
    /// `themeDarkMode`
    dark: bool,
    /// `nickColors` — the active palette.
    palette: &'static [&'static str],
}

static NICK_STATE: RwLock<NickState> = RwLock::new(NickState {
    coloring: true,
    dark: true,
    palette: &NICK_COLORS_DARK,
});

// ---- Setters ----

/// Highlight background colour by name (`setHighlightColor`).
/// Unknown names fall back to `red`.
pub fn set_highlight_color(name: &str) {
    let lower = name.to_lowercase();
    let (stored_name, code): (Cow<'static, str>, &'static str) = match lower.as_str() {
        "red" => (Cow::Owned(lower.clone()), ANSI_BG_RED),
        "blue" => (Cow::Owned(lower.clone()), ANSI_BG_BLUE),
        "green" => (Cow::Owned(lower.clone()), "\x1b[42m"),
        "magenta" => (Cow::Owned(lower.clone()), "\x1b[45m"),
        "cyan" => (Cow::Owned(lower.clone()), "\x1b[46m"),
        "yellow" => (Cow::Owned(lower.clone()), "\x1b[43m"),
        // No background — highlight is shown as reverse video.
        "none" => (Cow::Owned(lower.clone()), ANSI_REVERSE),
        _ => (Cow::Borrowed("red"), ANSI_BG_RED),
    };
    *HL_COLOR_NAME.write() = stored_name;
    *HL_BG_CODE.write() = code;
}

/// Enable/disable nick colouring (`setNickColors`).
pub fn set_nick_colors(enabled: bool) {
    NICK_STATE.write().coloring = enabled;
}

/// Timestamp config (`setTimestamps`): written once at startup / from `/set`.
/// An empty `format` keeps the current one.
pub fn set_timestamps(enabled: bool, format: &str) {
    *TIMESTAMPS_ENABLED.write() = enabled;
    if !format.is_empty() {
        *TS_FORMAT.write() = Cow::Owned(format.to_string());
    }
}

/// Timestamp colour by name (`setTSColor`).
pub fn set_ts_color(name: &str) {
    let lower = name.to_lowercase();
    let (n, code): (&'static str, &'static str) = match lower.as_str() {
        "blue" => ("blue", ANSI_BLUE),
        "green" => ("green", ANSI_GREEN),
        "cyan" => ("cyan", ANSI_CYAN),
        "none" | "" => ("none", ""),
        // "gray" or unrecognised
        _ => ("gray", ANSI_DARK_GRAY),
    };
    *TS_COLOR_NAME.write() = n;
    *TS_COLOR_CODE.write() = code;
}

/// Theme (`setTheme`, v0.4.8): `dark` (default) or `light`. Light swaps the
/// nick palette for dark colours. Caller should follow with
/// [`invalidate_render_caches`].
pub fn set_theme(name: &str) {
    let mut st = NICK_STATE.write();
    match name.to_lowercase().as_str() {
        "light" => {
            st.dark = false;
            st.palette = &NICK_COLORS_LIGHT;
        }
        // "dark" or unrecognised
        _ => {
            st.dark = true;
            st.palette = &NICK_COLORS_DARK;
        }
    }
}

// ---- Getters ----

/// Current timestamp colour code (`tsColorCode`); empty when disabled.
pub fn ts_color_code() -> &'static str {
    *TS_COLOR_CODE.read()
}

/// Current highlight background code (`hlBgCode`).
pub fn hl_bg_code() -> &'static str {
    *HL_BG_CODE.read()
}

/// Last configured timestamp colour NAME (`tsColorName`).
pub fn ts_color_name() -> &'static str {
    *TS_COLOR_NAME.read()
}

/// Last configured highlight colour NAME (`hlColorName`).
pub fn hl_color_name() -> String {
    HL_COLOR_NAME.read().to_string()
}

/// Whether timestamps are shown (`timestampsEnabled`).
pub fn timestamps_enabled() -> bool {
    *TIMESTAMPS_ENABLED.read()
}

/// Timestamp layout in Go time-layout syntax (`tsFormat`).
pub fn ts_format() -> String {
    TS_FORMAT.read().to_string()
}

/// `themeDarkMode`.
pub fn theme_dark_mode() -> bool {
    NICK_STATE.read().dark
}

/// `nickColoring`.
pub fn nick_coloring() -> bool {
    NICK_STATE.read().coloring
}

/// Colour for a nick: FNV-1a (32-bit) of the lower-cased nick indexes the
/// active palette. Empty when nick colouring is disabled (`nickColor`).
pub fn nick_color(nick: &str) -> &'static str {
    let (active, palette) = {
        let st = NICK_STATE.read();
        (st.coloring, st.palette)
    };
    if !active || palette.is_empty() {
        return "";
    }
    let h = fnv1a_32(nick.to_lowercase().as_bytes());
    palette[(h % palette.len() as u32) as usize]
}

/// FNV-1a 32-bit (Go `hash/fnv.New32a`).
pub fn fnv1a_32(data: &[u8]) -> u32 {
    let mut h: u32 = 2166136261;
    for &b in data {
        h ^= b as u32;
        h = h.wrapping_mul(16777619);
    }
    h
}

// ---- Accent colours / NO_COLOR ----

/// Accent for "needs attention" UI elements (unread windows, ops in the nick
/// list, the scroll banner). Plain yellow (33) is unreadable on many light
/// backgrounds, so the light theme uses a dark goldenrod instead.
pub fn accent_color() -> &'static str {
    if theme_dark_mode() {
        ANSI_YELLOW
    } else {
        "\x1b[38;5;136m"
    }
}

/// Spalva „foniniams“ kanalo įvykiams (kitų join/part/quit/nick, kitų
/// išmetimai): blankesnė už pokalbį, kad judriuose kanaluose jie
/// neužgožtų teksto. Tamsi tema — tamsiai pilka; šviesi — šviesiai pilka
/// (ant balto fono pilka 90 beveik tokia pat ryški kaip juodas tekstas).
pub fn event_color() -> &'static str {
    if theme_dark_mode() {
        ANSI_DARK_GRAY
    } else {
        "\x1b[38;5;246m"
    }
}

// ---- Join/part/quit filtras (`show_joins`) ----

/// Kurios KITŲ naudotojų join/part/quit/nick eilutės rodomos.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum JoinFilter {
    /// Visos (numatyta).
    All,
    /// Tik nick'ų, neseniai kalbėjusių kanale
    /// ([`super::channel::SMART_FILTER_SECS`]).
    Smart,
    /// Jokios.
    None,
}

impl JoinFilter {
    pub fn parse(v: &str) -> Option<JoinFilter> {
        match v.trim().to_ascii_lowercase().as_str() {
            "all" => Some(JoinFilter::All),
            "smart" => Some(JoinFilter::Smart),
            "none" => Some(JoinFilter::None),
            _ => None,
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            JoinFilter::All => "all",
            JoinFilter::Smart => "smart",
            JoinFilter::None => "none",
        }
    }
}

static JOIN_FILTER: AtomicU8 = AtomicU8::new(0);

/// `show_joins` (all|smart|none); nežinoma reikšmė = `all`. Po jo reikia
/// [`invalidate_render_caches`] — atvaizduota istorija nuo jo priklauso.
pub fn set_show_joins(v: &str) {
    let n = match JoinFilter::parse(v).unwrap_or(JoinFilter::All) {
        JoinFilter::All => 0,
        JoinFilter::Smart => 1,
        JoinFilter::None => 2,
    };
    JOIN_FILTER.store(n, Ordering::Relaxed);
}

pub fn show_joins() -> JoinFilter {
    match JOIN_FILTER.load(Ordering::Relaxed) {
        1 => JoinFilter::Smart,
        2 => JoinFilter::None,
        _ => JoinFilter::All,
    }
}

/// `NO_COLOR` (<https://no-color.org>): when set, every colour SGR is
/// removed from the frame; bold/underline/reverse stay so the layout is
/// still readable, and coloured BACKGROUNDS (status bar, active window,
/// highlights) become reverse video.
static NO_COLOR: AtomicBool = AtomicBool::new(false);

pub fn set_no_color(on: bool) {
    NO_COLOR.store(on, Ordering::Relaxed);
}

pub fn no_color() -> bool {
    NO_COLOR.load(Ordering::Relaxed)
}

/// `NO_COLOR` is honoured when present and non-empty (per no-color.org).
pub fn no_color_from_env(value: Option<&std::ffi::OsStr>) -> bool {
    value.is_some_and(|v| !v.is_empty())
}

/// Rewrites every SGR (`ESC [ … m`) in `s` without colours: foreground
/// colours (30–37, 90–97, 38;5;n, 38;2;r;g;b) are dropped, background
/// colours (40–47, 100–107, 48;…) become reverse video (7). Attributes and
/// resets are kept; other escape sequences are copied unchanged.
pub fn strip_colors(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(i) = rest.find("\x1b[") {
        out.push_str(&rest[..i]);
        let after = &rest[i + 2..];
        // Parameter bytes are digits and ';' — anything else ends the CSI.
        let plen = after
            .bytes()
            .take_while(|b| b.is_ascii_digit() || *b == b';')
            .count();
        if after.as_bytes().get(plen) != Some(&b'm') {
            out.push_str("\x1b[");
            rest = after;
            continue;
        }
        let params: Vec<&str> = after[..plen].split(';').collect();
        let mut keep: Vec<&str> = Vec::new();
        let mut k = 0;
        while k < params.len() {
            let p = params[k];
            let n = p.parse::<u32>().ok();
            match n {
                Some(38) | Some(48) => {
                    if n == Some(48) && !keep.contains(&"7") {
                        keep.push("7");
                    }
                    // 38;5;n or 38;2;r;g;b
                    k += match params.get(k + 1).copied() {
                        Some("5") => 3,
                        Some("2") => 5,
                        _ => 1,
                    };
                    continue;
                }
                Some(30..=37) | Some(39) | Some(90..=97) | Some(49) => {}
                Some(40..=47) | Some(100..=107) => {
                    if !keep.contains(&"7") {
                        keep.push("7");
                    }
                }
                _ => keep.push(p),
            }
            k += 1;
        }
        if !keep.is_empty() {
            out.push_str("\x1b[");
            out.push_str(&keep.join(";"));
            out.push('m');
        }
        rest = &after[plen + 1..];
    }
    out.push_str(rest);
    out
}

// ---- Cache invalidation ----

/// After a theme / nick-colour change (v0.4.8): already-rendered lines carry
/// the OLD SGR codes. Invalidate the render cache of every channel — the
/// next draw re-renders everything. `rendered_gen = -1` as well, otherwise
/// `cache_ok` (`rendered_gen == gen`) could stay true and an empty cache
/// would be drawn as ZERO lines.
///
/// The caller holds the client lock and is responsible for queueing the
/// redraw (Go `invalidateRenderCaches` did both).
pub fn invalidate_render_caches<'a, I>(channels: I)
where
    I: IntoIterator<Item = &'a mut Channel>,
{
    for ch in channels {
        ch.invalidate_render_cache();
    }
}

#[cfg(test)]
pub(crate) static TEST_LOCK: parking_lot::Mutex<()> = parking_lot::Mutex::new(());

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fnv_known_vectors() {
        // Reference FNV-1a 32-bit values.
        assert_eq!(fnv1a_32(b""), 0x811c9dc5);
        assert_eq!(fnv1a_32(b"a"), 0xe40c292c);
        assert_eq!(fnv1a_32(b"foobar"), 0xbf9cf968);
    }

    #[test]
    fn highlight_color_names() {
        let _g = TEST_LOCK.lock();
        set_highlight_color("Blue");
        assert_eq!(hl_bg_code(), ANSI_BG_BLUE);
        assert_eq!(hl_color_name(), "blue");
        set_highlight_color("none");
        assert_eq!(hl_bg_code(), ANSI_REVERSE);
        set_highlight_color("bogus");
        assert_eq!(hl_bg_code(), ANSI_BG_RED);
        assert_eq!(hl_color_name(), "red");
        set_highlight_color("");
        assert_eq!(hl_bg_code(), ANSI_BG_RED);
    }

    #[test]
    fn ts_color_names() {
        let _g = TEST_LOCK.lock();
        set_ts_color("cyan");
        assert_eq!(ts_color_code(), ANSI_CYAN);
        set_ts_color("none");
        assert_eq!(ts_color_code(), "");
        assert_eq!(ts_color_name(), "none");
        set_ts_color("whatever");
        assert_eq!(ts_color_name(), "gray");
        assert_eq!(ts_color_code(), ANSI_DARK_GRAY);
    }

    #[test]
    fn theme_switch_changes_palette() {
        let _g = TEST_LOCK.lock();
        set_nick_colors(true);
        set_theme("dark");
        let dark = nick_color("alice");
        assert!(NICK_COLORS_DARK.contains(&dark));
        set_theme("LIGHT");
        assert!(!theme_dark_mode());
        let light = nick_color("alice");
        assert!(NICK_COLORS_LIGHT.contains(&light));
        // Case-insensitive.
        assert_eq!(nick_color("ALICE"), light);
        set_nick_colors(false);
        assert_eq!(nick_color("alice"), "");
        set_nick_colors(true);
        set_theme("dark");
        assert!(theme_dark_mode());
    }

    #[test]
    fn strip_colors_keeps_attributes_and_turns_backgrounds_into_reverse() {
        assert_eq!(strip_colors("\x1b[31mred\x1b[0m"), "red\x1b[0m");
        assert_eq!(strip_colors("\x1b[44m\x1b[1mbar"), "\x1b[7m\x1b[1mbar");
        assert_eq!(strip_colors("\x1b[38;5;208mx"), "x");
        assert_eq!(strip_colors("\x1b[1;38;2;1;2;3;4my"), "\x1b[1;4my");
        assert_eq!(strip_colors("\x1b[41;97mhl"), "\x1b[7mhl");
        assert_eq!(strip_colors("\x1b[48;5;12mbg"), "\x1b[7mbg");
        // Non-SGR sequences (cursor moves, erase) pass through untouched.
        assert_eq!(strip_colors("\x1b[3;20H\x1b[5X"), "\x1b[3;20H\x1b[5X");
        assert_eq!(strip_colors("\x1b[?25l"), "\x1b[?25l");
        assert_eq!(strip_colors("plain ž"), "plain ž");
    }

    #[test]
    fn no_color_env_rules() {
        assert!(!no_color_from_env(None));
        assert!(!no_color_from_env(Some(std::ffi::OsStr::new(""))));
        assert!(no_color_from_env(Some(std::ffi::OsStr::new("1"))));
    }

    #[test]
    fn mirc_colors_stay_visible_on_both_backgrounds() {
        let _g = TEST_LOCK.lock();
        set_theme("dark");
        assert_ne!(mirc_colors()[1], "\x1b[30m", "black on dark");
        assert_eq!(mirc_colors()[0], "\x1b[97m");
        set_theme("light");
        assert_ne!(mirc_colors()[0], "\x1b[97m", "white on light");
        assert_eq!(mirc_colors()[1], "\x1b[30m");
        set_theme("dark");
    }

    #[test]
    fn accent_follows_theme() {
        let _g = TEST_LOCK.lock();
        set_theme("light");
        assert_ne!(accent_color(), ANSI_YELLOW);
        set_theme("dark");
        assert_eq!(accent_color(), ANSI_YELLOW);
    }

    #[test]
    fn event_color_is_dim_on_both_themes() {
        let _g = TEST_LOCK.lock();
        set_theme("dark");
        assert_eq!(event_color(), ANSI_DARK_GRAY);
        set_theme("light");
        assert_ne!(event_color(), ANSI_DARK_GRAY);
        // NO_COLOR: spalva išmetama visiškai.
        assert!(strip_colors(event_color()).is_empty());
        set_theme("dark");
    }

    #[test]
    fn show_joins_values() {
        let _g = TEST_LOCK.lock();
        for (v, f) in [
            ("all", JoinFilter::All),
            ("SMART", JoinFilter::Smart),
            ("none", JoinFilter::None),
        ] {
            set_show_joins(v);
            assert_eq!(show_joins(), f);
            assert_eq!(JoinFilter::parse(f.as_str()), Some(f));
        }
        assert_eq!(JoinFilter::parse("taip"), None);
        set_show_joins("taip");
        assert_eq!(show_joins(), JoinFilter::All);
    }

    #[test]
    fn timestamps_config() {
        let _g = TEST_LOCK.lock();
        set_timestamps(true, "15:04:05");
        assert!(timestamps_enabled());
        assert_eq!(ts_format(), "15:04:05");
        set_timestamps(false, "");
        assert!(!timestamps_enabled());
        assert_eq!(ts_format(), "15:04:05");
        set_timestamps(true, "15:04");
    }
}
