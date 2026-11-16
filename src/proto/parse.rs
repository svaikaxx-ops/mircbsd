//! IRC line parsing (`parseIRC` front half of `irc.go`).
//!
//! Splits a raw line into IRCv3 tags, prefix, command and parameters. The
//! behaviour matches the Go client: parameters are split on single spaces
//! (consecutive spaces yield empty parameters) and a trailing parameter
//! starts with `:`.

use std::collections::HashMap;

use chrono::{DateTime, Utc};

/// A parsed IRC line.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct IrcLine {
    /// IRCv3 message tags (`@a=b;c`). Values are unescaped; missing → "".
    pub tags: HashMap<String, String>,
    /// Source prefix without the leading `:` (`nick!user@host` or server).
    pub prefix: String,
    /// Command or numeric, exactly as sent.
    pub cmd: String,
    pub params: Vec<String>,
}

impl IrcLine {
    /// Parameter `n`, or "" when absent (`p(n)` in Go).
    pub fn param(&self, n: usize) -> &str {
        self.params.get(n).map(String::as_str).unwrap_or("")
    }

    /// Nick part of the prefix (`from_nick`).
    pub fn from_nick(&self) -> &str {
        from_nick(&self.prefix)
    }

    /// `time=` tag (IRCv3 server-time) when present and valid.
    pub fn server_time(&self) -> Option<DateTime<Utc>> {
        server_time(&self.tags)
    }
}

/// Nick portion of `nick!user@host` (or the whole prefix when there is no `!`).
pub fn from_nick(prefix: &str) -> &str {
    match prefix.find('!') {
        Some(j) => &prefix[..j],
        None => prefix,
    }
}

/// Parse the contents of a tag block (without the leading `@`).
pub fn parse_tags(raw: &str) -> HashMap<String, String> {
    let mut out = HashMap::new();
    for kv in raw.split(';') {
        if kv.is_empty() {
            continue;
        }
        let (k, v) = match kv.find('=') {
            Some(i) => (&kv[..i], &kv[i + 1..]),
            None => (kv, ""),
        };
        if k.is_empty() {
            continue;
        }
        out.insert(k.to_string(), unescape_tag_value(v));
    }
    out
}

/// IRCv3 tag value unescaping (`\:` `\s` `\\` `\r` `\n`).
pub fn unescape_tag_value(v: &str) -> String {
    if !v.contains('\\') {
        return v.to_string();
    }
    let mut out = String::with_capacity(v.len());
    let mut it = v.chars();
    while let Some(c) = it.next() {
        if c != '\\' {
            out.push(c);
            continue;
        }
        match it.next() {
            Some(':') => out.push(';'),
            Some('s') => out.push(' '),
            Some('\\') => out.push('\\'),
            Some('r') => out.push('\r'),
            Some('n') => out.push('\n'),
            Some(other) => out.push(other),
            None => {}
        }
    }
    out
}

/// Parse the `time` tag (RFC 3339 with fractional seconds) into local time.
pub fn server_time(tags: &HashMap<String, String>) -> Option<DateTime<Utc>> {
    let raw = tags.get("time")?;
    DateTime::parse_from_rfc3339(raw)
        .ok()
        .map(|t| t.with_timezone(&Utc))
}

/// Parse one IRC line (without CRLF). Returns `None` for empty or malformed
/// lines (tag block or prefix without a following command).
pub fn parse_line(line: &str) -> Option<IrcLine> {
    if line.is_empty() {
        return None;
    }
    let mut out = IrcLine::default();
    let mut rest = line;

    if let Some(stripped) = rest.strip_prefix('@') {
        let i = stripped.find(' ')?;
        out.tags = parse_tags(&stripped[..i]);
        rest = &stripped[i + 1..];
    }

    if let Some(stripped) = rest.strip_prefix(':') {
        let i = stripped.find(' ')?;
        out.prefix = stripped[..i].to_string();
        rest = &stripped[i + 1..];
    }

    match rest.find(' ') {
        None => {
            out.cmd = rest.to_string();
            rest = "";
        }
        Some(i) => {
            out.cmd = rest[..i].to_string();
            rest = &rest[i + 1..];
        }
    }

    while !rest.is_empty() {
        if let Some(trailing) = rest.strip_prefix(':') {
            out.params.push(trailing.to_string());
            break;
        }
        match rest.find(' ') {
            None => {
                out.params.push(rest.to_string());
                break;
            }
            Some(i) => {
                out.params.push(rest[..i].to_string());
                rest = &rest[i + 1..];
            }
        }
    }
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_basic_privmsg() {
        let l = parse_line(":nick!u@h PRIVMSG #chan :hello world").unwrap();
        assert_eq!(l.prefix, "nick!u@h");
        assert_eq!(l.from_nick(), "nick");
        assert_eq!(l.cmd, "PRIVMSG");
        assert_eq!(l.params, vec!["#chan", "hello world"]);
        assert_eq!(l.param(5), "");
    }

    #[test]
    fn parses_tags_and_time() {
        let l =
            parse_line("@time=2011-10-19T16:40:51.620Z;msgid=a\\sb :n!u@h NOTICE x :y").unwrap();
        assert_eq!(l.tags.get("msgid").unwrap(), "a b");
        assert!(l.server_time().is_some());
        assert_eq!(l.cmd, "NOTICE");
    }

    #[test]
    fn bad_time_is_ignored() {
        let l = parse_line("@time=garbage PING :x").unwrap();
        assert!(l.server_time().is_none());
        assert_eq!(l.params, vec!["x"]);
    }

    #[test]
    fn rejects_malformed() {
        assert!(parse_line("").is_none());
        assert!(parse_line("@tags").is_none());
        assert!(parse_line(":prefixonly").is_none());
    }

    #[test]
    fn command_without_params() {
        let l = parse_line("PING").unwrap();
        assert_eq!(l.cmd, "PING");
        assert!(l.params.is_empty());
    }

    #[test]
    fn empty_trailing_and_double_space() {
        let l = parse_line("CMD a  b :").unwrap();
        assert_eq!(l.params, vec!["a", "", "b", ""]);
    }

    #[test]
    fn server_prefix_has_no_bang() {
        assert_eq!(from_nick("irc.example.org"), "irc.example.org");
    }
}
