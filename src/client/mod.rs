//! IRC client core (port of the `Client` parts of `irc.go`).
//!
//! * [`types`] — `Client`, `State`, `Channel`, `Message`
//! * [`send`] — write queue, 5 lines/s limiter, CR/LF stripping + truncation
//! * [`channels`] — window management, `add_msg`, ignore list
//! * [`connect`] — connect loop, TCP/TLS, pinning, registration, I/O loop
//! * [`draw`] — `Client::draw` (snapshot, render cache, frame, terminal write)
//! * [`handlers`] — server line dispatch (`parse_irc`)
//! * [`session`] — stop/shutdown, persistence helpers, `/clearlogs`, URL opener

pub mod channels;
pub mod connect;
pub mod draw;
pub mod handlers;
pub mod send;
pub mod session;
pub mod types;

pub use types::{Channel, Client, Message, MsgType, State};

/// Application version (CTCP VERSION, UI).
pub const VERSION: &str = "0.5.1";

/// IRC line limit in bytes (RFC 1459/2812), CRLF included.
pub const MAX_LINE_BYTES: usize = 512;

/// Per-connection read buffer. Larger than [`MAX_LINE_BYTES`]: IRCv3 tags
/// (server-time, msgid) are NOT part of the 512-byte budget, so real lines
/// reach ~550+ bytes. Longer lines are rejected, never buffered.
pub const MAX_READ_BUFFER: usize = 4096;

/// Max lines per second sent to the server (flood protection).
pub const IRC_SEND_BURST: usize = 5;

/// Max bytes reflected in a CTCP PING reply (no amplification).
pub const MAX_CTCP_BYTES: usize = 64;

/// CTCP replies (VERSION/PING) allowed per [`CTCP_REPLY_WINDOW`]: a CTCP
/// flood from many nicks must not fill the send queue (the user's own lines
/// would be dropped) or get us killed for excess flood.
pub const MAX_CTCP_REPLIES: usize = 4;
pub const CTCP_REPLY_WINDOW: std::time::Duration = std::time::Duration::from_secs(10);

/// Max real channels per session.
pub const MAX_CHANNELS: usize = 100;

/// Separate budget for private conversation windows, so PM floods cannot
/// starve real channels.
pub const MAX_PRIVATE_CHANNELS: usize = 200;

/// Max entries in the AWAY throttle map.
pub const MAX_AWAY_NOTIFIED: usize = 5000;

/// OS label for CTCP VERSION.
pub fn os_label() -> &'static str {
    match std::env::consts::OS {
        "linux" => "Linux",
        "freebsd" | "openbsd" | "netbsd" | "dragonfly" => "FreeBSD",
        "macos" => "macOS",
        "windows" => "Windows",
        other => other,
    }
}
