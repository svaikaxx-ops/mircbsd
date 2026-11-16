//! Connection management: connect loop with back-off, TCP / rustls TLS
//! dialing, TLS pinning (TOFU, SPKI SHA-256), registration
//! (CAP LS 302 / PASS / NICK / USER) and the per-connection I/O loop.
//!
//! One thread per connection owns the socket: it reads with a short socket
//! timeout, feeds complete lines to `parse_irc`, and drains the write queue
//! (rate limited) between reads. This replaces the Go reader + writer + ping
//! goroutines and works identically for plain TCP and TLS (a rustls stream
//! cannot be split between threads).

use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, Read, Write};
use std::net::{Shutdown, TcpStream, ToSocketAddrs};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, Instant};

use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::crypto::CryptoProvider;
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{
    ClientConfig, ClientConnection, DigitallySignedStruct, RootCertStore, SignatureScheme,
    StreamOwned,
};
use sha2::{Digest, Sha256};

use crate::config::config_dir;

use super::send::{new_write_queue, pump_writes, BurstLimiter, WriteHandle, WriteQueue};
use super::types::Client;
use super::MAX_READ_BUFFER;

const DIAL_TIMEOUT: Duration = Duration::from_secs(15);
/// Socket read timeout of the I/O loop (how often writes/pings are serviced).
const POLL_INTERVAL: Duration = Duration::from_millis(50);
const PING_INTERVAL: Duration = Duration::from_secs(60);
const PING_TIMEOUT: Duration = Duration::from_secs(120);
const MAX_RECONNECT_DELAY: Duration = Duration::from_secs(120);

/// `host:port` with IPv6 literals in brackets (`joinHostPort`).
pub fn join_host_port(host: &str, port: &str) -> String {
    if host.contains(':') {
        format!("[{host}]:{port}")
    } else {
        format!("{host}:{port}")
    }
}

// ---- Line splitting (Go readLoop / bufio.ReadSlice) ----

#[derive(Debug, PartialEq, Eq)]
pub enum LineEvent {
    /// A complete line (without the terminating `\n`; a trailing `\r` may remain).
    Line(Vec<u8>),
    /// A line exceeded the buffer limit; its remainder is discarded.
    TooLong,
}

/// Splits a byte stream into lines with a hard length limit so a hostile
/// server cannot make us buffer unbounded data.
pub struct LineSplitter {
    cap: usize,
    buf: Vec<u8>,
    discarding: bool,
}

impl LineSplitter {
    pub fn new(cap: usize) -> Self {
        LineSplitter {
            cap,
            buf: Vec::new(),
            discarding: false,
        }
    }

    pub fn push(&mut self, mut data: &[u8]) -> Vec<LineEvent> {
        let mut out = Vec::new();
        while !data.is_empty() {
            match data.iter().position(|&b| b == b'\n') {
                Some(i) => {
                    let (chunk, rest) = (&data[..i], &data[i + 1..]);
                    if self.discarding {
                        self.discarding = false;
                    } else if self.buf.len() + chunk.len() + 1 > self.cap {
                        out.push(LineEvent::TooLong);
                    } else {
                        self.buf.extend_from_slice(chunk);
                        out.push(LineEvent::Line(std::mem::take(&mut self.buf)));
                    }
                    self.buf.clear();
                    data = rest;
                }
                None => {
                    if !self.discarding {
                        if self.buf.len() + data.len() >= self.cap {
                            self.discarding = true;
                            self.buf.clear();
                            out.push(LineEvent::TooLong);
                        } else {
                            self.buf.extend_from_slice(data);
                        }
                    }
                    break;
                }
            }
        }
        out
    }
}

// ---- TLS pinning (TOFU) ----

/// `configDir/fingerprints`.
pub fn fingerprints_path() -> Option<PathBuf> {
    config_dir().map(|d| d.join("fingerprints"))
}

/// Parse "host:sha256hex" lines; broken lines and comments are skipped.
pub fn parse_pins(text: &str) -> std::collections::HashMap<String, String> {
    let mut pins = std::collections::HashMap::new();
    for line in text.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        match line.find(':') {
            Some(i) if i > 0 && i < line.len() - 1 => {
                pins.insert(line[..i].to_lowercase(), line[i + 1..].to_lowercase());
            }
            _ => {}
        }
    }
    pins
}

