//! Outgoing line queue: CR/LF stripping, 512-byte truncation, and the
//! 5 lines/second burst limiter.
//!
//! The Go client has a dedicated writer goroutine per connection. Here the
//! connection thread owns the socket and drains the queue itself (see
//! `connect.rs`), so this module provides the queue handle, the limiter and
//! the flush helper.

use std::io::Write;
use std::sync::mpsc::{sync_channel, Receiver, SyncSender, TrySendError};
use std::sync::Arc;
use std::time::{Duration, Instant};

use parking_lot::{Condvar, Mutex};

use crate::sanitize::truncate_irc;

use super::types::{new_msg, Client, MsgType};
use super::IRC_SEND_BURST;

/// Capacity of the per-connection write queue (Go: `make(chan string, 256)`).
pub const WRITE_QUEUE_CAP: usize = 256;

/// Remove CR/LF/NUL (protocol-injection guard) and cut to the 512-byte line
/// limit. NUL is illegal in an IRC line: servers cut the line there or drop
/// it, so a NUL from server data (PING token, CTCP payload) or a paste must
/// never reach the socket.
pub fn prepare_line(line: &str) -> String {
    let cleaned: String = if line.contains(['\r', '\n', '\0']) {
        line.chars()
            .filter(|&c| c != '\r' && c != '\n' && c != '\0')
            .collect()
    } else {
        line.to_string()
    };
    truncate_irc(&cleaned)
}

#[derive(Default)]
struct WriteShared {
    /// "Flush what is queued, then stop" (Go `writeStop`).
    finish: Mutex<bool>,
    /// Connection thread has finished writing (Go `writeDone`).
    done: Mutex<bool>,
    done_cv: Condvar,
}

/// Cloneable sender side of a connection's write queue.
#[derive(Clone)]
pub struct WriteHandle {
    tx: SyncSender<String>,
    shared: Arc<WriteShared>,
}

/// Receiver side, owned by the connection thread.
pub struct WriteQueue {
    pub rx: Receiver<String>,
    shared: Arc<WriteShared>,
}

/// Create a fresh queue for a new connection.
pub fn new_write_queue() -> (WriteHandle, WriteQueue) {
    let (tx, rx) = sync_channel(WRITE_QUEUE_CAP);
    let shared = Arc::new(WriteShared::default());
    (
        WriteHandle {
            tx,
            shared: shared.clone(),
        },
        WriteQueue { rx, shared },
    )
}

impl WriteHandle {
    /// Non-blocking enqueue of an already prepared line.
    pub fn try_send(&self, line: String) -> Result<(), TrySendError<String>> {
        self.tx.try_send(line)
    }

    /// Ask the writer to flush the queue and stop (idempotent).
    pub fn stop(&self) {
        *self.shared.finish.lock() = true;
    }

    /// Wait until the writer is done, at most `timeout`.
    pub fn wait_done(&self, timeout: Duration) -> bool {
        let deadline = Instant::now() + timeout;
        let mut done = self.shared.done.lock();
        while !*done {
            if self
                .shared
                .done_cv
                .wait_until(&mut done, deadline)
                .timed_out()
            {
                break;
            }
        }
        *done
    }
}

impl WriteQueue {
    /// Has `finish_writes` / `stop` been requested?
    pub fn finish_requested(&self) -> bool {
        *self.shared.finish.lock()
    }

    /// Signal [`WriteHandle::wait_done`] waiters.
    pub fn mark_done(&self) {
        *self.shared.done.lock() = true;
        self.shared.done_cv.notify_all();
    }
}

/// Sliding one-second window allowing `burst` lines (Go write goroutine).
#[derive(Debug)]
pub struct BurstLimiter {
    burst: usize,
    sent: usize,
    t0: Instant,
}

impl BurstLimiter {
    pub fn new(burst: usize) -> Self {
        BurstLimiter {
            burst,
            sent: 0,
            t0: Instant::now(),
        }
    }

    /// How long to wait before the next line may be sent (`None` = now).
    pub fn wait_needed(&mut self, now: Instant) -> Option<Duration> {
        let elapsed = now.saturating_duration_since(self.t0);
        if elapsed >= Duration::from_secs(1) {
            self.sent = 0;
            self.t0 = now;
            return None;
        }
        if self.sent >= self.burst {
            Some(Duration::from_secs(1) - elapsed)
        } else {
            None
        }
    }

