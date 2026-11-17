//! Dispatch of parsed server lines (`parseIRC` in `irc.go`).
//!
//! Locking reminder: `Client::mu` is not reentrant. Every handler takes the
//! lock for state changes, drops it, and only then calls `add_msg`,
//! `server_msg`, `err_msg` or `send` (which lock again).

use crate::timeutil::Ts;
use std::time::Duration;

use crate::proto::caps::{
    authenticate_chunks, parse_cap_names, sasl_decide, sasl_plain_payload, wanted_caps, SaslInputs,
    IRCV3_WANTED_CAPS, SASL_EXTERNAL_WARN,
};
use crate::proto::isupport::parse_chan_modes;
use crate::proto::parse::{parse_line, IrcLine};
use crate::sanitize::{is_safe_url, sanitize_chan_name, sanitize_text};
use crate::validate::{nick_equal, strip_mode};
use crate::width::{fit_width, pad_right};

use crate::ui::strip_irc_format;

use super::chan_keys::HIDDEN_KEY;
use super::types::{new_msg, new_msg_hl, Client, MsgType, State, SERVER_WINDOW};
use super::{
    os_label, CTCP_REPLY_WINDOW, MAX_AWAY_NOTIFIED, MAX_CTCP_BYTES, MAX_CTCP_REPLIES, VERSION,
};

const AWAY_THROTTLE_MINUTES: i64 = 10;
/// Most capability names kept from one `CAP LS`/`NEW` round.
const MAX_CAP_NAMES: usize = 1024;

/// Does `text` mention `nick` as a whole word (case-insensitive)?
///
/// Tikrinamos VISOS vietos: „bobby, bob“ turi paryškinti „bob“ (anksčiau
/// žiūrėta tik į pirmą atitikmenį „bobby“ ir highlight dingdavo).
pub fn contains_nick(text: &str, nick: &str) -> bool {
    contains_nick_cm(text, nick, "")
}

/// [`contains_nick`] pagal serverio CASEMAPPING (atlaidus lankstymas
/// [`crate::validate::fold_lenient`]: Unicode mažosios + rfc1459 `[]\^`).
pub fn contains_nick_cm(text: &str, nick: &str, cm: &str) -> bool {
    if nick.is_empty() {
        return false;
    }
    let text_low = crate::validate::fold_lenient(text, cm);
    let nick_low = crate::validate::fold_lenient(nick, cm);
    let tb = text_low.as_bytes();
    let mut from = 0;
    while let Some(off) = text_low[from..].find(&nick_low) {
        let idx = from + off;
        let before = idx == 0 || !is_nick_char(tb[idx - 1] as char);
        let end = idx + nick_low.len();
        let after = end >= tb.len() || !is_nick_char(tb[end] as char);
        if before && after {
            return true;
        }
        // Kitas simbolis (ne baitas) — find grąžina simbolio ribą.
        from = idx + text_low[idx..].chars().next().map_or(1, char::len_utf8);
    }
    false
}

/// RFC nick characters: letters, digits and ``_-[]\`^{}|``.
pub fn is_nick_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || "_-[]\\`^{}|".contains(c)
}