pub fn load_tls_pins() -> std::collections::HashMap<String, String> {
    let Some(path) = fingerprints_path() else {
        return Default::default();
    };
    let Ok(f) = File::open(path) else {
        return Default::default();
    };
    let mut text = String::new();
    for line in BufReader::new(f).lines().map_while(Result::ok) {
        text.push_str(&line);
        text.push('\n');
    }
    parse_pins(&text)
}

/// Append "host:fp" (file 0600, directory 0700).
pub fn save_tls_pin(host: &str, fp: &str) {
    let Some(path) = fingerprints_path() else {
        return;
    };
    if let Some(dir) = path.parent() {
        if fs::create_dir_all(dir).is_err() {
            return;
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = fs::set_permissions(dir, fs::Permissions::from_mode(0o700));
        }
    }
    let mut opts = OpenOptions::new();
    opts.create(true).append(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    if let Ok(mut f) = opts.open(&path) {
        let _ = writeln!(f, "{}:{}", host.to_lowercase(), fp);
    }
}

/// Read one DER TLV: returns (tag, content, rest, whole TLV).
fn der_tlv(buf: &[u8]) -> Option<(u8, &[u8], &[u8], &[u8])> {
    let tag = *buf.first()?;
    let l0 = *buf.get(1)?;
    let (len, hdr) = if l0 < 0x80 {
        (l0 as usize, 2)
    } else {
        let n = (l0 & 0x7f) as usize;
        if n == 0 || n > 4 {
            return None;
        }
        let bytes = buf.get(2..2 + n)?;
        (
            bytes.iter().fold(0usize, |a, &b| (a << 8) | b as usize),
            2 + n,
        )
    };
    let end = hdr.checked_add(len)?;
    let content = buf.get(hdr..end)?;
    Some((tag, content, &buf[end..], &buf[..end]))
}

/// Extract the DER-encoded SubjectPublicKeyInfo from an X.509 certificate.
pub fn spki_from_cert_der(cert: &[u8]) -> Option<&[u8]> {
    let (tag, cert_body, _, _) = der_tlv(cert)?;
    if tag != 0x30 {
        return None;
    }
    let (tag, tbs, _, _) = der_tlv(cert_body)?;
    if tag != 0x30 {
        return None;
    }
    let mut rest = tbs;
    // Optional [0] EXPLICIT version.
    if *rest.first()? == 0xA0 {
        rest = der_tlv(rest)?.2;
    }
    // serialNumber, signature, issuer, validity, subject
    for _ in 0..5 {
        rest = der_tlv(rest)?.2;
    }
    let (tag, _, _, whole) = der_tlv(rest)?;
    if tag != 0x30 {
        return None;
    }
    Some(whole)
}

pub fn hex_encode(b: &[u8]) -> String {
    const H: &[u8; 16] = b"0123456789abcdef";
    let mut s = String::with_capacity(b.len() * 2);
    for &x in b {
        s.push(H[(x >> 4) as usize] as char);
        s.push(H[(x & 15) as usize] as char);
    }
    s
}

/// SHA-256 (hex) of the certificate's SPKI — survives certificate renewal
/// with the same key pair. Empty when the certificate cannot be parsed.
pub fn cert_fingerprint(cert_der: &[u8]) -> String {
    match spki_from_cert_der(cert_der) {
        Some(spki) => hex_encode(&Sha256::digest(spki)),
        None => String::new(),
    }
}

// ---- TLS configuration ----

/// Verifier used when pinning is on: chain validation is skipped (a
/// self-signed certificate would never pass) but handshake signatures are
/// still checked; trust comes from the pinned SPKI instead.
#[derive(Debug)]
struct PinVerifier {
    provider: Arc<CryptoProvider>,
}

impl ServerCertVerifier for PinVerifier {
    fn verify_server_cert(
        &self,
        _end_entity: &CertificateDer<'_>,
        _intermediates: &[CertificateDer<'_>],
        _server_name: &ServerName<'_>,
        _ocsp_response: &[u8],
        _now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        Ok(ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls12_signature(
            message,
            cert,
            dss,
            &self.provider.signature_verification_algorithms,
        )
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls13_signature(
            message,
            cert,
            dss,
            &self.provider.signature_verification_algorithms,
        )
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.provider
            .signature_verification_algorithms
            .supported_schemes()
    }
}

/// Build the rustls client configuration (TLS 1.2+; webpki roots, or the
/// pin verifier when pinning; optional client certificate for SASL EXTERNAL).
pub fn build_tls_config(
    pinning: bool,
    cert_path: &str,
    key_path: &str,
) -> Result<Arc<ClientConfig>, String> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let builder = ClientConfig::builder_with_provider(provider.clone())
        .with_protocol_versions(&[&rustls::version::TLS13, &rustls::version::TLS12])
        .map_err(|e| e.to_string())?;

    let wants_auth = if pinning {
        builder
            .dangerous()
            .with_custom_certificate_verifier(Arc::new(PinVerifier { provider }))
    } else {
        let mut roots = RootCertStore::empty();
        roots.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
        builder.with_root_certificates(roots)
    };

    let cfg = if cert_path.is_empty() {
        wants_auth.with_no_client_auth()
    } else {
        let mut cr = BufReader::new(File::open(cert_path).map_err(|e| e.to_string())?);
        let certs = rustls_pemfile::certs(&mut cr)
            .collect::<Result<Vec<_>, _>>()
            .map_err(|e| e.to_string())?;
        if certs.is_empty() {
            return Err(format!("{cert_path}: sertifikatų nerasta"));
        }
        let kp = if key_path.is_empty() {
            cert_path
        } else {
            key_path
        };
        let mut kr = BufReader::new(File::open(kp).map_err(|e| e.to_string())?);
        let key = rustls_pemfile::private_key(&mut kr)
            .map_err(|e| e.to_string())?
            .ok_or_else(|| format!("{kp}: privataus rakto nerasta"))?;
        wants_auth
            .with_client_auth_cert(certs, key)
            .map_err(|e| e.to_string())?
    };
    Ok(Arc::new(cfg))
}

// ---- Transport ----

enum Transport {
    Plain(TcpStream),
    Tls(Box<StreamOwned<ClientConnection, TcpStream>>),
}

impl Transport {
    fn sock(&self) -> &TcpStream {
        match self {
            Transport::Plain(s) => s,
            Transport::Tls(t) => &t.sock,
        }
    }

    /// Best-effort orderly close.
    fn close(&mut self) {
        if let Transport::Tls(t) = self {
            t.conn.send_close_notify();
            let _ = t.conn.write_tls(&mut t.sock);
        }
        let _ = self.sock().shutdown(Shutdown::Both);
    }
}

impl Read for Transport {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        match self {
            Transport::Plain(s) => s.read(buf),
            Transport::Tls(t) => t.read(buf),
        }
    }
}

impl Write for Transport {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        match self {
            Transport::Plain(s) => s.write(buf),
            Transport::Tls(t) => t.write(buf),
        }
    }
    fn flush(&mut self) -> io::Result<()> {
        match self {
            Transport::Plain(s) => s.flush(),
            Transport::Tls(t) => t.flush(),
        }
    }
}

