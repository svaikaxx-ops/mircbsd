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
use std::io::{self, Read, Write};
use std::net::{Shutdown, TcpStream, ToSocketAddrs};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::crypto::CryptoProvider;
use rustls::pki_types::{pem::PemObject, CertificateDer, PrivateKeyDer, ServerName, UnixTime};
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
        match line.rfind(':') {
            Some(i) if i > 0 && i < line.len() - 1 => {
                pins.insert(line[..i].to_lowercase(), line[i + 1..].to_lowercase());
            }
            _ => {}
        }
    }
    pins
}

fn read_tls_pins(path: &Path) -> io::Result<std::collections::HashMap<String, String>> {
    let text = match fs::read_to_string(path) {
        Ok(text) => text,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(Default::default()),
        Err(err) => return Err(err),
    };
    let mut pins = std::collections::HashMap::new();
    for line in text.lines().map(str::trim) {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let (host, pin) = line.rsplit_once(':').ok_or_else(|| {
            io::Error::new(io::ErrorKind::InvalidData, "netinkamas TLS atspašo įrašas")
        })?;
        if host.is_empty() || pin.len() != 64 || !pin.bytes().all(|b| b.is_ascii_hexdigit()) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "netinkamas TLS atspašo įrašas",
            ));
        }
        let host = host.to_lowercase();
        let pin = pin.to_ascii_lowercase();
        if let Some(previous) = pins.insert(host, pin.clone()) {
            if previous != pin {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "prieštaringi TLS atspaudai",
                ));
            }
        }
    }
    Ok(pins)
}

/// Append "host:fp" (file 0600, new directories 0700).
fn save_tls_pin(path: &Path, host: &str, fp: &str) -> io::Result<()> {
    crate::persist::ensure_parent_dir(path)?;
    let mut opts = OpenOptions::new();
    opts.create(true).append(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut f = opts.open(path)?;
    writeln!(f, "{}:{}", host.to_lowercase(), fp)?;
    f.sync_all()
}

/// Serialize TOFU read/check/write across processes; the OS releases the lock
/// even if a process exits unexpectedly.
#[cfg(unix)]
fn lock_tls_pins(path: &Path) -> io::Result<File> {
    use std::os::fd::AsRawFd;
    use std::os::unix::fs::OpenOptionsExt;

    crate::persist::ensure_parent_dir(path)?;
    let mut lock_path = path.as_os_str().to_os_string();
    lock_path.push(".lock");
    let file = OpenOptions::new()
        .create(true)
        .write(true)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW)
        .open(lock_path)?;
    loop {
        // SAFETY: the file descriptor remains open for the lifetime of `file`.
        if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX) } == 0 {
            return Ok(file);
        }
        let err = io::Error::last_os_error();
        if err.kind() != io::ErrorKind::Interrupted {
            return Err(err);
        }
    }
}

fn check_or_store_pin(path: &Path, host: &str, fp: &str) -> Result<bool, String> {
    #[cfg(unix)]
    let _lock = lock_tls_pins(path).map_err(|e| format!("TLS atspaudai neužrakinti: {e}"))?;
    #[cfg(not(unix))]
    return Err("TLS TOFU atspaudų užrakinimas šioje sistemoje nepalaikomas".into());

    #[cfg(unix)]
    {
        let host = host.to_lowercase();
        let pins = read_tls_pins(path).map_err(|e| format!("TLS atspaudai neperskaityti: {e}"))?;
        if let Some(old) = pins.get(&host) {
            if old != fp {
                return Err(format!("saugotas {old}, priešingas {fp}"));
            }
            return Ok(false);
        }
        save_tls_pin(path, &host, fp).map_err(|e| format!("TLS atspaudas neišsaugotas: {e}"))?;
        Ok(true)
    }
}

