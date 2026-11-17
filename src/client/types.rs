//! Client state: `Message`, `Channel` and the `Client` struct (mirrors the Go
//! `Client` in `irc.go`).
//!
//! Locking model (same as Go):
//! * `Client::mu` — everything mutable (Go `c.mu`). `parking_lot` mutexes are
//!   NOT reentrant: never call a method that locks `mu` (`add_msg`,
//!   `server_msg`, `send`, …) while holding the guard.
//! * `Client::isup` — ISUPPORT parameters (Go `isupMu`). Lock order is
//!   `mu` → `isup`; never take `mu` while holding `isup`.
//!
//! `Message` / `Channel` / `MsgType` come from [`crate::ui`].

use std::collections::{HashMap, HashSet};
use std::net::TcpStream;
use std::sync::atomic::AtomicBool;
use std::sync::mpsc::{sync_channel, Receiver, SyncSender};
use std::sync::Arc;
use std::time::{Duration, Instant};

use chrono::{DateTime, Utc};
use parking_lot::{Condvar, Mutex, RwLock};

use crate::config::{expand_home_path, Config};
use crate::proto::isupport::Isup;
use crate::validate::{is_valid_nick, is_valid_port, is_valid_server, norm_nick};

use super::send::WriteHandle;

/// Name of the always-present internal window.
pub const SERVER_WINDOW: &str = "*server*";

// ---- Message / Channel (shared with the UI) ----

pub use crate::ui::{Channel, Message, MsgType};

/// Plain message (no highlight) — shorthand for `Message::new(.., false)`.
pub fn new_msg(ts: DateTime<Utc>, nick: &str, text: &str, msg_type: MsgType) -> Message {
    Message::new(ts, nick, text, msg_type, false)
}

/// Message with an explicit highlight flag.
pub fn new_msg_hl(
    ts: DateTime<Utc>,
    nick: &str,
    text: &str,
    msg_type: MsgType,
    hl: bool,
) -> Message {
    Message::new(ts, nick, text, msg_type, hl)
}

// ---- Log queue entry ----

#[derive(Clone, Debug)]
pub struct LogEntry {
    pub log_dir: String,
    pub server: String,
    pub chan_name: String,
    pub msg: Message,
}

/// Callback for a command to run after registration.
pub type PerformHook = Arc<dyn Fn(&str) + Send + Sync>;
/// Callback receiving a pair of text arguments.
pub type TextPairHook = Arc<dyn Fn(&str, &str) + Send + Sync>;

/// Optional callbacks the embedding application can install.
#[derive(Clone, Default)]
pub struct Hooks {
    /// Run a `perform` line (a `/command` or raw IRC) after registration
    /// (Go `handleInput`).
    pub perform: Option<PerformHook>,
    /// Desktop notification: (title, body).
    pub notify: Option<TextPairHook>,
    /// Called once from `Client::stop` (after history is saved): the
    /// embedding application drains the log queue and closes log files here
    /// (Go `stopLogWorker` + `closeLogs`).
    pub on_stop: Option<Arc<dyn Fn() + Send + Sync>>,
    /// Called by `/clearlogs` BEFORE files are deleted so a log writer can
    /// close its open handles: (server, channel — `""` = every channel).
    pub close_logs: Option<TextPairHook>,
}

// ---- Client ----

/// Everything guarded by `Client::mu` (Go `c.mu`).
pub struct State {
    pub nick: String,
    pub server: String,
    pub port: String,
    pub use_tls: bool,
    /// Clone of the socket used to force-close the current connection.
    pub conn: Option<TcpStream>,
    pub connected: bool,

    pub real_name: String,
    pub nickserv_pass: String,
    pub sasl_pass: String,
    pub sasl_external: bool,
    pub tls_cert_path: String,
    pub tls_key_path: String,
    pub tls_pinning: bool,
    pub tls_pin: String,
    /// "PLAIN" / "EXTERNAL" — mechanism that was started.
    pub sasl_mech: String,
    pub server_pass: String,
    /// The configured server the credentials above (PASS, SASL, NickServ
    /// password, `perform`) belong to; "" = not bound. See
    /// [`State::creds_allowed`].
    pub cred_server: String,
    pub auto_join: Vec<String>,
    pub away: String,
    pub perform: Vec<String>,
    pub ignore_list: Vec<String>,

    pub reconnect: bool,
    pub reconnect_delay: Duration,
    /// Epoch of the running connect loop; -1 = not connecting.
    pub connecting_gen: i64,
    pub ctcp_version: bool,
    pub notify: bool,
    pub mouse: bool,
    pub msg_area_top: i32,
    pub last_notify: Option<Instant>,