    /// Record one sent line.
    pub fn record(&mut self) {
        self.sent += 1;
    }
}

impl Default for BurstLimiter {
    fn default() -> Self {
        BurstLimiter::new(IRC_SEND_BURST)
    }
}

/// Write queued lines (respecting the limiter) and flush once.
///
/// `pending` holds a line that is waiting for the limiter. Returns the
/// remaining wait when the limiter blocked further sending, `Ok(None)` when
/// the queue is empty.
pub fn pump_writes<W: Write>(
    w: &mut W,
    queue: &WriteQueue,
    pending: &mut Option<String>,
    limiter: &mut BurstLimiter,
) -> std::io::Result<Option<Duration>> {
    let mut buf: Vec<u8> = Vec::new();
    let mut wait = None;
    loop {
        let line = match pending.take() {
            Some(l) => l,
            None => match queue.rx.try_recv() {
                Ok(l) => l,
                Err(_) => break,
            },
        };
        if let Some(d) = limiter.wait_needed(Instant::now()) {
            *pending = Some(line);
            wait = Some(d);
            break;
        }
        limiter.record();
        buf.extend_from_slice(line.as_bytes());
        buf.extend_from_slice(b"\r\n");
    }
    if !buf.is_empty() {
        w.write_all(&buf)?;
        w.flush()?;
    }
    Ok(wait)
}

impl Client {
    /// Queue one line for the server (`send`): strips CR/LF, truncates to
    /// 512 bytes. A full queue produces a visible warning, never a panic.
    pub fn send(&self, line: &str) {
        let line = prepare_line(line);
        let handle = self.mu.lock().write.clone();
        let Some(h) = handle else { return };
        match h.try_send(line) {
            Ok(()) => {}
            Err(TrySendError::Full(_)) => {
                self.add_msg(
                    "*server*",
                    new_msg(
                        crate::timeutil::now(),
                        "",
                        "ĮSPĖJIMAS: siuntimo eilė pilna, žinutė prarasta",
                        MsgType::Error,
                    ),
                );
            }
            Err(TrySendError::Disconnected(_)) => {}
        }
    }

    /// Tell the active writer to flush and stop, waiting at most `timeout`
    /// (`finishWrites`) — used by /quit so QUIT is really sent.
    pub fn finish_writes(&self, timeout: Duration) {
        let handle = self.mu.lock().write.clone();
        if let Some(h) = handle {
            h.stop();
            h.wait_done(timeout);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn prepare_strips_crlf_and_truncates() {
        assert_eq!(prepare_line("PRIVMSG a :x\r\nQUIT"), "PRIVMSG a :xQUIT");
        // NUL is illegal on the wire as well (server cuts the line there).
        assert_eq!(prepare_line("PONG :a\0b"), "PONG :ab");
        let long = "é".repeat(400);
        let out = prepare_line(&long);
        assert!(out.len() <= 510);
        assert!(out.is_char_boundary(out.len()));
    }

    #[test]
    fn limiter_allows_burst_then_waits() {
        let mut l = BurstLimiter::new(5);
        let now = Instant::now();
        for _ in 0..5 {
            assert!(l.wait_needed(now).is_none());
            l.record();
        }
        let w = l.wait_needed(now).expect("must wait after burst");
        assert!(w <= Duration::from_secs(1));
        // After the window passes, sending is allowed again.
        assert!(l.wait_needed(now + Duration::from_millis(1100)).is_none());
    }

    #[test]
    fn pump_writes_respects_limit() {
        let (h, q) = new_write_queue();
        for i in 0..8 {
            h.try_send(format!("L{i}")).unwrap();
        }
        let mut out: Vec<u8> = Vec::new();
        let mut pending = None;
        let mut lim = BurstLimiter::new(5);
        let wait = pump_writes(&mut out, &q, &mut pending, &mut lim).unwrap();
        assert!(wait.is_some());
        assert_eq!(String::from_utf8(out).unwrap().matches("\r\n").count(), 5);
        assert_eq!(pending.as_deref(), Some("L5"));
    }

    #[test]
    fn done_signal() {
        let (h, q) = new_write_queue();
        assert!(!h.wait_done(Duration::from_millis(10)));
        q.mark_done();
        assert!(h.wait_done(Duration::from_millis(10)));
        assert!(!q.finish_requested());
        h.stop();
        assert!(q.finish_requested());
    }
}
