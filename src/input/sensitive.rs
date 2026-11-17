//! Password detection for the input history and the chat logs.
//!
//! * [`is_sensitive_history`] — the USER'S INPUT LINE: may be broad (the first
//!   word `register`/`pass` of something the user typed really is a command).
//! * [`is_sensitive_log_line`] — a LOG line: logs also hold ordinary chat, so
//!   only command contexts count (`register your vote` must stay logged).
//!
//! All inputs are matched on the lower-cased line; separators are ASCII, so
//! byte-wise searching is safe on UTF-8 text.

/// Does this input line carry a password (NickServ IDENTIFY / REGISTER /
/// CONFIRM / SET PASSWORD, PASS, OPER, …)? Such lines are never stored in the
/// history file (`isSensitiveHistory`).
pub fn is_sensitive_history(line: &str) -> bool {
    let low = line.trim().to_lowercase();
    if low.is_empty() {
        return false;
    }
    // Server and operator passwords are secret in any context ("/raw PASS x").
    if is_raw_secret_command(&low) {
        return true;
    }
    let fields: Vec<&str> = low.split_whitespace().collect();
    if matches!(
        fields.as_slice(),
        ["/join" | "/j", _, _, ..] | ["/raw", "join", _, _, ..]
    ) {
        return true;
    }
    // /msg nickserv identify <pass> / register <pass> <email> / confirm <code> / set password <pass>
    const NS_PREFIX: &str = "msg nickserv";
    let nick_cmd = low.starts_with("/ns ")
        || low.starts_with("/nickserv ")
        || low.starts_with("/squery ")
        || low.starts_with("nickserv ")
        || low.contains(NS_PREFIX);
    if !nick_cmd {
        // Not a NickServ command: look for the word "identify" as a whole
        // field ("identification" / "re-register" inside words must not match).
        if contains_word(&low, "identify") {
            return true;
        }
        // Direct forms: the line STARTS with the command ("register …",
        // "set password …", "confirm …", "auth …").
        return is_bare_secret_command(&low);
    }
    for kw in [
        "identify",
        "ident ",
        "register",
        "confirm",
        "set password",
        "auth",
    ] {
        if low.contains(kw) {
            return true;
        }
    }
    // GHOST / RECOVER / RELEASE <nick> <password>: the password is optional,
    // so only the form with it (a word after the nick) counts.
    if fields
        .windows(3)
        .any(|w| matches!(w[0], "ghost" | "recover" | "release"))
    {
        return true;
    }
    // Short form "set p <password>".
    is_set_password_short(&low)
}

/// Does this LOG line have to hide a password? (`isSensitiveLogLine`)
///
/// Only command CONTEXTS count: `msg nickserv …` (also `/ns …`,
/// `/nickserv …`, `nickserv …`), lines starting with `/`, and server /
/// operator password commands (`PASS x`, `OPER x`, `/raw …`). Plain chat such
/// as "register your vote today" or "pass the salt please" stays logged.
pub fn is_sensitive_log_line(line: &str) -> bool {
    let low = line.trim().to_lowercase();
    if low.is_empty() {
        return false;
    }
    if is_sensitive_log_raw_secret(&low) {
        return true;
    }
    // A user command ("/ns identify …", "/register …") is clearly a command.
    if low.starts_with('/') {
        return is_sensitive_history(&low);
    }
    if low.starts_with("/ns ")
        || low.starts_with("/nickserv ")
        || low.starts_with("/squery ")
        || low.starts_with("nickserv ")
        || low.contains("msg nickserv")
    {
        return is_sensitive_history(&low);
    }
    // Without a command context nothing is hidden.
    false
}

/// Text shown instead of a hidden password (screen, logs, `/exportjson`).
pub const HIDDEN_SECRET: &str = "[slaptažodis paslėptas]";

/// Is `target` an IRC services bot? `NickServ`, `ChanServ`, `OperServ`, … —
/// any name ending in `serv` — and the `service@server.name` form
/// (`Q@CServe.quakenet.org`, `NickServ@services.`).
pub fn is_service_target(target: &str) -> bool {
    let low = target.trim().to_ascii_lowercase();
    let nick = low.split('@').next().unwrap_or("");
    nick.ends_with("serv") || (low.contains('@') && !nick.is_empty())
}

/// Does the user's OWN message `text`, sent to `target`, carry a password?
/// Used for the on-screen echo (and therefore `/search`, logs and
/// `/exportjson`, which all read the stored message).
///
/// Same rules as the logs: a services bot gets the NickServ command rules
/// (`IDENTIFY x`, `REGISTER x mail`, `SET PASSWORD x`, …); anyone else only
/// the command contexts of [`is_sensitive_log_line`] (`PASS x`, `/oper …`),
/// so ordinary chat ("identify the bug") is shown as typed.
pub fn is_sensitive_outgoing(target: &str, text: &str) -> bool {
    let plain = text.trim();
    if is_sensitive_log_line(plain) {
        return true;
    }
    is_service_target(target) && is_sensitive_history(&format!("nickserv {plain}"))
}