    // IRCv3 CAP / SASL
    pub active_caps: HashSet<String>,
    pub cap_sasl: bool,
    pub sasl_ok: bool,
    pub sasl_pending: bool,
    pub nick_attempts: u32,
    /// Serverio CASEMAPPING kopija (Isup → State, kad langų vardų paieška
    /// veiktų laikant tik `mu`; lock tvarka mu → isup to neleistų atvirkščiai).
    pub casemapping: String,
    /// 001 gautas šiame ryšyje (po jo 433 — tik klaida, ne automatinis
    /// nick keitimas; /nick laukia serverio NICK patvirtinimo).
    pub registered: bool,
    /// Kanalai, į kuriuos grįžtama po (pakartotinės) registracijos: pildoma
    /// nutrūkus ryšiui (kanalai, kuriuose buvome) ir /join be ryšio.
    pub rejoin: Vec<String>,
    /// Connection epoch: /connect and /reconnect bump it; stale threads exit.
    pub connect_gen: i64,
    pub cap_available: HashSet<String>,

    pub last_urls: Vec<String>,

    // MONITOR (/notify)
    pub monitor_list: Vec<String>,
    pub monitor_online: HashSet<String>,

    pub custom_binds: HashMap<String, String>,
    pub highlight_words: Vec<String>,

    /// Away throttle: lowercased nick → last notice time (bounded).
    pub away_notified: HashMap<String, DateTime<Utc>>,

    /// Where WHO / WHOIS replies go ("" = *server*).
    pub who_chan: String,
    pub whois_chan: String,

    // Paste protection
    pub paste_buf: Vec<String>,
    pub paste_partial: String,
    pub paste_collect_until: Option<Instant>,

    pub last_pong: Instant,
    /// Times of recent CTCP replies (rate limit, at most `MAX_CTCP_REPLIES`).
    pub ctcp_replies: Vec<Instant>,

    pub channels: Vec<Channel>,
    pub active_idx: usize,
    pub max_messages: i32,
    /// Global budget for stored messages over ALL windows, in bytes
    /// (`maxmemory`); see `State::enforce_memory_budget`.
    pub max_buffer_bytes: usize,

    /// Last channel whose message was dropped because of the channel limit.
    pub chan_limit_hit: String,
    pub last_drawn: Option<String>,

    /// Writer handle of the current connection.
    pub write: Option<WriteHandle>,

    // Terminal / input state (owned by the UI and input modules)
    pub width: i32,
    pub height: i32,
    pub input: Vec<char>,
    pub input_pos: usize,
    pub scroll_off: i32,
    pub nick_scroll_off: i32,
    pub chan_scroll_off: i32,
    pub chase_until: Option<Instant>,
    pub history: Vec<String>,
    pub history_idx: i64,
    pub tab_candidates: Vec<String>,
    pub tab_idx: usize,
    pub tab_prefix: String,
    /// Įvesties tekstas už kursoriaus Tab pradžioje (išlaikomas užbaigiant).
    pub tab_tail: String,
    pub keys_debug: bool,
    pub search_mode: bool,
    pub search_query: String,
    pub search_result_idx: i64,
    pub last_search_query: String,

    pub log_dir: String,
}

pub struct Client {
    /// Go `c.mu`.
    pub mu: Mutex<State>,
    /// Go `isupMu` + protected fields.
    pub isup: RwLock<Isup>,
    pub hooks: RwLock<Hooks>,
    redraw_tx: SyncSender<()>,
    /// UI side of the redraw signal (capacity 1; coalesced).
    pub redraw_rx: Mutex<Receiver<()>>,
    log_tx: SyncSender<LogEntry>,
    /// Consumer side of the log queue (taken by a log worker).
    pub log_rx: Mutex<Receiver<LogEntry>>,
    /// `stop()` ran (guards the one-shot shutdown work, Go `stopOnce`).
    pub(super) stop_once: AtomicBool,
    /// Shutdown finished (Go `stopCh` closed); see `wait_stop`.
    pub(super) stop_done: Mutex<bool>,
    pub(super) stop_cv: Condvar,
}