fn expected_tls_pin(path: &Path, host: &str) -> Result<Option<String>, String> {
    #[cfg(unix)]
    let _lock = lock_tls_pins(path).map_err(|e| format!("TLS atspaudai neužrakinti: {e}"))?;
    #[cfg(not(unix))]
    return Err("TLS TOFU atspaudų užrakinimas šioje sistemoje nepalaikomas".into());

    #[cfg(unix)]
    read_tls_pins(path)
        .map(|pins| pins.get(&host.to_lowercase()).cloned())
        .map_err(|e| format!("TLS atspaudai neperskaityti: {e}"))
}

type DerTlv<'a> = (u8, &'a [u8], &'a [u8], &'a [u8]);

/// Read one DER TLV: returns (tag, content, rest, whole TLV).
fn der_tlv(buf: &[u8]) -> Option<DerTlv<'_>> {
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
///
/// FIRST USE (no pin yet) accepts any certificate — that is TOFU — but the
/// chain is still checked against the webpki roots and the result recorded
/// in `ca_trusted`, so the caller can warn loudly when the first, unpinned
/// certificate is not CA-signed either (nothing at all vouches for it).
#[derive(Debug)]
struct PinVerifier {
    provider: Arc<CryptoProvider>,
    expected: Option<String>,
    ca: Option<Arc<rustls::client::WebPkiServerVerifier>>,
    ca_trusted: Arc<AtomicBool>,
}

impl ServerCertVerifier for PinVerifier {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        intermediates: &[CertificateDer<'_>],
        server_name: &ServerName<'_>,
        ocsp_response: &[u8],
        now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        if let Some(expected) = &self.expected {
            if &cert_fingerprint(end_entity.as_ref()) != expected {
                return Err(rustls::Error::General("TLS atspaudas nesutampa".into()));
            }
        } else if let Some(ca) = &self.ca {
            let ok = ca
                .verify_server_cert(end_entity, intermediates, server_name, ocsp_response, now)
                .is_ok();
            self.ca_trusted.store(ok, Ordering::SeqCst);
        }
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
    build_tls_config_with_pin(pinning, cert_path, key_path, None).map(|(cfg, _)| cfg)
}

fn webpki_roots_store() -> RootCertStore {
    let mut roots = RootCertStore::empty();
    roots.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
    roots
}

/// Pin verifier; `ca` is only built for first use (no expected pin).
fn pin_verifier(provider: Arc<CryptoProvider>, expected: Option<String>) -> PinVerifier {
    let ca = if expected.is_none() {
        rustls::client::WebPkiServerVerifier::builder_with_provider(
            Arc::new(webpki_roots_store()),
            provider.clone(),
        )
        .build()
        .ok()
    } else {
        None
    };
    PinVerifier {
        provider,
        expected,
        ca,
        ca_trusted: Arc::new(AtomicBool::new(false)),
    }
}

/// Returns the config and the "first certificate was CA-signed" flag (only
/// meaningful with pinning and no stored pin).
fn build_tls_config_with_pin(
    pinning: bool,
    cert_path: &str,
    key_path: &str,
    expected: Option<String>,
) -> Result<(Arc<ClientConfig>, Arc<AtomicBool>), String> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let builder = ClientConfig::builder_with_provider(provider.clone())
        .with_protocol_versions(&[&rustls::version::TLS13, &rustls::version::TLS12])
        .map_err(|e| e.to_string())?;

    let mut ca_trusted = Arc::new(AtomicBool::new(false));
    let wants_auth = if pinning {
        let verifier = pin_verifier(provider, expected);
        ca_trusted = Arc::clone(&verifier.ca_trusted);
        builder
            .dangerous()
            .with_custom_certificate_verifier(Arc::new(verifier))
    } else {
        builder.with_root_certificates(webpki_roots_store())
    };

