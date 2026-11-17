//! mircBSD terminal client (port of `main.go` plus the log worker from `irc.go`).

use std::collections::HashMap;
use std::fs::{self, File, OpenOptions};
use std::io::{self, Write};
use std::path::Path;
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, RecvTimeoutError};
use std::sync::{Arc, Mutex as StdMutex, Once};
use std::thread;
use std::time::Duration;

use chrono::Local;
use crossterm::terminal::{disable_raw_mode, enable_raw_mode, size};
use crossterm::tty::IsTty;
use parking_lot::Mutex;

use mircbsd::client::session::{term_write, MOUSE_OFF, MOUSE_ON};
use mircbsd::client::types::LogEntry;
use mircbsd::client::{os_label, Client, MsgType, VERSION};
use mircbsd::config::{config_path, default_config, load_config, Config};
use mircbsd::input::{is_sensitive_history, is_sensitive_log_line};
use mircbsd::sanitize::{sanitize_path, sanitize_text};
use mircbsd::ui::{
    format_go_time, set_highlight_color, set_nick_colors, set_theme, set_timestamps, set_ts_color,
    strip_irc_format,
};
use mircbsd::validate::{is_valid_nick, is_valid_port, is_valid_server};

fn main() {
    if !io::stdin().is_tty() {
        eprintln!("Reikalingas terminalas (TTY)");
        std::process::exit(1);
    }
    if let Err(e) = enable_raw_mode() {
        eprintln!("Nepavyko nustatyti raw terminalo: {e}");
        std::process::exit(1);
    }
    let _raw = RawTerm;
    install_panic_hook();
    // Block SIGINT / SIGTERM / SIGWINCH before any worker thread exists so
    // every child inherits the mask and only the sigwait thread receives them.
    let sigset = block_app_signals();

    let (mut cfg, mut warns) = match config_path() {
        Some(path) => load_config(&path),
        None => (default_config(), Vec::new()),
    };
    set_ts_color(&cfg.timestamp_color);
    set_timestamps(cfg.timestamps, &cfg.ts_format);
    set_highlight_color(&cfg.highlight_color);
    set_nick_colors(cfg.nick_colors);
    set_theme(&cfg.theme);
    apply_cli(&mut cfg, &mut warns);

    let client = Client::new(&cfg);
    if cfg.mouse {
        term_write(MOUSE_ON);
    }
    client.load_history();
    client.load_ignore();
    client.load_monitor();
    warns.extend(client.load_keys());

    client.install_input_hooks();
    client.hooks.write().notify = Some(Arc::new(notify_desktop));
    start_log_worker(&client);
    if let Some(set) = sigset {
        let c = Arc::clone(&client);
        if thread::Builder::new()
            .name("mircbsd-signal".into())
            .spawn(move || signal_loop(c, set))
            .is_err()
        {
            // `set` is Copy, so the mask can be lifted when the thread did not start.
            unblock_app_signals(&set);
        }
    }

    apply_term_size(&client);
    term_write("\x1b[2J\x1b[H");
    welcome(&client, &cfg, &warns);

    if !cfg.server.is_empty() {
        let c = Arc::clone(&client);
        let _ = thread::Builder::new()
            .name("mircbsd-connect".into())
            .spawn(move || c.connect_loop());
    }

    client.draw();
    let keys = Arc::clone(&client);
    let _ = thread::Builder::new()
        .name("mircbsd-keys".into())
        .spawn(move || keys.read_keys());

    loop {
        if client.is_stopped() {
            break;
        }
        let woke = {
            let rx = client.redraw_rx.lock();
            rx.recv_timeout(Duration::from_millis(200))
        };
        if client.is_stopped() {
            break;
        }
        match woke {
            Ok(()) => {
                if draw_guarded(&client) {
                    break;
                }
            }
            Err(RecvTimeoutError::Timeout) => {
                if apply_term_size(&client) && draw_guarded(&client) {
                    break;
                }
            }
            Err(RecvTimeoutError::Disconnected) => break,
        }
    }

    restore_terminal();
    println!("Viso gero!");
}