fn dial(addr: &str) -> io::Result<TcpStream> {
    let mut last = io::Error::new(io::ErrorKind::NotFound, "adresas nerastas");
    for sa in addr.to_socket_addrs()? {
        match TcpStream::connect_timeout(&sa, DIAL_TIMEOUT) {
            Ok(s) => return Ok(s),
            Err(e) => last = e,
        }
    }
    Err(last)
}

fn is_timeout(e: &io::Error) -> bool {
    matches!(
        e.kind(),
        io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut | io::ErrorKind::Interrupted
    )
}

/// Marks the connect loop as finished when it ends (also on panic).
struct ConnectingGuard<'a> {
    c: &'a Client,
    gen: i64,
}

impl Drop for ConnectingGuard<'_> {
    fn drop(&mut self) {
        let mut st = self.c.mu.lock();
        // Reset only if nobody changed the epoch after us.
        if st.connecting_gen == self.gen {
            st.connecting_gen = -1;
        }
    }
}

impl Client {
    /// Drop the current connection and begin a new connect loop
    /// (`startConnect`). Bumping `connect_gen` makes old threads retire.
    pub fn start_connect(self: &Arc<Self>) {
        {
            let mut st = self.mu.lock();
            st.connecting_gen = -1;
            st.connect_gen += 1;
            if let Some(conn) = st.conn.take() {
                let _ = conn.shutdown(Shutdown::Both);
            }
        }
        let c = Arc::clone(self);
        std::thread::spawn(move || c.connect_loop());
    }

