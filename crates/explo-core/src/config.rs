//! User configuration. Stored as JSON in the platform config directory and edited from the GUI.

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::path::PathBuf;

/// Playlist types ListenBrainz generates for a user: (id, label).
pub const PLAYLIST_KINDS: [(&str, &str); 4] = [
    ("weekly-exploration", "Weekly Exploration"),
    ("weekly-jams", "Weekly Jams"),
    ("daily-jams", "Daily Jams"),
    ("on-repeat", "On Repeat (your top tracks this month)"),
];

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(default)]
pub struct Config {
    pub listenbrainz: ListenBrainzCfg,
    pub server: ServerCfg,
    pub download: DownloadCfg,
    pub playlists: PlaylistsCfg,
    pub schedule: ScheduleCfg,
    /// Show a desktop notification when a scheduled run finishes.
    pub notify: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct ListenBrainzCfg {
    pub user: String,
    pub token: String,
    /// Merge featured artists into the title ("Song (feat. X)") instead of the artist field.
    pub single_artist: bool,
    /// Artist names or MusicBrainz artist IDs to leave out.
    pub artist_blacklist: Vec<String>,
}

/// A Subsonic-compatible server (Navidrome, Airsonic, Gonic, ...).
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct ServerCfg {
    pub url: String,
    pub user: String,
    pub password: String,
    /// Optional: some servers only let an admin trigger a library scan.
    pub admin_user: String,
    pub admin_password: String,
    pub public_playlist: bool,
    /// Minimum score (0-100+) for a library song to count as a match for a track.
    pub match_score: i32,
    /// How long to wait for the server's library scan to finish.
    pub scan_timeout_minutes: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct DownloadCfg {
    /// Folder inside (or shared with) your server's music library.
    pub dir: String,
    /// Put each playlist's tracks in its own sub-folder.
    pub use_subdirectory: bool,
    /// mp3, m4a, opus, ogg or flac.
    pub extension: String,
    /// Skip YouTube results whose title contains one of these words (unless the track itself does).
    pub filter_words: Vec<String>,
    /// 0 = no limit. Tracks over the limit wait for the next run.
    pub max_downloads_per_run: u32,
    pub embed_cover_art: bool,
    pub cover_size: u32,
    /// Empty = look in PATH and the usual Homebrew locations.
    pub ytdlp_path: String,
    pub ffmpeg_path: String,
    /// Optional Netscape cookies file for yt-dlp.
    pub cookies_path: String,
    /// Simultaneous downloads.
    pub parallel: usize,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "lowercase")]
pub enum NameFormat {
    #[default]
    Week,
    Date,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct PlaylistsCfg {
    /// Playlist ids from [`PLAYLIST_KINDS`] that scheduled runs (and "Run all") process.
    pub enabled: Vec<String>,
    /// Replace the existing playlist of the same name instead of keeping one per week.
    pub replace: bool,
    pub name_format: NameFormat,
    /// Optional display name per playlist id, e.g. "weekly-exploration" -> "Discover Weekly".
    pub custom_names: BTreeMap<String, String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct ScheduleCfg {
    /// 1 = Monday ... 7 = Sunday.
    pub weekday: u8,
    pub hour: u8,
    pub minute: u8,
}

impl Default for ListenBrainzCfg {
    fn default() -> Self {
        Self { user: String::new(), token: String::new(), single_artist: true, artist_blacklist: vec![] }
    }
}
impl Default for ServerCfg {
    fn default() -> Self {
        Self {
            url: "http://127.0.0.1:4533".into(),
            user: String::new(),
            password: String::new(),
            admin_user: String::new(),
            admin_password: String::new(),
            public_playlist: false,
            match_score: 60,
            scan_timeout_minutes: 15,
        }
    }
}
impl Default for DownloadCfg {
    fn default() -> Self {
        let dir = dirs::audio_dir().or_else(|| dirs::home_dir().map(|h| h.join("Music"))).unwrap_or_default().join("Explo");
        Self {
            dir: dir.to_string_lossy().into_owned(),
            use_subdirectory: true,
            extension: "mp3".into(),
            filter_words: ["live", "remix", "instrumental", "extended", "clean", "acapella"].iter().map(|s| s.to_string()).collect(),
            max_downloads_per_run: 0,
            embed_cover_art: false,
            cover_size: 500,
            ytdlp_path: String::new(),
            ffmpeg_path: String::new(),
            cookies_path: String::new(),
            parallel: 3,
        }
    }
}
impl Default for PlaylistsCfg {
    fn default() -> Self {
        Self { enabled: vec!["weekly-exploration".into()], replace: true, name_format: NameFormat::Week, custom_names: BTreeMap::new() }
    }
}
impl Default for ScheduleCfg {
    fn default() -> Self {
        Self { weekday: 1, hour: 9, minute: 0 }
    }
}

/// Set `EXPLO_HOME` to keep config, state, logs and caches in one folder of your choosing
/// (used by the tests, handy for a portable setup).
const HOME_ENV: &str = "EXPLO_HOME";

fn home_override() -> Option<PathBuf> {
    std::env::var_os(HOME_ENV).filter(|v| !v.is_empty()).map(PathBuf::from)
}

/// State, logs and caches: `~/Library/Application Support/Explo` on macOS.
pub fn app_dir() -> PathBuf {
    home_override().unwrap_or_else(|| dirs::data_dir().unwrap_or_else(|| PathBuf::from(".")).join("Explo"))
}
/// `config.json`: next to the other files on macOS, in the XDG config dir on Linux.
pub fn config_path() -> PathBuf {
    match home_override() {
        Some(h) => h.join("config.json"),
        None => dirs::config_dir().unwrap_or_else(|| PathBuf::from(".")).join("Explo").join("config.json"),
    }
}
pub fn log_dir() -> PathBuf {
    app_dir().join("logs")
}
pub fn cache_dir() -> PathBuf {
    app_dir().join("cache")
}

impl Config {
    pub fn load() -> Result<Self> {
        let path = config_path();
        match std::fs::read_to_string(&path) {
            Ok(text) => serde_json::from_str(&text).with_context(|| format!("parsing {}", path.display())),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Self::default()),
            Err(e) => Err(e).with_context(|| format!("reading {}", path.display())),
        }
    }

    /// Writes the config readable only by the current user (it contains passwords and tokens).
    pub fn save(&self) -> Result<()> {
        let path = config_path();
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir)?;
        }
        let text = serde_json::to_string_pretty(self)?;
        let tmp = path.with_extension("json.tmp");
        write_private(&tmp, text.as_bytes())?;
        std::fs::rename(&tmp, &path)?;
        Ok(())
    }