impl Client {
    /// Build a client from configuration (`newClient`). Server, nick and port
    /// are validated here so every entry path is covered.
    pub fn new(cfg: &Config) -> Arc<Client> {
        let server = if is_valid_server(&cfg.server) {
            cfg.server.clone()
        } else {
            String::new()
        };
        let nick = if is_valid_nick(&cfg.nick) {
            cfg.nick.clone()
        } else {
            "mircbsd".to_string()
        };
        let port = if is_valid_port(&cfg.port) {
            cfg.port.clone()
        } else {
            "6667".to_string()
        };

        let cred_server = server.clone();
        let st = State {
            nick,
            server,
            port,
            use_tls: cfg.tls,
            conn: None,
            connected: false,
            real_name: cfg.real_name.clone(),
            nickserv_pass: cfg.nick_serv.clone(),
            sasl_pass: cfg.sasl_pass.clone(),
            sasl_external: cfg.sasl_external,
            tls_cert_path: expand_home_path(&cfg.tls_cert),
            tls_key_path: expand_home_path(&cfg.tls_key),
            tls_pinning: cfg.tls_pinning,
            tls_pin: cfg.tls_pin.clone(),
            sasl_mech: String::new(),
            server_pass: cfg.server_pass.clone(),
            cred_server,
            auto_join: cfg.auto_join.clone(),
            away: String::new(),
            perform: cfg.perform.clone(),
            ignore_list: Vec::new(),
            reconnect: cfg.reconnect,
            reconnect_delay: Duration::from_secs(5),
            connecting_gen: -1,
            ctcp_version: cfg.ctcp_version,
            notify: cfg.notifications,
            mouse: cfg.mouse,
            msg_area_top: 0,
            last_notify: None,
            active_caps: HashSet::new(),
            cap_sasl: false,
            sasl_ok: false,
            sasl_pending: false,
            nick_attempts: 0,
            casemapping: "rfc1459".into(),
            registered: false,
            rejoin: Vec::new(),
            connect_gen: 0,
            cap_available: HashSet::new(),
            last_urls: Vec::new(),
            monitor_list: Vec::new(),
            monitor_online: HashSet::new(),
            custom_binds: HashMap::new(),
            highlight_words: cfg.highlight_words.clone(),
            away_notified: HashMap::new(),
            who_chan: String::new(),
            whois_chan: String::new(),
            paste_buf: Vec::new(),
            paste_partial: String::new(),
            paste_collect_until: None,
            last_pong: Instant::now(),
            ctcp_replies: Vec::new(),
            channels: vec![Channel::new(
                SERVER_WINDOW,
                cfg.max_messages.max(0) as usize,
            )],
            active_idx: 0,
            max_messages: cfg.max_messages,
            max_buffer_bytes: crate::config::max_memory_bytes(cfg.max_memory_mb),
            chan_limit_hit: String::new(),
            last_drawn: None,
            write: None,
            width: 0,
            height: 0,
            input: Vec::new(),
            input_pos: 0,
            scroll_off: 0,
            nick_scroll_off: 0,
            chan_scroll_off: 0,
            chase_until: None,
            history: Vec::new(),
            history_idx: -1,
            tab_candidates: Vec::new(),
            tab_idx: 0,
            tab_prefix: String::new(),
            tab_tail: String::new(),
            keys_debug: false,
            search_mode: false,
            search_query: String::new(),
            search_result_idx: -1,
            last_search_query: String::new(),
            log_dir: cfg.log_dir.clone(),
        };

        let (redraw_tx, redraw_rx) = sync_channel(1);
        let (log_tx, log_rx) = sync_channel(1024);
        Arc::new(Client {
            mu: Mutex::new(st),
            isup: RwLock::new(Isup::default()),
            hooks: RwLock::new(Hooks::default()),
            redraw_tx,
            redraw_rx: Mutex::new(redraw_rx),
            log_tx,
            log_rx: Mutex::new(log_rx),
            stop_once: AtomicBool::new(false),
            stop_done: Mutex::new(false),
            stop_cv: Condvar::new(),
        })
    }

    /// Coalescing redraw request (`queueRedraw`).
    pub fn queue_redraw(&self) {
        let _ = self.redraw_tx.try_send(());
    }

    /// Queue a message for the log worker; dropped when the queue is full.
    pub fn enqueue_log(&self, e: LogEntry) {
        if e.log_dir.is_empty() {
            return;
        }
        let _ = self.log_tx.try_send(e);
    }

    // ---- small safe accessors ----

    pub fn is_connected(&self) -> bool {
        self.mu.lock().connected
    }

    pub fn my_nick(&self) -> String {
        self.mu.lock().nick.clone()
    }

    pub fn has_cap(&self, name: &str) -> bool {
        self.mu.lock().active_caps.contains(name)
    }

    /// Consistent copy of ISUPPORT parameters:
    /// (chantypes, prefix_signs, prefix_modes, casemapping, chanmodes).
    pub fn isup_snapshot(&self) -> (String, String, String, String, String) {
        let i = self.isup.read();
        (
            i.chantypes.clone(),
            i.prefix_signs.clone(),
            i.prefix_modes.clone(),
            i.casemapping.clone(),
            i.chan_modes.clone(),
        )
    }

    /// Apply 005 tokens.
    pub fn apply_isupport<S: AsRef<str>>(&self, tokens: &[S]) {
        let cm = {
            let mut i = self.isup.write();
            i.apply(tokens);
            i.casemapping.clone()
        };
        self.mu.lock().casemapping = cm;
    }