    /// Connect with exponential back-off between attempts (`connectLoop`).
    pub fn connect_loop(&self) {
        let gen = {
            let mut st = self.mu.lock();
            let gen = st.connect_gen;
            if st.connecting_gen == gen {
                return; // already connecting in this epoch
            }
            st.connecting_gen = gen;
            st.reconnect_delay = Duration::from_secs(5);
            gen
        };
        let _guard = ConnectingGuard { c: self, gen };

        loop {
            self.do_connect(gen);

            let (stale, should_reconnect, delay) = {
                let mut st = self.mu.lock();
                let delay = st.reconnect_delay;
                st.reconnect_delay = (st.reconnect_delay * 2).min(MAX_RECONNECT_DELAY);
                (st.connect_gen != gen, st.reconnect, delay)
            };
            if stale || !should_reconnect {
                return;
            }
            self.server_msg(&format!("Jungiamasi iš naujo po {}s...", delay.as_secs()));
            self.queue_redraw();
            if !self.sleep_reconnect(delay, gen) {
                return;
            }
        }
    }

    /// Wait `delay`, returning false early when the epoch changed.
    fn sleep_reconnect(&self, delay: Duration, gen: i64) -> bool {
        let deadline = Instant::now() + delay;
        loop {
            if self.mu.lock().connect_gen != gen {
                return false;
            }
            let now = Instant::now();
            if now >= deadline {
                return true;
            }
            std::thread::sleep((deadline - now).min(Duration::from_millis(50)));
        }
    }

    /// TLS pin policy (`checkTLSPin`): manual `tls_pin` wins; else TOFU
    /// against the fingerprints file. `Ok` when pinning is off.
    pub fn check_tls_pin(&self, fp: &str, host: &str) -> Result<(), String> {
        let (pinning, manual) = {
            let st = self.mu.lock();
            (st.tls_pinning, st.tls_pin.clone())
        };
        if !pinning && manual.is_empty() {
            return Ok(());
        }
        if fp.is_empty() {
            return Err("atspaudo negalima gauti (serveris be sertifikato?)".into());
        }
        if !manual.is_empty() {
            let m: String = manual
                .chars()
                .filter(|c| *c != ':' && !c.is_whitespace())
                .collect::<String>()
                .to_lowercase();
            if m != fp {
                return Err(format!("tls_pin nesutampa: nustatytas {m}, gautas {fp}"));
            }
            return Ok(());
        }
        let host_low = host.to_lowercase();
        if let Some(old) = load_tls_pins().get(&host_low) {
            if old != fp {
                return Err(format!("saugotas {old}, priešingas {fp}"));
            }
            return Ok(());
        }
        save_tls_pin(&host_low, fp);
        self.server_msg(&format!(
            "TLS: serverio atspaudas pirmą kartą išsaugotas ({}…)",
            &fp[..16.min(fp.len())]
        ));
        Ok(())
    }

    /// Open the transport: TCP, then TLS handshake + pin check when enabled.
    /// Errors are reported to the user; `None` means "give up this attempt".
    fn open_transport(&self, srv_name: &str, srv: &str, use_tls: bool) -> Option<Transport> {
        if !use_tls {
            return match dial(srv) {
                Ok(s) => Some(Transport::Plain(s)),
                Err(e) => {
                    self.err_msg(&format!("Nepavyko prisijungti: {e}"));
                    None
                }
            };
        }

        let (pinning_on, cert, key) = {
            let st = self.mu.lock();
            (
                st.tls_pinning || !st.tls_pin.is_empty(),
                st.tls_cert_path.clone(),
                st.tls_key_path.clone(),
            )
        };
        // A bad client certificate aborts with a clear error (no silent PLAIN fallback).
        let cfg = match build_tls_config(pinning_on, &cert, &key) {
            Ok(c) => c,
            Err(e) => {
                if cert.is_empty() {
                    self.err_msg(&format!("Nepavyko prisijungti: TLS konfigūracija: {e}"));
                } else {
                    self.err_msg(&format!("TLS kliento sertifikatas neišsiskaitoma: {e}"));
                }
                return None;
            }
        };
        let name = match ServerName::try_from(srv_name.to_string()) {
            Ok(n) => n,
            Err(e) => {
                self.err_msg(&format!(
                    "Nepavyko prisijungti: neteisingas serverio vardas: {e}"
                ));
                return None;
            }
        };
        let mut sock = match dial(srv) {
            Ok(s) => s,
            Err(e) => {
                self.err_msg(&format!("Nepavyko prisijungti: {e}"));
                return None;
            }
        };
        let mut conn = match ClientConnection::new(cfg, name) {
            Ok(c) => c,
            Err(e) => {
                self.err_msg(&format!("Nepavyko prisijungti: {e}"));
                return None;
            }
        };
        let _ = sock.set_read_timeout(Some(DIAL_TIMEOUT));
        let _ = sock.set_write_timeout(Some(DIAL_TIMEOUT));
        while conn.is_handshaking() {
            if let Err(e) = conn.complete_io(&mut sock) {
                self.err_msg(&format!("Nepavyko prisijungti: {e}"));
                let _ = sock.shutdown(Shutdown::Both);
                return None;
            }
        }
        if pinning_on {
            // Verify BEFORE NICK/USER/PASS: identity and passwords never reach a changed server.
            let fp = conn
                .peer_certificates()
                .and_then(|c| c.first())
                .map(|c| cert_fingerprint(c.as_ref()))
                .unwrap_or_default();
            if let Err(e) = self.check_tls_pin(&fp, srv_name) {
                self.err_msg(&format!("TLS atspaudo nesutapimas: {e}"));
                self.err_msg("Serverio sertifikatas pasikeitė! Galimas MITM. (tls_pinning=off išjungti tikrinti, žinodami ką darote)");
                conn.send_close_notify();
                let _ = conn.write_tls(&mut sock);
                let _ = sock.shutdown(Shutdown::Both);
                return None;
            }
        }
        Some(Transport::Tls(Box::new(StreamOwned::new(conn, sock))))
    }

