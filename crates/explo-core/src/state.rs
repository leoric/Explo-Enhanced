//! Persistent run state: history, catch-up bookkeeping, and a lock so two runs never overlap.

use anyhow::Result;
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::fs::File;
use std::path::PathBuf;

use crate::config::app_dir;

const MAX_HISTORY: usize = 20;

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct State {
    /// When the weekly schedule was installed. Slots before this are never "caught up".
    pub installed_at: Option<DateTime<Utc>>,
    /// Last time a run of all enabled playlists succeeded.
    pub last_success: Option<DateTime<Utc>>,
    /// The scheduled slot (RFC 3339) the `attempts` counter belongs to.
    pub slot: String,
    pub attempts: u32,
    /// Newest first.
    pub history: Vec<RunRecord>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RunRecord {
    pub started: DateTime<Utc>,
    pub finished: DateTime<Utc>,
    /// "scheduled" or "manual".
    pub trigger: String,
    pub ok: bool,
    pub playlists: Vec<PlaylistResult>,
    pub error: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct PlaylistResult {
    pub kind: String,
    pub name: String,
    pub total: usize,
    pub already_in_library: usize,
    pub downloaded: usize,
    pub download_failed: usize,
    pub deferred: usize,
    pub in_playlist: usize,
    pub error: Option<String>,
}

impl PlaylistResult {
    pub fn summary(&self) -> String {
        match &self.error {
            Some(e) => format!("{}: failed — {e}", self.kind),
            None => format!(
                "{}: {} in playlist ({} already in library, {} downloaded{}{})",
                self.name,
                self.in_playlist,
                self.already_in_library,
                self.downloaded,
                if self.download_failed > 0 { format!(", {} failed", self.download_failed) } else { String::new() },
                if self.deferred > 0 { format!(", {} deferred", self.deferred) } else { String::new() },
            ),
        }
    }
}

impl RunRecord {
    pub fn summary(&self) -> String {
        if let Some(e) = &self.error {
            return e.clone();
        }
        self.playlists.iter().map(PlaylistResult::summary).collect::<Vec<_>>().join("\n")
    }
}

fn path() -> PathBuf {
    app_dir().join("state.json")
}

impl State {
    pub fn load() -> Self {
        std::fs::read_to_string(path()).ok().and_then(|t| serde_json::from_str(&t).ok()).unwrap_or_default()
    }

    pub fn save(&self) -> Result<()> {
        let p = path();
        if let Some(d) = p.parent() {
            std::fs::create_dir_all(d)?;
        }
        let tmp = p.with_extension("json.tmp");
        std::fs::write(&tmp, serde_json::to_vec_pretty(self)?)?;
        std::fs::rename(tmp, p)?;
        Ok(())
    }

    pub fn push_history(&mut self, r: RunRecord) {
        self.history.insert(0, r);
        self.history.truncate(MAX_HISTORY);
    }
}

/// Held for the duration of a run. The OS releases it if the process dies.
pub struct RunLock {
    _file: File,
}

impl RunLock {
    /// `Ok(None)` when another run (GUI or scheduled) is already in progress.
    pub fn try_acquire() -> Result<Option<RunLock>> {
        std::fs::create_dir_all(app_dir())?;
        let file = std::fs::OpenOptions::new().create(true).truncate(false).write(true).open(app_dir().join("run.lock"))?;
        match file.try_lock() {
            Ok(()) => Ok(Some(RunLock { _file: file })),
            Err(std::fs::TryLockError::WouldBlock) => Ok(None),
            Err(std::fs::TryLockError::Error(e)) => Err(e.into()),
        }
    }
}
