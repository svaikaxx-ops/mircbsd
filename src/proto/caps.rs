//! IRCv3 capability negotiation helpers and SASL decision logic.

use std::collections::HashSet;

/// Capabilities requested on `CAP LS` and `CAP NEW` (shared so the two paths
/// do not drift).
pub const IRCV3_WANTED_CAPS: &[&str] = &[
    "multi-prefix",
    "away-notify",
    "server-time",
    "echo-message",
    // v0.4.7
    "extended-join",
    "account-notify",
    "chghost",
    "invite-notify",
];

/// Warning shown when `sasl_external` is set but cannot be honoured.
pub const SASL_EXTERNAL_WARN: &str = "sasl_external=true, bet nenurodyti sertifikatai (tls_cert/tls_key) arba TLS išjungtas — SASL EXTERNAL nenaudojamas";

/// Lowercased capability names from a `CAP LS`/`NEW` list (`name=value` → `name`).
pub fn parse_cap_names(caps: &str) -> Vec<String> {
    caps.split_whitespace()
        .map(|c| c.split('=').next().unwrap_or("").to_lowercase())
        .collect()
}

/// Wanted capabilities that the server offers and (when `active` is given)
/// that are not yet enabled.
pub fn wanted_caps(
    available: &HashSet<String>,
    active: Option<&HashSet<String>>,
) -> Vec<&'static str> {
    IRCV3_WANTED_CAPS
        .iter()
        .copied()
        .filter(|w| available.contains(*w) && active.map_or(true, |a| !a.contains(*w)))
        .collect()
}

/// Inputs to the SASL decision (all from the client configuration).
#[derive(Clone, Copy, Debug, Default)]
pub struct SaslInputs {
    pub external: bool,
    pub has_cert: bool,
    pub use_tls: bool,
    pub has_password: bool,
    pub already_ok: bool,
}

/// Result of [`sasl_decide`].
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct SaslDecision {
    /// Request the `sasl` capability.
    pub request: bool,
    /// Warn that `sasl_external` is unusable (the caller prints it after
    /// releasing the client lock).
    pub warn: bool,
    /// Mechanism to start when `request` is true.
    pub mech: &'static str,
}

/// EXTERNAL is only usable with a client certificate over TLS.
pub fn sasl_ext_usable(i: &SaslInputs) -> bool {
    i.external && i.has_cert && i.use_tls
}

/// Shared SASL request logic for `CAP LS` and `CAP NEW` (`saslDecide`).
///
/// EXTERNAL (certificate over TLS) wins when usable, otherwise PLAIN with a
/// password; with neither, SASL is not requested.
pub fn sasl_decide(available: &HashSet<String>, i: &SaslInputs) -> SaslDecision {
    let ext_usable = sasl_ext_usable(i);
    let warn = i.external && !ext_usable;
    let request = available.contains("sasl") && (i.has_password || ext_usable) && !i.already_ok;
    SaslDecision {
        request,
        warn,
        mech: if !request {
            ""
        } else if ext_usable {
            "EXTERNAL"
        } else {
            "PLAIN"
        },
    }
}

const B64: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

/// Standard base64 with padding.
pub fn base64_encode(data: &[u8]) -> String {
    let mut out = String::with_capacity(data.len().div_ceil(3) * 4);
    for chunk in data.chunks(3) {
        let b0 = chunk[0] as u32;
        let b1 = chunk.get(1).copied().unwrap_or(0) as u32;
        let b2 = chunk.get(2).copied().unwrap_or(0) as u32;
        let n = (b0 << 16) | (b1 << 8) | b2;
        out.push(B64[(n >> 18) as usize & 63] as char);
        out.push(B64[(n >> 12) as usize & 63] as char);
        out.push(if chunk.len() > 1 {
            B64[(n >> 6) as usize & 63] as char
        } else {
            '='
        });
        out.push(if chunk.len() > 2 {
            B64[n as usize & 63] as char
        } else {
            '='
        });
    }
    out
}

/// SASL PLAIN payload: base64 of `\0authcid\0password`.
pub fn sasl_plain_payload(nick: &str, pass: &str) -> String {
    base64_encode(format!("\0{nick}\0{pass}").as_bytes())
}

/// Split an AUTHENTICATE payload into ≤400-byte chunks; a payload whose last
/// chunk is exactly 400 bytes (or that is empty) ends with `+`.
pub fn authenticate_chunks(payload: &str) -> Vec<String> {
    const MAX: usize = 400;
    if payload.is_empty() {
        return vec!["+".to_string()];
    }
    let mut out: Vec<String> = payload
        .as_bytes()
        .chunks(MAX)
        .map(|c| String::from_utf8_lossy(c).into_owned())
        .collect();
    if payload.len() % MAX == 0 {
        out.push("+".to_string());
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn set(items: &[&str]) -> HashSet<String> {
        items.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn base64_vectors() {
        assert_eq!(base64_encode(b""), "");
        assert_eq!(base64_encode(b"f"), "Zg==");
        assert_eq!(base64_encode(b"fo"), "Zm8=");
        assert_eq!(base64_encode(b"foo"), "Zm9v");
        assert_eq!(base64_encode(b"foobar"), "Zm9vYmFy");
    }

    #[test]
    fn plain_payload() {
        assert_eq!(sasl_plain_payload("a", "b"), "AGEAYg==");
    }

    #[test]
    fn parse_names() {
        assert_eq!(
            parse_cap_names("SASL=PLAIN,EXTERNAL Server-Time"),
            vec!["sasl", "server-time"]
        );
    }

    #[test]
    fn wanted_filters_active() {
        let avail = set(&["server-time", "away-notify", "other"]);
        assert_eq!(
            wanted_caps(&avail, None),
            vec!["away-notify", "server-time"]
        );
        let act = set(&["server-time"]);
        assert_eq!(wanted_caps(&avail, Some(&act)), vec!["away-notify"]);
    }

    #[test]
    fn sasl_plain_vs_external() {
        let avail = set(&["sasl"]);
        let d = sasl_decide(
            &avail,
            &SaslInputs {
                has_password: true,
                ..Default::default()
            },
        );
        assert!(d.request && !d.warn);
        assert_eq!(d.mech, "PLAIN");

        let d = sasl_decide(
            &avail,
            &SaslInputs {
                external: true,
                has_cert: true,
                use_tls: true,
                ..Default::default()
            },
        );
        assert!(d.request);
        assert_eq!(d.mech, "EXTERNAL");

        // External requested but no TLS → warn, no request without password.
        let d = sasl_decide(
            &avail,
            &SaslInputs {
                external: true,
                has_cert: true,
                ..Default::default()
            },
        );
        assert!(d.warn && !d.request);

        // Already authenticated, or no credentials, or not offered.
        assert!(
            !sasl_decide(
                &avail,
                &SaslInputs {
                    has_password: true,
                    already_ok: true,
                    ..Default::default()
                }
            )
            .request
        );
        assert!(!sasl_decide(&avail, &SaslInputs::default()).request);
        assert!(
            !sasl_decide(
                &set(&[]),
                &SaslInputs {
                    has_password: true,
                    ..Default::default()
                }
            )
            .request
        );
    }

    #[test]
    fn authenticate_chunking() {
        assert_eq!(authenticate_chunks("abc"), vec!["abc"]);
        let long = "a".repeat(400);
        assert_eq!(authenticate_chunks(&long).len(), 2);
        assert_eq!(authenticate_chunks(&long)[1], "+");
        assert_eq!(authenticate_chunks(&"a".repeat(401)).len(), 2);
    }
}
