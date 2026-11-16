//! Dispatch of parsed server lines (`parseIRC` in `irc.go`).
//!
//! Locking reminder: `Client::mu` is not reentrant. Every handler takes the
//! lock for state changes, drops it, and only then calls `add_msg`,
//! `server_msg`, `err_msg` or `send` (which lock again).

use std::time::Duration;

use chrono::{DateTime, Local, TimeZone, Utc};

use crate::proto::caps::{
    authenticate_chunks, parse_cap_names, sasl_decide, sasl_plain_payload, wanted_caps, SaslInputs,
    SASL_EXTERNAL_WARN,
};
use crate::proto::isupport::parse_chan_modes;
use crate::proto::parse::{parse_line, IrcLine};
use crate::sanitize::{sanitize_chan_name, sanitize_text};
use crate::validate::{nick_equal, strip_mode};
use crate::width::{fit_width, pad_right};

use crate::ui::strip_irc_format;

use super::types::{new_msg, new_msg_hl, Client, MsgType, State, SERVER_WINDOW};
use super::{os_label, MAX_AWAY_NOTIFIED, MAX_CTCP_BYTES, VERSION};

const AWAY_THROTTLE_MINUTES: i64 = 10;

/// Does `text` mention `nick` as a whole word (case-insensitive, first occurrence)?
pub fn contains_nick(text: &str, nick: &str) -> bool {
    if nick.is_empty() {
        return false;
    }
    let text_low = text.to_lowercase();
    let nick_low = nick.to_lowercase();
    let Some(idx) = text_low.find(&nick_low) else {
        return false;
    };
    let tb = text_low.as_bytes();
    let before = idx == 0 || !is_nick_char(tb[idx - 1] as char);
    let end = idx + nick_low.len();
    let after = end >= tb.len() || !is_nick_char(tb[end] as char);
    before && after
}

/// RFC nick characters: letters, digits and ``_-[]\`^{}|``.
pub fn is_nick_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || "_-[]\\`^{}|".contains(c)
}

/// http/https/ftp URLs in `text` (formatting stripped, trailing punctuation trimmed).
pub fn extract_urls(text: &str) -> Vec<String> {
    strip_irc_format(text)
        .split_whitespace()
        .map(|w| w.trim_end_matches(['.', ',', '!', '?', ')']))
        .filter(|w| {
            w.starts_with("http://") || w.starts_with("https://") || w.starts_with("ftp://")
        })
        .map(str::to_string)
        .collect()
}

fn cut_utf8(s: &str, max: usize) -> &str {
    if s.len() <= max {
        return s;
    }
    let mut cut = max;
    while cut > 0 && !s.is_char_boundary(cut) {
        cut -= 1;
    }
    &s[..cut]
}

impl State {
    /// Bound `away_notified` (hostile servers can send AWAY from endless
    /// nicks): drop expired entries, then the oldest (`pruneAwayNotifiedLocked`).
    pub fn prune_away_notified(&mut self, now: DateTime<Utc>) {
        const SLACK: usize = MAX_AWAY_NOTIFIED / 4;
        if self.away_notified.len() <= MAX_AWAY_NOTIFIED + SLACK {
            return;
        }
        let ttl = chrono::Duration::minutes(AWAY_THROTTLE_MINUTES);
        self.away_notified
            .retain(|_, t| now.signed_duration_since(*t) < ttl);
        if self.away_notified.len() <= MAX_AWAY_NOTIFIED {
            return;
        }
        let mut entries: Vec<(String, DateTime<Utc>)> = self
            .away_notified
            .iter()
            .map(|(k, t)| (k.clone(), *t))
            .collect();
        entries.sort_by_key(|(_, t)| *t);
        let excess = entries.len() - MAX_AWAY_NOTIFIED;
        for (k, _) in entries.into_iter().take(excess) {
            self.away_notified.remove(&k);
        }
    }
}

impl Client {
    /// Parse and handle one raw server line.
    pub fn parse_irc(&self, line: &str) {
        let Some(msg) = parse_line(line) else { return };
        self.handle_line(&msg);
    }

    /// Run a `perform` entry: through the installed hook (which understands
    /// `/commands`), else raw IRC.
    pub fn run_perform(&self, line: &str) {
        let hook = self.hooks.read().perform.clone();
        match hook {
            Some(h) => h(line),
            None => {
                if line.starts_with('/') {
                    self.server_msg(&format!(
                        "perform: komanda nevykdyta (nėra apdorotojo): {}",
                        sanitize_text(line)
                    ));
                } else if !line.is_empty() {
                    self.send(line);
                }
            }
        }
    }

