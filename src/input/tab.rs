//! Tab autocomplete (`doTab`): `/commands`, nicks of the active window (plus
//! nicks that spoke in it) and window / channel names.
//!
//! The first Tab replaces the word before the cursor by the first candidate
//! and remembers the list; every further Tab cycles through it. With several
//! candidates the input carries a visible `" [i/N]"` hint after the cursor;
//! it is dropped as soon as another key is pressed (see
//! `State::accept_tab_completion`).

use std::collections::HashSet;

use crate::client::types::{Client, State};
use crate::sanitize::sanitize_text;
use crate::ui::strip_mode;

/// Every command Tab can complete (`allCommands`).
pub const ALL_COMMANDS: &[&str] = &[
    "/away",
    "/back",
    "/clear",
    "/clearlogs",
    "/close",
    "/connect",
    "/cycle",
    "/exit",
    "/exportjson",
    "/ignore",
    "/ignores",
    "/invite",
    "/join",
    "/j",
    "/list",
    "/leave",
    "/logsearch",
    "/me",
    "/msg",
    "/m",
    "/mode",
    "/kick",
    "/k",
    "/names",
    "/notice",
    "/notify",
    "/nick",
    "/open",
    "/op",
    "/deop",
    "/voice",
    "/devoice",
    "/part",
    "/query",
    "/quit",
    "/q",
    "/raw",
    "/reconnect",
    "/search",
    "/set",
    "/time",
    "/topic",
    "/unignore",
    "/urls",
    "/w",
    "/wc",
    "/whois",
    "/wi",
    "/win",
    "/window",
    "/help",
    "/keys",
];

/// Does `word` start with a channel-type character (CHANTYPES)? An empty
/// `chan_types` means the standard `#`.
pub fn is_channel_word(word: &str, chan_types: &str) -> bool {
    let Some(first) = word.chars().next() else {
        return false;
    };
    chan_types.contains(first) || first == '#'
}

/// The last space-separated word (trailing spaces ignored), as a slice
/// (`wordBefore`).
pub fn word_before(s: &str) -> &str {
    let s = s.trim_end_matches(' ');
    match s.rfind(' ') {
        Some(i) => &s[i + 1..],
        None => s,
    }
}

/// `s` without its last word, counted in CHARACTERS (a byte count would cut
/// UTF-8 in half: "žinutė kai" must give "žinutė ") (`stripLastWord`).
pub fn strip_last_word(s: &str) -> String {
    let total = s.chars().count();
    let word = word_before(s).chars().count();
    if word >= total {
        return String::new();
    }
    s.chars().take(total - word).collect()
}

/// Text appended after the completed word: `: ` for a nick at the start of
/// the line, a space for a nick/channel later on, nothing for a command.
fn suffix_for(word: &str, prefix: &str) -> &'static str {
    if word.starts_with('/') {
        ""
    } else if prefix.is_empty() {
        ": "
    } else {
        " "
    }
}

impl State {
    /// Write `prefix + chosen + suffix (+ hint)` into the input, cursor
    /// before the hint.
    fn apply_completion(&mut self, prefix: &str, chosen: &str, suffix: &str, hint: &str) {
        // Tekstas UŽ kursoriaus išsaugomas (anksčiau Tab eilutės viduryje jį
        // ištrindavo); jei jis prasideda tarpu, priesagos tarpas nedubliuojamas.
        let tail = self.tab_tail.clone();
        let suffix = if tail.starts_with(' ') {
            suffix.trim_end_matches(' ')
        } else {
            suffix
        };
        let head = format!("{prefix}{chosen}{suffix}");
        self.input_pos = head.chars().count();
        self.input = format!("{head}{hint}{tail}").chars().collect();
    }
}