/// `true` when a panic in `draw` already requested shutdown.
fn draw_guarded(client: &Client) -> bool {
    let drawn = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| client.draw()));
    if drawn.is_err() {
        client.stop();
        true
    } else {
        false
    }
}

fn welcome(client: &Client, cfg: &Config, warns: &[String]) {
    client.server_msg(&format!(
        "Sveiki! mircBSD {VERSION} - IRC klientas ({})",
        os_label()
    ));
    match config_path() {
        Some(path) => client.server_msg(&format!("Config: {}", path.display())),
        None => {
            client.err_msg("Nepavyko nustatyti konfigūracijos kelio (naudotojo katalogas nerastas)")
        }
    }
    for w in warns {
        client.err_msg(&format!("ĮSPĖJIMAS: {w}"));
    }
    if !cfg.server.is_empty() {
        client.server_msg(&format!(
            "Serveris: {}:{} (TLS:{}, reconnect:{})",
            cfg.server, cfg.port, cfg.tls, cfg.reconnect
        ));
    } else {
        client.server_msg("Naudojimas: /connect <serveris> <portas> [nick] [tls]");
        client.server_msg("Pavyzdys:   /connect irc.libera.chat 6697 manick tls");
    }
    client.server_msg("/help - komandų sąrašas  |  Ctrl+R - paieška");
}

/// `mircbsd [server port [nick] [tls]]`. Invalid values become warnings and
/// do not replace a valid config nick; an invalid server or port is cleared
/// (same as the Go client).
fn apply_cli(cfg: &mut Config, warns: &mut Vec<String>) {
    let args: Vec<String> = std::env::args().skip(1).collect();
    apply_cli_args(cfg, warns, &args);
}

/// [`apply_cli`] on explicit arguments. A server other than the configured
/// one does NOT inherit the config passwords (PASS, SASL, NickServ,
/// `perform`): they were written for the configured network.
fn apply_cli_args(cfg: &mut Config, warns: &mut Vec<String>, args: &[String]) {
    if args.len() < 2 {
        return;
    }
    let mut server = args[0].clone();
    let mut port = args[1].clone();
    if !is_valid_server(&server) {
        warns.push(format!("Neteisingas serverio vardas: {server}"));
        server.clear();
    }
    if !is_valid_port(&port) {
        warns.push(format!("Neteisingas portas: {port}"));
        port.clear();
    }
    if !cfg.server.is_empty() && !server.eq_ignore_ascii_case(&cfg.server) {
        let had = !cfg.server_pass.is_empty()
            || !cfg.sasl_pass.is_empty()
            || !cfg.nick_serv.is_empty()
            || !cfg.perform.is_empty();
        cfg.server_pass.clear();
        cfg.sasl_pass.clear();
        cfg.nick_serv.clear();
        cfg.perform.clear();
        if had {
            warns.push(format!(
                "Serveris {server} ≠ konfigūracijos {} — config slaptažodžiai ir perform nenaudojami",
                cfg.server
            ));
        }
    }
    cfg.server = server;
    cfg.port = port;
    if args.len() >= 3 {
        if !is_valid_nick(&args[2]) {
            warns.push(format!("Neteisingas nick: {}", args[2]));
        } else {
            cfg.nick = args[2].clone();
        }
    }
    if args.len() >= 4 && args[3] == "tls" {
        cfg.tls = true;
    }
}

/// Update `width`/`height`. Returns whether the size changed.
fn apply_term_size(client: &Client) -> bool {
    match size() {
        Ok((w, h)) => {
            let mut st = client.mu.lock();
            let changed = st.width != i32::from(w) || st.height != i32::from(h);
            st.width = i32::from(w);
            st.height = i32::from(h);
            changed
        }
        Err(_) => false,
    }
}

fn install_panic_hook() {
    let default = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        restore_terminal();
        default(info);
    }));
}

