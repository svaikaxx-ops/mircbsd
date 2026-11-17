//! `Channel` and nick-list management ported from `ui.go`.

use std::collections::HashSet;

use crate::validate::{fold_equal_irc, norm_nick};

use super::message::Message;

/// Default ring size of a channel's message buffer (`defaultMaxMessages`).
pub const DEFAULT_MAX_MESSAGES: usize = 5000;

/// Standard IRC status prefix signs when the server sent no PREFIX
/// (`defaultPrefixSigns`).
pub const DEFAULT_PREFIX_SIGNS: &str = "~&@%+";
/// Standard IRC status prefix MODE letters (`defaultPrefixModes`).
pub const DEFAULT_PREFIX_MODES: &str = "qaohv";

/// Largest number of nicks in ONE channel (`maxNicksPerChannel`).
///
/// Without a limit a hostile server could send thousands of JOINs into one
/// channel, each with its own nick: the list would grow without bound and
/// every insertion is O(n) → O(n²) overall.
pub const MAX_NICKS_PER_CHANNEL: usize = 5000;

#[derive(Debug, Clone)]
pub struct Channel {
    pub name: String,
    pub messages: Vec<Message>,
    pub unread: i32,
    /// Index of the first UNREAD message (set when `unread` goes 0→1). On
    /// returning to the channel a "── new messages ──" marker is inserted
    /// before it (v0.4.7). `-1` = not recorded.
    pub unread_from: isize,
    pub highlight: i32,
    pub nicks: Vec<String>,
    pub topic: String,
    pub max_messages: usize,
    pub scroll_off: i32,
    /// Direct conversation (not a channel).
    pub is_private: bool,
    /// Number of rendered lines at the last draw (history freezing).
    pub draw_line_count: i32,
    /// Server nick comparison rule (ISUPPORT CASEMAPPING).
    pub casemapping: String,
    /// Server PREFIX status signs (ISUPPORT).
    pub prefix: String,
    /// Server PREFIX mode letters (ISUPPORT).
    pub prefix_modes: String,

    /// Normalised nicks (no prefix, with casemapping) — fast "is this nick
    /// here?" check.
    ///
    /// WHY: without it every nick insertion was O(n) (strip_mode +
    /// nick_equal per element); 5 000 nicks × 5 000 = 25M operations is an
    /// O(n²) DoS.
    ///
    /// WHY NOT a map to indices: after each insertion ALL indices after the
    /// insertion point shift → rebuild is O(n) → O(n²) again. A set without
    /// indices is stable across insertions; it only needs updating when a
    /// nick is REMOVED or RENAMED (rare).
    ///
    /// INVARIANT: only valid under the client lock. Every `nicks` change
    /// (add_nick, remove_nick, rename_nick, change_nick_mode, `/names` →
    /// `clear_nicks`) keeps it in sync. An empty set with non-empty `nicks`
    /// means "not built yet" and is rebuilt lazily.
    pub nick_set: HashSet<String>,

    // Render cache: lines depend on messages, width and own nick.
    // `render_gen` is bumped on every message change (add_msg, clear).
    //
    // `rendered_msg_count` — how many messages are already RENDERED in the
    // cache. A new message keeps the old lines valid and only appends the
    // new ones (incremental rendering). Previously every new message threw
    // away the WHOLE cache (5000 messages → 5000 render_lines per draw).
    pub render_gen: i64,
    /// `== render_gen` when the cache is valid; `-1` = invalid.
    pub rendered_gen: i64,
    pub rendered_w: i32,
    pub rendered_nick: String,
    pub rendered: Vec<String>,
    pub rendered_msg_count: usize,
}

impl Channel {
    /// `newChannel`: `max_msg == 0` means the default.
    pub fn new(name: impl Into<String>, max_msg: usize) -> Self {
        let max_msg = if max_msg == 0 {
            DEFAULT_MAX_MESSAGES
        } else {
            max_msg
        };
        Channel {
            name: name.into(),
            messages: Vec::new(),
            unread: 0,
            unread_from: -1,
            highlight: 0,
            nicks: Vec::new(),
            topic: String::new(),
            max_messages: max_msg,
            scroll_off: 0,
            is_private: false,
            draw_line_count: 0,
            casemapping: String::new(),
            prefix: String::new(),
            prefix_modes: String::new(),
            nick_set: HashSet::new(),
            render_gen: 0,
            rendered_gen: -1,
            rendered_w: 0,
            rendered_nick: String::new(),
            rendered: Vec::new(),
            rendered_msg_count: 0,
        }
    }

