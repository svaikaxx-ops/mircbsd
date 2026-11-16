//! ISUPPORT (005) parameters, CHANMODES groups and mode-parameter rules.

/// Default CHANMODES when the server did not send any (RFC 2811).
pub const DEFAULT_CHAN_MODES: &str = "eIbq,k,flj,CFLMPQScgimnprstz";

/// Server limits (005). Zero means "server did not announce" (use defaults).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct IsupLimits {
    pub nick_len: i32,
    pub channel_len: i32,
    pub topic_len: i32,
    pub kick_len: i32,
    pub modes: i32,
    /// `MONITOR=` — max watched nicks (0 = unsupported).
    pub monitor: i32,
}

/// Per-server protocol parameters (the Go `isupMu`-protected fields).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Isup {
    pub chantypes: String,
    pub prefix_signs: String,
    pub prefix_modes: String,
    pub casemapping: String,
    pub chan_modes: String,
    pub network: String,
    pub limits: IsupLimits,
}

impl Default for Isup {
    fn default() -> Self {
        Isup {
            chantypes: "#&".into(),
            prefix_signs: "~&@%+".into(),
            prefix_modes: "qaohv".into(),
            casemapping: "rfc1459".into(),
            chan_modes: String::new(),
            network: String::new(),
            limits: IsupLimits::default(),
        }
    }
}

/// Parse a non-negative integer; invalid → `def` (`atoiDefault`).
pub fn atoi_default(s: &str, def: i32) -> i32 {
    match s.parse::<i32>() {
        Ok(n) if n >= 0 => n,
        _ => def,
    }
}

impl Isup {
    /// Apply 005 tokens (`applyISUPPORT`): CHANTYPES, PREFIX, CHANMODES,
    /// CASEMAPPING, NETWORK, NICKLEN, CHANNELLEN, TOPICLEN, KICKLEN, MODES, MONITOR.
    pub fn apply<S: AsRef<str>>(&mut self, tokens: &[S]) {
        for tok in tokens {
            let tok = tok.as_ref();
            if let Some(v) = tok.strip_prefix("CHANTYPES=") {
                if !v.is_empty() {
                    // "-" means "no channels".
                    let t = v.trim_matches('-');
                    self.chantypes = if t.is_empty() { "#&".into() } else { t.into() };
                }
            } else if let Some(v) = tok.strip_prefix("PREFIX=") {
                if let Some(i) = v.find(')') {
                    if i > 0 {
                        if let (Some(modes), Some(signs)) = (v.get(1..i), v.get(i + 1..)) {
                            if !modes.is_empty() && modes.chars().count() == signs.chars().count() {
                                self.prefix_modes = modes.to_string();
                                self.prefix_signs = signs.to_string();
                            }
                        }
                    }
                }
            } else if let Some(v) = tok.strip_prefix("CHANMODES=") {
                self.chan_modes = v.to_string();
            } else if let Some(v) = tok.strip_prefix("CASEMAPPING=") {
                if matches!(v, "ascii" | "rfc1459" | "strict-rfc1459") {
                    self.casemapping = v.to_string();
                }
            } else if let Some(v) = tok.strip_prefix("NETWORK=") {
                self.network = v.to_string();
            } else if let Some(v) = tok.strip_prefix("NICKLEN=") {
                self.limits.nick_len = atoi_default(v, self.limits.nick_len);
            } else if let Some(v) = tok.strip_prefix("CHANNELLEN=") {
                self.limits.channel_len = atoi_default(v, self.limits.channel_len);
            } else if let Some(v) = tok.strip_prefix("TOPICLEN=") {
                self.limits.topic_len = atoi_default(v, self.limits.topic_len);
            } else if let Some(v) = tok.strip_prefix("KICKLEN=") {
                self.limits.kick_len = atoi_default(v, self.limits.kick_len);
            } else if let Some(v) = tok.strip_prefix("MODES=") {
                self.limits.modes = atoi_default(v, self.limits.modes);
            } else if let Some(v) = tok.strip_prefix("MONITOR=") {
                self.limits.monitor = atoi_default(v, 0);
            }
        }
    }

    /// Is `name` a channel according to CHANTYPES (`isChannel`)?
    pub fn is_channel(&self, name: &str) -> bool {
        match name.chars().next() {
            Some(c) => self.chantypes.contains(c),
            None => false,
        }
    }

    /// NICKLEN or the default of 30.
    pub fn nick_len_limit(&self) -> i32 {
        if self.limits.nick_len > 0 {
            self.limits.nick_len
        } else {
            30
        }
    }