fn restore_terminal() {
    static ONCE: Once = Once::new();
    ONCE.call_once(|| {
        let _ = disable_raw_mode();
        let mut out = io::stdout();
        let _ = write!(out, "{MOUSE_OFF}\x1b[?25h\x1b[2J\x1b[H");
        let _ = out.flush();
    });
}

struct RawTerm;

impl Drop for RawTerm {
    fn drop(&mut self) {
        restore_terminal();
    }
}

/// Desktop notification via `notify-send` (`notifyDesktop`). Missing binary
/// is ignored. `--` keeps a server-controlled title from being parsed as a flag.
fn notify_desktop(title: &str, body: &str) {
    let (title, body) = notify_texts(title, body);
    let mut child = match Command::new("notify-send")
        .args(["-a", "mircBSD", "-t", "5000", "--", &title, &body])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
    {
        Ok(child) => child,
        Err(_) => return,
    };
    thread::spawn(move || {
        let _ = child.wait();
    });
}

/// Title and body for `notify-send`: sanitized and clipped. The BODY is
/// interpreted as markup by freedesktop notification daemons (`<a href>`,
/// `<img src>` …), so `&`, `<`, `>` from the network are escaped (after
/// clipping, so an entity is never cut in half).
fn notify_texts(title: &str, body: &str) -> (String, String) {
    let title = clip_runes(&sanitize_text(title), 60, false);
    let body = clip_runes(&sanitize_text(body), 120, true);
    (title, escape_markup(&body))
}

fn escape_markup(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            _ => out.push(c),
        }
    }
    out
}

fn clip_runes(s: &str, max: usize, ellipsis: bool) -> String {
    let chars: Vec<char> = s.chars().collect();
    if chars.len() <= max {
        return s.to_string();
    }
    let mut out: String = chars[..max].iter().collect();
    if ellipsis {
        out.push('…');
    }
    out
}

// ---- Log worker (irc.go logMessage) ----

fn start_log_worker(client: &Arc<Client>) {
    let rx = take_log_rx(client);
    let files = Arc::new(Mutex::new(HashMap::<String, File>::new()));
    let stop = Arc::new(AtomicBool::new(false));

    let files_w = Arc::clone(&files);
    let stop_w = Arc::clone(&stop);
    let client_w = Arc::downgrade(client);
    let handle = thread::Builder::new()
        .name("mircbsd-log".into())
        .spawn(move || log_loop(rx, files_w, stop_w, client_w))
        .ok();
    let handle = Arc::new(StdMutex::new(handle));

    let stop_hook = Arc::clone(&stop);
    let files_hook = Arc::clone(&files);
    let handle_hook = Arc::clone(&handle);
    client.hooks.write().on_stop = Some(Arc::new(move || {
        stop_hook.store(true, Ordering::SeqCst);
        let worker = handle_hook.lock().unwrap_or_else(|e| e.into_inner()).take();
        if let Some(h) = worker {
            let _ = h.join();
        }
        files_hook.lock().clear();
    }));

    let files_close = Arc::clone(&files);
    client.hooks.write().close_logs = Some(Arc::new(move |server, chan| {
        close_log_files(&mut files_close.lock(), server, chan);
    }));
}

fn take_log_rx(client: &Client) -> Receiver<LogEntry> {
    let (_tx, dummy) = std::sync::mpsc::sync_channel(1);
    let mut slot = client.log_rx.lock();
    std::mem::replace(&mut *slot, dummy)
}

fn log_loop(
    rx: Receiver<LogEntry>,
    files: Arc<Mutex<HashMap<String, File>>>,
    stop: Arc<AtomicBool>,
    client: std::sync::Weak<Client>,
) {
    let mut reported_error = false;
    loop {
        if stop.load(Ordering::SeqCst) {
            let mut open = files.lock();
            while let Ok(e) = rx.try_recv() {
                let _ = write_log_entry(&mut open, &e);
            }
            open.clear();
            return;
        }
        match rx.recv_timeout(Duration::from_millis(50)) {
            Ok(e) => {
                let mut open = files.lock();
                let mut error = write_log_entry(&mut open, &e).err();
                while let Ok(more) = rx.try_recv() {
                    if let Err(err) = write_log_entry(&mut open, &more) {
                        error.get_or_insert(err);
                    }
                }
                drop(open);
                if !reported_error {
                    if let (Some(err), Some(client)) = (error, client.upgrade()) {
                        reported_error = true;
                        client.err_msg(&format!("Žurnalo įrašyti nepavyko: {err}"));
                    }
                }
            }
            Err(RecvTimeoutError::Timeout) => {}
            Err(RecvTimeoutError::Disconnected) => {
                files.lock().clear();
                return;
            }
        }
    }
}

