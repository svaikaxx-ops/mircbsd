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

use crate::sanitize::sanitize_text;
use crate::width::{fit_width, string_width, truncate_width};

use super::channel::{mode_prefix, Channel};
use super::message::{changed_day, day_sep_line, strip_irc_format};
use super::theme::{
    move_to, ANSI_BG_BLUE, ANSI_BOLD, ANSI_CYAN, ANSI_DARK_GRAY, ANSI_GREEN, ANSI_HIDE_CUR,
    ANSI_MAGENTA, ANSI_RED, ANSI_RESET, ANSI_SHOW_CUR, ANSI_WHITE, ANSI_YELLOW,
};

/// Width of the channel list column (`chanListWidth`).
pub const CHAN_LIST_WIDTH: i32 = 18;
/// Width of the nick list column (`nickListWidth`).
pub const NICK_LIST_WIDTH: i32 = 16;

/// Smallest terminal that is drawn at all (`w < 40 || h < 8` → skip).
pub const MIN_WIDTH: i32 = 40;
pub const MIN_HEIGHT: i32 = 8;

/// Right half of the status bar (`statusRight`).
pub const STATUS_RIGHT: &str = " PgUp/Dn:scroll Ctrl+N/P:kan Ctrl+←→:žodis Tab:AC /help ";

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
/// its separator and the nick panel; at least 10.
pub fn msg_width(w: i32, show_nicks: bool) -> i32 {
    let nick_panel_w = if show_nicks { NICK_LIST_WIDTH + 1 } else { 0 };
    (w - CHAN_LIST_WIDTH - 2 - nick_panel_w).max(10)
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
    pub my_nick: &'a str,
    pub server: &'a str,
    pub away: &'a str,
    pub use_tls: bool,
    pub search_mode: bool,
    pub search_query: &'a str,
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
    let mut prev_ts = if incremental && ch.rendered_msg_count > 0 {
        Some(ch.messages[ch.rendered_msg_count - 1].ts)
    } else {
        None
    };

    let mut lines = if incremental {
        std::mem::take(&mut ch.rendered)
    } else {
        Vec::new()
    };
    for m in &ch.messages[start..] {
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
    let tls_mark = if p.use_tls { "[TLS] " } else { "" };
    let away_mark = if !p.away.is_empty() { " [AWAY]" } else { "" };
    let reconn_mark = if !p.connected {
        " [RECONNECTING...]"
    } else {
        ""
    };
    let mut status_left = format!(
        " mircBSD {}| {}{}{} @ {} | {} ",
        tls_mark, p.my_nick, away_mark, reconn_mark, p.server, p.active_name
    );
    if !p.connected && p.server.is_empty() {
        status_left = " mircBSD | [NEPRISIJUNGTA] ".to_string();
    }
    let status_left = sanitize_text(&status_left);
    // Clip the left part to the free space: a long channel name (a hostile
    // server has no length limit) would wrap the bar onto a second row and
    // break topic/search drawing.
    let (status_left, pad) = clamp_status_left(&status_left, STATUS_RIGHT, w);
    out.push_str(&status_left);
    out.push_str(&" ".repeat(pad as usize));
    out.push_str(STATUS_RIGHT);
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
    let n_chan = channels.len() as i32;
    let max_chan_scroll = (n_chan - msg_rows).max(0);
    let chan_scroll = p.chan_scroll_off.min(max_chan_scroll).max(0);
    for i in 0..msg_rows {
        let r = msg_area_top + i;
        out.push_str(&move_to(r, 1));
        out.push_str(&erase(CHAN_LIST_WIDTH));
        out.push_str(&move_to(r, 1));
        let chan_idx = chan_scroll + i;
        if chan_idx < n_chan {
            let ci = chan_idx as usize;
            let st = &channels[ci];
            if ci == p.active_idx {
                out.push_str(ANSI_BG_BLUE);
                out.push_str(ANSI_BOLD);
                out.push_str(ANSI_WHITE);
            } else if st.highlight > 0 {
                out.push_str(ANSI_RED);
                out.push_str(ANSI_BOLD);
            } else if st.unread > 0 {
                out.push_str(ANSI_YELLOW);
                out.push_str(ANSI_BOLD);
            } else if st.is_private {
                out.push_str(ANSI_MAGENTA);
            } else {
                out.push_str(ANSI_DARK_GRAY);
            }
            let mut display_name = sanitize_text(&st.name);
            if st.is_private {
                // Private chats are marked ">" — the name must be sanitised
                // as well (a hostile PRIVMSG nick may carry ESC).
                display_name = format!(">{}", display_name);
            }
            let fkey_prefix = if chan_idx < 12 {
                format!("{}:", chan_idx + 1)
            } else {
                String::new()
            };
            let max_name_w = CHAN_LIST_WIDTH - 2 - fkey_prefix.len() as i32;
            let display_name = truncate_width(&display_name, max_name_w);
            let mut label = format!(" {}{}", fkey_prefix, fit_width(&display_name, max_name_w));
            if st.unread > 0 && ci != p.active_idx {
                let unread_w = (max_name_w - 3).max(1);
                // Column width in CELLS (fit_width), not bytes: otherwise
                // non-ASCII names (Lithuanian, CJK) would push the "!N"
                // badge past CHAN_LIST_WIDTH and rows would drift.
                let mut badge = if st.highlight > 0 {
                    format!("!{}", st.highlight)
                } else {
                    st.unread.to_string()
                };
                // Badge limited to the space left on the row: a huge
                // unread count (100000+) would otherwise spill past "│".
                let max_badge = (CHAN_LIST_WIDTH - 1 - fkey_prefix.len() as i32 - unread_w).max(1);
                if string_width(&badge) > max_badge {
                    badge = truncate_width(&badge, max_badge);
                    if badge.is_empty() {
                        badge = "+".to_string();
                    }
                }
                label = format!(
                    " {}{}{}",
                    fkey_prefix,
                    fit_width(&display_name, unread_w),
                    badge
                );
            }
            out.push_str(&label);
            out.push_str(ANSI_RESET);
        }
        out.push_str(&move_to(r, CHAN_LIST_WIDTH + 1));
        out.push_str(ANSI_DARK_GRAY);
        out.push('│');
        out.push_str(ANSI_RESET);
    }
    // Channel list arrows (more channels than rows).
    if max_chan_scroll > 0 {
        if chan_scroll > 0 {
            out.push_str(&move_to(msg_area_top, CHAN_LIST_WIDTH));
            out.push_str(ANSI_YELLOW);
            out.push('▲');
            out.push_str(ANSI_RESET);
        }
        if chan_scroll < max_chan_scroll {
            out.push_str(&move_to(msg_area_bot, CHAN_LIST_WIDTH));
            out.push_str(ANSI_YELLOW);
            out.push('▼');
            out.push_str(ANSI_RESET);
        }
    }

    // --- Messages ---
    let nick_panel_w = if p.show_nicks { NICK_LIST_WIDTH + 1 } else { 0 };
    let msg_w = msg_width(w, p.show_nicks);

    let lines = p.lines;
    let total_lines = lines.len() as i32;
    let scroll_off = p.scroll_off.max(0);
    let start_line = (total_lines - msg_rows - scroll_off).max(0);

    let msg_col_start = CHAN_LIST_WIDTH + 2;
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
            " ↑ SCROLL (+{} eilučių žemiau) ─ PgDn/↓ žemyn, Shift+PgDn apačion ",
            scroll_off
        );
        let banner: String = if banner.chars().count() > msg_w as usize {
            banner.chars().take(msg_w as usize).collect()
        } else {
            banner
        };
        out.push_str(&move_to(msg_area_top, msg_col_start));
        out.push_str(ANSI_YELLOW);
        out.push_str(ANSI_BOLD);
        out.push_str(&banner);
        out.push_str(ANSI_RESET);
    }

    // --- Nick list ---
    if p.show_nicks {
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
                let mut hint = String::new();
                if n_nicks > visible_nicks {
                    let shown = (nick_scroll_off + visible_nicks).min(n_nicks);
                    hint = format!("({}/{})", shown, n_nicks);
                }
                out.push_str(ANSI_BOLD);
                out.push_str(ANSI_CYAN);
                out.push_str(&fit_width(&format!(" Nick{}", hint), NICK_LIST_WIDTH));
                out.push_str(ANSI_RESET);
            } else {
                let ni = nick_scroll_off + i - 1;
                if ni < n_nicks {
                    let n = sanitize_text(&nicks[ni as usize]);
                    let display = truncate_width(&n, NICK_LIST_WIDTH);
                    let color = match mode_prefix(&n, p.prefix_signs) {
                        "~" | "&" | "@" => ANSI_YELLOW,
                        "%" => ANSI_GREEN,
                        "+" => ANSI_CYAN,
                        _ => ANSI_WHITE,
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
                out.push_str(ANSI_YELLOW);
                out.push('▲');
                out.push_str(ANSI_RESET);
            }
            if nick_scroll_off < max_scroll {
                out.push_str(&move_to(msg_area_bot, nick_col + NICK_LIST_WIDTH - 1));
                out.push_str(ANSI_YELLOW);
                out.push('▼');
                out.push_str(ANSI_RESET);
            }
        }
    }

    // --- Separator ---
    out.push_str(&move_to(h - 1, 1));
    out.push_str(ANSI_DARK_GRAY);
    out.push_str(&"─".repeat(w as usize));
    out.push_str(ANSI_RESET);

    // --- Input line ---
    out.push_str(&move_to(h, 1));
    out.push_str(&erase(w));
    out.push_str(&move_to(h, 1));
    if !p.away.is_empty() {
        out.push_str(ANSI_YELLOW);
        out.push_str("[AWAY] ");
        out.push_str(ANSI_RESET);
    }
    out.push_str(ANSI_GREEN);
    out.push_str("> ");
    out.push_str(ANSI_RESET);
    let mut prefix_len = 2;
    if !p.away.is_empty() {
        prefix_len += 7;
    }
    // SANITISE THE INPUT LINE — the only place where unsanitised text
    // reaches the terminal. Server data (a nick prefix that arrived via
    // JOIN/NAMES) may carry an ESC/OSC sequence: without this filter "OSC 52"
    // (clipboard write) or "OSC 0" (window title) would reach the terminal
    // without the user's knowledge.
    let input_str: String = p.input.iter().collect();
    let safe_input = sanitize_text(&input_str);
    let disp_runes: Vec<char> = safe_input.chars().collect();
    let max_input = (w - prefix_len - 1).max(0) as usize;
    let disp_start = if disp_runes.len() > max_input {
        disp_runes.len() - max_input
    } else {
        0
    };
    out.extend(disp_runes[disp_start..].iter());

    // Cursor position — over the SANITISED text (sanitising removes
    // characters, so the raw index no longer matches): count how many
    // characters remain before input_pos.
    let upto = p.input_pos.min(p.input.len());
    let prefix_str: String = p.input[..upto].iter().collect();
    let cur_pos = sanitize_text(&prefix_str).chars().count() as i32;
    let cursor_col = (prefix_len + 1 + (cur_pos - disp_start as i32)).clamp(1, w);
    out.push_str(&move_to(h, cursor_col));
    out.push_str(ANSI_SHOW_CUR);

    Some(Frame {
        output: out,
        msg_area_top,
    })
}

#[cfg(test)]
#[path = "draw_tests.rs"]
mod tests;