    /// Appends a message and trims the buffer (`addMsg`).
    pub fn add_msg(&mut self, m: Message) {
        self.messages.push(m);
        self.render_gen += 1;
        // Limit the number of messages.
        if self.messages.len() > self.max_messages {
            // Drop the oldest quarter.
            // Bent viena: su maxmessages < 4 ketvirtis = 0 ir buferis augtų be ribų.
            let trim = (self.max_messages / 4)
                .max(self.messages.len() - self.max_messages)
                .max(1)
                .min(self.messages.len());
            self.messages.drain(..trim);
            // Neperskaitytų pradžios indeksas slenka kartu su žinutėmis,
            // kitaip „naujos žinutės“ žyma atsidurtų per toli (arba dingtų).
            if self.unread_from >= 0 {
                self.unread_from = (self.unread_from - trim as isize).max(0);
            }
            self.render_gen += 1;
            // Cached lines no longer match the messages — rebuild from
            // scratch (an incremental cache would show deleted messages).
            self.invalidate_render_cache();
        }
    }

    /// Drops all messages and the render cache (`/clear`).
    pub fn clear_messages(&mut self) {
        self.messages.clear();
        self.scroll_off = 0;
        self.render_gen += 1;
        // The cache must be DELETED, not just marked: stale lines would
        // make the incremental cache expect messages that no longer exist.
        self.invalidate_render_cache();
    }

    /// Marks the render cache invalid (`rendered = nil`, `renderedGen = -1`,
    /// `renderedMsgCount = 0`).
    pub fn invalidate_render_cache(&mut self) {
        self.rendered.clear();
        self.rendered_msg_count = 0;
        self.rendered_gen = -1;
    }

    /// Status signs for this channel: explicit `opt`, else the server's
    /// PREFIX, else the default (`prefixSigns`).
    pub fn prefix_signs(&self, opt: Option<&str>) -> String {
        if let Some(o) = opt {
            return o.to_string();
        }
        if !self.prefix.is_empty() {
            return self.prefix.clone();
        }
        DEFAULT_PREFIX_SIGNS.to_string()
    }

    /// Mode letters for this channel (server PREFIX modes or default).
    pub fn prefix_modes_or_default(&self) -> &str {
        if self.prefix_modes.is_empty() {
            DEFAULT_PREFIX_MODES
        } else {
            &self.prefix_modes
        }
    }

    // ---- nick set ----

    /// Rebuilds the set from `nicks` (`rebuildNickSet`). Needed after
    /// operations that REMOVE or RENAME a nick.
    pub fn rebuild_nick_set(&mut self, sign: &str) {
        let mut set = HashSet::with_capacity(self.nicks.len());
        for n in &self.nicks {
            set.insert(norm_nick(n, sign, &self.casemapping));
        }
        self.nick_set = set;
    }

    /// Set must exist: an empty set with non-empty `nicks` is "not built".
    fn ensure_nick_set(&mut self, sign: &str) {
        if self.nick_set.is_empty() && !self.nicks.is_empty() {
            self.rebuild_nick_set(sign);
        }
    }

    /// Is this nick in the channel (O(1))? (`hasNickKey`)
    pub fn has_nick_key(&mut self, nick: &str, sign: &str) -> bool {
        self.ensure_nick_set(sign);
        self.nick_set
            .contains(&norm_nick(nick, sign, &self.casemapping))
    }