    /// One connection attempt, from dialing to disconnect (`doConnect`).
    pub fn do_connect(&self, gen: i64) {
        let (srv_name, srv, use_tls, nick, stale) = {
            let st = self.mu.lock();
            (
                st.server.clone(),
                join_host_port(&st.server, &st.port),
                st.use_tls,
                st.nick.clone(),
                st.connect_gen != gen,
            )
        };
        if stale {
            return;
        }
        self.server_msg(&format!("Jungiamasi prie {srv} (TLS: {use_tls})..."));

        let Some(mut transport) = self.open_transport(&srv_name, &srv, use_tls) else {
            return;
        };
        let _ = transport.sock().set_nodelay(true);
        let _ = transport.sock().set_read_timeout(Some(POLL_INTERVAL));
        let _ = transport
            .sock()
            .set_write_timeout(Some(Duration::from_secs(30)));

        // Fresh write queue per connection: an old thread can't steal messages.
        let (handle, queue) = new_write_queue();
        {
            let mut st = self.mu.lock();
            if st.connect_gen != gen {
                // /connect or /reconnect happened while dialing.
                drop(st);
                transport.close();
                return;
            }
            st.conn = transport.sock().try_clone().ok();
            st.connected = true;
            st.last_pong = Instant::now();
            st.write = Some(handle.clone());
            st.active_caps.clear();
            st.cap_available.clear();
            st.cap_sasl = false;
            st.sasl_ok = false;
            st.sasl_pending = false;
        }
        self.server_msg("Prisijungta! Registruojamasi...");
        self.queue_redraw();

        let (real_name, server_pass) = {
            let st = self.mu.lock();
            (st.real_name.clone(), st.server_pass.clone())
        };
        let real_name = if real_name.is_empty() {
            "mircBSD IRC Client".to_string()
        } else {
            real_name
        };
        // IRCv3 negotiation first: the server waits for CAP END before 001.
        self.send("CAP LS 302");
        // PASS goes before NICK (RFC 2812).
        if !server_pass.is_empty() {
            self.send(&format!("PASS {server_pass}"));
        }
        self.send(&format!("NICK {nick}"));
        self.send(&format!("USER {nick} 0 * :{real_name}"));

        self.io_loop(gen, &mut transport, &queue, &handle);

        // Teardown.
        queue.mark_done();
        transport.close();
        let stale = {
            let mut st = self.mu.lock();
            let stale = st.connect_gen != gen;
            if !stale {
                st.connected = false;
                st.conn = None;
                st.write = None;
            }
            stale
        };
        if stale {
            // A newer connection owns the state now.
            return;
        }
        self.err_msg("Ryšys nutrūko");
        self.queue_redraw();
    }

