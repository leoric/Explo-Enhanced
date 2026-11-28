//! Plain-text run log shared by the GUI and the scheduled (headless) runs.

use chrono::Local;
use std::io::Write;
use std::path::PathBuf;

use crate::config::log_dir;
use crate::events::Level;

const MAX_BYTES: u64 = 2 * 1024 * 1024;

pub fn path() -> PathBuf {
    log_dir().join("explo.log")
}

/// Appends one line. Rotates to `explo.log.1` when the file passes 2 MB. Never fails loudly.
pub fn append(level: Level, msg: &str) {
    let p = path();
    let _ = std::fs::create_dir_all(log_dir());
    if std::fs::metadata(&p).map(|m| m.len() > MAX_BYTES).unwrap_or(false) {
        let _ = std::fs::rename(&p, p.with_extension("log.1"));
    }
    if let Ok(mut f) = std::fs::OpenOptions::new().create(true).append(true).open(&p) {
        let tag = match level {
            Level::Debug => "DEBUG",
            Level::Info => "INFO ",
            Level::Warn => "WARN ",
            Level::Error => "ERROR",
        };
        for line in msg.lines() {
            let _ = writeln!(f, "{} {tag} {line}", Local::now().format("%Y-%m-%d %H:%M:%S"));
        }
    }
}
