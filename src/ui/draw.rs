//! Screen drawing ported from `ui.go` (`Client.draw`).
//!
//! The Go `draw()` interleaves locking with string building. Here the pure
//! parts are separated so the future `Client` can do:
//!
//! 1. lock; snapshot everything into a [`FrameParams`] (see
//!    [`ChanState::from_channel`]);
//! 2. compute `msg_w = msg_width(w, show_nicks)` and
//!    [`update_render_cache`] for the active channel;
//! 3. [`apply_history_freeze`] to get the effective scroll offset;
//! 4. unlock; call [`draw_frame`] and write the returned string to the
//!    terminal; store [`Frame::msg_area_top`] back into the client.

use std::time::Duration;

use crate::sanitize::sanitize_text;
use crate::width::{fit_width, rune_width, string_width, truncate_width};

use super::channel::{mode_prefix, Channel};
use super::message::{changed_day, day_sep_line, strip_irc_format};
use super::theme::{
    accent_color, move_to, no_color, strip_colors, ANSI_BG_BLUE, ANSI_BG_RED, ANSI_BOLD, ANSI_CYAN,
    ANSI_DARK_GRAY, ANSI_GREEN, ANSI_HIDE_CUR, ANSI_MAGENTA, ANSI_RED, ANSI_RESET, ANSI_REVERSE,
    ANSI_SHOW_CUR, ANSI_WHITE,
};

/// Width of the channel list column (`chanListWidth`) on a normal terminal.
pub const CHAN_LIST_WIDTH: i32 = 18;
/// Channel list width on a narrow terminal (< [`NARROW_WIDTH`] columns).
pub const CHAN_LIST_WIDTH_NARROW: i32 = 12;
/// Below this terminal width the channel list shrinks to
/// [`CHAN_LIST_WIDTH_NARROW`].
pub const NARROW_WIDTH: i32 = 60;
/// Width of the nick list column (`nickListWidth`).
pub const NICK_LIST_WIDTH: i32 = 16;
/// The nick list is shown only if the message column keeps at least this
/// many cells; otherwise it is hidden so the text stays readable.
pub const MIN_MSG_WIDTH_WITH_NICKS: i32 = 40;

/// Smallest terminal that is drawn at all (`w < 40 || h < 8` → skip).
pub const MIN_WIDTH: i32 = 40;
pub const MIN_HEIGHT: i32 = 8;

/// Right half of the status bar (`statusRight`): key hints, shown only when
/// the left part (nick, server, window) still fits.
pub const STATUS_RIGHT: &str = " PgUp/Dn:slinkti  Ctrl+N/P:langai  Tab:užbaigti  /help ";
/// Short hint used when [`STATUS_RIGHT`] does not fit.
pub const STATUS_RIGHT_SHORT: &str = " /help ";

/// Column geometry of one frame.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Layout {
    /// Channel list width (its `│` separator is the next column).
    pub chan_w: i32,
    /// Nick list width; 0 when the nick list is hidden.
    pub nick_w: i32,
    /// Message column width (the render-cache key).
    pub msg_w: i32,
}

/// Responsive layout: the channel list shrinks on narrow terminals and the
/// nick list disappears when it would squeeze the messages below
/// [`MIN_MSG_WIDTH_WITH_NICKS`].
pub fn layout(w: i32, show_nicks: bool) -> Layout {
    let chan_w = chan_list_width(w);
    let base = w - chan_w - 2;
    let nick_w = if show_nicks && base - (NICK_LIST_WIDTH + 1) >= MIN_MSG_WIDTH_WITH_NICKS {
        NICK_LIST_WIDTH
    } else {
        0
    };
    let nick_panel_w = if nick_w > 0 { nick_w + 1 } else { 0 };
    Layout {
        chan_w,
        nick_w,
        msg_w: (base - nick_panel_w).max(10),
    }
}

/// Channel list width for a terminal `w` columns wide (mouse hit-testing
/// uses the same value).
pub fn chan_list_width(w: i32) -> i32 {
    if w < NARROW_WIDTH {
        CHAN_LIST_WIDTH_NARROW
    } else {
        CHAN_LIST_WIDTH
    }
}