    let cfg = if cert_path.is_empty() {
        wants_auth.with_no_client_auth()
    } else {
        let certs = CertificateDer::pem_file_iter(cert_path)
            .map_err(|e| e.to_string())?
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
        let key = PrivateKeyDer::from_pem_file(kp).map_err(|e| format!("{kp}: {e}"))?;
        wants_auth
            .with_client_auth_cert(certs, key)
            .map_err(|e| e.to_string())?
    };
    Ok((Arc::new(cfg), ca_trusted))
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
            // Senos jungties teardown čia jau „stale“ ir būsenos neliečia —
            // kanalus grįžimui (/reconnect) įsimename patys.
            if st.connected {
                st.connected = false;
                st.write = None;
                st.note_disconnect();
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
    ///
    /// `ca_trusted`: the certificate also passed webpki chain validation
    /// (only used for the first-use message).
    pub fn check_tls_pin(&self, fp: &str, host: &str, ca_trusted: bool) -> Result<(), String> {
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
        let path = fingerprints_path().ok_or("TLS atspaudų katalogas nenustatytas")?;
        if check_or_store_pin(&path, host, fp)? {
            // The FULL fingerprint, so it can be compared out of band.
            self.server_msg(&format!(
                "TLS: serverio atspaudas pirmą kartą išsaugotas (SHA-256 SPKI {fp})"
            ));
            if !ca_trusted {
                self.err_msg(&format!(
                    "ĮSPĖJIMAS: {host} sertifikato nepatvirtina jokia CA, o atspaudas dar nebuvo žinomas — jei jo nepatikrinai kitu kanalu, ryšys gali būti perimtas (MITM)"
                ));
            }
        }
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

        let (pinning_on, manual_pin, cert, key) = {
            let st = self.mu.lock();
            (
                st.tls_pinning || !st.tls_pin.is_empty(),
                st.tls_pin.clone(),
                st.tls_cert_path.clone(),
                st.tls_key_path.clone(),
            )
        };
        // A bad client certificate aborts with a clear error (no silent PLAIN fallback).
        let expected = if !manual_pin.is_empty() {
            Some(
                manual_pin
                    .chars()
                    .filter(|c| *c != ':' && !c.is_whitespace())
                    .collect::<String>()
                    .to_lowercase(),
            )
        } else if pinning_on {
            let pins = fingerprints_path()
                .ok_or_else(|| "TLS atspaudų katalogas nenustatytas".to_string())
                .and_then(|path| expected_tls_pin(&path, srv_name));
            match pins {
                Ok(pin) => pin,
                Err(e) => {
                    self.err_msg(&e);
                    return None;
                }
            }
        } else {
            None
        };
        let (cfg, ca_trusted) = match build_tls_config_with_pin(pinning_on, &cert, &key, expected) {
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
            if let Err(e) = self.check_tls_pin(&fp, srv_name, ca_trusted.load(Ordering::SeqCst)) {
                self.err_msg(&format!("TLS atspaudo patikra nepavyko: {e}"));
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

        let (sends_password, withheld) = {
            let st = self.mu.lock();
            let has = !st.server_pass.is_empty()
                || !st.nickserv_pass.is_empty()
                || !st.sasl_pass.is_empty();
            let allowed = st.creds_allowed();
            (has && allowed, has && !allowed)
        };
        if withheld {
            self.server_msg(
                "Konfigūracijos slaptažodžiai (PASS/SASL/NickServ, perform) šiam serveriui nesiunčiami — jie skirti konfigūracijoje nurodytam serveriui",
            );
        }
        if !use_tls && sends_password {
            self.err_msg("ĮSPĖJIMAS: be TLS slaptažodžiai gali būti siunčiami atviru tekstu");
        }

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
            st.registered = false;
            st.nick_attempts = 0;
            st.monitor_online.clear();
        }
        // Naujas ryšys (galbūt kitas serveris per /connect): seno serverio
        // ISUPPORT (CHANTYPES, PREFIX, CASEMAPPING, limitai) nebegalioja.
        let cm = {
            let mut i = self.isup.write();
            *i = crate::proto::isupport::Isup::default();
            i.casemapping.clone()
        };
        self.mu.lock().casemapping = cm;
        self.server_msg("Prisijungta! Registruojamasi...");
        self.queue_redraw();

        let (real_name, server_pass) = {
            let st = self.mu.lock();
            let pass = if st.creds_allowed() {
                st.server_pass.clone()
            } else {
                String::new()
            };
            (st.real_name.clone(), pass)
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
                st.note_disconnect();
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
#[path = "connect_tests.rs"]
mod tests;
