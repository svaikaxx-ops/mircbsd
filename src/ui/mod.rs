//! Terminal UI layer ported from `ui.go`.
//!
//! * [`theme`]   — ANSI codes, colours, theme/timestamp configuration (globals)
//! * [`message`] — `Message`, mIRC formatting, wrapping, URL highlighting, JSON
//! * [`channel`] — `Channel` state and nick-list management
//! * [`draw`]    — layout helpers and the frame builder

pub mod channel;
pub mod draw;
pub mod message;
pub mod theme;

pub use channel::{
    mode_prefix, mode_prefix_default, nick_less, sort_nicks, strip_mode, strip_mode_default,
    Channel, DEFAULT_MAX_MESSAGES, DEFAULT_PREFIX_MODES, DEFAULT_PREFIX_SIGNS,
    MAX_NICKS_PER_CHANNEL,
};
pub use draw::{
    apply_history_freeze, clamp_status_left, draw_frame, msg_area_top, msg_width,
    update_render_cache, ChanState, Frame, FrameParams, CHAN_LIST_WIDTH, NICK_LIST_WIDTH,
    STATUS_RIGHT,
};
pub use message::{
    changed_day, day_sep_line, find_chunk, format_go_time, highlight_urls, messages_to_json_indent,
    plain_to_original_bounds, render_irc_format, strip_irc_format, wrap_text, Message, MsgType,
};
pub use theme::{
    hl_bg_code, hl_color_name, invalidate_render_caches, mirc_colors, move_to, nick_color,
    nick_coloring, set_highlight_color, set_nick_colors, set_theme, set_timestamps, set_ts_color,
    theme_dark_mode, timestamps_enabled, ts_color_code, ts_color_name, ts_format,
};
