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
fn splitter_gives_same_result_for_many_fragment_sizes() {
    let input = b"PING :one\r\n\xffinvalid\n123456789012345678901234567890123\nPONG\npartial";
    let mut expected_splitter = LineSplitter::new(24);
    let expected = expected_splitter.push(input);
    for chunk_size in 1..=input.len() {
        let mut splitter = LineSplitter::new(24);
        let events: Vec<_> = input
            .chunks(chunk_size)
            .flat_map(|chunk| splitter.push(chunk))
            .collect();
        assert_eq!(events, expected, "chunk size {chunk_size}");
    }
}

#[test]
fn pins_parse_and_skip_junk() {
    let p = parse_pins("# c\n\nIrc.X.org:ABCD\nbad\n:nohost\nhost:\n");
    assert_eq!(p.len(), 1);
    assert_eq!(p["irc.x.org"], "abcd");
    assert_eq!(parse_pins("2001:db8::1:ABCD\n")["2001:db8::1"], "abcd");
}

#[test]
fn pin_store_rejects_corrupt_or_conflicting_records() {
    let dir = std::env::temp_dir().join(format!("mircbsd-pins-{}", std::process::id()));
    std::fs::create_dir(&dir).expect("create isolated pin directory");
    let path = dir.join("fingerprints");
    let pin = "a".repeat(64);
    save_tls_pin(&path, "2001:db8::1", &pin).unwrap();
    assert_eq!(read_tls_pins(&path).unwrap()["2001:db8::1"], pin);
    std::fs::write(&path, "broken record\n").unwrap();
    assert_eq!(
        read_tls_pins(&path).unwrap_err().kind(),
        io::ErrorKind::InvalidData
    );
    std::fs::write(
        &path,
        format!("host:{}\nhost:{}\n", "a".repeat(64), "b".repeat(64)),
    )
    .unwrap();
    assert_eq!(
        read_tls_pins(&path).unwrap_err().kind(),
        io::ErrorKind::InvalidData
    );
    assert!(save_tls_pin(&dir, "host", &pin).is_err());
    std::fs::remove_dir_all(dir).unwrap();
}

#[cfg(unix)]
#[test]
fn concurrent_first_pins_cannot_both_be_trusted() {
    let dir = std::env::temp_dir().join(format!("mircbsd-pin-race-{}", std::process::id()));
    std::fs::create_dir(&dir).unwrap();
    let path = dir.join("fingerprints");
    let barrier = Arc::new(std::sync::Barrier::new(2));
    let threads: Vec<_> = ["a".repeat(64), "b".repeat(64)]
        .into_iter()
        .map(|pin| {
            let path = path.clone();
            let barrier = barrier.clone();
            std::thread::spawn(move || {
                barrier.wait();
                check_or_store_pin(&path, "example.org", &pin)
            })
        })
        .collect();
    let results: Vec<_> = threads.into_iter().map(|t| t.join().unwrap()).collect();
    assert_eq!(results.iter().filter(|r| r.is_ok()).count(), 1);
    assert_eq!(read_tls_pins(&path).unwrap().len(), 1);
    std::fs::remove_dir_all(dir).unwrap();
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
use std::io::{BufRead, BufReader};
use std::net::TcpListener;

const TEST_CERT_PEM: &str = "-----BEGIN CERTIFICATE-----\nMIIBljCCATugAwIBAgIUcxFYzC5od/fTtslgTlkfkAO1z/QwCgYIKoZIzj0EAwIw\nFDESMBAGA1UEAwwJbG9jYWxob3N0MCAXDTI2MTExNjIxNDkzM1oYDzIxMjYxMDIz\nMjE0OTMzWjAUMRIwEAYDVQQDDAlsb2NhbGhvc3QwWTATBgcqhkjOPQIBBggqhkjO\nPQMBBwNCAAQe3zO+y2zhFAwm6nEi95Peqclk53+smalTyjC95vel3GqLZDCuqrZf\n66/PQqwUOPtZE7+pb7WSIom549p4WneBo2kwZzAdBgNVHQ4EFgQUihTdy30V/1tp\nMPBx58kKRyuaPkMwHwYDVR0jBBgwFoAUihTdy30V/1tpMPBx58kKRyuaPkMwDwYD\nVR0TAQH/BAUwAwEB/zAUBgNVHREEDTALgglsb2NhbGhvc3QwCgYIKoZIzj0EAwID\nSQAwRgIhAOxlotfohXdDh0/3FZYZ65f9Ws1l3FK6K0Ituo2FLVMFAiEAqoSfHdsM\nZWbDEVc0ErWzO8xVSN+wLLw2or0xHFooMNs=\n-----END CERTIFICATE-----\n";
const TEST_KEY_PEM: &str = "-----BEGIN PRIVATE KEY-----\nMIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgRaERAzlntXvMOmYn\nd2MxQUO8oeY9rIPHnKDRoD3XnQqhRANCAAQe3zO+y2zhFAwm6nEi95Peqclk53+s\nmalTyjC95vel3GqLZDCuqrZf66/PQqwUOPtZE7+pb7WSIom549p4WneB\n-----END PRIVATE KEY-----\n";
/// sha256 of the test certificate's SPKI (computed with openssl).
const TEST_CERT_SPKI_SHA256: &str =
    "1047a24da1a68647d8cbec2607e9fa2c4415b61a8ae5b857ea3130e182c622cf";

#[test]
fn tls_client_certificate_and_key_are_loaded_from_pem() {
    let dir = std::env::temp_dir().join(format!("mircbsd-client-cert-{}", std::process::id()));
    std::fs::create_dir(&dir).unwrap();
    let cert = dir.join("cert.pem");
    let key = dir.join("key.pem");
    std::fs::write(&cert, TEST_CERT_PEM).unwrap();
    std::fs::write(&key, TEST_KEY_PEM).unwrap();
    assert!(build_tls_config(false, cert.to_str().unwrap(), key.to_str().unwrap()).is_ok());
    std::fs::write(&key, "not a private key").unwrap();
    assert!(build_tls_config(false, cert.to_str().unwrap(), key.to_str().unwrap()).is_err());
    std::fs::remove_dir_all(dir).unwrap();
}

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

#[test]
fn warns_before_password_on_plain_connection() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let (tx, rx) = std::sync::mpsc::channel();
    let srv = std::thread::spawn(move || {
        let (socket, _) = listener.accept().unwrap();
        serve_irc(socket, tx);
    });
    let client = test_client(port, false);
    client.mu.lock().server_pass = "secret".into();
    client.start_connect();
    wait_until("plaintext warning", || {
        server_texts(&client)
            .iter()
            .any(|text| text.contains("atviru tekstu"))
    });
    wait_until("registration", || client.is_connected());
    client.send("QUIT :bye");
    client.finish_writes(Duration::from_secs(3));
    srv.join().unwrap();
    assert!(rx.try_iter().any(|line| line == "PASS secret"));
}