    /// Dispatch an already parsed line.
    pub fn handle_line(&self, msg: &IrcLine) {
        let now = msg.server_time().unwrap_or_else(Utc::now);
        let params = &msg.params;
        let from_nick = msg.from_nick();
        let prefix = msg.prefix.as_str();
        let p = |n: usize| msg.param(n);

        match msg.cmd.as_str() {
            "PING" => self.send(&format!("PONG :{}", p(0))),
            "PONG" => self.mu.lock().last_pong = std::time::Instant::now(),

            "001" => self.on_welcome(params),
            "002" | "003" | "004" | "372" | "375" | "376" => {
                if params.len() > 1 {
                    self.server_msg(&params[1..].join(" "));
                }
            }
            "005" => {
                // ISUPPORT — CHANTYPES, PREFIX, CASEMAPPING, ...
                if params.len() > 1 {
                    self.apply_isupport(&params[1..]);
                    self.server_msg(&params[1..].join(" "));
                }
            }

            "JOIN" => self.on_join(msg, now),
            "PART" => self.on_part(msg, now),
            "QUIT" => self.on_quit(msg, now),
            "KICK" => self.on_kick(msg, now),

            "ACCOUNT" => {
                // account-notify: :nick!u@h ACCOUNT <account|*>
                let acct = p(0);
                if acct.is_empty() {
                    return;
                }
                if acct == "*" {
                    self.server_msg(&format!(
                        "{} atsijungė nuo paskyros",
                        sanitize_text(from_nick)
                    ));
                } else {
                    self.server_msg(&format!(
                        "{} paskyra: {}",
                        sanitize_text(from_nick),
                        sanitize_text(acct)
                    ));
                }
            }
            "CHGHOST" => {
                // chghost: :nick!oldU@oldH CHGHOST <user> <host>
                let (u, h) = (p(0), p(1));
                if u.is_empty() || h.is_empty() {
                    return;
                }
                self.server_msg(&format!(
                    "{} dabar {}@{}",
                    sanitize_text(from_nick),
                    sanitize_text(u),
                    sanitize_text(h)
                ));
            }
            "INVITE" => {
                // :nick!user@host INVITE <target-nick> :#channel
                let (invited, chan) = (p(0), p(1));
                if chan.is_empty() {
                    return;
                }
                if !invited.is_empty() {
                    let me = self.my_nick();
                    if !self.nick_equal(invited, &me) {
                        return; // invite for somebody else (invite-notify)
                    }
                }
                self.server_msg(&format!(
                    "*** {from_nick} kviečia tave į {chan} (rašyk /join {chan})"
                ));
                self.queue_redraw();
            }

            "NICK" => self.on_nick(msg, now),
            "MODE" => self.on_mode(msg, now),
            "TOPIC" => {
                let (ch, new_topic) = (p(0), p(1));
                // Never create windows from TOPIC (find, not ensure).
                let found = {
                    let mut st = self.mu.lock();
                    match st.find_ch(ch) {
                        Some(i) => {
                            st.channels[i].topic = new_topic.to_string();
                            true
                        }
                        None => false,
                    }
                };
                if !found {
                    return;
                }
                self.add_msg(
                    ch,
                    new_msg(
                        now,
                        from_nick,
                        &format!("keičia temą į: {new_topic}"),
                        MsgType::Server,
                    ),
                );
                self.queue_redraw();
            }

            "PRIVMSG" => self.on_privmsg(msg, now),
            "NOTICE" => {
                if self.is_ignored(prefix) {
                    return;
                }
                self.add_msg(
                    SERVER_WINDOW,
                    new_msg(now, from_nick, p(1), MsgType::Notice),
                );
            }

            "332" => {
                let (ch, topic) = (p(1), p(2));
                if ch.is_empty() {
                    return;
                }
                let found = {
                    let mut st = self.mu.lock();
                    match st.find_ch(ch) {
                        Some(i) => {
                            st.channels[i].topic = topic.to_string();
                            true
                        }
                        None => false,
                    }
                };
                if !found {
                    return;
                }
                self.add_msg(
                    ch,
                    new_msg(now, "", &format!("Tema: {topic}"), MsgType::Server),
                );
                self.queue_redraw();
            }
            "353" => {
                // NAMES reply must never create windows (flood protection).
                let (ch, users) = (p(2), p(3));
                if ch.is_empty() {
                    return;
                }
                {
                    let mut st = self.mu.lock();
                    let Some(i) = st.find_ch(ch) else { return };
                    for u in users.split_whitespace() {
                        st.channels[i].add_nick(u, None);
                    }
                }
                self.queue_redraw();
            }
            "366" => {} // end of NAMES

            "321" => self.server_msg("── Kanalų sąrašas ──"),
            "322" => {
                let topic = strip_irc_format(p(3));
                self.server_msg(&format!(
                    "  {} {:>5}  {}",
                    pad_right(&sanitize_text(p(1)), 30),
                    p(2),
                    topic
                ));
            }
            "323" => self.server_msg("── Sąrašo pabaiga ──"),

            "315" => self.who_reply_msg(&format!("── WHO {}: pabaiga ──", p(1))),
            "352" => {
                // <client> <channel> <user> <host> <server> <nick> <flags> :<hop> <real>
                let (who_chan, who_nick, who_flags) = (p(1), p(5), p(6));
                if !who_nick.is_empty() {
                    self.who_reply_msg(&format!(
                        "│  {} {} {}",
                        fit_width(&sanitize_text(who_chan), 16),
                        sanitize_text(who_flags),
                        sanitize_text(who_nick)
                    ));
                }
            }
            "354" => {
                let (who_nick, who_flags, who_real) = (p(1), p(2), p(7));
                if !who_nick.is_empty() {
                    self.who_reply_msg(&format!("│  {who_flags} {who_nick} — {who_real}"));
                }
            }

            "367" => self.server_msg(&format!("  ban: {}", p(2))),
            "368" => self.server_msg("── Banų sąrašo pabaiga ──"),
            "369" => self.server_msg("── WHOWAS pabaiga ──"),

            "251" => self.server_msg(&format!("Serveris: {}", sanitize_text(p(1)))),
            "252" => self.server_msg(&format!("Operatorių: {}", p(1))),
            "253" => self.server_msg(&format!("Nežinomų serverių: {}", p(1))),
            "255" => self.server_msg(&format!("Kanalų: {}", p(1))),
            "265" => self.server_msg(&format!("Vietinių vartotojų: {}, maks. {}", p(1), p(2))),
            "266" => self.server_msg(&format!("Iš viso vartotojų: {}, maks. {}", p(1), p(2))),
            "391" => self.server_msg(&format!("Serverio laikas [{}]: {}", p(1), p(2))),

            "301" => self.whois_reply_msg(&format!("{} yra away: {}", p(1), p(2))),
            "311" => self.whois_reply_msg(&format!(
                "┌─[whois] {} ({}@{}) — {}",
                p(1),
                p(2),
                p(3),
                p(5)
            )),
            "312" => self.whois_reply_msg(&format!("│  Serveris : {} ({})", p(2), p(3))),
            "313" => self.whois_reply_msg(&format!("│  Operatorius: {}", p(1))),
            "317" => {
                let idle: i64 = p(2).parse().unwrap_or(0);
                let signon: i64 = p(3).parse().unwrap_or(0);
                let idle_str = format!("{}m {}s", idle / 60, idle % 60);
                let mut signon_str = String::new();
                if signon > 0 {
                    if let Some(t) = Local.timestamp_opt(signon, 0).single() {
                        signon_str = format!("  prisijungė: {}", t.format("%Y-%m-%d %H:%M"));
                    }
                }
                self.whois_reply_msg(&format!("│  Idle      : {idle_str}{signon_str}"));
            }
            "319" => self.whois_reply_msg(&format!("│  Kanalai   : {}", p(2))),
            "330" => self.whois_reply_msg(&format!("│  Paskyra   : {}", p(2))),
            "671" => self.whois_reply_msg("│  Ryšys     : TLS (šifruotas)"),
            "318" => self.whois_reply_msg(&format!("└─[whois] {} — pabaiga", p(1))),

            "305" => {
                self.mu.lock().away.clear();
                self.server_msg("Away išjungtas");
                self.queue_redraw();
            }
            "306" => {
                {
                    let mut st = self.mu.lock();
                    if st.away.is_empty() {
                        st.away = "Away".to_string();
                    }
                }
                self.server_msg("Away įjungtas");
                self.queue_redraw();
            }

            "431" => self.err_msg("Nenurodytas slapyvardis"),
            "432" => self.err_msg(&format!("Netinkamas slapyvardis: {}", p(1))),
            "433" => self.on_nick_in_use(),

            "ERROR" => {
                let m = p(0);
                self.err_msg(m);
                if m.to_lowercase().contains("throttl") {
                    self.mu.lock().reconnect_delay = Duration::from_secs(120);
                    self.server_msg("Throttled: laukiama 2 minutes prieš kitą bandymą");
                }
            }

            "CAP" => self.on_cap(msg),
            "AUTHENTICATE" => self.on_authenticate(msg),
            "900" => self.server_msg(&format!("SASL: prisijungta kaip {}", p(2))),
            "903" => {
                self.server_msg("SASL: autentifikacija sėkminga");
                {
                    let mut st = self.mu.lock();
                    st.sasl_pending = false;
                    st.sasl_ok = true;
                }
                self.send("CAP END");
            }
            "902" | "904" | "905" => {
                let m = params.last().map(String::as_str).unwrap_or("");
                self.err_msg(&format!("SASL klaida ({}): {}", msg.cmd, m));
                {
                    let mut st = self.mu.lock();
                    st.sasl_pending = false;
                    st.sasl_ok = false;
                }
                self.send("CAP END");
            }

            // MONITOR (v0.4.8)
            "730" => {
                for mask in p(1).split(',') {
                    let nick = monitor_nick(mask);
                    if nick.is_empty() {
                        continue;
                    }
                    let key = self.mon_key(&nick);
                    self.mu.lock().monitor_online.insert(key);
                    self.server_msg(&format!("● {nick} prisijungė (žiūrimas)"));
                }
            }
            "731" => {
                for mask in p(1).split(',') {
                    let nick = monitor_nick(mask);
                    if nick.is_empty() {
                        continue;
                    }
                    let key = self.mon_key(&nick);
                    self.mu.lock().monitor_online.remove(&key);
                    self.server_msg(&format!("○ {nick} atsijungė (žiūrimas)"));
                }
            }
            "732" => {
                for m in p(1).split(',') {
                    let m = sanitize_text(m.trim());
                    if !m.is_empty() {
                        self.server_msg(&format!("MONITOR sąrašas: {m}"));
                    }
                }
            }
            "733" => self.server_msg("MONITOR sąrašo pabaiga"),
            "734" => {
                // <limit> <nicks,...> :<reason> — the nick list is p(2), p(1) is the limit.
                for m in p(2).split(',') {
                    let cleaned = sanitize_text(m.trim());
                    let nick = cleaned.split('!').next().unwrap_or("").to_string();
                    if nick.is_empty() {
                        continue;
                    }
                    {
                        // Refused nicks must leave our list, or they'd be "watched" forever.
                        let mut st = self.mu.lock();
                        let pos = st
                            .monitor_list
                            .iter()
                            .position(|e| self.nick_equal(e, &nick));
                        if let Some(i) = pos {
                            st.monitor_list.remove(i);
                        }
                    }
                    self.err_msg(&format!("MONITOR sąrašas pilnas — „{nick}“ nepridėtas"));
                }
                self.save_monitor();
            }

            "AWAY" => self.on_away(msg, now),

            _ => {
                if params.len() > 1 {
                    self.server_msg(&format!("[{}] {}", msg.cmd, params[1..].join(" ")));
                }
            }
        }
    }

