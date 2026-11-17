//! Channel/window management: lookup, creation limits, active window,
//! message delivery, ignore list.
//!
//! Methods taking `&mut State`/`&State` correspond to the Go functions that
//! require `c.mu` to be held ("TIK SU c.mu užrakintu"); the caller passes the
//! lock guard's contents.

use std::io::Write;
use std::time::{Duration, Instant};

use chrono::Utc;

use crate::sanitize::{sanitize_chan_name, sanitize_text};
use crate::validate::wildcard_match;

use crate::ui::strip_irc_format;

use super::types::{new_msg, Channel, Client, LogEntry, Message, MsgType, State, SERVER_WINDOW};
use super::{MAX_CHANNELS, MAX_PRIVATE_CHANNELS};

thread_local! {
    /// Set while a line typed by the user is being handled (see
    /// [`with_cmd_feedback`]). Thread-local: network threads and threads a
    /// command spawns (e.g. `/connect`) keep reporting to `*server*`.
    static CMD_FEEDBACK: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
}

/// Runs `f` with command feedback on: [`Client::err_msg`] calls made by it
/// on this thread go to the active window instead of `*server*`.
pub fn with_cmd_feedback<R>(f: impl FnOnce() -> R) -> R {
    struct Reset(bool);
    impl Drop for Reset {
        fn drop(&mut self) {
            CMD_FEEDBACK.with(|c| c.set(self.0));
        }
    }
    let _reset = Reset(CMD_FEEDBACK.with(|c| c.replace(true)));
    f()
}

impl State {
    /// Index of the channel named `name` (compared with the sanitized name,
    /// case-insensitively) — `findCh`.
    pub fn find_ch(&self, name: &str) -> Option<usize> {
        let safe = sanitize_text(name).to_lowercase();
        self.channels
            .iter()
            .position(|ch| ch.name.to_lowercase() == safe)
    }

    /// Active channel, if the index is valid (`activeChanLocked`).
    pub fn active_chan(&self) -> Option<&Channel> {
        self.channels.get(self.active_idx)
    }

    pub fn active_chan_mut(&mut self) -> Option<&mut Channel> {
        let i = self.active_idx;
        self.channels.get_mut(i)
    }

    /// Keep `active_idx` valid after the channel list changed
    /// (`normalizeActiveIdx`). `removed_idx` is the index that was removed.
    pub fn normalize_active_idx(&mut self, removed_idx: Option<usize>) {
        let len = self.channels.len();
        if let Some(r) = removed_idx {
            if r == self.active_idx {
                self.active_idx = if r < len { r } else { len.saturating_sub(1) };
                return;
            }
            if r < self.active_idx {
                self.active_idx -= 1;
            }
        }
        if self.active_idx >= len {
            self.active_idx = len.saturating_sub(1);
        }
    }

    /// Remove channel `idx`, always keeping at least one window (`removeCh`).
    pub fn remove_ch(&mut self, idx: usize) -> bool {
        if idx >= self.channels.len() || self.channels.len() <= 1 {
            return false;
        }
        let was_active = idx == self.active_idx;
        self.channels.remove(idx);
        self.normalize_active_idx(Some(idx));
        if was_active {
            // Naujai aktyvus langas — jo paties slinkimo pozicija, ne uždaryto.
            self.scroll_off = self.channels[self.active_idx].scroll_off;
        }
        true
    }

    /// Rows available for the channel list (`visibleChannelRowsLocked`).
    pub fn visible_channel_rows(&self) -> i32 {
        let mut top = 2;
        if self.active_chan().is_some_and(|c| !c.topic.is_empty()) {
            top += 1;
        }
        if self.search_mode {
            top += 1;
        }
        let rows = self.height - 1 - top;
        rows.max(1)
    }

    /// Clamp `chan_scroll_off` into `[0, len - rows]` (`clampChanScrollLocked`).
    pub fn clamp_chan_scroll(&mut self) {
        let max = (self.channels.len() as i32 - self.visible_channel_rows()).max(0);
        self.chan_scroll_off = self.chan_scroll_off.clamp(0, max);
    }
}