    /// Index of a nick in the list, `None` when absent (`findNickIdx`).
    ///
    /// The set is a fast "exists?" check; the index is found by direct scan
    /// (only needed for rare operations). The scan uses `fold_equal_irc` —
    /// no per-element allocations (a flood of removals would otherwise stall
    /// the client on a 5 000-nick list).
    pub fn find_nick_idx(&self, nick: &str, sign: &str) -> Option<usize> {
        // Read-only: when the set was not built yet fall straight to the scan.
        if !(self.nick_set.is_empty() && !self.nicks.is_empty())
            && !self
                .nick_set
                .contains(&norm_nick(nick, sign, &self.casemapping))
        {
            return None;
        }
        let target = strip_mode(nick, sign);
        let ascii = self.casemapping == "ascii";
        self.nicks
            .iter()
            .position(|n| fold_equal_irc(strip_mode(n, sign), target, ascii))
    }

    /// Adds a nick (`addNick`). `sign` — server PREFIX signs (from ISUPPORT)
    /// so sorting and alignment stay consistent; `None` = the channel's.
    pub fn add_nick(&mut self, nick: &str, sign: Option<&str>) {
        let sign = self.prefix_signs(sign);
        self.ensure_nick_set(&sign);
        let key = norm_nick(nick, &sign, &self.casemapping);
        if self.nick_set.contains(&key) {
            // The nick is already listed. But 353 (NAMES) can bring a
            // STATUS prefix that was missing when it was added (JOIN added
            // "nick", 353 brought "@nick"). Update only when the NEW nick
            // has a prefix — otherwise an existing prefix (from MODE) stays.
            let Some(i) = self.find_nick_idx(nick, &sign) else {
                return;
            };
            let new_prefix = mode_prefix(nick, &sign);
            let updated = {
                let n = &self.nicks[i];
                if new_prefix.is_empty() || new_prefix == mode_prefix(n, &sign) {
                    return;
                }
                format!("{}{}", new_prefix, strip_mode(n, &sign))
            };
            self.nicks[i] = updated;
            sort_nicks(&mut self.nicks, Some(&sign));
            // Key (no prefix) is the same — the set needs no update.
            return;
        }
        // LIMIT — a channel cannot hold unbounded nicks.
        if self.nicks.len() >= MAX_NICKS_PER_CHANNEL {
            return;
        }
        // Insert at its PLACE (binary search) — a full sort per insertion
        // was O(n log n).
        let pos = self.nicks.partition_point(|n| nick_less(n, nick, &sign));
        self.nicks.insert(pos, nick.to_string());
        // The set is updated in O(1) — indices are not stored.
        self.nick_set.insert(key);
    }

    /// Is this nick in the channel list (with or without prefix)? Used by
    /// NICK so the notice appears only where the nick really was.
    pub fn has_nick(&self, nick: &str) -> bool {
        self.find_nick_idx(nick, &self.prefix_signs(None)).is_some()
    }

    /// Removes a nick (`removeNick`).
    pub fn remove_nick(&mut self, nick: &str, sign: Option<&str>) {
        let sign = self.prefix_signs(sign);
        self.ensure_nick_set(&sign);
        let Some(i) = self.find_nick_idx(nick, &sign) else {
            return;
        };
        self.nicks.remove(i);
        // Set must follow.
        let key = norm_nick(nick, &sign, &self.casemapping);
        self.nick_set.remove(&key);
    }

    /// Renames a nick keeping its status prefix (`renameNick`).
    pub fn rename_nick(&mut self, old_nick: &str, new_nick: &str, sign: Option<&str>) {
        let sign = self.prefix_signs(sign);
        self.ensure_nick_set(&sign);
        let Some(i) = self.find_nick_idx(old_nick, &sign) else {
            return;
        };
        let old_key = norm_nick(&self.nicks[i], &sign, &self.casemapping);
        self.nicks[i] = format!("{}{}", mode_prefix(&self.nicks[i], &sign), new_nick);
        sort_nicks(&mut self.nicks, Some(&sign));
        // The key (nick name) changed — update the set.
        self.nick_set.remove(&old_key);
        self.nick_set
            .insert(norm_nick(new_nick, &sign, &self.casemapping));
    }