/// Frame shown instead of the UI when the terminal is smaller than
/// [`MIN_WIDTH`]×[`MIN_HEIGHT`]; `None` when the size is unknown (0×0).
pub fn too_small_frame(w: i32, h: i32) -> Option<String> {
    if w <= 0 || h <= 0 {
        return None;
    }
    let msg = format!("Terminalas per mažas ({w}×{h}); reikia bent {MIN_WIDTH}×{MIN_HEIGHT}");
    let mut out = String::from("\x1b[?25l\x1b[2J");
    for (i, line) in wrap_plain(&msg, w).iter().take(h as usize).enumerate() {
        out.push_str(&move_to(i as i32 + 1, 1));
        out.push_str(line);
    }
    Some(out)
}

/// Word wrap for short plain notices (no formatting codes).
fn wrap_plain(s: &str, w: i32) -> Vec<String> {
    super::message::wrap_text(s, w.max(1))
}

/// Trims the left part of the status bar to the free space (`w` minus the
/// cell width of `status_right`) and returns the number of padding spaces
/// before the right part — otherwise a long channel name wraps the bar onto
/// a second row (`clampStatusLeft`).
pub fn clamp_status_left(status_left: &str, status_right: &str, w: i32) -> (String, i32) {
    let max_left = (w - string_width(status_right)).max(0);
    let left = truncate_width(status_left, max_left);
    let pad = (max_left - string_width(&left)).max(0);
    (left, pad)
}

/// Width of the message column (`msgW`): the window minus the channel list,
/// its separator and the nick panel (if [`layout`] shows it); at least 10.
pub fn msg_width(w: i32, show_nicks: bool) -> i32 {
    layout(w, show_nicks).msg_w
}

/// Picks the status bar hint that still leaves room for `left`:
/// the full [`STATUS_RIGHT`], the short [`STATUS_RIGHT_SHORT`] or nothing.
pub fn status_right_for(left: &str, w: i32) -> &'static str {
    let lw = string_width(left);
    if lw + string_width(STATUS_RIGHT) <= w {
        STATUS_RIGHT
    } else if lw + string_width(STATUS_RIGHT_SHORT) <= w {
        STATUS_RIGHT_SHORT
    } else {
        ""
    }
}

/// Left part of the status bar, shortened step by step until it fits `w`
/// cells: first the "mircBSD" label goes, then the server. The active window
/// name is kept as long as possible (it is the one thing the user must see).
///
/// `who` is the nick part (with `[AWAY]`), `server` the server part (with
/// `[TLS]` / connection state); an empty `server` means "never connected".
pub fn status_left(who: &str, server: &str, active_name: &str, w: i32) -> String {
    let candidates = if server.is_empty() {
        [
            format!(" mircBSD | [NEPRISIJUNGTA] | {active_name} "),
            format!(" [NEPRISIJUNGTA] | {active_name} "),
            format!(" [NEPRISIJUNGTA] | {active_name} "),
        ]
    } else {
        [
            format!(" mircBSD | {who} @ {server} | {active_name} "),
            format!(" {who} @ {server} | {active_name} "),
            format!(" {who} | {active_name} "),
        ]
    };
    for c in &candidates[..2] {
        if string_width(c) <= w {
            return c.clone();
        }
    }
    candidates[2].clone()
}

/// Nuo šio lag statuso juostoje paryškinamas (strigusi jungtis).
pub const LAG_WARN: Duration = Duration::from_secs(5);

/// Kokį lag rodyti: paskutinį išmatuotą, o jei atsakymo laukiama ilgiau už
/// jį (ir ilgiau nei 1 s — kad greitas ryšys nemirguliuotų) — augantį
/// laukimo laiką: vartotojas mato strigstančią jungtį dar prieš timeout'ą.
pub fn lag_shown(measured: Option<Duration>, waiting: Option<Duration>) -> Option<Duration> {
    let floor = measured.unwrap_or_default().max(Duration::from_secs(1));
    match waiting {
        Some(w) if w > floor => Some(w),
        _ => measured,
    }
}