impl Client {
    /// Real channels and private windows currently open (`*server*` excluded).
    pub fn count_channels(&self, st: &State) -> (usize, usize) {
        let isup = self.isup.read();
        let (mut real, mut private) = (0, 0);
        for ch in &st.channels {
            if ch.name == SERVER_WINDOW {
                continue;
            }
            if isup.is_channel(&ch.name) {
                real += 1;
            } else {
                private += 1;
            }
        }
        (real, private)
    }

    /// Find or create a window (`ensureCh`). Returns its index, or `None`
    /// for empty/unsafe names and when the channel/PM budget is exhausted
    /// (then `chan_limit_hit` records the refused name).
    pub fn ensure_ch(&self, st: &mut State, name: &str) -> Option<usize> {
        if name.is_empty() {
            return None;
        }
        if let Some(i) = st.find_ch(name) {
            return Some(i);
        }
        // Sanitize BEFORE creating: no control chars or spaces (command-target forgery).
        let safe = sanitize_chan_name(name);
        if safe.is_empty() {
            return None;
        }
        let (is_real, signs, modes, cm) = {
            let isup = self.isup.read();
            (
                isup.is_channel(&safe),
                isup.prefix_signs.clone(),
                isup.prefix_modes.clone(),
                isup.casemapping.clone(),
            )
        };
        let (real, private) = self.count_channels(st);
        if is_real {
            if real >= MAX_CHANNELS {
                st.chan_limit_hit = safe;
                return None;
            }
        } else if private >= MAX_PRIVATE_CHANNELS {
            // Private conversations have their own budget.
            st.chan_limit_hit = safe;
            return None;
        }
        let mut ch = Channel::new(&safe, st.max_messages.max(0) as usize);
        ch.prefix = signs;
        ch.prefix_modes = modes;
        ch.casemapping = cm;
        st.channels.push(ch);
        Some(st.channels.len() - 1)
    }

    /// `find_ch` with locking.
    pub fn find_ch(&self, name: &str) -> Option<usize> {
        self.mu.lock().find_ch(name)
    }

    /// `remove_ch` with locking.
    pub fn remove_ch(&self, idx: usize) -> bool {
        self.mu.lock().remove_ch(idx)
    }

    /// Name of the active window ("" if none) — `activeName`.
    pub fn active_name(&self) -> String {
        self.mu
            .lock()
            .active_chan()
            .map(|c| c.name.clone())
            .unwrap_or_default()
    }

    /// Run `f` on the active channel under the lock (`activeChanSafe`).
    pub fn with_active_chan<R>(&self, f: impl FnOnce(&Channel) -> R) -> Option<R> {
        let st = self.mu.lock();
        st.active_chan().map(f)
    }

    /// Switch to window `idx` (`switchCh`): stores/restores scroll position,
    /// inserts the "new messages" separator and clears unread counters.
    pub fn switch_ch(&self, idx: usize) {
        {
            let mut st = self.mu.lock();
            if idx < st.channels.len() {
                let scroll = st.scroll_off;
                if let Some(cur) = st.active_chan_mut() {
                    cur.scroll_off = scroll;
                }
                st.active_idx = idx;
                st.scroll_off = st.channels[idx].scroll_off;
                State::insert_unread_separator(&mut st.channels[idx]);
                let target = &mut st.channels[idx];
                target.unread = 0;
                target.highlight = 0;
                target.unread_from = -1;
                st.nick_scroll_off = 0;
                st.search_mode = false;
                st.search_result_idx = -1;
                st.last_search_query.clear();
                // Keep the active channel visible in the sidebar.
                let rows = st.visible_channel_rows();
                let i = idx as i32;
                if i < st.chan_scroll_off || i >= st.chan_scroll_off + rows {
                    st.chan_scroll_off = i;
                }
                st.clamp_chan_scroll();
            }
        }
        self.queue_redraw();
    }

    /// Does `prefix` (`nick!user@host`) match the ignore list (`isIgnored`)?
    /// Plain entries are nicks (casemapped); `*`/`?` entries are masks.
    pub fn is_ignored(&self, prefix: &str) -> bool {
        let list: Vec<String> = self.mu.lock().ignore_list.clone();
        if list.is_empty() {
            return false;
        }
        let nick = match prefix.find('!') {
            Some(i) => &prefix[..i],
            None => prefix,
        };
        for ig in &list {
            if ig.is_empty() {
                continue;
            }
            if !ig.contains(['*', '?']) {
                if self.nick_equal(ig, nick) {
                    return true;
                }
                continue;
            }
            if wildcard_match(ig, prefix) || wildcard_match(ig, nick) {
                return true;
            }
        }
        false
    }