#[test]
fn connect_to_other_server_does_not_send_config_password() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let (tx, rx) = std::sync::mpsc::channel();
    let srv = std::thread::spawn(move || {
        let (socket, _) = listener.accept().unwrap();
        serve_irc(socket, tx);
    });
    let mut cfg = default_config();
    cfg.server = "irc.example.org".into();
    cfg.server_pass = "secret".into();
    cfg.reconnect = false;
    cfg.log_dir = String::new();
    let client = Client::new(&cfg);
    // The user connects somewhere else than the configured network.
    client.handle_cmd("connect", &format!("127.0.0.1 {port} tester"));
    wait_until("registration", || {
        server_texts(&client).iter().any(|t| t == "Welcome")
    });
    client.send("QUIT :bye");
    client.finish_writes(Duration::from_secs(3));
    srv.join().unwrap();
    let seen: Vec<String> = rx.try_iter().collect();
    assert!(!seen.iter().any(|l| l.starts_with("PASS")), "{seen:?}");
    assert!(server_texts(&client)
        .iter()
        .any(|t| t.contains("nesiunčiami")));
}

fn tls_server(
    listener: TcpListener,
    seen: std::sync::mpsc::Sender<String>,
) -> std::thread::JoinHandle<()> {
    let certs = CertificateDer::pem_slice_iter(TEST_CERT_PEM.as_bytes())
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    let key = PrivateKeyDer::from_pem_slice(TEST_KEY_PEM.as_bytes()).unwrap();
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
fn first_use_pin_records_that_the_certificate_is_not_ca_signed() {
    let certs = CertificateDer::pem_slice_iter(TEST_CERT_PEM.as_bytes())
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    let name = ServerName::try_from("localhost").unwrap();
    let provider = Arc::new(rustls::crypto::ring::default_provider());

    // First use (no pin): accepted (TOFU), but flagged as not CA-signed.
    let v = pin_verifier(provider.clone(), None);
    v.ca_trusted.store(true, Ordering::SeqCst);
    assert!(v
        .verify_server_cert(&certs[0], &[], &name, &[], UnixTime::now())
        .is_ok());
    assert!(!v.ca_trusted.load(Ordering::SeqCst));

    // Known pin: compared, the CA check is not needed.
    let v = pin_verifier(provider.clone(), Some(TEST_CERT_SPKI_SHA256.into()));
    assert!(v.ca.is_none());
    assert!(v
        .verify_server_cert(&certs[0], &[], &name, &[], UnixTime::now())
        .is_ok());
    let v = pin_verifier(provider, Some("00".repeat(32)));
    assert!(v
        .verify_server_cert(&certs[0], &[], &name, &[], UnixTime::now())
        .is_err());
}

#[test]
fn spki_fingerprint_matches_openssl() {
    let certs = CertificateDer::pem_slice_iter(TEST_CERT_PEM.as_bytes())
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
            .any(|t| t.contains("TLS atspaudas nesutampa"))
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