/// Normalise whitespace: split on Unicode whitespace, rejoin with one space.
fn norm_ws(low: &str) -> String {
    low.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// LOG level: is this a server/operator password COMMAND (not chat)?
/// `/raw pass x`, `/pass x` and a bare `pass x` whose argument LOOKS like a
/// password are secret; "pass the salt please" is a conversation.
pub fn is_sensitive_log_raw_secret(low: &str) -> bool {
    let norm = norm_ws(low);
    if norm.is_empty() {
        return false;
    }
    for cmd in ["pass", "oper", "authenticate"] {
        if norm.starts_with(&format!("/{cmd} ")) || norm.starts_with(&format!("/raw {cmd} ")) {
            return true;
        }
        // Bare form: the server sent a command into the *server* window.
        // "pass the salt please" has several words — conversation.
        if let Some(arg) = norm.strip_prefix(&format!("{cmd} ")) {
            if is_secret_argument(arg) {
                return true;
            }
        }
    }
    false
}

/// Does a command argument look like a password?
///
/// One word, and with a password SHAPE: a digit, an upper-case/mixed-case
/// letter or a special character ("hunter2", "Secret1!", "my!pass"); an
/// all-lower-case plain word counts only at 6 characters or more ("secret") —
/// shorter words ("the", "me", "salt", "labas") are conversation.
pub fn is_secret_argument(arg: &str) -> bool {
    if arg.is_empty() || arg.len() > 64 {
        return false;
    }
    if arg.contains([' ', '\t', '\r', '\n']) {
        return false;
    }
    if arg.chars().any(|c| c.is_ascii_digit()) {
        return true;
    }
    if arg != arg.to_lowercase() {
        return true;
    }
    if arg.chars().any(|c| !(c.is_alphabetic() || c.is_numeric())) {
        // Special characters ("my!pass") — most likely a password.
        return true;
    }
    // Six or more letters: "secret" is a password, "the" / "salt" / "labas" are not.
    arg.chars().count() >= 6
}

/// `PASS <pass>` / `OPER <pass>` — also with `/` or `/raw ` prefix. Used for
/// the HISTORY: the user typed the line himself, so a bare `pass x` is a
/// command. Whitespace is normalised (`/raw  PASS x` == `/raw pass x`).
pub fn is_raw_secret_command(low: &str) -> bool {
    let norm = norm_ws(low);
    if norm.is_empty() {
        return false;
    }
    for cmd in ["pass", "oper", "authenticate"] {
        for prefix in [
            format!("{cmd} "),
            format!("/{cmd} "),
            format!("/raw {cmd} "),
        ] {
            if norm.starts_with(&prefix) {
                return true;
            }
        }
        if norm == cmd || norm == format!("/{cmd}") {
            return true;
        }
    }
    false
}

/// Is `word` present as a separate field (not inside another word)?
pub fn contains_word(low: &str, word: &str) -> bool {
    if word.is_empty() {
        return false;
    }
    let lb = low.as_bytes();
    let mut idx = 0;
    loop {
        let Some(rel) = low[idx..].find(word) else {
            return false;
        };
        let i = idx + rel;
        let before = i == 0 || is_word_sep(lb[i - 1]);
        let after_idx = i + word.len();
        let after = after_idx >= lb.len() || is_word_sep(lb[after_idx]);
        if before && after {
            return true;
        }
        idx = i + word.len();
        if idx >= low.len() {
            return false;
        }
    }
}

fn is_word_sep(b: u8) -> bool {
    matches!(b, b' ' | b'\t' | b'\n' | b'\r')
}

/// Commands that START the line and carry a password (no `nickserv` prefix):
/// `register <pass> <email>`, `confirm <code>`, `set password <pass>`
/// (also `set p <pass>`), `auth <pass>`, `identify <pass>`.
pub fn is_bare_secret_command(low: &str) -> bool {
    let fields: Vec<&str> = low.split_whitespace().collect();
    let Some(first) = fields.first() else {
        return false;
    };
    match *first {
        "register" | "confirm" | "auth" | "identify" | "ident" | "password" => fields.len() > 1,
        "set" => {
            // "set password <pass>" / "set p <pass>" — but "set away" is not a password.
            fields.len() >= 3 && matches!(fields[1], "p" | "pass" | "password")
        }
        _ => false,
    }
}

/// NickServ `set p <password>` (short form; also `set pass`, `set password`
/// anywhere in a NickServ command).
pub fn is_set_password_short(low: &str) -> bool {
    let mut idx = 0;
    loop {
        let Some(rel) = low[idx..].find("set ") else {
            return false;
        };
        let i = idx + rel;
        let rest = low[i + 4..].trim();
        if rest.is_empty() {
            return false;
        }
        let word = match rest.find([' ', '\t']) {
            Some(j) => &rest[..j],
            None => rest,
        };
        if matches!(word, "p" | "pass" | "password") {
            return true;
        }
        idx = i + 4;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn history_hides_nickserv_passwords() {
        for s in [
            "/msg nickserv IDENTIFY Slaptazodis1",
            "/msg NickServ identify pass",
            "/ns identify pass",
            "/ns ident pass",
            "/msg NickServ REGISTER Slaptazodis1 asdf@lt.lt",
            "/msg nickserv register pass123 mail@example.com",
            "/msg NickServ CONFIRM 12345",
            "/msg NickServ SET PASSWORD naujas1",
            "/msg NickServ set p slapta",
            "/nickserv register pass mail@x.lt",
            "register histSecret42 a@b.c",
            "set password histSecret42",
        ] {
            assert!(is_sensitive_history(s), "{s:?} must be sensitive");
        }
        for s in [
            "labas",
            "/join #lietuviai",
            "/ns info",
            "sakykite register kitoj vietoj",
            "/msg kazkadas labas",
            "/me šoka",
            "/msg NickServ help",
            "identification",
            "sveiki",
            "",
        ] {
            assert!(!is_sensitive_history(s), "{s:?} must not be sensitive");
        }
    }

    #[test]
    fn raw_secret_whitespace_is_normalised() {
        for s in [
            "/raw pass secret",
            "/raw  pass secret",
            "/raw   pass secret",
            "/raw\tpass\tsecret",
            "/raw  oper secret",
            "/pass secret",
            "/oper secret",
            "PASS secret",
            "OPER secret",
            "/raw AUTHENTICATE AG5pY2sAc2VjcmV0",
            "/raw\tAUTHENTICATE\tAG5pY2sAc2VjcmV0",
            "/join #private channelkey",
            "/j #private channelkey",
            "/raw JOIN #private channelkey",
        ] {
            assert!(is_sensitive_history(s), "{s:?} history");
            assert!(is_sensitive_log_line(s), "{s:?} log");
        }
        assert!(!is_sensitive_history("/join #public"));
    }

    #[test]
    fn secret_argument_heuristics() {
        for s in [
            "",
            "the",
            "me",
            "salt",
            "it",
            "labas",
            "pass the salt please",
        ] {
            assert!(!is_secret_argument(s), "{s:?}");
        }
        for s in [
            "hunter2",
            "Secret1",
            "my!pass",
            "labas123",
            "myverysecretpass",
        ] {
            assert!(is_secret_argument(s), "{s:?}");
        }
        assert!(!is_sensitive_log_line("pass the"));
        assert!(is_sensitive_log_line("pass hunter2"));
    }

    #[test]
    fn log_line_keeps_ordinary_chat() {
        for s in [
            "register your vote today",
            "identify the problem",
            "pass the salt please",
            "set away",
        ] {
            assert!(!is_sensitive_log_line(s), "{s:?}");
        }
        assert!(is_sensitive_log_line("/ns identify hunter2"));
        assert!(is_sensitive_log_line("/msg NickServ set p slapta"));
    }

    #[test]
    fn contains_word_checks_boundaries() {
        assert!(contains_word("please identify now", "identify"));
        assert!(!contains_word("identification", "identify"));
        assert!(contains_word("x identifyy identify", "identify"));
        assert!(!contains_word("abc", "identify"));
        assert!(!contains_word("abc", ""));
    }

    #[test]
    fn outgoing_to_services_and_ghost() {
        for (t, m) in [
            ("NickServ", "IDENTIFY hunter2"),
            ("nickserv", "ghost bob hunter2"),
            ("NickServ", "RECOVER bob hunter2"),
            ("ChanServ", "IDENTIFY #c hunter2"),
            ("Q@CServe.quakenet.org", "AUTH bob hunter2"),
            ("bob", "/oper admin hunter2"),
        ] {
            assert!(is_sensitive_outgoing(t, m), "{t} {m}");
        }
        for (t, m) in [
            ("bob", "please identify the bug"),
            ("bob", "register your vote"),
            ("NickServ", "INFO bob"),
            ("NickServ", "GHOST bob"),
            ("#chan", "ghost town tonight"),
        ] {
            assert!(!is_sensitive_outgoing(t, m), "{t} {m}");
        }
        assert!(is_sensitive_history("/msg nickserv ghost bob hunter2"));
        assert!(is_service_target("OperServ"));
        assert!(!is_service_target("bob"));
    }
}