    // ---- registration ----

    fn on_welcome(&self, params: &[String]) {
        {
            let mut st = self.mu.lock();
            st.reconnect_delay = Duration::from_secs(5);
            st.nick_attempts = 0;
        }
        if params.len() > 1 {
            self.server_msg(&params[1..].join(" "));
        }
        let (pass, auto_join, perform, monitor) = {
            let st = self.mu.lock();
            (
                st.nickserv_pass.clone(),
                st.auto_join.clone(),
                st.perform.clone(),
                st.monitor_list.clone(),
            )
        };
        // IDENTIFY only with an explicit NickServ password: sasl_pass belongs to
        // the bouncer/bridge and must never leak to services.
        if !pass.is_empty() {
            self.send(&format!("PRIVMSG NickServ :IDENTIFY {pass}"));
            self.server_msg("NickServ: siunčiamas IDENTIFY...");
        }
        for ch in &auto_join {
            self.send(&format!("JOIN {ch}"));
        }
        for cmd in &perform {
            self.run_perform(cmd);
        }
        if !monitor.is_empty() {
            self.send(&format!("MONITOR + {}", monitor.join(",")));
        }
    }

    fn on_nick_in_use(&self) {
        self.err_msg("Slapyvardis jau naudojamas!");
        let (cand, nick) = {
            let mut st = self.mu.lock();
            let mut cand = String::new();
            // Sequence: nick_, nick2, nick3
            if st.nick_attempts < 3 {
                let mut base = st
                    .nick
                    .trim_end_matches(|c: char| "_0123456789".contains(c))
                    .to_string();
                if base.is_empty() {
                    base = "n".repeat(10);
                }
                st.nick_attempts += 1;
                cand = if st.nick_attempts == 1 {
                    format!("{base}_")
                } else {
                    format!("{base}{}", st.nick_attempts)
                };
                if cand.chars().count() > 30 {
                    cand = cand.chars().take(30).collect();
                }
                st.nick = cand.clone();
            }
            (cand, st.nick.clone())
        };
        let gave_up = cand.is_empty();
        if gave_up {
            self.err_msg(
                "Paskutinis bandymas baigtas keisti nick po 433 – jungiantis rankiniu būdu /nick",
            );
            return;
        }
        self.server_msg(&format!("Bandomas nick: {nick}"));
        self.send(&format!("NICK {nick}"));
    }

