//! Terminal frame (`Client::draw`).
//!
//! Locks just long enough to refresh the render cache and copy a
//! [`FrameParams`] snapshot, then writes [`draw_frame`]'s output in one go.

use std::io::{self, Write};
use std::time::Instant;

use crate::ui::draw::{
    apply_history_freeze, draw_frame, msg_width, too_small_frame, update_render_cache, ChanState,
    FrameParams, MIN_HEIGHT, MIN_WIDTH,
};

use super::types::Client;

/// Owned copy of everything [`draw_frame`] needs. Taken under `Client::mu`
/// so the frame can be built and written without holding the lock.
struct DrawSnap {
    width: i32,
    height: i32,
    channels: Vec<ChanState>,
    active_idx: usize,
    active_name: String,
    topic: String,
    nicks: Vec<String>,
    prefix_signs: String,
    show_nicks: bool,
    lines: Vec<String>,
    input: Vec<char>,
    input_pos: usize,
    scroll_off: i32,
    nick_scroll_off: i32,
    chan_scroll_off: i32,
    connected: bool,
    connecting: bool,
    my_nick: String,
    server: String,
    away: String,
    use_tls: bool,
    search_mode: bool,
    search_query: String,
}

impl Client {
    /// Paint one frame (`draw`). A terminal smaller than 40×8 is skipped.
    pub fn draw(&self) {
        let Some(snap) = self.snapshot_frame() else {
            // Too small to lay out: say so instead of leaving a stale,
            // half-overwritten frame on screen.
            let (w, h) = {
                let st = self.mu.lock();
                (st.width, st.height)
            };
            if w < MIN_WIDTH || h < MIN_HEIGHT {
                if let Some(msg) = too_small_frame(w, h) {
                    let mut out = io::stdout().lock();
                    let _ = out.write_all(msg.as_bytes());
                    let _ = out.flush();
                }
            }
            return;
        };
        let params = FrameParams {
            width: snap.width,
            height: snap.height,
            channels: &snap.channels,
            active_idx: snap.active_idx,
            active_name: &snap.active_name,
            topic: &snap.topic,
            nicks: &snap.nicks,
            prefix_signs: &snap.prefix_signs,
            show_nicks: snap.show_nicks,
            lines: &snap.lines,
            input: &snap.input,
            input_pos: snap.input_pos,
            scroll_off: snap.scroll_off,
            nick_scroll_off: snap.nick_scroll_off,
            chan_scroll_off: snap.chan_scroll_off,
            connected: snap.connected,
            connecting: snap.connecting,
            my_nick: &snap.my_nick,
            server: &snap.server,
            away: &snap.away,
            use_tls: snap.use_tls,
            search_mode: snap.search_mode,
            search_query: &snap.search_query,
        };
        let Some(frame) = draw_frame(&params) else {
            return;
        };
        let mut out = io::stdout().lock();
        let _ = out.write_all(frame.output.as_bytes());
        let _ = out.flush();
        drop(out);
        self.mu.lock().msg_area_top = frame.msg_area_top;
    }

    /// Refresh the active channel's cache and copy the frame inputs.
    fn snapshot_frame(&self) -> Option<DrawSnap> {
        let mut st = self.mu.lock();
        if st.width < MIN_WIDTH || st.height < MIN_HEIGHT {
            return None;
        }
        if st.active_idx >= st.channels.len() {
            return None;
        }
        // Keep the stored sidebar offset inside the rows `draw` will show,
        // so a mouse click after a resize hits the channel that is on screen.
        st.clamp_chan_scroll();

        let active_idx = st.active_idx;
        let active_name = st.channels[active_idx].name.clone();
        // `mu` → `isup` is the allowed lock order. `is_channel` takes `&self`
        // and would overlap the `mu` borrow, so read `isup` directly.
        let show_nicks = self.isup.read().is_channel(&active_name);
        let my_nick = st.nick.clone();
        let msg_w = msg_width(st.width, show_nicks);
        update_render_cache(&mut st.channels[active_idx], msg_w, &my_nick);

        let was_drawn = st.last_drawn.as_deref() == Some(active_name.as_str());
        let chase = st.chase_until.is_some_and(|t| Instant::now() < t);
        let total = st.channels[active_idx].rendered.len() as i32;
        let prev_scroll = st.scroll_off;
        let scroll_off = apply_history_freeze(
            &mut st.channels[active_idx],
            was_drawn,
            total,
            prev_scroll,
            chase,
        );
        st.scroll_off = scroll_off;
        st.last_drawn = Some(active_name.clone());

        let ch = &st.channels[active_idx];
        Some(DrawSnap {
            width: st.width,
            height: st.height,
            channels: st.channels.iter().map(ChanState::from_channel).collect(),
            active_idx,
            active_name,
            topic: ch.topic.clone(),
            nicks: ch.nicks.clone(),
            prefix_signs: ch.prefix_signs(None),
            show_nicks,
            lines: ch.rendered.clone(),
            input: st.input.clone(),
            input_pos: st.input_pos,
            scroll_off,
            nick_scroll_off: st.nick_scroll_off,
            chan_scroll_off: st.chan_scroll_off,
            connected: st.connected,
            connecting: st.connecting_gen != -1,
            my_nick,
            server: st.server.clone(),
            away: st.away.clone(),
            use_tls: st.use_tls,
            search_mode: st.search_mode,
            search_query: st.search_query.clone(),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::default_config;

    #[test]
    fn draw_skips_a_tiny_terminal() {
        let c = Client::new(&default_config());
        c.draw();
        let st = c.mu.lock();
        assert_eq!(st.msg_area_top, 0);
        assert!(st.last_drawn.is_none());
    }

    #[test]
    fn draw_records_message_area_and_active_window() {
        let c = Client::new(&default_config());
        {
            let mut st = c.mu.lock();
            st.width = 80;
            st.height = 24;
        }
        c.draw();
        let st = c.mu.lock();
        assert_eq!(st.msg_area_top, 2);
        assert_eq!(st.last_drawn.as_deref(), Some("*server*"));
    }
}