    /// Reads server lines, services the write queue and keepalive until the
    /// connection ends (`readLoop` + writer + ping goroutines).
    fn io_loop(&self, gen: i64, stream: &mut Transport, queue: &WriteQueue, handle: &WriteHandle) {
        let mut splitter = LineSplitter::new(MAX_READ_BUFFER);
        let mut limiter = BurstLimiter::default();
        let mut pending: Option<String> = None;
        let mut buf = [0u8; 4096];
        let mut last_ping = Instant::now();

        loop {
            match pump_writes(stream, queue, &mut pending, &mut limiter) {
                Ok(_) => {}
                Err(_) => return,
            }
            if queue.finish_requested() && pending.is_none() {
                return; // everything queued has been flushed
            }

            let (stale, last_pong) = {
                let st = self.mu.lock();
                (st.connect_gen != gen, st.last_pong)
            };
            if stale {
                return;
            }

            if last_ping.elapsed() >= PING_INTERVAL {
                last_ping = Instant::now();
                if last_pong.elapsed() > PING_TIMEOUT {
                    self.err_msg("Ping timeout - jungiamasi iš naujo");
                    return;
                }
                // Straight to THIS connection's queue; full queue → skip.
                let _ = handle.try_send(format!("PING :{}", chrono::Utc::now().timestamp()));
            }

            match stream.read(&mut buf) {
                Ok(0) => return,
                Ok(n) => {
                    for ev in splitter.push(&buf[..n]) {
                        match ev {
                            LineEvent::TooLong => {
                                self.err_msg("ĮSPĖJIMAS: serverio eilutė per ilga — atmesta");
                            }
                            LineEvent::Line(bytes) => {
                                let text = String::from_utf8_lossy(&bytes);
                                let line = text.trim_end_matches(['\r', '\n']);
                                if catch_unwind(AssertUnwindSafe(|| self.parse_irc(line))).is_err()
                                {
                                    self.err_msg(
                                        "Vidinė klaida apdorojant serverio eilutę (praleista)",
                                    );
                                }
                                // Answer promptly (PONG etc.) without waiting for the next poll.
                                if pump_writes(stream, queue, &mut pending, &mut limiter).is_err() {
                                    return;
                                }
                            }
                        }
                    }
                }
                Err(e) if is_timeout(&e) => {}
                Err(_) => return,
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn host_port_brackets_ipv6() {
        assert_eq!(join_host_port("irc.x.org", "6667"), "irc.x.org:6667");
        assert_eq!(join_host_port("::1", "6697"), "[::1]:6697");
    }

    #[test]
    fn splitter_handles_fragmented_lines() {
        let mut s = LineSplitter::new(4096);
        assert!(s.push(b"PING :a").is_empty());
        let ev = s.push(b"bc\r\nPONG x\r\nPART");
        assert_eq!(
            ev,
            vec![
                LineEvent::Line(b"PING :abc\r".to_vec()),
                LineEvent::Line(b"PONG x\r".to_vec())
            ]
        );
        let ev = s.push(b" #c\n");
        assert_eq!(ev, vec![LineEvent::Line(b"PART #c".to_vec())]);
    }

    #[test]
    fn splitter_discards_overlong_lines() {
        let mut s = LineSplitter::new(16);
        let mut ev = s.push(&[b'a'; 20]);
        assert_eq!(ev, vec![LineEvent::TooLong]);
        ev = s.push(&[b'b'; 10]);
        assert!(ev.is_empty());
        ev = s.push(b"\nOK\n");
        assert_eq!(ev, vec![LineEvent::Line(b"OK".to_vec())]);
        // Overlong line delivered in one chunk with its newline.
        let ev = s.push(b"0123456789abcdefXYZ\nnext\n");
        assert_eq!(
            ev,
            vec![LineEvent::TooLong, LineEvent::Line(b"next".to_vec())]
        );
    }

    #[test]
    fn pins_parse_and_skip_junk() {
        let p = parse_pins("# c\n\nIrc.X.org:ABCD\nbad\n:nohost\nhost:\n");
        assert_eq!(p.len(), 1);
        assert_eq!(p["irc.x.org"], "abcd");
    }

    #[test]
    fn hex_encode_works() {
        assert_eq!(hex_encode(&[0, 0xab, 0xff]), "00abff");
    }

    #[test]
    fn spki_rejects_garbage() {
        assert!(spki_from_cert_der(&[]).is_none());
        assert!(spki_from_cert_der(&[0x30, 0x03, 1, 2, 3]).is_none());
        assert_eq!(cert_fingerprint(b"junk"), "");
    }

    #[test]
    fn tls_config_builds_without_client_cert() {
        assert!(build_tls_config(false, "", "").is_ok());
        assert!(build_tls_config(true, "", "").is_ok());
        assert!(build_tls_config(false, "/nonexistent/cert.pem", "/nonexistent/key.pem").is_err());
    }

    // ---- loopback integration tests ----

    use crate::config::default_config;
    use std::io::BufRead;
    use std::net::TcpListener;

    const TEST_CERT_PEM: &str = "-----BEGIN CERTIFICATE-----\nMIIBljCCATugAwIBAgIUcxFYzC5od/fTtslgTlkfkAO1z/QwCgYIKoZIzj0EAwIw\nFDESMBAGA1UEAwwJbG9jYWxob3N0MCAXDTI2MTExNjIxNDkzM1oYDzIxMjYxMDIz\nMjE0OTMzWjAUMRIwEAYDVQQDDAlsb2NhbGhvc3QwWTATBgcqhkjOPQIBBggqhkjO\nPQMBBwNCAAQe3zO+y2zhFAwm6nEi95Peqclk53+smalTyjC95vel3GqLZDCuqrZf\n66/PQqwUOPtZE7+pb7WSIom549p4WneBo2kwZzAdBgNVHQ4EFgQUihTdy30V/1tp\nMPBx58kKRyuaPkMwHwYDVR0jBBgwFoAUihTdy30V/1tpMPBx58kKRyuaPkMwDwYD\nVR0TAQH/BAUwAwEB/zAUBgNVHREEDTALgglsb2NhbGhvc3QwCgYIKoZIzj0EAwID\nSQAwRgIhAOxlotfohXdDh0/3FZYZ65f9Ws1l3FK6K0Ituo2FLVMFAiEAqoSfHdsM\nZWbDEVc0ErWzO8xVSN+wLLw2or0xHFooMNs=\n-----END CERTIFICATE-----\n";
    const TEST_KEY_PEM: &str = "-----BEGIN PRIVATE KEY-----\nMIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgRaERAzlntXvMOmYn\nd2MxQUO8oeY9rIPHnKDRoD3XnQqhRANCAAQe3zO+y2zhFAwm6nEi95Peqclk53+s\nmalTyjC95vel3GqLZDCuqrZf66/PQqwUOPtZE7+pb7WSIom549p4WneB\n-----END PRIVATE KEY-----\n";
    /// sha256 of the test certificate's SPKI (computed with openssl).
    const TEST_CERT_SPKI_SHA256: &str =
        "1047a24da1a68647d8cbec2607e9fa2c4415b61a8ae5b857ea3130e182c622cf";

    fn wait_until(what: &str, f: impl Fn() -> bool) {
        let deadline = Instant::now() + Duration::from_secs(10);
        while Instant::now() < deadline {
            if f() {
                return;
            }
            std::thread::sleep(Duration::from_millis(20));
        }
        panic!("timed out waiting for: {what}");
    }

    fn server_texts(c: &Client) -> Vec<String> {
        let st = c.mu.lock();
        st.channels[0]
            .messages
            .iter()
            .map(|m| m.text.clone())
            .collect()
    }

    fn test_client(port: u16, tls: bool) -> Arc<Client> {
        let mut cfg = default_config();
        cfg.server = "127.0.0.1".into();
        cfg.port = port.to_string();
        cfg.nick = "tester".into();
        cfg.tls = tls;
        cfg.reconnect = false;
        cfg.log_dir = String::new();
        Client::new(&cfg)
    }

    /// Minimal IRC server: waits for USER, welcomes, pings, expects PONG then QUIT.
    fn serve_irc<S: Read + Write>(stream: S, seen: std::sync::mpsc::Sender<String>) {
        let mut rd = BufReader::new(stream);
        let mut line = String::new();
        loop {
            line.clear();
            match rd.read_line(&mut line) {
                Ok(0) | Err(_) => return,
                Ok(_) => {}
            }
            let l = line.trim_end().to_string();
            let _ = seen.send(l.clone());
            if l.starts_with("USER ") {
                let w = rd.get_mut();
                w.write_all(b":srv 001 tester :Welcome\r\nPING :tok\r\n")
                    .unwrap();
                w.flush().unwrap();
            }
            if l.starts_with("QUIT") {
                return;
            }
        }
    }

    #[test]
    fn plain_connect_register_ping_quit() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let (tx, rx) = std::sync::mpsc::channel();
        let srv = std::thread::spawn(move || {
            let (s, _) = listener.accept().unwrap();
            serve_irc(s, tx);
        });

        let c = test_client(port, false);
        c.start_connect();
        wait_until("welcome", || {
            server_texts(&c).iter().any(|t| t == "Welcome")
        });
        assert!(c.is_connected());
        c.send("QUIT :bye\r\nEVIL");
        c.finish_writes(Duration::from_secs(3));
        srv.join().unwrap();
        wait_until("disconnect", || !c.is_connected());
        wait_until("lost msg", || {
            server_texts(&c).iter().any(|t| t == "Ryšys nutrūko")
        });

        let seen: Vec<String> = rx.try_iter().collect();
        assert_eq!(seen[0], "CAP LS 302");
        assert_eq!(seen[1], "NICK tester");
        assert!(seen[2].starts_with("USER tester 0 * :"));
        assert!(seen.contains(&"PONG :tok".to_string()), "{seen:?}");
        assert_eq!(seen.last().unwrap(), "QUIT :byeEVIL");
    }

    fn tls_server(
        listener: TcpListener,
        seen: std::sync::mpsc::Sender<String>,
    ) -> std::thread::JoinHandle<()> {
        let certs = rustls_pemfile::certs(&mut TEST_CERT_PEM.as_bytes())
            .collect::<Result<Vec<_>, _>>()
            .unwrap();
        let key = rustls_pemfile::private_key(&mut TEST_KEY_PEM.as_bytes())
            .unwrap()
            .unwrap();
        let provider = Arc::new(rustls::crypto::ring::default_provider());
        let cfg = rustls::ServerConfig::builder_with_provider(provider)
            .with_safe_default_protocol_versions()
            .unwrap()
            .with_no_client_auth()
            .with_single_cert(certs, key)
            .unwrap();
        let cfg = Arc::new(cfg);
        std::thread::spawn(move || {
            let (mut sock, _) = listener.accept().unwrap();
            let mut conn = rustls::ServerConnection::new(cfg).unwrap();
            // Handshake may fail when the client aborts on a pin mismatch.
            while conn.is_handshaking() {
                if conn.complete_io(&mut sock).is_err() {
                    return;
                }
            }
            serve_irc(StreamOwned::new(conn, sock), seen);
        })
    }

    #[test]
    fn spki_fingerprint_matches_openssl() {
        let certs = rustls_pemfile::certs(&mut TEST_CERT_PEM.as_bytes())
            .collect::<Result<Vec<_>, _>>()
            .unwrap();
        assert_eq!(cert_fingerprint(certs[0].as_ref()), TEST_CERT_SPKI_SHA256);
    }

    #[test]
    fn tls_connect_with_matching_manual_pin() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let (tx, rx) = std::sync::mpsc::channel();
        let srv = tls_server(listener, tx);

        let c = test_client(port, true);
        c.mu.lock().tls_pin = TEST_CERT_SPKI_SHA256.to_uppercase();
        c.start_connect();
        wait_until("welcome over tls", || {
            server_texts(&c).iter().any(|t| t == "Welcome")
        });
        c.send("QUIT :bye");
        c.finish_writes(Duration::from_secs(3));
        srv.join().unwrap();
        let seen: Vec<String> = rx.try_iter().collect();
        assert_eq!(seen[0], "CAP LS 302");
        assert!(seen.contains(&"PONG :tok".to_string()), "{seen:?}");
    }

    #[test]
    fn tls_pin_mismatch_aborts_before_registration() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let (tx, rx) = std::sync::mpsc::channel();
        let srv = tls_server(listener, tx);

        let c = test_client(port, true);
        c.mu.lock().tls_pin = "00".repeat(32);
        c.start_connect();
        wait_until("pin error", || {
            server_texts(&c)
                .iter()
                .any(|t| t.starts_with("TLS atspaudo nesutapimas"))
        });
        let _ = srv.join();
        assert!(!c.is_connected());
        assert!(
            rx.try_iter().next().is_none(),
            "nothing may reach the server"
        );
    }

    #[test]
    fn tls_without_pinning_rejects_self_signed() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let (tx, rx) = std::sync::mpsc::channel();
        let srv = tls_server(listener, tx);

        let c = test_client(port, true);
        c.start_connect();
        wait_until("verify error", || {
            server_texts(&c)
                .iter()
                .any(|t| t.starts_with("Nepavyko prisijungti"))
        });
        let _ = srv.join();
        assert!(rx.try_iter().next().is_none());
    }
}