    // ---- membership ----

    fn take_limit_warning(st: &mut State, name: &str) -> String {
        if !st.chan_limit_hit.is_empty() && st.chan_limit_hit == sanitize_chan_name(name) {
            std::mem::take(&mut st.chan_limit_hit)
        } else {
            String::new()
        }
    }

    fn on_join(&self, msg: &IrcLine, now: DateTime<Utc>) {
        let ch = msg.param(0);
        let from = msg.from_nick();
        let mut st = self.mu.lock();
        let is_me = self.nick_equal(from, &st.nick);
        let Some(i) = self.ensure_ch(&mut st, ch) else {
            // Rejected: invalid name (ignore) or channel budget (tell the user).
            let hit = Self::take_limit_warning(&mut st, ch);
            drop(st);
            if !hit.is_empty() {
                self.server_msg(&format!(
                    "ĮSPĖJIMAS: pasiektas kanalų limitas — kanalas „{}“ neatidarytas",
                    sanitize_text(&hit)
                ));
            }
            return;
        };
        st.channels[i].add_nick(from, None);
        if is_me {
            st.active_idx = i;
        }
        drop(st);

        // extended-join: JOIN <channel> <account|*> <realname>
        let mut join_msg = String::new();
        if self.has_cap("extended-join") {
            let acct = msg.param(1);
            if !acct.is_empty() && acct != "*" {
                join_msg = format!("(paskyra: {})", sanitize_text(acct));
            }
        }
        self.add_msg(ch, new_msg(now, from, &join_msg, MsgType::Join));
    }

    fn on_part(&self, msg: &IrcLine, now: DateTime<Utc>) {
        let ch = msg.param(0);
        let reason = msg.param(1);
        let from = msg.from_nick();
        if ch.is_empty() {
            return;
        }
        let (existed, is_me) = {
            let mut st = self.mu.lock();
            let idx = st.find_ch(ch);
            if let Some(i) = idx {
                st.channels[i].remove_nick(from, None);
            }
            let is_me = self.nick_equal(from, &st.nick);
            if is_me && !ch.eq_ignore_ascii_case(SERVER_WINDOW) {
                if let Some(i) = idx {
                    st.remove_ch(i);
                }
            }
            (idx.is_some(), is_me)
        };
        // Only show when the window was already ours (no side windows via stray PARTs).
        if !is_me && existed {
            self.add_msg(ch, new_msg(now, from, reason, MsgType::Part));
        }
    }

    fn on_quit(&self, msg: &IrcLine, now: DateTime<Utc>) {
        let reason = msg.param(0);
        let from = msg.from_nick();
        // O(1) set lookup per channel — never an O(n) scan per QUIT.
        let affected: Vec<String> = {
            let mut st = self.mu.lock();
            let mut names = Vec::new();
            for ch in st.channels.iter_mut() {
                let sign = ch.prefix_signs(None);
                if ch.has_nick_key(from, &sign) {
                    ch.remove_nick(from, None);
                    names.push(ch.name.clone());
                }
            }
            names
        };
        for name in affected {
            self.add_msg(&name, new_msg(now, from, reason, MsgType::Quit));
        }
    }

    fn on_kick(&self, msg: &IrcLine, now: DateTime<Utc>) {
        let (ch, kicked, reason) = (msg.param(0), msg.param(1), msg.param(2));
        let from = msg.from_nick();
        let (me, found) = {
            let mut st = self.mu.lock();
            let me = st.nick.clone();
            // find, not ensure: KICK must not create windows.
            let found = match st.find_ch(ch) {
                Some(i) => {
                    st.channels[i].remove_nick(kicked, None);
                    true
                }
                None => false,
            };
            (me, found)
        };
        if !found {
            return;
        }
        if self.nick_equal(kicked, &me) {
            self.err_msg(&format!("Buvai išmestas iš {ch} ({reason})"));
        } else {
            self.add_msg(
                ch,
                new_msg(
                    now,
                    from,
                    &format!("išmetė {kicked} ({reason})"),
                    MsgType::Server,
                ),
            );
        }
        self.queue_redraw();
    }