fn close_log_files(files: &mut HashMap<String, File>, server: &str, chan: &str) {
    if chan.is_empty() {
        if server.is_empty() {
            files.clear();
            return;
        }
        let prefix = format!("{server}/");
        files.retain(|k, _| !k.starts_with(&prefix));
        return;
    }
    let prefix = format!("{server}/{}/", sanitize_path(chan));
    files.retain(|k, _| !k.starts_with(&prefix));
}

fn write_log_entry(files: &mut HashMap<String, File>, e: &LogEntry) -> io::Result<()> {
    if e.log_dir.is_empty() {
        return Ok(());
    }
    let local = e.msg.ts.with_timezone(&Local);
    let day = format_go_time(&local, "2006-01-02");
    let chan = sanitize_path(&e.chan_name);
    let prefix = format!("{}/{chan}/", e.server);
    let key = format!("{prefix}{day}");
    if !files.contains_key(&key) {
        let stale: Vec<String> = files
            .keys()
            .filter(|k| k.starts_with(&prefix))
            .cloned()
            .collect();
        for k in stale {
            files.remove(&k);
        }
        let f = open_log_file(&e.log_dir, &e.server, &chan, &day)?;
        files.insert(key.clone(), f);
    }
    if let Some(f) = files.get_mut(&key) {
        let line = format_log_line(e);
        f.write_all(line.as_bytes())?;
    }
    Ok(())
}

/// `<log_dir>/<server>/<channel>-YYYY-MM-DD.log`, mode 0600, directory 0700.
///
/// The log file is opened with `O_NOFOLLOW` and both directories must belong
/// to us: in a shared log directory a planted symlink would otherwise make
/// server-controlled chat text land in another file (e.g. `~/.bashrc`).
fn open_log_file(log_dir: &str, server: &str, chan: &str, day: &str) -> io::Result<File> {
    let dir = Path::new(log_dir).join(sanitize_path(server));
    fs::create_dir_all(&dir)
        .map_err(|err| io::Error::new(err.kind(), format!("{}: {err}", dir.display())))?;
    restrict_dir(Path::new(log_dir));
    restrict_dir(&dir);
    ensure_own_dir(Path::new(log_dir))?;
    ensure_own_dir(&dir)?;
    let path = dir.join(format!("{chan}-{day}.log"));
    let mut opts = OpenOptions::new();
    opts.create(true).append(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600).custom_flags(libc::O_NOFOLLOW);
    }
    opts.open(&path)
        .map_err(|err| io::Error::new(err.kind(), format!("{}: {err}", path.display())))
}

/// The directory must be owned by the effective user (not someone else's
/// directory in a shared location).
fn ensure_own_dir(path: &Path) -> io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        let md = fs::metadata(path)?;
        // SAFETY: geteuid has no preconditions and cannot fail.
        let me = unsafe { libc::geteuid() };
        if md.uid() != me {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                format!("{}: katalogas priklauso kitam naudotojui", path.display()),
            ));
        }
    }
    #[cfg(not(unix))]
    {
        let _ = path;
    }
    Ok(())
}

fn restrict_dir(path: &Path) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(path, fs::Permissions::from_mode(0o700));
    }
    #[cfg(not(unix))]
    {
        let _ = path;
    }
}