/// `0.12s`, `3.40s`, `12.3s`, `187s`.
pub fn format_lag(d: Duration) -> String {
    let s = d.as_secs_f64();
    if s < 10.0 {
        format!("{s:.2}s")
    } else if s < 100.0 {
        format!("{s:.1}s")
    } else {
        format!("{}s", d.as_secs())
    }
}

/// Statuso juostos kairė dalis su lag segmentu (`| lag 0.12s `). Prioritetai
/// siaurėjant: pirmiausia trumpėja/dingsta užuominos (dešinė), tada
/// „mircBSD“, tada lag, tada serveris — lango vardas lieka visada.
/// Grąžina (kairė dalis, lag segmentas arba tuščias).
pub fn status_left_lag(
    who: &str,
    server: &str,
    active_name: &str,
    lag: Option<&str>,
    w: i32,
) -> (String, String) {
    let left = status_left(who, server, active_name, w);
    let Some(lag) = lag.filter(|_| !server.is_empty()) else {
        return (left, String::new());
    };
    let seg = format!("| lag {lag} ");
    let seg_w = string_width(&seg);
    for c in [
        format!(" mircBSD | {who} @ {server} | {active_name} "),
        format!(" {who} @ {server} | {active_name} "),
    ] {
        if string_width(&c) + seg_w <= w {
            return (c, seg);
        }
    }
    (left, String::new())
}

/// First row of the message area (status row + optional topic + optional
/// search bar), 1-based (`msgAreaTop`).
pub fn msg_area_top(has_topic: bool, search_mode: bool) -> i32 {
    let mut row = 2; // after the status bar
    if has_topic {
        row += 1;
    }
    if search_mode {
        row += 1;
    }
    row
}

/// Per-channel values the sidebar needs, copied under the client lock
/// (reading `unread`/`highlight`/`is_private` after unlocking raced with the
/// network thread in the Go version).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ChanState {
    pub name: String,
    pub unread: i32,
    pub highlight: i32,
    pub is_private: bool,
}

impl ChanState {
    pub fn from_channel(ch: &Channel) -> Self {
        ChanState {
            name: ch.name.clone(),
            unread: ch.unread,
            highlight: ch.highlight,
            is_private: ch.is_private,
        }
    }
}

/// Everything `draw_frame` needs; a snapshot taken under the client lock.
#[derive(Debug, Clone)]
pub struct FrameParams<'a> {
    pub width: i32,
    pub height: i32,
    pub channels: &'a [ChanState],
    pub active_idx: usize,
    pub active_name: &'a str,
    pub topic: &'a str,
    /// Nick list of the active channel (with status prefixes).
    pub nicks: &'a [String],
    /// PREFIX signs of the active channel (for nick colouring).
    pub prefix_signs: &'a str,
    /// Show the nick panel (`c.isChannel(activeName)`).
    pub show_nicks: bool,
    /// Rendered message lines of the active channel (the render cache).
    pub lines: &'a [String],
    pub input: &'a [char],
    pub input_pos: usize,
    /// Effective scroll offset (after [`apply_history_freeze`]).
    pub scroll_off: i32,
    pub nick_scroll_off: i32,
    pub chan_scroll_off: i32,
    pub connected: bool,
    /// A connect loop is running (dialing / waiting to reconnect).
    pub connecting: bool,
    pub my_nick: &'a str,
    pub server: &'a str,
    pub away: &'a str,
    pub use_tls: bool,
    pub search_mode: bool,
    pub search_query: &'a str,
    /// Rodomas lag ([`lag_shown`]); `None` — nerodomas.
    pub lag: Option<Duration>,
}

/// Result of [`draw_frame`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Frame {
    /// Terminal output (escape sequences included); print it in one write.
    pub output: String,
    /// First row of the message area — the client stores it (`c.msgAreaTop`)
    /// for mouse hit-testing.
    pub msg_area_top: i32,
}