    fn on_nick(&self, msg: &IrcLine, now: DateTime<Utc>) {
        let new_nick = msg.param(0);
        let from = msg.from_nick();
        if new_nick.is_empty() {
            return;
        }
        let chans: Vec<String> = {
            let mut st = self.mu.lock();
            if self.nick_equal(from, &st.nick) {
                st.nick = new_nick.to_string();
            }
            let mut names = Vec::new();
            for ch in st.channels.iter_mut() {
                if ch.has_nick(from) {
                    ch.rename_nick(from, new_nick, None);
                    names.push(ch.name.clone());
                }
            }
            names
        };
        for name in chans {
            self.add_msg(&name, new_msg(now, from, new_nick, MsgType::Nick));
        }
        self.server_msg(&format!("{from} → {new_nick}"));
    }

    fn on_mode(&self, msg: &IrcLine, now: DateTime<Utc>) {
        let target = msg.param(0);
        let mode_str = msg.param(1);
        let params = &msg.params;
        if !self.is_channel(target) {
            return;
        }
        let (_, signs, modes, _, chan_modes) = self.isup_snapshot();
        {
            let mut st = self.mu.lock();
            // MODE for a channel we are not in must not create a window.
            let Some(ci) = st.find_ch(target) else { return };
            if params.len() > 2 {
                // Count EVERY mode that takes an argument, per the server's
                // CHANMODES, or the argument index drifts and the status
                // prefix lands on the wrong nick.
                let groups = parse_chan_modes(&chan_modes);
                let mut adding = true;
                let mut mode_idx = 2;
                for mr in mode_str.chars() {
                    match mr {
                        '+' => adding = true,
                        '-' => adding = false,
                        _ if groups.takes_param(mr, adding, &modes) => {
                            if mode_idx < params.len() {
                                if modes.contains(mr) {
                                    let sign = if adding { '+' } else { '-' };
                                    let spec = format!("{sign}{mr}");
                                    let nick = params[mode_idx].clone();
                                    st.channels[ci].set_mode(
                                        &nick,
                                        &spec,
                                        Some(&signs),
                                        Some(&modes),
                                    );
                                }
                                mode_idx += 1;
                            }
                        }
                        _ => {}
                    }
                }
            }
        }
        let desc = if params.len() > 1 {
            params[1..].join(" ")
        } else {
            String::new()
        };
        self.add_msg(
            target,
            new_msg(now, "", &format!("Mode {target}: {desc}"), MsgType::Server),
        );
        self.queue_redraw();
    }

    // ---- messages ----

    fn on_privmsg(&self, msg: &IrcLine, now: DateTime<Utc>) {
        let target = msg.param(0);
        let text = msg.param(1);
        let from = msg.from_nick();
        let me = self.my_nick();

        if self.is_ignored(&msg.prefix) {
            return;
        }

        if text.len() >= 2 && text.starts_with('\x01') && text.ends_with('\x01') {
            let inner = &text[1..text.len() - 1];
            if let Some(action) = inner.strip_prefix("ACTION ") {
                // Our own /me is printed locally; drop the echo-message copy.
                let echo = {
                    let st = self.mu.lock();
                    st.active_caps.contains("echo-message") && self.nick_equal(from, &st.nick)
                };
                if echo {
                    return;
                }
                let dest = if self.nick_equal(target, &me) {
                    from
                } else {
                    target
                };
                let hl = strip_irc_format(action)
                    .to_lowercase()
                    .contains(&me.to_lowercase());
                self.add_msg(dest, new_msg_hl(now, from, action, MsgType::Action, hl));
            } else if inner.starts_with("VERSION") {
                let enabled = self.mu.lock().ctcp_version;
                if enabled {
                    self.send(&format!(
                        "NOTICE {from} :\x01VERSION mircBSD {VERSION} ({}/Rust)\x01",
                        os_label()
                    ));
                }
            } else if inner.starts_with("PING") {
                // Bounded reflection: no amplification.
                let payload = cut_utf8(inner, MAX_CTCP_BYTES);
                self.send(&format!("NOTICE {from} :\x01{payload}\x01"));
            }
            return;
        }

        let mut dest = target;
        let mut is_pm = false;
        if self.nick_equal(target, &me) {
            dest = from;
            is_pm = true;
        }
        // echo-message: our own messages come back; don't show twice.
        let is_echo = {
            let st = self.mu.lock();
            st.active_caps.contains("echo-message") && self.nick_equal(from, &st.nick)
        };
        if is_echo {
            return;
        }
        if is_pm {
            let mut st = self.mu.lock();
            if let Some(i) = self.ensure_ch(&mut st, dest) {
                st.channels[i].is_private = true;
            }
        }
        let plain = strip_irc_format(text);
        let mut hl = is_pm || contains_nick(&plain, &me);
        if !hl {
            let plain_low = plain.to_lowercase();
            let st = self.mu.lock();
            hl = st
                .highlight_words
                .iter()
                .any(|w| plain_low.contains(&w.to_lowercase()));
        }
        self.add_msg(dest, new_msg_hl(now, from, text, MsgType::Chat, hl));

        let urls = extract_urls(text);
        if !urls.is_empty() {
            let mut st = self.mu.lock();
            st.last_urls.extend(urls);
            if st.last_urls.len() > 50 {
                let drop_n = st.last_urls.len() - 50;
                st.last_urls.drain(..drop_n);
            }
        }
    }

