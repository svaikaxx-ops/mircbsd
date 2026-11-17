//! Kanalų raktai (`+k`): įsiminimas, laukiantys JOIN ir grįžimas po
//! pakartotinio prisijungimo.
//!
//! Raktai — SLAPTI: laikomi tik atmintyje (`State::chan_keys`), niekada
//! nerodomi, nerašomi į žurnalus, istoriją, `/exportjson` ar lango antraštę
//! (MODE +k / 324 argumentas rodomas kaip [`HIDDEN_KEY`]).

use crate::validate::{is_valid_channel_for, is_valid_channel_key, STANDARTINES_CHAN_TYPES};

use super::types::State;
use super::{MAX_CHANNELS, MAX_LINE_BYTES};

/// Kaip rodomas kanalo raktas MODE / 324 eilutėse (ir žurnale).
pub const HIDDEN_KEY: &str = "***";

/// Daugiausia įsimintų raktų (priešiškas serveris negali auginti be ribų).
const MAX_CHAN_KEYS: usize = MAX_CHANNELS;

impl State {
    /// Įsimintas kanalo raktas (lyginama pagal serverio CASEMAPPING).
    pub fn chan_key(&self, chan: &str) -> Option<&str> {
        self.chan_keys
            .iter()
            .find(|(c, _)| self.name_eq(c, chan))
            .map(|(_, k)| k.as_str())
    }

    /// Įsiminti kanalo raktą; tuščias raktas — pamiršti. Netinkami raktai
    /// (tarpai, kablelis, valdymo simboliai) neįsimenami: JOIN sąraše jie
    /// sugadintų kanalų / raktų atitikimą.
    pub fn set_chan_key(&mut self, chan: &str, key: &str) {
        if key.is_empty() {
            self.forget_chan_key(chan);
            return;
        }
        if chan.is_empty() || !is_valid_channel_key(key) {
            return;
        }
        let cm = self.casemapping.clone();
        if let Some(e) = self
            .chan_keys
            .iter_mut()
            .find(|(c, _)| crate::validate::nick_equal(c, chan, &cm))
        {
            e.1 = key.to_string();
            return;
        }
        if self.chan_keys.len() >= MAX_CHAN_KEYS {
            self.chan_keys.remove(0);
        }
        self.chan_keys.push((chan.to_string(), key.to_string()));
    }

    /// Pamiršti kanalo raktą (-k, /part, /close).
    pub fn forget_chan_key(&mut self, chan: &str) {
        let cm = self.casemapping.clone();
        self.chan_keys
            .retain(|(c, _)| !crate::validate::nick_equal(c, chan, &cm));
    }

    /// Po 001: kanalai, į kuriuos reikia įeiti — `autojoin` (įrašas
    /// „#kanalas [raktas]“) ir laukiantieji / buvusieji (`rejoin`, išimamas),
    /// be dublikatų, su raktais. Grąžina (kanalas, raktas) ir įspėjimus apie
    /// praleistus netinkamus `autojoin` įrašus.
    pub fn take_pending_joins(
        &mut self,
        chantypes: &str,
    ) -> (Vec<(String, Option<String>)>, Vec<String>) {
        // 005 ateina PO 001: CHANTYPES dar numatytieji, todėl priimami ir
        // standartiniai tipai (anksčiau autojoin siųstas be jokios patikros).
        let chantypes = format!("{chantypes}{STANDARTINES_CHAN_TYPES}");
        let chantypes = chantypes.as_str();
        let mut out: Vec<(String, Option<String>)> = Vec::new();
        let mut warnings = Vec::new();
        for entry in self.auto_join.clone() {
            let mut it = entry.split_whitespace();
            let Some(raw) = it.next() else { continue };
            let mut chan = raw.to_string();
            if !is_valid_channel_for(&chan, chantypes) {
                chan.insert(0, '#');
            }
            if !is_valid_channel_for(&chan, chantypes) {
                warnings.push(format!(
                    "autojoin: netinkamas kanalo pavadinimas „{}“ praleistas",
                    crate::sanitize::sanitize_text(raw)
                ));
                continue;
            }
            if let Some(key) = it.next() {
                if is_valid_channel_key(key) {
                    self.set_chan_key(&chan, key);
                } else {
                    warnings.push(format!(
                        "autojoin: netinkamas {chan} raktas — jungiamasi be jo"
                    ));
                }
            }
            if !out.iter().any(|(c, _)| self.name_eq(c, &chan)) {
                out.push((chan, None));
            }
        }
        for chan in std::mem::take(&mut self.rejoin) {
            if is_valid_channel_for(&chan, chantypes)
                && !out.iter().any(|(c, _)| self.name_eq(c, &chan))
            {
                out.push((chan, None));
            }
        }
        for (chan, key) in out.iter_mut() {
            *key = self.chan_key(chan).map(str::to_string);
        }
        (out, warnings)
    }
}

/// JOIN eilutės kanalų sąrašui: kanalai su raktais PIRMI (IRC susitarimas —
/// raktai priskiriami kanalams iš eilės), kanalų ir raktų sąrašai sulygiuoti,
/// kiekviena eilutė telpa į 512 baitų biudžetą (510 be CRLF).
pub fn join_lines(entries: &[(String, Option<String>)]) -> Vec<String> {
    const LIMIT: usize = MAX_LINE_BYTES - 2;
    let keyed = entries
        .iter()
        .filter_map(|(c, k)| k.as_deref().filter(|k| !k.is_empty()).map(|k| (c, Some(k))));
    let plain = entries
        .iter()
        .filter(|(_, k)| k.as_deref().is_none_or(str::is_empty))
        .map(|(c, _)| (c, None));
    let mut lines = Vec::new();
    let mut chans: Vec<&str> = Vec::new();
    let mut keys: Vec<&str> = Vec::new();
    let line_len = |chans: &[&str], keys: &[&str]| {
        let c: usize = chans.iter().map(|c| c.len() + 1).sum::<usize>();
        let k: usize = keys.iter().map(|k| k.len() + 1).sum::<usize>();
        "JOIN".len() + c + k
    };
    let flush = |chans: &mut Vec<&str>, keys: &mut Vec<&str>, lines: &mut Vec<String>| {
        if chans.is_empty() {
            return;
        }
        let mut l = format!("JOIN {}", chans.join(","));
        if !keys.is_empty() {
            l.push(' ');
            l.push_str(&keys.join(","));
        }
        lines.push(l);
        chans.clear();
        keys.clear();
    };
    for (chan, key) in keyed.chain(plain) {
        let mut try_chans = chans.clone();
        try_chans.push(chan);
        let mut try_keys = keys.clone();
        if let Some(k) = key {
            try_keys.push(k);
        }
        if !chans.is_empty() && line_len(&try_chans, &try_keys) > LIMIT {
            flush(&mut chans, &mut keys, &mut lines);
        }
        chans.push(chan);
        if let Some(k) = key {
            keys.push(k);
        }
    }
    flush(&mut chans, &mut keys, &mut lines);
    lines
}