    /// Deliver a message to window `chan_name`, creating it if allowed
    /// (`addMsg`). Handles unread counters, bell, log queue and notification.
    pub fn add_msg(&self, chan_name: &str, m: Message) {
        let mut st = self.mu.lock();
        let Some(ci) = self.ensure_ch(&mut st, chan_name) else {
            // Dropped. Warn only when the remembered refusal is THIS channel.
            let mut limit_hit = String::new();
            if !st.chan_limit_hit.is_empty() && st.chan_limit_hit == sanitize_chan_name(chan_name) {
                limit_hit = std::mem::take(&mut st.chan_limit_hit);
            }
            drop(st);
            if !limit_hit.is_empty() {
                self.server_msg(&format!(
                    "ĮSPĖJIMAS: pasiektas kanalų limitas — žinutė NEPRISTATYTA ({})",
                    sanitize_text(&limit_hit)
                ));
            }
            return;
        };

        let is_active = st.active_idx == ci;
        let chan_name = st.channels[ci].name.clone();
        st.channels[ci].add_msg(m.clone());
        if !is_active {
            let ch = &mut st.channels[ci];
            if ch.unread == 0 {
                ch.unread_from = ch.messages.len() as isize - 1;
            }
            ch.unread += 1;
            if m.highlight {
                ch.highlight += 1;
            }
        }
        let log_dir = st.log_dir.clone();
        let server = st.server.clone();
        let want_notify = m.highlight && !is_active && st.notify;
        let mut can_notify = false;
        if want_notify {
            can_notify = st
                .last_notify
                .is_none_or(|t| t.elapsed() > Duration::from_secs(3));
            if can_notify {
                st.last_notify = Some(Instant::now());
            }
        }
        drop(st);

        if m.highlight {
            let mut out = std::io::stdout();
            let _ = out.write_all(b"\x07");
            let _ = out.flush();
        }
        self.enqueue_log(LogEntry {
            log_dir,
            server,
            chan_name: chan_name.clone(),
            msg: m.clone(),
        });
        if can_notify {
            let title = if self.is_channel(&chan_name) {
                chan_name.clone()
            } else {
                format!("PM {chan_name}")
            };
            let mut body = strip_irc_format(&m.text);
            if !m.nick.is_empty() {
                body = format!("<{}> {}", m.nick, body);
            }
            let hook = self.hooks.read().notify.clone();
            if let Some(h) = hook {
                std::thread::spawn(move || h(&title, &body));
            }
        }
        self.queue_redraw();
    }

    /// Informational line in the `*server*` window.
    pub fn server_msg(&self, text: &str) {
        self.add_msg(
            SERVER_WINDOW,
            new_msg(Utc::now(), "", text, MsgType::Server),
        );
    }

    /// Error line in the `*server*` window — or, while a command typed by
    /// the user is running ([`with_cmd_feedback`]), in the window the user is
    /// looking at: otherwise a typo in a channel ("Nežinoma komanda") only
    /// bumps the `*server*` unread counter and seems to do nothing.
    pub fn err_msg(&self, text: &str) {
        let m = new_msg(Utc::now(), "", text, MsgType::Error);
        if CMD_FEEDBACK.with(|f| f.get()) {
            if let Some(m) = self.show_in_active(m) {
                self.add_msg(SERVER_WINDOW, m);
            }
            return;
        }
        self.add_msg(SERVER_WINDOW, m);
    }

    /// Informational line for the user in the ACTIVE window (`/help`); in
    /// the `*server*` window it is an ordinary server line.
    pub fn active_info_msg(&self, text: &str) {
        let m = new_msg(Utc::now(), "", text, MsgType::Server);
        if let Some(m) = self.show_in_active(m) {
            self.add_msg(SERVER_WINDOW, m);
        }
    }