    fn on_away(&self, msg: &IrcLine, now: DateTime<Utc>) {
        let from = msg.from_nick();
        if from.is_empty() {
            return;
        }
        let reason = msg.param(0);
        let cm = self.casemapping();
        let key = from.to_lowercase();
        let ttl = chrono::Duration::minutes(AWAY_THROTTLE_MINUTES);

        // (active window name, its nicks, signs) copied under the lock.
        let mut announce: Option<(String, Vec<String>, String, String)> = None;
        {
            let mut st = self.mu.lock();
            if !reason.is_empty() {
                let fresh = match st.away_notified.get(&key) {
                    Some(last) => now.signed_duration_since(*last) >= ttl,
                    None => true,
                };
                if fresh {
                    st.away_notified.insert(key, now);
                    st.prune_away_notified(now);
                    if let Some(a) = st.active_chan() {
                        announce = Some((
                            a.name.clone(),
                            a.nicks.clone(),
                            a.prefix_signs(None),
                            format!("{from} yra away: {reason}"),
                        ));
                    }
                }
            } else {
                // Nick is back: clear the throttle; announce only if we had noted the away.
                let was_away = st.away_notified.remove(&key).is_some();
                if was_away {
                    if let Some(a) = st.active_chan() {
                        announce = Some((
                            a.name.clone(),
                            a.nicks.clone(),
                            a.prefix_signs(None),
                            format!("{from} grįžo iš away"),
                        ));
                    }
                }
            }
        }
        if let Some((name, nicks, signs, text)) = announce {
            if nicks
                .iter()
                .any(|n| nick_equal(strip_mode(n, &signs), from, &cm))
            {
                self.add_msg(&name, new_msg(now, from, &text, MsgType::Server));
            }
        }
    }

    // ---- IRCv3 ----

    fn sasl_inputs(st: &State) -> SaslInputs {
        SaslInputs {
            external: st.sasl_external,
            has_cert: !st.tls_cert_path.is_empty(),
            use_tls: st.use_tls,
            has_password: !st.sasl_pass.is_empty(),
            already_ok: st.sasl_ok,
        }
    }

    /// Finish a capability round: decide SASL, send `CAP REQ`/`CAP END`.
    /// `st` is consumed (lock released before any output).
    fn finish_cap_round(&self, mut st: parking_lot::MutexGuard<'_, State>, track_active: bool) {
        let available = std::mem::take(&mut st.cap_available);
        let mut wanted: Vec<String> = wanted_caps(
            &available,
            if track_active {
                Some(&st.active_caps)
            } else {
                None
            },
        )
        .into_iter()
        .map(str::to_string)
        .collect();
        let decision = sasl_decide(&available, &Self::sasl_inputs(&st));
        if decision.request {
            st.cap_sasl = true;
            st.sasl_mech = decision.mech.to_string();
            wanted.push("sasl".to_string());
        }
        drop(st);
        if decision.warn {
            self.server_msg(SASL_EXTERNAL_WARN);
        }
        if wanted.is_empty() {
            self.send("CAP END");
        } else {
            self.send(&format!("CAP REQ :{}", wanted.join(" ")));
        }
    }

    /// `CAP NEW`: server offers capabilities after registration.
    pub fn handle_cap_new(&self, caps: &str) {
        let mut st = self.mu.lock();
        st.cap_available.extend(parse_cap_names(caps));
        self.finish_cap_round(st, true);
    }

    fn on_cap(&self, msg: &IrcLine) {
        let sub = msg.param(1).to_uppercase();
        // 302 continuation: CAP nick LS * :caps
        let cont = msg.param(2) == "*";
        let caps = if cont { msg.param(3) } else { msg.param(2) };

        match sub.as_str() {
            "LS" => {
                let mut st = self.mu.lock();
                st.cap_available.extend(parse_cap_names(caps));
                if cont {
                    return;
                }
                self.finish_cap_round(st, false);
            }
            "ACK" => {
                let mut sasl_acked = false;
                {
                    let mut st = self.mu.lock();
                    for cap in caps.split_whitespace() {
                        // "-cap" ACK removes it (a hostile "-sasl" must not activate SASL).
                        if let Some(name) = cap.strip_prefix('-') {
                            st.active_caps.remove(&name.to_lowercase());
                            continue;
                        }
                        let name = cap.to_lowercase();
                        if name == "sasl" {
                            sasl_acked = true;
                        }
                        st.active_caps.insert(name);
                    }
                    // Start AUTHENTICATE only if WE asked for sasl and have a credential.
                    if !st.cap_sasl || (st.sasl_mech != "EXTERNAL" && st.sasl_pass.is_empty()) {
                        sasl_acked = false;
                    }
                }
                if cont {
                    return; // more ACK lines follow
                }
                if sasl_acked {
                    let mech = {
                        let mut st = self.mu.lock();
                        st.sasl_pending = true;
                        st.sasl_mech.clone()
                    };
                    self.send(&format!("AUTHENTICATE {mech}"));
                } else {
                    self.send("CAP END");
                }
            }
            "NAK" => self.send("CAP END"),
            "NEW" => self.handle_cap_new(caps),
            "LIST" => {
                let mut active: Vec<String> = self.mu.lock().active_caps.iter().cloned().collect();
                active.sort();
                self.send(&format!("CAP LIST :{}", active.join(" ")));
            }
            "DEL" => {
                let mut st = self.mu.lock();
                for cap in caps.split_whitespace() {
                    st.active_caps
                        .remove(&cap.trim_start_matches('-').to_lowercase());
                }
            }
            _ => {}
        }
    }

    fn on_authenticate(&self, msg: &IrcLine) {
        if msg.param(0) != "+" {
            return;
        }
        let (pending, nick, pass, mech) = {
            let st = self.mu.lock();
            (
                st.sasl_pending,
                st.nick.clone(),
                st.sasl_pass.clone(),
                st.sasl_mech.clone(),
            )
        };
        // Only answer a challenge we asked for: otherwise empty reply, never the password.
        if !pending || mech == "EXTERNAL" {
            // EXTERNAL: certificate already presented in the TLS handshake → empty payload.
            self.send("AUTHENTICATE +");
            return;
        }
        for chunk in authenticate_chunks(&sasl_plain_payload(&nick, &pass)) {
            self.send(&format!("AUTHENTICATE {chunk}"));
        }
    }
}

