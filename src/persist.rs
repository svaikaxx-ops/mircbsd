//! Atomic line-oriented persistence (history, ignore, monitor).

use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, Write};
use std::path::{Path, PathBuf};

use crate::validate::is_valid_nick;

const HISTORY_MAX: usize = 1000;
const MONITOR_MAX: usize = 100;

fn tmp_path(path: &Path) -> PathBuf {
    let mut s = path.as_os_str().to_os_string();
    s.push(".tmp");
    PathBuf::from(s)
}

/// Create parent directories with mode 0700 on Unix.
pub fn ensure_parent_dir(path: &Path) -> io::Result<()> {
    if let Some(parent) = path.parent() {
        if parent.as_os_str().is_empty() {
            return Ok(());
        }
        #[cfg(unix)]
        {
            use std::fs::DirBuilder;
            use std::os::unix::fs::DirBuilderExt;
            let mut builder = DirBuilder::new();
            builder.mode(0o700);
            builder.recursive(true).create(parent)?;
        }
        #[cfg(not(unix))]
        {
            fs::create_dir_all(parent)?;
        }
    }
    Ok(())
}

/// Write bytes atomically via `path.tmp` then rename; file mode 0600 on Unix.
pub fn atomic_write(path: &Path, data: &[u8]) -> io::Result<()> {
    ensure_parent_dir(path)?;
    let tmp = tmp_path(path);
    let mut file = open_new_private(&tmp)?;
    file.write_all(data)?;
    file.sync_all()?;
    file.flush()?;
    drop(file);
    fs::rename(&tmp, path)?;
    Ok(())
}

#[cfg(unix)]
fn open_new_private(path: &Path) -> io::Result<File> {
    use std::os::unix::fs::OpenOptionsExt;
    OpenOptions::new()
        .create(true)
        .write(true)
        .truncate(true)
        .mode(0o600)
        .open(path)
}

#[cfg(not(unix))]
fn open_new_private(path: &Path) -> io::Result<File> {
    OpenOptions::new()
        .create(true)
        .write(true)
        .truncate(true)
        .open(path)
}

/// Save lines one per row (trailing newline after each).
pub fn save_lines(path: &Path, lines: &[String]) -> io::Result<()> {
    let mut out = String::new();
    for line in lines {
        out.push_str(line);
        out.push('\n');
    }
    atomic_write(path, out.as_bytes())
}

/// Load non-empty lines (no trimming of content; empty lines skipped).
pub fn load_lines(path: &Path) -> io::Result<Vec<String>> {
    let file = match File::open(path) {
        Ok(f) => f,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(e),
    };
    let reader = BufReader::new(file);
    let mut lines = Vec::new();
    for line in reader.lines() {
        let line = line?;
        if !line.is_empty() {
            lines.push(line);
        }
    }
    Ok(lines)
}

/// Last `HISTORY_MAX` lines to disk.
pub fn save_history(path: &Path, lines: &[String]) -> io::Result<()> {
    let start = lines.len().saturating_sub(HISTORY_MAX);
    save_lines(path, &lines[start..])
}

pub fn load_history(path: &Path) -> io::Result<Vec<String>> {
    load_lines(path)
}

pub fn save_ignore(path: &Path, list: &[String]) -> io::Result<()> {
    save_lines(path, list)
}

/// Trimmed non-empty lines.
pub fn load_ignore(path: &Path) -> io::Result<Vec<String>> {
    let file = match File::open(path) {
        Ok(f) => f,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(e),
    };
    let reader = BufReader::new(file);
    let mut list = Vec::new();
    for line in reader.lines() {
        let n = line?.trim().to_string();
        if !n.is_empty() {
            list.push(n);
        }
    }
    Ok(list)
}

/// At most `MONITOR_MAX` entries.
pub fn save_monitor(path: &Path, list: &[String]) -> io::Result<()> {
    let end = list.len().min(MONITOR_MAX);
    save_lines(path, &list[..end])
}

/// Valid nicks only (`is_valid_nick`).
pub fn load_monitor(path: &Path) -> io::Result<Vec<String>> {
    let file = match File::open(path) {
        Ok(f) => f,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(e),
    };
    let reader = BufReader::new(file);
    let mut list = Vec::new();
    for line in reader.lines() {
        let n = line?.trim().to_string();
        if !n.is_empty() && is_valid_nick(&n) {
            list.push(n);
        }
    }
    Ok(list)
}