    /// Show a local (client-generated) line in the active window WITHOUT
    /// logging it — channel logs keep only the conversation. Returns the
    /// message back when the active window is `*server*` (or missing), so
    /// the caller can deliver it the normal way.
    fn show_in_active(&self, m: Message) -> Option<Message> {
        let mut st = self.mu.lock();
        let Some(ch) = st.active_chan_mut() else {
            return Some(m);
        };
        if ch.name == SERVER_WINDOW {
            return Some(m);
        }
        ch.add_msg(m);
        drop(st);
        self.queue_redraw();
        None
    }

    /// WHO reply: shown in the channel where it was requested (else `*server*`).
    pub fn who_reply_msg(&self, text: &str) {
        let target = self.mu.lock().who_chan.clone();
        self.reply_to(&target, text);
    }

    /// WHOIS reply (same routing as WHO).
    pub fn whois_reply_msg(&self, text: &str) {
        let target = self.mu.lock().whois_chan.clone();
        self.reply_to(&target, text);
    }

    fn reply_to(&self, target: &str, text: &str) {
        let target = if target.is_empty() || !self.is_channel(target) {
            SERVER_WINDOW
        } else {
            target
        };
        self.add_msg(target, new_msg(Utc::now(), "", text, MsgType::Server));
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::default_config;

    fn client() -> std::sync::Arc<Client> {
        let mut cfg = default_config();
        cfg.server = "irc.example.org".into();
        Client::new(&cfg)
    }

    #[test]
    fn ensure_ch_rejects_bad_names_and_dedups() {
        let c = client();
        let mut st = c.mu.lock();
        assert!(c.ensure_ch(&mut st, "").is_none());
        assert!(c.ensure_ch(&mut st, "#a b").is_none());
        let i = c.ensure_ch(&mut st, "#Rust").unwrap();
        assert_eq!(c.ensure_ch(&mut st, "#rust"), Some(i));
        assert_eq!(st.channels.len(), 2);
    }

    #[test]
    fn channel_budget_is_enforced_separately_for_pms() {
        let c = client();
        {
            let mut st = c.mu.lock();
            for i in 0..MAX_PRIVATE_CHANNELS {
                assert!(c.ensure_ch(&mut st, &format!("pm{i}")).is_some());
            }
            assert!(c.ensure_ch(&mut st, "onemore").is_none());
            assert_eq!(st.chan_limit_hit, "onemore");
            // real channels still have room
            assert!(c.ensure_ch(&mut st, "#real").is_some());
        }
    }

    #[test]
    fn remove_keeps_active_valid() {
        let c = client();
        let mut st = c.mu.lock();
        for n in ["#a", "#b", "#c"] {
            c.ensure_ch(&mut st, n);
        }
        st.active_idx = 3; // #c
        assert!(st.remove_ch(1)); // remove #a
        assert_eq!(st.channels[st.active_idx].name, "#c");
        let a = st.active_idx;
        assert!(st.remove_ch(a));
        assert!(st.active_idx < st.channels.len());
        while st.channels.len() > 1 {
            assert!(st.remove_ch(0));
        }
        assert!(!st.remove_ch(0));
    }

    #[test]
    fn add_msg_counts_unread_in_background() {
        let c = client();
        c.add_msg("#bg", new_msg(Utc::now(), "n", "hi", MsgType::Chat));
        let st = c.mu.lock();
        let i = st.find_ch("#bg").unwrap();
        assert_eq!(st.channels[i].unread, 1);
        assert_eq!(st.channels[i].unread_from, 0);
    }

    #[test]
    fn ignore_nick_and_mask() {
        let c = client();
        c.mu.lock().ignore_list = vec!["Bad".into(), "*!*@evil.host".into()];
        assert!(c.is_ignored("bad!u@h"));
        assert!(c.is_ignored("x!u@evil.host"));
        assert!(!c.is_ignored("good!u@h"));
    }

    #[test]
    fn switch_ch_resets_unread_state() {
        let c = client();
        c.add_msg("#bg", new_msg(Utc::now(), "n", "one", MsgType::Chat));
        c.add_msg("#bg", new_msg(Utc::now(), "n", "two", MsgType::Chat));
        c.mu.lock().height = 30;
        let idx = c.find_ch("#bg").unwrap();
        c.switch_ch(idx);
        let st = c.mu.lock();
        assert_eq!(st.active_idx, idx);
        assert_eq!(st.channels[idx].unread, 0);
        assert_eq!(st.channels[idx].unread_from, -1);
    }
}