/// Erase `n` cells from the cursor (`\033[nX`).
fn erase(n: i32) -> String {
    format!("\x1b[{}X", n)
}

/// Brings the active channel's render cache up to date for `msg_w` /
/// `my_nick` (the cache block in `draw`).
///
/// INCREMENTAL: when messages were only APPENDED (same width and nick) the
/// old lines stay valid and only the new messages are rendered, taking draw
/// cost from O(n) to O(new messages). Day separators (v0.4.7) are inserted
/// while rendering, never stored in the messages (they would be logged
/// twice).
pub fn update_render_cache(ch: &mut Channel, msg_w: i32, my_nick: &str) {
    let gen = ch.render_gen;
    let cache_ok = ch.rendered_gen == gen && ch.rendered_w == msg_w && ch.rendered_nick == my_nick;
    if cache_ok {
        return;
    }
    // Messages are only appended (add_msg), so a cache with the same width
    // and nick that has not seen more messages than exist can be continued.
    let incremental = ch.rendered_gen != -1
        && ch.rendered_w == msg_w
        && ch.rendered_nick == my_nick
        && ch.rendered_msg_count <= ch.messages.len();

    let start = if incremental {
        ch.rendered_msg_count
    } else {
        0
    };
    // Timestamp of the last already-rendered message (day separator).
    // (Paskutinės MATOMOS — paslėptos join/part eilutės skirtiklio nepiešia.)
    let mut prev_ts = if incremental {
        ch.messages[..start]
            .iter()
            .rev()
            .find(|m| !m.is_hidden())
            .map(|m| m.ts)
    } else {
        None
    };

    let mut lines = if incremental {
        std::mem::take(&mut ch.rendered)
    } else {
        Vec::new()
    };
    for m in &ch.messages[start..] {
        // `show_joins` paslėptos eilutės nepiešiamos (ir neperkelia paros
        // skirtiklio). Nustatymo keitimas išvalo cache'ą — viskas perpiešiama.
        if m.is_hidden() {
            continue;
        }
        if let Some(prev) = prev_ts {
            if changed_day(&prev, &m.ts) {
                lines.push(day_sep_line(&m.ts));
            }
        }
        lines.extend(m.render_lines(msg_w, my_nick));
        prev_ts = Some(m.ts);
    }
    ch.rendered = lines;
    ch.rendered_gen = gen;
    ch.rendered_w = msg_w;
    ch.rendered_nick = my_nick.to_string();
    ch.rendered_msg_count = ch.messages.len();
}

/// History "freezing": while scrolled up (`scroll_off > 0`) new messages
/// would slide the visible text upward, because the offset is measured from
/// the bottom. Compensate by the number of new lines so the view stays put.
///
/// `was_drawn` — the client's `lastDrawn == active`; `chase` — the user just
/// scrolled down (`now < chaseUntil`): do NOT freeze or the user could never
/// reach the bottom while messages keep arriving.
///
/// Updates `ch.draw_line_count` and returns the new scroll offset.
pub fn apply_history_freeze(
    ch: &mut Channel,
    was_drawn: bool,
    total_lines: i32,
    mut scroll_off: i32,
    chase: bool,
) -> i32 {
    let mut prev_count = ch.draw_line_count;
    if !was_drawn || prev_count > total_lines {
        prev_count = total_lines;
    }
    ch.draw_line_count = total_lines;
    if scroll_off > 0 && was_drawn && total_lines > prev_count && !chase {
        scroll_off += total_lines - prev_count;
    }
    scroll_off
}