/// `nick!user@host` → sanitized nick (MONITOR numerics).
fn monitor_nick(mask: &str) -> String {
    let nick = mask.split('!').next().unwrap_or("");
    sanitize_text(nick.trim())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::client::send::{new_write_queue, WriteQueue};
    use crate::config::default_config;
    use std::sync::Arc;

    fn setup() -> (Arc<Client>, WriteQueue) {
        let mut cfg = default_config();
        cfg.server = "irc.example.org".into();
        cfg.nick = "me".into();
        let c = Client::new(&cfg);
        let (h, q) = new_write_queue();
        c.mu.lock().write = Some(h);
        (c, q)
    }

    fn sent(q: &WriteQueue) -> Vec<String> {
        let mut v = Vec::new();
        while let Ok(l) = q.rx.try_recv() {
            v.push(l);
        }
        v
    }

    fn texts(c: &Client, chan: &str) -> Vec<String> {
        let st = c.mu.lock();
        let i = st.find_ch(chan).expect("channel");
        st.channels[i]
            .messages
            .iter()
            .map(|m| m.text.clone())
            .collect()
    }

    #[test]
    fn ping_pong() {
        let (c, q) = setup();
        c.parse_irc("PING :abc");
        assert_eq!(sent(&q), vec!["PONG :abc"]);
    }

    #[test]
    fn join_names_quit_flow() {
        let (c, _q) = setup();
        c.parse_irc(":me!u@h JOIN #rust");
        assert_eq!(c.active_name(), "#rust");
        c.parse_irc(":srv 353 me = #rust :@op bob");
        c.parse_irc(":bob!u@h QUIT :bye");
        {
            let st = c.mu.lock();
            let i = st.find_ch("#rust").unwrap();
            assert_eq!(st.channels[i].nicks, vec!["@op", "me"]);
        }
        assert!(texts(&c, "#rust").contains(&"bye".to_string()));
    }

    #[test]
    fn names_does_not_create_windows() {
        let (c, _q) = setup();
        c.parse_irc(":srv 353 me = #ghost :a b c");
        c.parse_irc(":srv 332 me #ghost :topic");
        c.parse_irc(":x!u@h MODE #ghost +o y");
        assert!(c.find_ch("#ghost").is_none());
    }

    #[test]
    fn mode_prefix_applies_to_right_nick() {
        let (c, _q) = setup();
        c.parse_irc(":me!u@h JOIN #c");
        c.parse_irc(":srv 353 me = #c :bob me");
        c.apply_isupport(&["CHANMODES=beI,k,l,imnpst", "PREFIX=(ov)@+"]);
        // +l takes a parameter (C), then +o takes bob.
        c.parse_irc(":op!u@h MODE #c +lo 30 bob");
        let st = c.mu.lock();
        let i = st.find_ch("#c").unwrap();
        assert!(
            st.channels[i].nicks.contains(&"@bob".to_string()),
            "{:?}",
            st.channels[i].nicks
        );
    }

    #[test]
    fn nick_change_updates_self_and_channels() {
        let (c, _q) = setup();
        c.parse_irc(":me!u@h JOIN #c");
        c.parse_irc(":me!u@h NICK :me2");
        assert_eq!(c.my_nick(), "me2");
        let st = c.mu.lock();
        let i = st.find_ch("#c").unwrap();
        assert_eq!(st.channels[i].nicks, vec!["me2"]);
    }

    #[test]
    fn privmsg_pm_highlight_and_urls() {
        let (c, _q) = setup();
        c.parse_irc(":bob!u@h PRIVMSG me :see https://example.org/x.");
        let st = c.mu.lock();
        let i = st.find_ch("bob").unwrap();
        assert!(st.channels[i].is_private);
        assert!(st.channels[i].messages[0].highlight);
        assert_eq!(st.last_urls, vec!["https://example.org/x"]);
    }

    #[test]
    fn ctcp_version_and_ping_reply() {
        let (c, q) = setup();
        c.parse_irc(":bob!u@h PRIVMSG me :\x01VERSION\x01");
        let long = "x".repeat(200);
        c.parse_irc(&format!(":bob!u@h PRIVMSG me :\x01PING {long}\x01"));
        let s = sent(&q);
        assert!(s[0].starts_with("NOTICE bob :\x01VERSION mircBSD 0.4.8"));
        assert!(s[1].len() < 100, "{}", s[1].len());
    }

    #[test]
    fn echo_message_is_suppressed() {
        let (c, _q) = setup();
        c.mu.lock().active_caps.insert("echo-message".into());
        c.parse_irc(":me!u@h PRIVMSG #c :hi");
        assert!(c.find_ch("#c").is_none());
    }

    #[test]
    fn nick_in_use_sequence() {
        let (c, q) = setup();
        c.parse_irc(":srv 433 * me :in use");
        c.parse_irc(":srv 433 * me_ :in use");
        c.parse_irc(":srv 433 * me2 :in use");
        c.parse_irc(":srv 433 * me3 :in use");
        assert_eq!(sent(&q), vec!["NICK me_", "NICK me2", "NICK me3"]);
    }

    #[test]
    fn isupport_005_applies() {
        let (c, _q) = setup();
        c.parse_irc(":srv 005 me CHANTYPES=# PREFIX=(ov)@+ CASEMAPPING=ascii :are supported");
        assert!(c.is_channel("#a"));
        assert!(!c.is_channel("&a"));
        assert_eq!(c.casemapping(), "ascii");
    }

    #[test]
    fn cap_ls_requests_wanted_and_sasl_plain() {
        let (c, q) = setup();
        c.mu.lock().sasl_pass = "secret".into();
        c.parse_irc(":srv CAP * LS * :multi-prefix away-notify");
        assert!(sent(&q).is_empty(), "continuation must not answer yet");
        c.parse_irc(":srv CAP * LS :sasl=PLAIN server-time unknown");
        let s = sent(&q);
        assert_eq!(
            s,
            vec!["CAP REQ :multi-prefix away-notify server-time sasl"]
        );

        c.parse_irc(":srv CAP me ACK :multi-prefix sasl");
        assert_eq!(sent(&q), vec!["AUTHENTICATE PLAIN"]);
        c.parse_irc("AUTHENTICATE +");
        assert_eq!(
            sent(&q),
            vec![format!(
                "AUTHENTICATE {}",
                sasl_plain_payload("me", "secret")
            )]
        );
        c.parse_irc(":srv 903 me :ok");
        assert_eq!(sent(&q), vec!["CAP END"]);
        assert!(c.mu.lock().sasl_ok);
    }

    #[test]
    fn unsolicited_sasl_ack_never_leaks_password() {
        let (c, q) = setup();
        c.mu.lock().sasl_pass = "secret".into();
        // We never requested sasl (cap_sasl false) → hostile ACK is ignored.
        c.parse_irc(":srv CAP me ACK :sasl");
        assert_eq!(sent(&q), vec!["CAP END"]);
        // And an AUTHENTICATE challenge without pending gets an empty reply.
        c.parse_irc("AUTHENTICATE +");
        assert_eq!(sent(&q), vec!["AUTHENTICATE +"]);
    }

    #[test]
    fn cap_ack_minus_removes() {
        let (c, q) = setup();
        c.mu.lock().active_caps.insert("sasl".into());
        c.parse_irc(":srv CAP me ACK :-sasl");
        assert!(!c.has_cap("sasl"));
        assert_eq!(sent(&q), vec!["CAP END"]);
    }

    #[test]
    fn welcome_runs_autojoin_and_identify() {
        let (c, q) = setup();
        {
            let mut st = c.mu.lock();
            st.auto_join = vec!["#a".into(), "#b".into()];
            st.nickserv_pass = "pw".into();
            st.monitor_list = vec!["x".into(), "y".into()];
            st.perform = vec!["MODE me +i".into()];
        }
        c.parse_irc(":srv 001 me :Welcome");
        assert_eq!(
            sent(&q),
            vec![
                "PRIVMSG NickServ :IDENTIFY pw",
                "JOIN #a",
                "JOIN #b",
                "MODE me +i",
                "MONITOR + x,y"
            ]
        );
    }

    #[test]
    fn part_self_closes_window_and_kick_message() {
        let (c, _q) = setup();
        c.parse_irc(":me!u@h JOIN #a");
        c.parse_irc(":me!u@h JOIN #b");
        c.parse_irc(":me!u@h PART #a");
        assert!(c.find_ch("#a").is_none());
        c.parse_irc(":op!u@h KICK #b me :bad");
        assert!(c.find_ch("#b").is_some());
        assert!(texts(&c, SERVER_WINDOW)
            .iter()
            .any(|t| t.contains("išmestas")));
    }

    #[test]
    fn away_notify_announces_once() {
        let (c, _q) = setup();
        c.parse_irc(":me!u@h JOIN #a");
        c.parse_irc(":srv 353 me = #a :bob");
        c.parse_irc(":bob!u@h AWAY :lunch");
        c.parse_irc(":bob!u@h AWAY :lunch");
        c.parse_irc(":bob!u@h AWAY");
        let t = texts(&c, "#a");
        assert_eq!(t.iter().filter(|x| x.contains("yra away")).count(), 1);
        assert_eq!(t.iter().filter(|x| x.contains("grįžo")).count(), 1);
    }

    #[test]
    fn monitor_numerics_track_state() {
        let (c, _q) = setup();
        c.parse_irc(":srv 730 me :Bob!u@h,al!u@h");
        assert!(c.mu.lock().monitor_online.contains(&c.mon_key("bob")));
        c.parse_irc(":srv 731 me :bob!u@h");
        assert!(!c.mu.lock().monitor_online.contains(&c.mon_key("bob")));
    }

    #[test]
    fn server_time_tag_sets_timestamp() {
        let (c, _q) = setup();
        c.parse_irc("@time=2020-01-02T03:04:05.000Z :bob!u@h PRIVMSG me :hi");
        let st = c.mu.lock();
        let i = st.find_ch("bob").unwrap();
        let ts = st.channels[i].messages[0].ts;
        assert_eq!(
            ts.with_timezone(&chrono::Utc)
                .format("%Y-%m-%d")
                .to_string(),
            "2020-01-02"
        );
    }

    #[test]
    fn whois_and_unknown_numeric_render() {
        let (c, _q) = setup();
        c.parse_irc(":srv 311 me bob u h * :Real Name");
        c.parse_irc(":srv 999 me foo bar");
        let t = texts(&c, SERVER_WINDOW);
        assert!(t.iter().any(|x| x.contains("[whois] bob (u@h)")));
        assert!(t.iter().any(|x| x == "[999] foo bar"));
    }

    #[test]
    fn helpers() {
        assert!(contains_nick("hello Me!", "me"));
        assert!(!contains_nick("game", "me"));
        assert!(!contains_nick("x", ""));
        assert_eq!(
            extract_urls("a http://x.org, b ftp://y.z) c"),
            vec!["http://x.org", "ftp://y.z"]
        );
        assert_eq!(cut_utf8("ééé", 3), "é");
    }
}