/// One log line (`logMessage`). Passwords are replaced, never written.
fn format_log_line(e: &LogEntry) -> String {
    let local = e.msg.ts.with_timezone(&Local);
    let ts = format_go_time(&local, "2006-01-02 15:04:05");
    let plain = sanitize_text(&strip_irc_format(&e.msg.text));
    let nick = sanitize_text(&e.msg.nick);
    let mut sensitive = is_sensitive_log_line(&plain)
        || is_sensitive_log_line(&nick)
        || is_sensitive_log_line(&format!("{plain} {nick}"));
    if nick.eq_ignore_ascii_case("NickServ") || e.chan_name.eq_ignore_ascii_case("NickServ") {
        sensitive = sensitive || is_sensitive_history(&plain);
    }
    if sensitive {
        return format!("[{ts}] <{nick}> [slaptažodis paslėptas]\n");
    }
    match e.msg.msg_type {
        MsgType::Chat => format!("[{ts}] <{nick}> {plain}\n"),
        MsgType::Action => format!("[{ts}] * {nick} {plain}\n"),
        MsgType::Join => format!("[{ts}] --> {nick} prisijungė\n"),
        MsgType::Part => format!("[{ts}] <-- {nick} išėjo ({plain})\n"),
        MsgType::Quit => format!("[{ts}] <-- {nick} atsijungė ({plain})\n"),
        MsgType::Nick => format!("[{ts}] *** {nick} → {plain}\n"),
        MsgType::Notice => format!("[{ts}] -{nick}- {plain}\n"),
        MsgType::Server | MsgType::Error => format!("[{ts}] {plain}\n"),
    }
}

// ---- Signals (SIGINT, SIGTERM, SIGWINCH) ----

#[cfg(unix)]
#[repr(C)]
#[derive(Clone, Copy)]
struct Sigset {
    _pad: [u64; 16],
}

#[cfg(unix)]
extern "C" {
    fn sigemptyset(set: *mut Sigset) -> i32;
    fn sigaddset(set: *mut Sigset, sig: i32) -> i32;
    fn pthread_sigmask(how: i32, set: *const Sigset, old: *mut Sigset) -> i32;
    fn sigwait(set: *const Sigset, sig: *mut i32) -> i32;
}

#[cfg(unix)]
const SIGINT: i32 = 2;
#[cfg(unix)]
const SIGTERM: i32 = 15;
#[cfg(unix)]
const SIGWINCH: i32 = 28;

#[cfg(any(target_os = "linux", target_os = "android"))]
const SIG_BLOCK: i32 = 0;
#[cfg(any(target_os = "linux", target_os = "android"))]
const SIG_UNBLOCK: i32 = 1;
#[cfg(all(unix, not(any(target_os = "linux", target_os = "android"))))]
const SIG_BLOCK: i32 = 1;
#[cfg(all(unix, not(any(target_os = "linux", target_os = "android"))))]
const SIG_UNBLOCK: i32 = 2;

#[cfg(unix)]
fn unblock_app_signals(set: &Sigset) {
    unsafe {
        pthread_sigmask(SIG_UNBLOCK, set, std::ptr::null_mut());
    }
}

#[cfg(unix)]
fn block_app_signals() -> Option<Sigset> {
    let set = app_sigset()?;
    let rc = unsafe { pthread_sigmask(SIG_BLOCK, &set, std::ptr::null_mut()) };
    if rc == 0 {
        Some(set)
    } else {
        None
    }
}

#[cfg(not(unix))]
fn unblock_app_signals(_set: &()) {}

#[cfg(not(unix))]
fn block_app_signals() -> Option<()> {
    None
}

#[cfg(unix)]
fn app_sigset() -> Option<Sigset> {
    unsafe {
        let mut set = Sigset { _pad: [0; 16] };
        if sigemptyset(&mut set) != 0 {
            return None;
        }
        if sigaddset(&mut set, SIGINT) != 0
            || sigaddset(&mut set, SIGTERM) != 0
            || sigaddset(&mut set, SIGWINCH) != 0
        {
            return None;
        }
        Some(set)
    }
}