/// Builds one full frame (`Client.draw` without the locking and the final
/// print). Returns `None` when the terminal is too small to draw or there is
/// no valid active channel.
pub fn draw_frame(p: &FrameParams) -> Option<Frame> {
    let (w, h) = (p.width, p.height);
    if w < MIN_WIDTH || h < MIN_HEIGHT {
        return None;
    }
    if p.active_idx >= p.channels.len() {
        // No active channel (invalid activeIdx) — do not draw.
        return None;
    }
    let channels = p.channels;
    let nicks = p.nicks;

    let mut out = String::with_capacity((w * h) as usize * 2);
    out.push_str(ANSI_HIDE_CUR);

    // Terminal WINDOW TITLE (OSC 0, BEL-terminated): another desktop row
    // sees which channel is active and how many unread messages the other
    // windows hold (v0.4.7). Sanitised and clipped — an OSC title with
    // control characters would otherwise become terminal control.
    let title_total: i32 = channels
        .iter()
        .enumerate()
        .filter(|(i, _)| *i != p.active_idx)
        .map(|(_, st)| st.unread)
        .sum();
    let title_name = truncate_width(&sanitize_text(p.active_name), 40);
    let mut title = format!("mircBSD — {}", title_name);
    if title_total > 0 {
        title.push_str(&format!(" ({})", title_total));
    }
    out.push_str(&format!("\x1b]0;{}\x07", title));

    let mut row = 1;

    // --- Status ---
    out.push_str(&move_to(row, 1));
    out.push_str(&erase(w));
    out.push_str(&move_to(row, 1));
    out.push_str(ANSI_BG_BLUE);
    out.push_str(ANSI_BOLD);
    out.push_str(ANSI_WHITE);
    let mut who = sanitize_text(p.my_nick);
    if !p.away.is_empty() {
        who.push_str(" [AWAY]");
    }
    let mut srv = String::new();
    if !(p.server.is_empty() && !p.connected) {
        srv = sanitize_text(p.server);
        if p.use_tls {
            srv.push_str(" [TLS]");
        }
        if !p.connected {
            srv.push_str(if p.connecting {
                " [JUNGIAMASI…]"
            } else {
                " [ATSIJUNGTA]"
            });
        }
    }
    let lag_txt = p.lag.filter(|_| p.connected).map(format_lag);
    let (status_left, lag_seg) = status_left_lag(
        &who,
        &srv,
        &sanitize_text(p.active_name),
        lag_txt.as_deref(),
        w,
    );
    let status_right = status_right_for(&format!("{status_left}{lag_seg}"), w);
    // Clip the left part to the free space: a long channel name (a hostile
    // server has no length limit) would wrap the bar onto a second row and
    // break topic/search drawing.
    let (status_left, pad) =
        clamp_status_left(&status_left, status_right, w - string_width(&lag_seg));
    out.push_str(&status_left);
    if !lag_seg.is_empty() {
        let warn = p.lag.is_some_and(|l| l >= LAG_WARN);
        if warn {
            // NO_COLOR: raudonas fonas taptų inversija — o juosta jau
            // invertuota, tad segmentas rodomas atvirkščiai (be inversijos).
            out.push_str(if no_color() { "\x1b[27m" } else { ANSI_BG_RED });
        }
        out.push_str(&lag_seg);
        if warn {
            out.push_str(ANSI_RESET);
            out.push_str(ANSI_BG_BLUE);
            out.push_str(ANSI_BOLD);
            out.push_str(ANSI_WHITE);
        }
    }
    out.push_str(&" ".repeat(pad as usize));
    out.push_str(status_right);
    out.push_str(ANSI_RESET);
    row += 1;

    // --- Topic bar ---
    if !p.topic.is_empty() {
        out.push_str(&move_to(row, 1));
        out.push_str(&erase(w));
        out.push_str(&move_to(row, 1));
        out.push_str(ANSI_DARK_GRAY);
        out.push_str(" ✦ ");
        out.push_str(ANSI_RESET);
        let topic_plain = sanitize_text(&strip_irc_format(p.topic));
        let max_topic = w - 4;
        let topic_plain = truncate_width(&topic_plain, max_topic);
        out.push_str(ANSI_CYAN);
        out.push_str(&topic_plain);
        out.push_str(ANSI_RESET);
        row += 1;
    }

    // --- Search bar ---
    if p.search_mode {
        out.push_str(&move_to(row, 1));
        out.push_str(&erase(w));
        out.push_str(&move_to(row, 1));
        out.push_str(ANSI_BG_BLUE);
        out.push_str(" Paieška: ");
        out.push_str(&sanitize_text(p.search_query));
        out.push_str(" (Enter=ieškoti, Enter iš naujo=atgal, Esc=išeiti) ");
        out.push_str(ANSI_RESET);
        row += 1;
    }

    let msg_area_top = row;
    let msg_area_bot = h - 2;
    let msg_rows = (msg_area_bot - msg_area_top + 1).max(1);

    // --- Channel list ---
    //
    // SCROLLING: there can be more channels than rows (up to 100). Scroll
    // offset (chan_scroll_off) + ▲▼ arrows.
    let lay = layout(w, p.show_nicks);
    let chan_w = lay.chan_w;
    let accent = accent_color();
    let n_chan = channels.len() as i32;
    let max_chan_scroll = (n_chan - msg_rows).max(0);
    let chan_scroll = p.chan_scroll_off.min(max_chan_scroll).max(0);
    // Window numbers (for /win N, F1–F12, Alt+1–9) right-aligned to the
    // widest number so the names line up past window 9.
    let num_w = n_chan.max(1).to_string().len();
    for i in 0..msg_rows {
        let r = msg_area_top + i;
        out.push_str(&move_to(r, 1));
        out.push_str(&erase(chan_w));
        out.push_str(&move_to(r, 1));
        let chan_idx = chan_scroll + i;
        if chan_idx < n_chan {
            let ci = chan_idx as usize;
            let st = &channels[ci];
            let active = ci == p.active_idx;
            if active {
                out.push_str(ANSI_BG_BLUE);
                out.push_str(ANSI_BOLD);
                out.push_str(ANSI_WHITE);
            } else if st.highlight > 0 {
                out.push_str(ANSI_RED);
                out.push_str(ANSI_BOLD);
            } else if st.unread > 0 {
                out.push_str(accent);
                out.push_str(ANSI_BOLD);
            } else if st.is_private {
                out.push_str(ANSI_MAGENTA);
            }
            // Idle windows use the terminal's default colour: dark grey (90)
            // vanishes on some dark themes (e.g. Solarized, where it equals
            // the background).
            let mut display_name = sanitize_text(&st.name);
            if st.is_private {
                // Private chats are marked ">" — the name must be sanitised
                // as well (a hostile PRIVMSG nick may carry ESC).
                display_name = format!(">{}", display_name);
            }
            let num = format!("{:>num_w$}:", chan_idx + 1);
            // Row = " " + number + name area + " " (the last cell keeps the
            // name off the separator).
            let name_area = (chan_w - 2 - num.len() as i32).max(1);
            let mut badge = String::new();
            if st.unread > 0 && !active {
                badge = if st.highlight > 0 {
                    format!("!{}", st.highlight)
                } else {
                    st.unread.to_string()
                };
                // A huge unread count (100000+) must not push the name out
                // completely or spill past "│": cap the badge at half the
                // name area.
                let max_badge = (name_area / 2).max(1);
                if string_width(&badge) > max_badge {
                    badge = truncate_width(&badge, max_badge);
                    if badge.is_empty() {
                        badge = "+".to_string();
                    }
                }
            }
            // Badge right-aligned at the end of the name area. Widths in
            // CELLS (fit_width), not bytes: otherwise non-ASCII names
            // (Lithuanian, CJK) would push it past the column.
            let label = if badge.is_empty() {
                format!(" {}{} ", num, fit_width(&display_name, name_area))
            } else {
                let bw = string_width(&badge);
                format!(
                    " {}{} {} ",
                    num,
                    fit_width(&display_name, (name_area - bw - 1).max(0)),
                    badge
                )
            };
            out.push_str(&label);
            out.push_str(ANSI_RESET);
        }
        out.push_str(&move_to(r, chan_w + 1));
        out.push_str(ANSI_DARK_GRAY);
        out.push('│');
        out.push_str(ANSI_RESET);
    }
    // Channel list arrows (more channels than rows).
    if max_chan_scroll > 0 {
        if chan_scroll > 0 {
            out.push_str(&move_to(msg_area_top, chan_w));
            out.push_str(accent);
            out.push('▲');
            out.push_str(ANSI_RESET);
        }
        if chan_scroll < max_chan_scroll {
            out.push_str(&move_to(msg_area_bot, chan_w));
            out.push_str(accent);
            out.push('▼');
            out.push_str(ANSI_RESET);
        }
    }

    // --- Messages ---
    let show_nicks = lay.nick_w > 0;
    let nick_panel_w = if show_nicks { lay.nick_w + 1 } else { 0 };
    let msg_w = lay.msg_w;

    let lines = p.lines;
    let total_lines = lines.len() as i32;
    let scroll_off = p.scroll_off.max(0);
    let start_line = (total_lines - msg_rows - scroll_off).max(0);

    let msg_col_start = chan_w + 2;
    let msg_col_end = w - nick_panel_w;
    for i in 0..msg_rows {
        let r = msg_area_top + i;
        out.push_str(&move_to(r, msg_col_start));
        out.push_str(&erase(msg_col_end - msg_col_start));
        out.push_str(&move_to(r, msg_col_start));
        let line_idx = start_line + i;
        if line_idx >= 0 && line_idx < total_lines {
            out.push_str(&lines[line_idx as usize]);
        }
    }

    if scroll_off > 0 {
        let banner = format!(
            " ↑ ISTORIJA: +{} eil. žemiau · PgDn žemyn · Shift+PgDn į apačią ",
            scroll_off
        );
        let banner = truncate_width(&banner, msg_w);
        out.push_str(&move_to(msg_area_top, msg_col_start));
        out.push_str(ANSI_REVERSE);
        out.push_str(ANSI_BOLD);
        out.push_str(&banner);
        out.push_str(ANSI_RESET);
    }

    // --- Nick list ---
    if show_nicks {
        let nick_col = w - NICK_LIST_WIDTH;
        let visible_nicks = msg_rows - 1;
        let n_nicks = nicks.len() as i32;
        let max_scroll = (n_nicks - visible_nicks).max(0);
        let nick_scroll_off = p.nick_scroll_off.min(max_scroll).max(0);
        for i in 0..msg_rows {
            let r = msg_area_top + i;
            out.push_str(&move_to(r, nick_col - 1));
            out.push_str(ANSI_DARK_GRAY);
            out.push('│');
            out.push_str(ANSI_RESET);
            out.push_str(&move_to(r, nick_col));
            out.push_str(&erase(NICK_LIST_WIDTH));
            out.push_str(&move_to(r, nick_col));
            if i == 0 {
                // Header: nick count; while scrolling "shown/total".
                let header = if n_nicks > visible_nicks {
                    let shown = (nick_scroll_off + visible_nicks).min(n_nicks);
                    format!(" Nick ({}/{})", shown, n_nicks)
                } else {
                    format!(" Nick ({})", n_nicks)
                };
                out.push_str(ANSI_BOLD);
                out.push_str(ANSI_CYAN);
                out.push_str(&fit_width(&header, NICK_LIST_WIDTH));
                out.push_str(ANSI_RESET);
            } else {
                let ni = nick_scroll_off + i - 1;
                if ni < n_nicks {
                    let n = sanitize_text(&nicks[ni as usize]);
                    let display = truncate_width(&n, NICK_LIST_WIDTH);
                    // Plain nicks use the default colour (37 "white" is
                    // invisible on a light background).
                    let color = match mode_prefix(&n, p.prefix_signs) {
                        "~" | "&" | "@" => accent,
                        "%" => ANSI_GREEN,
                        "+" => ANSI_CYAN,
                        _ => "",
                    };
                    out.push_str(color);
                    out.push_str(&fit_width(&display, NICK_LIST_WIDTH));
                    out.push_str(ANSI_RESET);
                }
            }
        }
        if n_nicks > visible_nicks {
            if nick_scroll_off > 0 {
                out.push_str(&move_to(msg_area_top, nick_col + NICK_LIST_WIDTH - 1));
                out.push_str(accent);
                out.push('▲');
                out.push_str(ANSI_RESET);
            }
            if nick_scroll_off < max_scroll {
                out.push_str(&move_to(msg_area_bot, nick_col + NICK_LIST_WIDTH - 1));
                out.push_str(accent);
                out.push('▼');
                out.push_str(ANSI_RESET);
            }
        }
    }

    // --- Separator (with ┴ under the column dividers) ---
    out.push_str(&move_to(h - 1, 1));
    out.push_str(ANSI_DARK_GRAY);
    let nick_sep_col = if show_nicks {
        w - NICK_LIST_WIDTH - 1
    } else {
        0
    };
    for col in 1..=w {
        out.push(if col == chan_w + 1 || col == nick_sep_col {
            '┴'
        } else {
            '─'
        });
    }
    out.push_str(ANSI_RESET);

    // --- Input line ---
    out.push_str(&move_to(h, 1));
    out.push_str(&erase(w));
    out.push_str(&move_to(h, 1));
    let mut prefix_len = 0;
    if !p.away.is_empty() {
        out.push_str(accent);
        out.push_str("[AWAY] ");
        out.push_str(ANSI_RESET);
        prefix_len += 7;
    }
    out.push_str(ANSI_GREEN);
    out.push_str(ANSI_BOLD);
    out.push_str("> ");
    out.push_str(ANSI_RESET);
    prefix_len += 2;
    // SANITISE THE INPUT LINE — the only place where unsanitised text
    // reaches the terminal. Server data (a nick prefix that arrived via
    // JOIN/NAMES) may carry an ESC/OSC sequence: without this filter "OSC 52"
    // (clipboard write) or "OSC 0" (window title) would reach the terminal
    // without the user's knowledge.
    let input_str: String = p.input.iter().collect();
    let safe_input = sanitize_text(&input_str);
    let disp_runes: Vec<char> = safe_input.chars().collect();

    // Cursor position — over the SANITISED text (sanitising removes
    // characters, so the raw index no longer matches): count how many
    // characters remain before input_pos.
    let upto = p.input_pos.min(p.input.len());
    let prefix_str: String = p.input[..upto].iter().collect();
    let cur_pos = sanitize_text(&prefix_str)
        .chars()
        .count()
        .min(disp_runes.len());
    let max_input = (w - prefix_len - 1).max(1);
    let (disp_start, disp_end) = input_window(&disp_runes, cur_pos, max_input);
    out.extend(disp_runes[disp_start..disp_end].iter());
    let cursor_cells: i32 = disp_runes[disp_start..cur_pos]
        .iter()
        .map(|&r| rune_width(r))
        .sum();
    let cursor_col = (prefix_len + 1 + cursor_cells).clamp(1, w);
    out.push_str(&move_to(h, cursor_col));
    out.push_str(ANSI_SHOW_CUR);

    if no_color() {
        out = strip_colors(&out);
    }
    Some(Frame {
        output: out,
        msg_area_top,
    })
}

/// Visible slice `[start, end)` of the input line that fits `max` CELLS and
/// keeps the cursor (`cur`, a char index) on screen. Wide characters (CJK,
/// emoji) take 2 cells, so counting chars would misplace the cursor.
pub fn input_window(chars: &[char], cur: usize, max: i32) -> (usize, usize) {
    let cur = cur.min(chars.len());
    let w = |r: &char| rune_width(*r);
    // Scroll right just enough that the text up to the cursor plus the
    // cursor cell itself fits.
    let mut start = 0;
    let mut used: i32 = chars[..cur].iter().map(w).sum();
    while start < cur && used + 1 > max {
        used -= w(&chars[start]);
        start += 1;
    }
    let mut end = start;
    let mut cells = 0;
    while end < chars.len() && cells + w(&chars[end]) <= max {
        cells += w(&chars[end]);
        end += 1;
    }
    (start, end.max(cur))
}

#[cfg(test)]
#[path = "draw_tests.rs"]
mod tests;
