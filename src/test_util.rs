//! Bendra testų įranga (tik `cfg(test)`): deterministinis atsitiktinių
//! skaičių generatorius „fuzz“ ciklams ir izoliuoti laikini katalogai.
//!
//! Sąmoningai be išorinių priklausomybių (proptest ir pan.) — MSRV 1.85 ir
//! `--locked` lieka paprasti. Sėkla fiksuota, todėl kiekvienas krentantis
//! atvejis atkuriamas; klaidos pranešime visada spausdinam įvestį.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

/// xorshift64* — pakankamai geras testų įvesčių generavimui.
pub struct Rng(u64);

impl Rng {
    pub fn new(seed: u64) -> Self {
        Rng(seed.max(1))
    }

    pub fn next_u64(&mut self) -> u64 {
        let mut x = self.0;
        x ^= x >> 12;
        x ^= x << 25;
        x ^= x >> 27;
        self.0 = x;
        x.wrapping_mul(0x2545_F491_4F6C_DD1D)
    }

    /// Tolygiai `0..n` (n > 0).
    pub fn below(&mut self, n: usize) -> usize {
        (self.next_u64() % n as u64) as usize
    }

    pub fn pick<'a, T>(&mut self, items: &'a [T]) -> &'a T {
        &items[self.below(items.len())]
    }

    /// Atsitiktiniai baitai (bet kokie, ne tik UTF-8).
    pub fn bytes(&mut self, max_len: usize) -> Vec<u8> {
        let n = self.below(max_len + 1);
        (0..n).map(|_| self.next_u64() as u8).collect()
    }

    /// Bet koks Unicode simbolis (be surogatų).
    pub fn any_char(&mut self) -> char {
        loop {
            let cp = match self.below(4) {
                0 => self.below(0x80) as u32,
                1 => self.below(0x800) as u32,
                2 => self.below(0x1_0000) as u32,
                _ => self.below(0x11_0000) as u32,
            };
            if let Some(c) = char::from_u32(cp) {
                return c;
            }
        }
    }

    /// Simbolis iš „įdomių“ klasių: valdymo (C0/C1/DEL/ESC), CJK, emoji,
    /// ZWJ, derinamieji ženklai, regioniniai rodikliai, odos tonai, tarpai.
    pub fn interesting_char(&mut self) -> char {
        const POOL: &[char] = &[
            'a',
            'Z',
            '0',
            ' ',
            ' ',
            '\t',
            '\0',
            '\x01',
            '\x07',
            '\x08',
            '\n',
            '\r',
            '\x1b',
            '\x7f',
            '\u{80}',
            '\u{85}',
            '\u{9b}',
            '\u{9f}',
            '\u{a0}',
            '\u{ad}',
            'ą',
            'ž',
            'é',
            '\u{301}',
            '\u{20e3}',
            '\u{200b}',
            '\u{200d}',
            '\u{202e}',
            '\u{feff}',
            '\u{fe0f}',
            '中',
            '文',
            '한',
            'ｱ',
            'Ａ',
            '\u{3000}',
            '😀',
            '👍',
            '👨',
            '👩',
            '👧',
            '\u{1f3fd}',
            '\u{1f1f1}',
            '\u{1f1f9}',
            '\u{1f1fa}',
            '…',
            '[',
            ']',
            ':',
            ';',
            '@',
            '!',
            '#',
            '/',
            '\\',
            '.',
            '*',
            '?',
            '=',
            '"',
            '\x02',
            '\x03',
            '\x0f',
            '\x16',
            '\x1d',
            '\x1f',
        ];
        if self.below(8) == 0 {
            self.any_char()
        } else {
            *self.pick(POOL)
        }
    }

    pub fn interesting_string(&mut self, max_len: usize) -> String {
        let n = self.below(max_len + 1);
        (0..n).map(|_| self.interesting_char()).collect()
    }
}

/// Testai, kurie rašo į BENDRUS `config_dir()` failus (config, history,
/// ignore, notify, keys), vykdomi nuosekliai per šį užraktą.
pub fn config_dir_lock() -> std::sync::MutexGuard<'static, ()> {
    static LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());
    LOCK.lock().unwrap_or_else(|e| e.into_inner())
}

static SEQ: AtomicU64 = AtomicU64::new(0);

/// Laikinas katalogas, ištrinamas `Drop` metu. Visada po `test_home()`,
/// todėl testai nerašo į tikrą `$HOME`.
pub struct TempDir(PathBuf);

impl TempDir {
    pub fn new(tag: &str) -> Self {
        let d = crate::config::test_home()
            .join(format!("tmp-{tag}-{}", SEQ.fetch_add(1, Ordering::Relaxed)));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).expect("sukurti laikiną katalogą");
        TempDir(d)
    }

    pub fn path(&self) -> &Path {
        &self.0
    }

    pub fn join(&self, p: &str) -> PathBuf {
        self.0.join(p)
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}