/// http/https/ftp URLs in `text` (formatting stripped, trailing punctuation
/// trimmed). URLs with hidden characters are skipped ([`is_safe_url`]): what
/// `/open` launches must be exactly what `/urls` showed.
pub fn extract_urls(text: &str) -> Vec<String> {
    strip_irc_format(text)
        .split_whitespace()
        .map(|w| w.trim_end_matches(['.', ',', '!', '?', ')']))
        .filter(|w| {
            (w.starts_with("http://") || w.starts_with("https://") || w.starts_with("ftp://"))
                && is_safe_url(w)
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
    pub fn prune_away_notified(&mut self, now: Ts) {
        const SLACK: usize = MAX_AWAY_NOTIFIED / 4;
        if self.away_notified.len() <= MAX_AWAY_NOTIFIED + SLACK {
            return;
        }
        let ttl = crate::timeutil::minutes(AWAY_THROTTLE_MINUTES);
        self.away_notified
            .retain(|_, t| now.duration_since(*t) < ttl);
        if self.away_notified.len() <= MAX_AWAY_NOTIFIED {
            return;
        }
        let mut entries: Vec<(String, Ts)> = self
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
        let now = msg.server_time().unwrap_or_else(crate::timeutil::now);
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
                // Kanalo NOTICE (taip pat STATUSMSG „@#kanalas“) — į to kanalo
                // langą, jei jis atidarytas; kitaip (ir asmeniniai) — *server*.
                let chan = self.statusmsg_target(p(0));
                let dest = if self.is_channel(chan) && self.find_ch(chan).is_some() {
                    chan
                } else {
                    SERVER_WINDOW
                };
                self.add_msg(dest, new_msg(now, from_nick, p(1), MsgType::Notice));
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
            "324" => self.on_channel_mode_is(params, now),
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
                    if let Some(t) = crate::timeutil::from_unix(signon) {
                        let t = crate::timeutil::local(t);
                        signon_str = format!("  prisijungė: {}", t.strftime("%Y-%m-%d %H:%M"));
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
            "432" => self.on_erroneous_nick(p(1)),
            "433" => self.on_nick_in_use(p(1)),
            "475" => self.on_bad_channel_key(p(1)),

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
                    {
                        // Only nicks WE watch: a hostile server could
                        // otherwise grow the set without bound.
                        let mut st = self.mu.lock();
                        if !st.monitor_list.iter().any(|n| self.nick_equal(n, &nick)) {
                            continue;
                        }
                        st.monitor_online.insert(key);
                    }
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

    /// 475 ERR_BADCHANNELKEY: serveris atmetė raktą — įsimintas raktas
    /// pamirštamas, kad grįžimas po pakartotinio prisijungimo ir `/join` be
    /// rakto nebandytų jo vėl. Pats raktas pranešime nerodomas.
    fn on_bad_channel_key(&self, chan: &str) {
        let had_key = {
            let mut st = self.mu.lock();
            let had = st.chan_key(chan).is_some();
            st.forget_chan_key(chan);
            had
        };
        let chan = crate::sanitize::sanitize_text(chan);
        if had_key {
            self.err_msg(&format!(
                "Neteisingas kanalo raktas: {chan} — įsimintas raktas pamirštas (/join {chan} <raktas>)"
            ));
        } else {
            self.err_msg(&format!(
                "Kanalui {chan} reikia rakto: /join {chan} <raktas>"
            ));
        }
    }

    // ---- registration ----

    fn on_welcome(&self, params: &[String]) {
        {
            let mut st = self.mu.lock();
            st.reconnect_delay = Duration::from_secs(5);
            st.nick_attempts = 0;
            st.registered = true;
            // 001 pirmasis parametras — serverio PRISKIRTAS nick (gali būti
            // sutrumpintas iki NICKLEN ar pakeistas 433 sekos metu).
            if let Some(n) = params.first() {
                if crate::validate::is_valid_nick(n) {
                    st.nick = n.clone();
                }
            }
        }
        if params.len() > 1 {
            self.server_msg(&params[1..].join(" "));
        }
        let chantypes = self.isup_snapshot().0;
        let (pass, joins, join_warnings, perform, monitor) = {
            let mut st = self.mu.lock();
            // NickServ password and `perform` (often "IDENTIFY …") belong to
            // the configured server only.
            let creds = st.creds_allowed();
            // Po pakartotinio prisijungimo grįžtame į kanalus, kuriuose buvome
            // (ir į /join be ryšio atidarytus) — su įsimintais raktais.
            let (joins, warnings) = st.take_pending_joins(&chantypes);
            (
                if creds {
                    st.nickserv_pass.clone()
                } else {
                    String::new()
                },
                joins,
                warnings,
                if creds {
                    st.perform.clone()
                } else {
                    Vec::new()
                },
                st.monitor_list.clone(),
            )
        };
        // IDENTIFY only with an explicit NickServ password: sasl_pass belongs to
        // the bouncer/bridge and must never leak to services.
        if !pass.is_empty() {
            self.send(&format!("PRIVMSG NickServ :IDENTIFY {pass}"));
            self.server_msg("NickServ: siunčiamas IDENTIFY...");
        }
        for w in &join_warnings {
            self.err_msg(w);
        }
        // Vienas JOIN keliems kanalams (raktai — pirmiesiems), ne po eilutę
        // kanalui: 5 eilučių/s riba kitaip vėlintų perform ir MONITOR.
        for line in crate::client::chan_keys::join_lines(&joins) {
            self.send(&line);
        }
        for cmd in &perform {
            self.run_perform(cmd);
        }
        if !monitor.is_empty() {
            self.send(&format!("MONITOR + {}", monitor.join(",")));
        }
    }

    /// 432 ERR_ERRONEUSNICKNAME. Per registraciją serveris laukia kito NICK —
    /// anksčiau rodyta tik klaida ir registracija užstrigdavo amžinai.
    /// Bandomas saugus „mircbsd“ (toliau, jei užimtas, veikia 433 seka).
    fn on_erroneous_nick(&self, wanted: &str) {
        const FALLBACK: &str = "mircbsd";
        let shown = sanitize_text(wanted);
        let retry = {
            let mut st = self.mu.lock();
            if st.registered {
                None
            } else if !st.nick.eq_ignore_ascii_case(FALLBACK) {
                st.nick = FALLBACK.to_string();
                Some(true)
            } else {
                Some(false)
            }
        };
        match retry {
            None => self.err_msg(&format!("Netinkamas slapyvardis: {shown}")),
            Some(true) => {
                self.err_msg(&format!(
                    "Serveris atmetė slapyvardį „{shown}“ — bandomas {FALLBACK}"
                ));
                self.send(&format!("NICK {FALLBACK}"));
            }
            Some(false) => self.err_msg(&format!(
                "Serveris atmetė slapyvardį „{shown}“ — registracija sustojo; rašyk /nick <kitas>"
            )),
        }
    }

    fn on_nick_in_use(&self, wanted: &str) {
        if self.mu.lock().registered {
            // Po registracijos 433 atsako į /nick: dabartinis nick nesikeičia,
            // automatinis „nick_“ bandymas čia būtų nepageidaujamas pervadinimas.
            self.err_msg(&format!(
                "Slapyvardis {} jau naudojamas",
                sanitize_text(wanted)
            ));
            return;
        }
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

    fn on_join(&self, msg: &IrcLine, now: Ts) {
        let ch = msg.param(0);
        let from = msg.from_nick();
        let mut st = self.mu.lock();
        let is_me = self.nick_equal(from, &st.nick);
        let existed = st.find_ch(ch).is_some();
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
        drop(st);
        // Į NAUJAI atidarytą kanalą pereinama per switch_ch (išsaugomas
        // ankstesnio lango slinkimas; anksčiau active_idx keistas tiesiogiai ir
        // naujas langas paveldėdavo seno scroll_off). Grįžimas į jau atidarytą
        // langą (rejoin po pakartotinio prisijungimo) fokuso nevagia.
        if is_me && !existed {
            self.switch_ch(i);
        }

        // extended-join: JOIN <channel> <account|*> <realname>
        let mut join_msg = String::new();
        if self.has_cap("extended-join") {
            let acct = msg.param(1);
            if !acct.is_empty() && acct != "*" {
                join_msg = format!("(paskyra: {})", sanitize_text(acct));
            }
        }
        // Kitų prisijungimai — foniniai (blanki spalva, show_joins filtras).
        let m = new_msg(now, from, &join_msg, MsgType::Join);
        self.add_msg(ch, if is_me { m } else { m.quiet() });
    }

    fn on_part(&self, msg: &IrcLine, now: Ts) {
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
                // Išėjome iš kanalo — jo raktas nebereikalingas.
                st.forget_chan_key(ch);
            }
            (idx.is_some(), is_me)
        };
        // Only show when the window was already ours (no side windows via stray PARTs).
        if !is_me && existed {
            self.add_msg(ch, new_msg(now, from, reason, MsgType::Part).quiet());
        }
    }

    fn on_quit(&self, msg: &IrcLine, now: Ts) {
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
            self.add_msg(&name, new_msg(now, from, reason, MsgType::Quit).quiet());
        }
    }

    fn on_kick(&self, msg: &IrcLine, now: Ts) {
        let (ch, kicked, reason) = (msg.param(0), msg.param(1), msg.param(2));
        let from = msg.from_nick();
        let (me, found) = {
            let mut st = self.mu.lock();
            let me = st.nick.clone();
            // find, not ensure: KICK must not create windows.
            let found = match st.find_ch(ch) {
                Some(i) => {
                    if self.nick_equal(kicked, &me) {
                        // Mus išmetė: kanale nebesame — senas sąrašas melagingas.
                        st.channels[i].clear_nicks();
                    } else {
                        st.channels[i].remove_nick(kicked, None);
                    }
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
                )
                .quiet(),
            );
        }
        self.queue_redraw();
    }

    fn on_nick(&self, msg: &IrcLine, now: Ts) {
        let new_nick = msg.param(0);
        let from = msg.from_nick();
        if new_nick.is_empty() {
            return;
        }
        let (chans, is_me): (Vec<String>, bool) = {
            let mut st = self.mu.lock();
            let is_me = self.nick_equal(from, &st.nick);
            if is_me {
                st.nick = new_nick.to_string();
            }
            let mut names = Vec::new();
            let is_chan = |n: &str| self.is_channel(n);
            for ch in st.channels.iter_mut() {
                if ch.has_nick(from) {
                    ch.rename_nick(from, new_nick, None);
                    names.push(ch.name.clone());
                } else if ch.name != SERVER_WINDOW
                    && !is_chan(&ch.name)
                    && self.nick_equal(&ch.name, from)
                {
                    // Privatus langas seka pašnekovo nick'ą (kitaip jo
                    // tolesnės žinutės atidarytų naują langą).
                    let renamed = crate::sanitize::sanitize_chan_name(new_nick);
                    if !renamed.is_empty() {
                        ch.name = renamed;
                        names.push(ch.name.clone());
                    }
                }
            }
            (names, is_me)
        };
        for name in chans {
            let m = new_msg(now, from, new_nick, MsgType::Nick);
            self.add_msg(&name, if is_me { m } else { m.quiet() });
        }
        self.server_msg(&format!("{from} → {new_nick}"));
    }

    fn on_mode(&self, msg: &IrcLine, now: Ts) {
        let target = msg.param(0);
        let mode_str = msg.param(1);
        let params = &msg.params;
        if !self.is_channel(target) {
            // Vartotojo režimai (MODE mano_nick +i) anksčiau dingdavo be žinios.
            if params.len() > 1 {
                self.server_msg(&format!(
                    "Mode {}: {}",
                    sanitize_text(target),
                    sanitize_text(&params[1..].join(" "))
                ));
            }
            return;
        }
        let (_, signs, modes, _, chan_modes) = self.isup_snapshot();
        let args: &[String] = params.get(2..).unwrap_or(&[]);
        // Count EVERY mode that takes an argument, per the server's
        // CHANMODES, or the argument index drifts and the status prefix lands
        // on the wrong nick.
        let groups = parse_chan_modes(&chan_modes);
        let changes = walk_chan_modes(mode_str, args.len(), &groups, &modes);
        {
            let mut st = self.mu.lock();
            // MODE for a channel we are not in must not create a window.
            let Some(ci) = st.find_ch(target) else { return };
            for &(adding, mr, arg) in &changes {
                if modes.contains(mr) {
                    if let Some(i) = arg {
                        let spec = format!("{}{mr}", if adding { '+' } else { '-' });
                        st.channels[ci].set_mode(&args[i], &spec, Some(&signs), Some(&modes));
                    }
                }
            }
            let name = st.channels[ci].name.clone();
            track_chan_key(&mut st, &name, &changes, args);
        }
        let desc = if params.len() > 1 {
            masked_mode_desc(mode_str, args, &changes)
        } else {
            String::new()
        };
        self.add_msg(
            target,
            new_msg(now, "", &format!("Mode {target}: {desc}"), MsgType::Server),
        );
        self.queue_redraw();
    }

    /// 324 RPL_CHANNELMODEIS: `<me> <kanalas> <režimai> [argumentai…]`.
    /// Raktas (+k) įsimenamas ir rodomas paslėptas (anksčiau visa eilutė su
    /// raktu keliaudavo į *server* langą ir žurnalą).
    fn on_channel_mode_is(&self, params: &[String], now: Ts) {
        let (Some(chan), Some(mode_str)) = (params.get(1), params.get(2)) else {
            return;
        };
        let args: &[String] = params.get(3..).unwrap_or(&[]);
        let (_, _, modes, _, chan_modes) = self.isup_snapshot();
        let groups = parse_chan_modes(&chan_modes);
        let changes = walk_chan_modes(mode_str, args.len(), &groups, &modes);
        let dest = {
            let mut st = self.mu.lock();
            match st.find_ch(chan) {
                Some(ci) => {
                    let name = st.channels[ci].name.clone();
                    track_chan_key(&mut st, &name, &changes, args);
                    name
                }
                None => SERVER_WINDOW.to_string(),
            }
        };
        let desc = masked_mode_desc(mode_str, args, &changes);
        self.add_msg(
            &dest,
            new_msg(
                now,
                "",
                &format!("Kanalo {} režimai: {desc}", sanitize_text(chan)),
                MsgType::Server,
            ),
        );
        self.queue_redraw();
    }

    // ---- messages ----

    /// STATUSMSG tikslas („@#kanalas“, „+#kanalas“) → kanalo vardas; kiti
    /// tikslai nekeičiami.
    fn statusmsg_target<'a>(&self, target: &'a str) -> &'a str {
        if self.is_channel(target) {
            return target;
        }
        let signs = self.isup.read().prefix_signs.clone();
        let chan = target.trim_start_matches(|c: char| signs.contains(c));
        if chan.len() != target.len() && self.is_channel(chan) {
            chan
        } else {
            target
        }
    }

    fn on_privmsg(&self, msg: &IrcLine, now: Ts) {
        // „@#kanalas“ anksčiau atidarydavo privatų langą „@#kanalas“.
        let target = self.statusmsg_target(msg.param(0));
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
                // Ta pati taisyklė kaip PRIVMSG: PM, nick kaip žodis, highlight_words.
                let plain = strip_irc_format(action);
                let plain_low = plain.to_lowercase();
                let hl = self.nick_equal(target, &me)
                    || contains_nick_cm(&plain, &me, &self.casemapping())
                    || self
                        .mu
                        .lock()
                        .highlight_words
                        .iter()
                        .any(|w| plain_low.contains(&w.to_lowercase()));
                self.add_msg(dest, new_msg_hl(now, from, action, MsgType::Action, hl));
            } else if inner.starts_with("VERSION") {
                let enabled = self.mu.lock().ctcp_version;
                if enabled && self.allow_ctcp_reply() {
                    self.send(&format!(
                        "NOTICE {from} :\x01VERSION mircBSD {VERSION} ({}/Rust)\x01",
                        os_label()
                    ));
                }
            } else if inner.starts_with("PING") {
                // Bounded reflection: no amplification.
                let payload = cut_utf8(inner, MAX_CTCP_BYTES);
                if self.allow_ctcp_reply() {
                    self.send(&format!("NOTICE {from} :\x01{payload}\x01"));
                }
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
        let mut hl = is_pm || contains_nick_cm(&plain, &me, &self.casemapping());
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

    /// CTCP reply budget: at most `MAX_CTCP_REPLIES` per `CTCP_REPLY_WINDOW`
    /// (sliding window). Over budget the request is silently ignored.
    fn allow_ctcp_reply(&self) -> bool {
        let now = std::time::Instant::now();
        let mut st = self.mu.lock();
        st.ctcp_replies
            .retain(|t| now.saturating_duration_since(*t) < CTCP_REPLY_WINDOW);
        if st.ctcp_replies.len() >= MAX_CTCP_REPLIES {
            return false;
        }
        st.ctcp_replies.push(now);
        true
    }

    fn on_away(&self, msg: &IrcLine, now: Ts) {
        let from = msg.from_nick();
        if from.is_empty() {
            return;
        }
        let reason = msg.param(0);
        let cm = self.casemapping();
        let key = crate::validate::fold_name(from, &cm);
        let ttl = crate::timeutil::minutes(AWAY_THROTTLE_MINUTES);

        // (active window name, its nicks, signs) copied under the lock.
        let mut announce: Option<(String, Vec<String>, String, String)> = None;
        {
            let mut st = self.mu.lock();
            if !reason.is_empty() {
                let fresh = match st.away_notified.get(&key) {
                    Some(last) => now.duration_since(*last) >= ttl,
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
            has_password: !st.sasl_pass.is_empty() && st.creds_allowed(),
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

    /// Collect offered capability names, at most [`MAX_CAP_NAMES`] (an endless
    /// `CAP LS *` continuation must not buffer unbounded data).
    fn add_cap_names(st: &mut State, caps: &str) {
        for name in parse_cap_names(caps) {
            if st.cap_available.len() >= MAX_CAP_NAMES {
                break;
            }
            st.cap_available.insert(name);
        }
    }

    /// `CAP NEW`: server offers capabilities after registration.
    pub fn handle_cap_new(&self, caps: &str) {
        let mut st = self.mu.lock();
        Self::add_cap_names(&mut st, caps);
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
                Self::add_cap_names(&mut st, caps);
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
                        // Only capabilities we can have requested: an ACK
                        // for anything else is ignored (bounded set).
                        if name != "sasl" && !IRCV3_WANTED_CAPS.contains(&name.as_str()) {
                            continue;
                        }
                        if name == "sasl" {
                            sasl_acked = true;
                        }
                        st.active_caps.insert(name);
                    }
                    // Start AUTHENTICATE only if WE asked for sasl and have a credential.
                    if !st.cap_sasl
                        || (st.sasl_mech != "EXTERNAL"
                            && (st.sasl_pass.is_empty() || !st.creds_allowed()))
                    {
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
                // Atsakymas į MŪSŲ „CAP LIST“ — tik parodom. Atsiuntus
                // „CAP LIST“ atgal serveris vėl atsakytų sąrašu → begalinis ciklas.
                self.server_msg(&format!("Aktyvios CAP: {}", sanitize_text(caps)));
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
                st.sasl_pending && st.creds_allowed(),
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

/// Kanalo režimų eilutė → (pridedama?, raidė, argumento indeksas `args`
/// sąraše). Argumentą ima PREFIX režimai ir CHANMODES A/B grupės, C — tik
/// pridedant; trūkstamas argumentas — `None` (indeksas nepasislenka).
pub(crate) fn walk_chan_modes(
    mode_str: &str,
    nargs: usize,
    groups: &crate::proto::isupport::ChanModeGroups,
    prefix_modes: &str,
) -> Vec<(bool, char, Option<usize>)> {
    let mut out = Vec::new();
    let mut adding = true;
    let mut idx = 0;
    for mr in mode_str.chars() {
        match mr {
            '+' => adding = true,
            '-' => adding = false,
            _ => {
                let arg = if groups.takes_param(mr, adding, prefix_modes) && idx < nargs {
                    idx += 1;
                    Some(idx - 1)
                } else {
                    None
                };
                out.push((adding, mr, arg));
            }
        }
    }
    out
}

/// +k / -k → įsiminti arba pamiršti kanalo raktą. „*“ (kai kurie serveriai
/// taip slepia raktą ne operatoriams) nieko nekeičia.
fn track_chan_key(
    st: &mut State,
    chan: &str,
    changes: &[(bool, char, Option<usize>)],
    args: &[String],
) {
    for &(adding, mr, arg) in changes {
        if mr != 'k' {
            continue;
        }
        if !adding {
            st.forget_chan_key(chan);
        } else if let Some(key) = arg.map(|i| args[i].as_str()) {
            if key != "*" {
                st.set_chan_key(chan, key);
            }
        }
    }
}

/// Režimų eilutė rodymui (ir žurnalui): +k / -k argumentas pakeičiamas
/// [`HIDDEN_KEY`] — raktas nerodomas ir nerašomas į žurnalą / eksportą.
fn masked_mode_desc(
    mode_str: &str,
    args: &[String],
    changes: &[(bool, char, Option<usize>)],
) -> String {
    let mut shown: Vec<&str> = args.iter().map(String::as_str).collect();
    for &(_, mr, arg) in changes {
        if let (true, Some(i)) = (mr == 'k', arg) {
            shown[i] = HIDDEN_KEY;
        }
    }
    let mut desc = mode_str.to_string();
    for a in shown {
        desc.push(' ');
        desc.push_str(a);
    }
    desc
}

/// `nick!user@host` → sanitized nick (MONITOR numerics).
fn monitor_nick(mask: &str) -> String {
    let nick = mask.split('!').next().unwrap_or("");
    sanitize_text(nick.trim())
}

#[cfg(test)]
#[path = "handlers_tests.rs"]
mod tests;

#[cfg(test)]
#[path = "handlers_cap_tests.rs"]
mod cap_tests;

#[cfg(test)]
#[path = "casemap_tests.rs"]
mod casemap_tests;