impl Client {
    /// Tab key: complete the word before the cursor or cycle the candidates.
    pub fn do_tab(&self) {
        {
            let mut st = self.mu.lock();
            if st.active_chan().is_none() {
                return;
            }

            // ---- cycling through an existing candidate list ----
            if !st.tab_candidates.is_empty() {
                let total = st.tab_candidates.len();
                st.tab_idx = (st.tab_idx + 1) % total;
                let chosen = st.tab_candidates[st.tab_idx].clone();
                let word = word_before(&st.tab_prefix).to_string();
                let prefix = strip_last_word(&st.tab_prefix);
                let suffix = suffix_for(&word, &prefix);
                let hint = if total > 1 {
                    format!(" [{}/{}]", st.tab_idx + 1, total)
                } else {
                    String::new()
                };
                st.apply_completion(&prefix, &chosen, suffix, &hint);
                drop(st);
                self.queue_redraw();
                return;
            }

            // ---- a fresh completion ----
            let pos = st.input_pos.min(st.input.len());
            let input_str: String = st.input[..pos].iter().collect();
            // The cursor is after a space: there is no word to complete.
            if input_str.ends_with(' ') {
                return;
            }
            let word = word_before(&input_str).to_string();
            if word.is_empty() {
                return;
            }
            let word_low = word.to_lowercase();

            let chan_types = self.isup_snapshot().0;
            let candidates: Vec<String> = if word.starts_with('/') {
                ALL_COMMANDS
                    .iter()
                    .filter(|cmd| cmd.starts_with(&word_low))
                    .map(|c| c.to_string())
                    .collect()
            } else if is_channel_word(&word, &chan_types) {
                // Channel names from the OPEN windows.
                let mut seen = HashSet::new();
                let mut out = Vec::new();
                for ch in &st.channels {
                    let bare = sanitize_text(&ch.name);
                    if !seen.contains(&bare) && bare.to_lowercase().starts_with(&word_low) {
                        seen.insert(bare.clone());
                        out.push(bare);
                    }
                }
                out
            } else {
                let mut seen = HashSet::new();
                let mut out = Vec::new();
                if let Some(active) = st.active_chan() {
                    let signs = active.prefix_signs(None);
                    // Nicks come from the server (JOIN/NAMES) and may carry ESC/OSC
                    // sequences — they land in the input line, so sanitize them
                    // (otherwise Tab would be a live OSC 52 clipboard sink).
                    for n in &active.nicks {
                        let bare = sanitize_text(strip_mode(n, &signs));
                        if !seen.contains(&bare) && bare.to_lowercase().starts_with(&word_low) {
                            seen.insert(bare.clone());
                            out.push(bare);
                        }
                    }
                    // Then everyone who spoke in this window.
                    for m in &active.messages {
                        if m.nick.is_empty() {
                            continue;
                        }
                        let nick = sanitize_text(&m.nick);
                        if seen.insert(nick.clone()) && nick.to_lowercase().starts_with(&word_low) {
                            out.push(nick);
                        }
                    }
                }
                out
            };

            if candidates.is_empty() {
                return;
            }
            let total = candidates.len();
            let chosen = candidates[0].clone();
            st.tab_candidates = candidates;
            st.tab_idx = 0;
            st.tab_prefix = input_str.clone();
            st.tab_tail = st.input[pos..].iter().collect();

            let prefix = strip_last_word(&input_str);
            let suffix = suffix_for(&word, &prefix);
            let hint = if total > 1 {
                format!(" [1/{total}]")
            } else {
                String::new()
            };
            st.apply_completion(&prefix, &chosen, suffix, &hint);
        }
        self.queue_redraw();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::input::testutil::*;

    #[test]
    fn strip_last_word_keeps_utf8() {
        for (inp, want) in [
            ("žinutė kai", "žinutė "),
            ("žinutė", ""),
            ("abc def", "abc "),
            ("", ""),
            ("a", ""),
        ] {
            assert_eq!(strip_last_word(inp), want, "{inp:?}");
        }
    }

    #[test]
    fn word_before_ignores_trailing_spaces() {
        assert_eq!(word_before("hello wor"), "wor");
        assert_eq!(word_before("hello  "), "hello");
        assert_eq!(word_before(""), "");
    }

    #[test]
    fn channel_word_uses_chantypes() {
        assert!(is_channel_word("#a", ""));
        assert!(is_channel_word("&a", "#&"));
        assert!(!is_channel_word("&a", "#"));
        assert!(!is_channel_word("", "#"));
    }

    #[test]
    fn nick_completion_adds_colon() {
        let c = client();
        open_active(&c, "#chan");
        {
            let mut st = c.mu.lock();
            let ch = st.active_chan_mut().unwrap();
            ch.add_nick("@Jonas", None);
            ch.add_nick("Janina", None);
            ch.add_nick("Petras", None);
        }
        set_input(&c, "Ja");
        c.do_tab();
        assert_eq!(input_text(&c), "Janina: ");
    }

    #[test]
    fn command_completion() {
        let c = client();
        set_input(&c, "/jo");
        c.do_tab();
        assert_eq!(input_text(&c), "/join");
    }

    #[test]
    fn tab_cycles_candidates_with_hint() {
        let c = client();
        open_active(&c, "#chan");
        {
            let mut st = c.mu.lock();
            let ch = st.active_chan_mut().unwrap();
            ch.add_nick("Alice", None);
            ch.add_nick("Alfredas", None);
        }
        set_input(&c, "Al");
        c.do_tab();
        let first = input_text(&c);
        c.do_tab();
        let second = input_text(&c);
        assert_ne!(first, second);
        assert!(
            first.starts_with("Alfredas: [1/2]") || first.starts_with("Alfredas:  [1/2]"),
            "{first:?}"
        );
        assert!(second.contains("Alice"), "{second:?}");
        assert!(second.ends_with(" [2/2]"));
        // Third Tab wraps around.
        c.do_tab();
        assert!(input_text(&c).contains("Alfredas"));
    }

    #[test]
    fn hint_is_dropped_when_typing_continues() {
        let c = client();
        open_active(&c, "#chan");
        {
            let mut st = c.mu.lock();
            let ch = st.active_chan_mut().unwrap();
            ch.add_nick("Alice", None);
            ch.add_nick("Alfredas", None);
        }
        set_input(&c, "Al");
        c.do_tab();
        assert!(input_text(&c).ends_with(" [1/2]"));
        c.handle_key(b"labas");
        assert_eq!(input_text(&c), "Alfredas: labas");
    }

    #[test]
    fn lithuanian_nick_completion() {
        let c = client();
        open_active(&c, "#kanalas");
        c.mu.lock()
            .active_chan_mut()
            .unwrap()
            .add_nick("Žilvinas", None);
        set_input(&c, "žil");
        c.do_tab();
        assert_eq!(input_text(&c), "Žilvinas: ");
    }

    #[test]
    fn channel_name_completion_cycles() {
        let c = client();
        c.parse_irc(":me!u@h JOIN #libera");
        c.parse_irc(":me!u@h JOIN #lietuva");
        set_input(&c, "/join #li");
        c.do_tab();
        let got = input_text(&c);
        assert!(
            got.starts_with("/join #libera") || got.starts_with("/join #lietuva"),
            "{got:?}"
        );
        assert_ne!(got, "/join #li");
        c.do_tab();
        assert_ne!(input_text(&c), got);
    }

    #[test]
    fn nicks_from_messages_are_candidates_after_listed_nicks() {
        let c = client();
        open_active(&c, "#chan");
        c.add_msg(
            "#chan",
            crate::client::types::new_msg(
                chrono::Utc::now(),
                "Zorro",
                "hi",
                crate::ui::MsgType::Chat,
            ),
        );
        set_input(&c, "zo");
        c.do_tab();
        assert_eq!(input_text(&c), "Zorro: ");
    }

    #[test]
    fn nick_candidates_are_sanitized() {
        let c = client();
        open_active(&c, "#chan");
        c.mu.lock()
            .active_chan_mut()
            .unwrap()
            .add_nick("ev\x1b]52;c;x\x07il", None);
        set_input(&c, "ev");
        c.do_tab();
        let t = input_text(&c);
        assert!(!t.contains('\x1b') && !t.contains('\x07'), "{t:?}");
    }

    #[test]
    fn no_completion_after_a_space_or_without_match() {
        let c = client();
        set_input(&c, "/join ");
        c.do_tab();
        assert_eq!(input_text(&c), "/join ");
        set_input(&c, "/zzz");
        c.do_tab();
        assert_eq!(input_text(&c), "/zzz");
        assert!(c.mu.lock().tab_candidates.is_empty());
    }

    #[test]
    fn all_commands_are_known_to_handle_cmd() {
        // Every completable command (except aliases) is dispatched, never "Nežinoma komanda".
        let c = client();
        {
            // /reconnect starts a connect loop. Point it at a closed port and
            // turn reconnect off so the spawned thread exits immediately.
            let mut st = c.mu.lock();
            st.reconnect = false;
            st.server = "127.0.0.1".into();
            st.port = "1".into();
            st.mouse = false;
        }
        for cmd in ALL_COMMANDS {
            if matches!(*cmd, "/quit" | "/exit" | "/q") {
                continue; // would stop the client
            }
            let name = &cmd[1..];
            c.handle_cmd(name, "");
            let t = server_texts(&c);
            assert!(
                !t.iter()
                    .any(|l| l.starts_with(&format!("Nežinoma komanda: /{name} "))),
                "{cmd} unknown"
            );
        }
    }
}