    /// Human-readable problems that would make a run fail. Empty = good to go.
    pub fn problems(&self) -> Vec<String> {
        let mut p = vec![];
        if self.listenbrainz.user.trim().is_empty() {
            p.push("ListenBrainz username is not set".to_string());
        }
        if self.server.url.trim().is_empty() {
            p.push("Server URL is not set".to_string());
        }
        if self.server.user.trim().is_empty() || self.server.password.is_empty() {
            p.push("Server username and password are required".to_string());
        }
        if self.download.dir.trim().is_empty() {
            p.push("Download folder is not set".to_string());
        }
        if self.playlists.enabled.is_empty() {
            p.push("No playlists are enabled".to_string());
        }
        p
    }

    pub fn playlist_enabled(&self, id: &str) -> bool {
        self.playlists.enabled.iter().any(|e| e == id)
    }
    pub fn set_playlist_enabled(&mut self, id: &str, on: bool) {
        self.playlists.enabled.retain(|e| e != id);
        if on {
            self.playlists.enabled.push(id.to_string());
        }
        // keep a stable order
        let order: Vec<&str> = PLAYLIST_KINDS.iter().map(|(k, _)| *k).collect();
        self.playlists.enabled.sort_by_key(|e| order.iter().position(|k| k == e).unwrap_or(usize::MAX));
    }
}

#[cfg(unix)]
fn write_private(path: &std::path::Path, data: &[u8]) -> std::io::Result<()> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = std::fs::OpenOptions::new().write(true).create(true).truncate(true).mode(0o600).open(path)?;
    f.write_all(data)
}
#[cfg(not(unix))]
fn write_private(path: &std::path::Path, data: &[u8]) -> std::io::Result<()> {
    std::fs::write(path, data)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn partial_json_fills_defaults() {
        let c: Config = serde_json::from_str(r#"{"listenbrainz":{"user":"bob"}}"#).unwrap();
        assert_eq!(c.listenbrainz.user, "bob");
        assert!(c.listenbrainz.single_artist);
        assert_eq!(c.server.match_score, 60);
        assert_eq!(c.playlists.enabled, vec!["weekly-exploration"]);
    }

    #[test]
    fn enabled_playlists_keep_stable_order() {
        let mut c = Config::default();
        c.set_playlist_enabled("on-repeat", true);
        c.set_playlist_enabled("weekly-jams", true);
        assert_eq!(c.playlists.enabled, vec!["weekly-exploration", "weekly-jams", "on-repeat"]);
        c.set_playlist_enabled("weekly-exploration", false);
        assert_eq!(c.playlists.enabled, vec!["weekly-jams", "on-repeat"]);
    }
}