#[cfg(unix)]
fn signal_loop(client: Arc<Client>, set: Sigset) {
    loop {
        let mut sig = 0i32;
        if unsafe { sigwait(&set, &mut sig) } != 0 {
            break;
        }
        match sig {
            SIGWINCH => {
                apply_term_size(&client);
                client.queue_redraw();
            }
            SIGINT | SIGTERM => {
                client.stop();
                break;
            }
            _ => {}
        }
    }
}

#[cfg(not(unix))]
fn signal_loop(_client: Arc<Client>, _set: ()) {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cli_server_other_than_config_drops_config_credentials() {
        let mut cfg = mircbsd::config::default_config();
        cfg.server = "irc.libera.chat".into();
        cfg.server_pass = "srv".into();
        cfg.sasl_pass = "sasl".into();
        cfg.nick_serv = "ns".into();
        cfg.perform = vec!["PRIVMSG NickServ :IDENTIFY x".into()];
        let mut same = cfg.clone();
        let mut warns = Vec::new();
        apply_cli_args(
            &mut same,
            &mut warns,
            &["IRC.Libera.Chat".into(), "6697".into()],
        );
        assert_eq!(same.sasl_pass, "sasl", "same server keeps credentials");
        assert!(warns.is_empty());

        apply_cli_args(
            &mut cfg,
            &mut warns,
            &["irc.evil.net".into(), "6667".into()],
        );
        assert_eq!(cfg.server, "irc.evil.net");
        assert!(cfg.server_pass.is_empty() && cfg.sasl_pass.is_empty());
        assert!(cfg.nick_serv.is_empty() && cfg.perform.is_empty());
        assert_eq!(warns.len(), 1);
    }

    #[test]
    fn notification_body_markup_is_escaped() {
        let (title, body) = notify_texts(
            "#c\x1b]0;x\x07",
            "<evil> <a href=\"http://x\">spausk</a> <img src=\"/etc/x.png\"> & co",
        );
        assert_eq!(title, "#c]0;x");
        assert!(!body.contains('<') && !body.contains('>'), "{body}");
        assert!(body.starts_with("&lt;evil&gt; &lt;a href="), "{body}");
        assert!(body.contains("&amp; co"), "{body}");
    }

    #[cfg(unix)]
    #[test]
    fn log_file_symlink_is_not_followed() {
        // A planted `<chan>-<day>.log` symlink (shared/world-writable log
        // directory) must not redirect server-controlled chat text into,
        // say, ~/.bashrc.
        let dir = std::env::temp_dir().join(format!("mircbsd-log-link-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(dir.join("srv")).unwrap();
        let victim = dir.join("victim");
        fs::write(&victim, b"keep").unwrap();
        std::os::unix::fs::symlink(&victim, dir.join("srv").join("#c-2026-01-01.log")).unwrap();
        let res = open_log_file(dir.to_str().unwrap(), "srv", "#c", "2026-01-01");
        if let Ok(mut f) = res {
            let _ = f.write_all(b"pwned");
        }
        assert_eq!(fs::read(&victim).unwrap(), b"keep");
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn log_open_error_identifies_directory() {
        let dir = std::env::temp_dir().join(format!("mircbsd-log-error-{}", std::process::id()));
        fs::create_dir(&dir).unwrap();
        let blocked = dir.join("not-a-directory");
        fs::write(&blocked, b"file").unwrap();
        let err = open_log_file(blocked.to_str().unwrap(), "server", "channel", "2026-01-01")
            .unwrap_err();
        assert!(err.to_string().contains("not-a-directory"));
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn nickserv_outgoing_secrets_are_redacted_in_logs() {
        use mircbsd::ui::{Message, MsgType};

        for secret in [
            "REGISTER secret email@example.org",
            "SET PASSWORD secret",
            "IDENTIFY secret",
        ] {
            let entry = LogEntry {
                log_dir: String::new(),
                server: "irc.example.org".into(),
                chan_name: "NickServ".into(),
                msg: Message::new(chrono::Utc::now(), "my-nick", secret, MsgType::Chat, false),
            };
            let line = format_log_line(&entry);
            assert!(!line.contains("secret"));
            assert!(line.contains("slaptažodis paslėptas"));
        }
    }
}