    /// CHANNELLEN or the default of 50.
    pub fn channel_len_limit(&self) -> i32 {
        if self.limits.channel_len > 0 {
            self.limits.channel_len
        } else {
            50
        }
    }

    /// TOPICLEN or the default of 390.
    pub fn topic_len_limit(&self) -> i32 {
        if self.limits.topic_len > 0 {
            self.limits.topic_len
        } else {
            390
        }
    }

    /// KICKLEN (0 = not announced).
    pub fn kick_len_limit(&self) -> i32 {
        self.limits.kick_len
    }

    pub fn chan_mode_groups(&self) -> ChanModeGroups {
        parse_chan_modes(&self.chan_modes)
    }
}

/// CHANMODES split into the four RFC groups A,B,C,D.
///
/// A: list modes (always a parameter); B: setting (always); C: setting
/// (parameter only when adding); D: no parameter.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ChanModeGroups {
    pub a: String,
    pub b: String,
    pub c: String,
    pub d: String,
}

/// Split CHANMODES into four groups; missing groups stay empty, an empty
/// string selects the RFC 2811 default (`parseChanModes`).
pub fn parse_chan_modes(chan_modes: &str) -> ChanModeGroups {
    let src = if chan_modes.is_empty() {
        DEFAULT_CHAN_MODES
    } else {
        chan_modes
    };
    let mut g = ChanModeGroups::default();
    for (i, part) in src.split(',').enumerate() {
        match i {
            0 => g.a = part.to_string(),
            1 => g.b = part.to_string(),
            2 => g.c = part.to_string(),
            3 => g.d = part.to_string(),
            _ => break,
        }
    }
    g
}

impl ChanModeGroups {
    /// Does mode `letter` take a parameter in this context (`takesParam`)?
    ///
    /// * `prefix_modes` — nick status modes: always take a nick.
    /// * `adding` — C-group modes take a parameter only when set (`+`).
    pub fn takes_param(&self, letter: char, adding: bool, prefix_modes: &str) -> bool {
        if prefix_modes.contains(letter) {
            return true;
        }
        if self.a.contains(letter) || self.b.contains(letter) {
            return true;
        }
        if self.c.contains(letter) {
            return adding;
        }
        false
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn chanmodes_groups() {
        let g = parse_chan_modes("beI,k,l,imnpstaqr");
        assert!(g.takes_param('b', false, "ov"));
        assert!(g.takes_param('k', false, "ov"));
        assert!(g.takes_param('l', true, "ov"));
        assert!(!g.takes_param('l', false, "ov"));
        assert!(!g.takes_param('m', true, "ov"));
        assert!(g.takes_param('o', false, "ov"));
    }

    #[test]
    fn chanmodes_default_and_short() {
        assert_eq!(parse_chan_modes("").a, "eIbq");
        let g = parse_chan_modes("b,k");
        assert_eq!(
            (g.a.as_str(), g.b.as_str(), g.c.as_str(), g.d.as_str()),
            ("b", "k", "", "")
        );
    }

    #[test]
    fn apply_tokens() {
        let mut i = Isup::default();
        i.apply(&[
            "CHANTYPES=#",
            "PREFIX=(ov)@+",
            "CASEMAPPING=ascii",
            "NICKLEN=20",
            "TOPICLEN=bad",
            "MONITOR=100",
            "NETWORK=Libera.Chat",
            "CHANMODES=eIbq,k,flj,CFLMPQScgimnprstz",
        ]);
        assert_eq!(i.chantypes, "#");
        assert_eq!(i.prefix_modes, "ov");
        assert_eq!(i.prefix_signs, "@+");
        assert_eq!(i.casemapping, "ascii");
        assert_eq!(i.limits.nick_len, 20);
        assert_eq!(i.limits.topic_len, 0);
        assert_eq!(i.topic_len_limit(), 390);
        assert_eq!(i.limits.monitor, 100);
        assert_eq!(i.network, "Libera.Chat");
    }

    #[test]
    fn rejects_bad_values() {
        let mut i = Isup::default();
        i.apply(&[
            "CHANTYPES=-",
            "PREFIX=(ov)@",
            "CASEMAPPING=weird",
            "PREFIX=",
            "PREFIX=)",
        ]);
        assert_eq!(i.chantypes, "#&");
        assert_eq!(i.prefix_modes, "qaohv");
        assert_eq!(i.casemapping, "rfc1459");
    }

    #[test]
    fn is_channel() {
        let i = Isup::default();
        assert!(i.is_channel("#a"));
        assert!(!i.is_channel("nick"));
        assert!(!i.is_channel(""));
    }
}