    /// Current server CASEMAPPING.
    pub fn casemapping(&self) -> String {
        self.isup.read().casemapping.clone()
    }

    /// Is `name` a channel per CHANTYPES?
    pub fn is_channel(&self, name: &str) -> bool {
        self.isup.read().is_channel(name)
    }

    /// Nick equality under the server casemapping.
    pub fn nick_equal(&self, a: &str, b: &str) -> bool {
        let i = self.isup.read();
        crate::validate::nick_equal(a, b, &i.casemapping)
    }

    /// MONITOR map key: nick folded by the server casemapping.
    pub fn mon_key(&self, nick: &str) -> String {
        norm_nick(nick, "", &self.isup.read().casemapping)
    }

    pub fn is_valid_nick_len(&self, nick: &str) -> bool {
        nick.chars().count() as i32 <= self.isup.read().nick_len_limit()
    }

    pub fn nick_len_limit(&self) -> i32 {
        self.isup.read().nick_len_limit()
    }

    pub fn channel_len_limit(&self) -> i32 {
        self.isup.read().channel_len_limit()
    }

    pub fn topic_len_limit(&self) -> i32 {
        self.isup.read().topic_len_limit()
    }

    pub fn kick_len_limit(&self) -> i32 {
        self.isup.read().kick_len_limit()
    }

    pub fn set_who_target(&self, target: &str) {
        self.mu.lock().who_chan = target.to_string();
    }

    pub fn set_whois_target(&self, target: &str) {
        self.mu.lock().whois_chan = target.to_string();
    }

    /// Persist the MONITOR list (≤100 entries).
    pub fn save_monitor(&self) {
        let Some(path) = crate::config::monitor_path() else {
            return;
        };
        let list = {
            let mut st = self.mu.lock();
            st.monitor_list.truncate(100);
            st.monitor_list.clone()
        };
        if let Err(err) = crate::persist::save_monitor(&path, &list) {
            self.err_msg(&format!(
                "Stebėjimo sąrašas neišsaugotas ({}): {err}",
                path.display()
            ));
        }
    }
}

impl State {
    /// May the configured credentials (server PASS, SASL PLAIN password,
    /// NickServ IDENTIFY, `perform`) be sent to the CURRENT server? Only to
    /// the server they were configured for: `/connect other.net …` must not
    /// hand the passwords to another network's operators. Unbound
    /// credentials (no server in the config) keep the old behaviour.
    pub fn creds_allowed(&self) -> bool {
        self.cred_server.is_empty() || self.server.eq_ignore_ascii_case(&self.cred_server)
    }

    /// Ryšys nutrūko: įsimename kanalus, kuriuose buvome (grįšime po 001),
    /// ir išvalome nick'ų sąrašus — jie nebeatitinka tikrovės (be JOIN/PART
    /// srauto senas sąrašas liktų amžinai, o po grįžimo 353 jį tik papildytų).
    pub fn note_disconnect(&mut self) {
        self.registered = false;
        let me = self.nick.clone();
        let mut rejoin = std::mem::take(&mut self.rejoin);
        for ch in self.channels.iter_mut() {
            if ch.name != SERVER_WINDOW
                && !ch.is_private
                && ch.has_nick(&me)
                && !rejoin
                    .iter()
                    .any(|r| crate::validate::nick_equal(r, &ch.name, &self.casemapping))
            {
                rejoin.push(ch.name.clone());
            }
            ch.clear_nicks();
        }
        self.rejoin = rejoin;
    }

    /// Insert the "new messages" separator before the first unread message.
    pub(crate) fn insert_unread_separator(target: &mut Channel) {
        if target.unread > 0
            && target.unread_from > 0
            && (target.unread_from as usize) < target.messages.len()
        {
            let mut at = target.unread_from as usize;
            // Vienintelė žyma: ankstesnė (jau perskaityta) pašalinama, kitaip
            // kiekvienas apsilankymas palikdavo dar vieną „naujos žinutės“.
            if let Some(old) = target.messages[..at].iter().rposition(is_unread_separator) {
                target.remove_msg(old);
                at -= 1;
            }
            let sep = new_msg(Utc::now(), "", UNREAD_SEPARATOR, MsgType::Server);
            target.insert_msg(at, sep);
            target.render_gen += 1;
            target.invalidate_render_cache();
        }
    }
}

const UNREAD_SEPARATOR: &str = "── naujos žinutės ──";

fn is_unread_separator(m: &Message) -> bool {
    m.msg_type == MsgType::Server && m.nick.is_empty() && m.text == UNREAD_SEPARATOR
}
