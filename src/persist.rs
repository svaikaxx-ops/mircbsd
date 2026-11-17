//! Atomic line-oriented persistence (history, ignore, monitor).

use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

use crate::validate::is_valid_nick;

const HISTORY_MAX: usize = 1000;
const MONITOR_MAX: usize = 100;

static TEMP_SEQUENCE: AtomicU64 = AtomicU64::new(0);

fn tmp_path(path: &Path) -> PathBuf {
    let mut s = path.as_os_str().to_os_string();
    let sequence = TEMP_SEQUENCE.fetch_add(1, Ordering::Relaxed);
    s.push(format!(".tmp.{}.{}", std::process::id(), sequence));
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

/// Write bytes to a new private temporary file, then atomically rename it.
pub fn atomic_write(path: &Path, data: &[u8]) -> io::Result<()> {
    ensure_parent_dir(path)?;
    let (tmp, mut file) = loop {
        let tmp = tmp_path(path);
        match open_new_private(&tmp) {
            Ok(file) => break (tmp, file),
            Err(err) if err.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(err) => return Err(err),
        }
    };
    let result = (|| {
        file.write_all(data)?;
        file.sync_all()?;
        drop(file);
        fs::rename(&tmp, path)
    })();
    if result.is_err() {
        let _ = fs::remove_file(&tmp);
    }
    result
}

#[cfg(unix)]
fn open_new_private(path: &Path) -> io::Result<File> {
    use std::os::unix::fs::OpenOptionsExt;
    OpenOptions::new()
        .create_new(true)
        .write(true)
        .mode(0o600)
        .open(path)
}

#[cfg(not(unix))]
fn open_new_private(path: &Path) -> io::Result<File> {
    OpenOptions::new().create_new(true).write(true).open(path)
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

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(unix)]
    #[test]
    fn atomic_write_does_not_follow_predictable_tmp_symlink() {
        use std::os::unix::fs::{symlink, PermissionsExt};

        let dir = std::env::temp_dir().join(format!(
            "mircbsd-persist-{}-{}",
            std::process::id(),
            TEMP_SEQUENCE.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&dir).expect("create isolated test directory");
        let target = dir.join("target");
        let path = dir.join("config");
        fs::write(&target, b"unchanged").unwrap();
        symlink(&target, dir.join("config.tmp")).unwrap();

        atomic_write(&path, b"private").unwrap();

        assert_eq!(fs::read(&target).unwrap(), b"unchanged");
        assert_eq!(fs::read(&path).unwrap(), b"private");
        assert_eq!(
            fs::metadata(&path).unwrap().permissions().mode() & 0o777,
            0o600
        );
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn concurrent_writes_leave_one_complete_value() {
        let dir = std::env::temp_dir().join(format!(
            "mircbsd-persist-race-{}-{}",
            std::process::id(),
            TEMP_SEQUENCE.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&dir).unwrap();
        let path = dir.join("history");
        let threads: Vec<_> = (0..8)
            .map(|n| {
                let path = path.clone();
                std::thread::spawn(move || atomic_write(&path, &vec![b'a' + n; 4096]).unwrap())
            })
            .collect();
        for thread in threads {
            thread.join().unwrap();
        }
        let contents = fs::read(path).unwrap();
        assert_eq!(contents.len(), 4096);
        assert!(contents.iter().all(|byte| *byte == contents[0]));
        fs::remove_dir_all(dir).unwrap();
    }
}

#[cfg(test)]
#[path = "persist_tests.rs"]
mod more_tests;