    /// Applies a MODE string such as `+o`, `-v`, `+ov` to a nick
    /// (`setMode`). `signs`/`modes` default to the channel's PREFIX
    /// (Go variadic: 1 arg = signs only, 2 = signs and modes).
    pub fn set_mode(&mut self, nick: &str, mode: &str, signs: Option<&str>, modes: Option<&str>) {
        let signs = self.prefix_signs(signs);
        let modes = match modes {
            Some(m) => m.to_string(),
            None => self.prefix_modes_or_default().to_string(),
        };
        let mut adding = true;
        for mr in mode.chars() {
            match mr {
                '+' => adding = true,
                '-' => adding = false,
                _ => {
                    // Mode letter → status sign by the server's PREFIX.
                    if let Some(i) = modes.chars().position(|c| c == mr) {
                        if let Some(prefix) = signs.chars().nth(i) {
                            self.change_nick_mode(nick, prefix, adding, &signs);
                        }
                    }
                }
            }
        }
    }

    /// Sets or clears one status sign on a nick, then re-sorts
    /// (`changeNickMode`). Like Go, adding REPLACES the existing prefix.
    pub fn change_nick_mode(&mut self, nick: &str, prefix: char, adding: bool, signs: &str) {
        let Some(i) = self.find_nick_idx(nick, signs) else {
            return;
        };
        let bare = strip_mode(&self.nicks[i], signs).to_string();
        self.nicks[i] = if adding {
            format!("{}{}", prefix, bare)
        } else {
            bare
        };
        sort_nicks(&mut self.nicks, Some(signs));
        // The key (no prefix) is unchanged — the set needs no update.
    }

    /// Drops the whole nick list and its set (`/names` refresh: `nicks = nil`,
    /// `nickSet = nil`). They must be cleared TOGETHER, or stale entries
    /// would make `add_nick`/`remove_nick` disagree with the real nicks.
    pub fn clear_nicks(&mut self) {
        self.nicks.clear();
        self.nick_set.clear();
    }
}

// ---- Free nick helpers ----

/// Removes a leading status sign (`stripMode`). `signs` is the server PREFIX
/// signs; unlike [`crate::validate::strip_mode`] an empty `signs` strips
/// nothing (Go semantics with an explicit argument). Use
/// [`strip_mode_default`] for the standard `~&@%+`.
pub fn strip_mode<'a>(nick: &'a str, signs: &str) -> &'a str {
    match nick.chars().next() {
        Some(c) if signs.contains(c) => &nick[c.len_utf8()..],
        _ => nick,
    }
}

/// [`strip_mode`] with the standard signs.
pub fn strip_mode_default(nick: &str) -> &str {
    strip_mode(nick, DEFAULT_PREFIX_SIGNS)
}

/// The nick's status sign, or `""` (`modePrefix`).
pub fn mode_prefix<'a>(nick: &'a str, signs: &str) -> &'a str {
    match nick.chars().next() {
        Some(c) if signs.contains(c) => &nick[..c.len_utf8()],
        _ => "",
    }
}

/// [`mode_prefix`] with the standard signs.
pub fn mode_prefix_default(nick: &str) -> &str {
    mode_prefix(nick, DEFAULT_PREFIX_SIGNS)
}

/// Sort rank of a nick: index of its sign in `signs` (higher status first),
/// `signs.len()` for no/unknown sign (`order`).
fn nick_order(n: &str, signs: &str) -> usize {
    match n.chars().next() {
        None => signs.len(),
        Some(c) => signs.find(c).unwrap_or(signs.len()),
    }
}

/// Sorts a nick list by status hierarchy, then case-insensitive name
/// (`sortNicks`). `signs` is taken ONCE from the caller so every nick is
/// ordered by the same PREFIX; `None` = the standard signs.
pub fn sort_nicks(nicks: &mut [String], signs: Option<&str>) {
    let signs = signs.unwrap_or(DEFAULT_PREFIX_SIGNS);
    nicks.sort_by_cached_key(|n| (nick_order(n, signs), strip_mode(n, signs).to_lowercase()));
}

/// The sort rule as a predicate so `add_nick` can insert in place
/// (`nickLess`).
pub fn nick_less(a: &str, b: &str, signs: &str) -> bool {
    let (oi, oj) = (nick_order(a, signs), nick_order(b, signs));
    if oi != oj {
        return oi < oj;
    }
    strip_mode(a, signs).to_lowercase() < strip_mode(b, signs).to_lowercase()
}

#[cfg(test)]
#[path = "channel_tests.rs"]
mod tests;
