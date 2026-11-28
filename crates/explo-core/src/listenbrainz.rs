//! ListenBrainz: finds the playlists ListenBrainz generated for the user and turns them into tracks.

use anyhow::{anyhow, bail, Result};
use chrono::{DateTime, Utc};
use reqwest::blocking::Client;
use serde::Deserialize;
use std::time::Duration;

use crate::config::{Config, ListenBrainzCfg};
use crate::events::Ctx;
use crate::http;
use crate::model::Track;

const API: &str = "https://api.listenbrainz.org/1";
const JSPF_PLAYLIST: &str = "https://musicbrainz.org/doc/jspf#playlist";
const JSPF_TRACK: &str = "https://musicbrainz.org/doc/jspf#track";

pub struct ListenBrainz {
    http: Client,
    cfg: ListenBrainzCfg,
    cover_size: u32,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct CreatedFor {
    count: usize,
    offset: usize,
    playlist_count: usize,
    playlists: Vec<PlaylistWrap>,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct PlaylistWrap {
    playlist: PlaylistHead,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct PlaylistHead {
    date: String,
    identifier: String,
    extension: std::collections::HashMap<String, PlaylistExt>,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct PlaylistExt {
    additional_metadata: AddMeta,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct AddMeta {
    algorithm_metadata: AlgoMeta,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct AlgoMeta {
    source_patch: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct PlaylistResp {
    playlist: PlaylistBody,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct PlaylistBody {
    title: String,
    track: Vec<JspfTrack>,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct JspfTrack {
    album: String,
    creator: String,
    duration: u32,
    title: String,
    identifier: Vec<String>,
    extension: std::collections::HashMap<String, TrackExt>,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct TrackExt {
    additional_metadata: TrackMeta,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct TrackMeta {
    artists: Vec<CreditedArtist>,
    caa_id: i64,
    caa_release_mbid: String,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct CreditedArtist {
    artist_credit_name: String,
    artist_mbid: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct TopRecordings {
    payload: TopPayload,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct TopPayload {
    recordings: Vec<TopRecording>,
}
#[derive(Deserialize, Default)]
#[serde(default)]
struct TopRecording {
    artist_name: String,
    recording_mbid: String,
    release_mbid: String,
    release_name: String,
    track_name: String,
}

impl ListenBrainz {
    pub fn new(cfg: &Config) -> Self {
        Self { http: http::client(30), cfg: cfg.listenbrainz.clone(), cover_size: normalize_cover_size(cfg.download.cover_size) }
    }

    fn headers(&self) -> Vec<(&str, String)> {
        if self.cfg.token.trim().is_empty() {
            vec![]
        } else {
            vec![("Authorization", format!("Token {}", self.cfg.token.trim()))]
        }
    }

    fn get<T: serde::de::DeserializeOwned>(&self, path: &str) -> Result<T> {
        // EXPLO_LISTENBRAINZ_API lets tests point at a local mock server
        let base = std::env::var("EXPLO_LISTENBRAINZ_API").unwrap_or_else(|_| API.to_string());
        let url = format!("{base}/{path}");
        let hs = self.headers();
        let refs: Vec<(&str, &str)> = hs.iter().map(|(k, v)| (*k, v.as_str())).collect();
        http::get_json(&self.http, &url, &refs)
    }

    /// Verifies the username exists (and that ListenBrainz is reachable).
    pub fn check_user(&self) -> Result<String> {
        let user = self.cfg.user.trim();
        if user.is_empty() {
            bail!("ListenBrainz username is empty");
        }
        let v: serde_json::Value = self.get(&format!("user/{user}/playlists/createdfor?count=1"))?;
        let n = v.get("playlist_count").and_then(|n| n.as_u64()).unwrap_or(0);
        Ok(format!("{n} generated playlists found for {user}"))
    }

    /// Fetches the tracks for one playlist kind, with blacklisted artists removed.
    pub fn tracks_for(&self, kind: &str, ctx: &Ctx) -> Result<(String, Vec<Track>)> {
        let (title, mut tracks) = if kind == "on-repeat" {
            ("On Repeat".to_string(), self.top_recordings()?)
        } else {
            let id = self.latest_playlist_id(kind, ctx)?;
            self.playlist(&id)?
        };
        let before = tracks.len();
        tracks.retain(|t| !self.blacklisted(t));
        if tracks.len() != before {
            ctx.info(format!("Skipped {} track(s) by blacklisted artists", before - tracks.len()));
        }
        Ok((title, tracks))
    }

    fn blacklisted(&self, t: &Track) -> bool {
        self.cfg.artist_blacklist.iter().map(|s| s.trim()).filter(|s| !s.is_empty()).any(|b| {
            b.eq_ignore_ascii_case(&t.main_artist) || (!t.mb_artist_id.is_empty() && b.eq_ignore_ascii_case(&t.mb_artist_id))
        })
    }

    /// ListenBrainz keeps older generated playlists around: pick the newest of this kind.
    fn latest_playlist_id(&self, kind: &str, ctx: &Ctx) -> Result<String> {
        let user = self.cfg.user.trim();
        let mut offset = 0usize;
        let mut best: Option<(DateTime<Utc>, String)> = None;
        loop {
            let page = self.get_createdfor_with_wait(user, offset, ctx)?;
            for p in &page.playlists {
                let head = &p.playlist;
                let patch = head.extension.get(JSPF_PLAYLIST).map(|e| e.additional_metadata.algorithm_metadata.source_patch.as_str()).unwrap_or("");
                if patch != kind {
                    continue;
                }
                let when = DateTime::parse_from_rfc3339(&head.date).map(|d| d.with_timezone(&Utc)).unwrap_or(DateTime::<Utc>::MIN_UTC);
                let id = head.identifier.rsplit('/').next().unwrap_or("").to_string();
                if id.is_empty() {
                    continue;
                }
                if best.as_ref().is_none_or(|(d, _)| when > *d) {
                    best = Some((when, id));
                }
            }
            if page.count + page.offset >= page.playlist_count || page.count == 0 {
                break;
            }
            offset += page.count;
        }
        best.map(|(_, id)| id).ok_or_else(|| anyhow!("ListenBrainz hasn't generated a '{kind}' playlist for {user} yet"))
    }

    fn get_createdfor_with_wait(&self, user: &str, offset: usize, ctx: &Ctx) -> Result<CreatedFor> {
        let mut last = None;
        for attempt in 1..=3 {
            ctx.check()?;
            match self.get::<CreatedFor>(&format!("user/{user}/playlists/createdfor?offset={offset}")) {
                Ok(p) => return Ok(p),
                Err(e) => {
                    ctx.warn(format!("ListenBrainz request failed (attempt {attempt}/3): {e:#}"));
                    last = Some(e);
                    if attempt < 3 {
                        std::thread::sleep(Duration::from_secs(20));
                    }
                }
            }
        }
        Err(last.unwrap())
    }

    fn playlist(&self, id: &str) -> Result<(String, Vec<Track>)> {
        let resp: PlaylistResp = self.get(&format!("playlist/{id}"))?;
        if resp.playlist.track.is_empty() {
            bail!("ListenBrainz playlist {id} has no tracks");
        }
        let tracks = resp.playlist.track.iter().map(|t| self.convert(t)).collect();
        Ok((resp.playlist.title, tracks))
    }

    fn convert(&self, t: &JspfTrack) -> Track {
        let meta = t.extension.get(JSPF_TRACK).map(|e| &e.additional_metadata);
        let artists: &[CreditedArtist] = meta.map(|m| m.artists.as_slice()).unwrap_or(&[]);
        let mut title = t.title.clone();
        let mut artist = t.creator.clone();
        let mut main_artist = t.creator.clone();
        if artists.len() > 1 {
            main_artist = artists[0].artist_credit_name.clone();
            if self.cfg.single_artist {
                let feats: Vec<&str> = artists[1..].iter().map(|a| a.artist_credit_name.as_str()).collect();
                title = build_title(&t.title, &feats);
                artist = main_artist.clone();
            }
        }
        let release = meta.map(|m| m.caa_release_mbid.clone()).unwrap_or_default();
        let cover_url = match meta {
            Some(m) if !m.caa_release_mbid.is_empty() && m.caa_id != 0 => {
                format!("https://coverartarchive.org/release/{}/{}-{}.jpg", m.caa_release_mbid, m.caa_id, self.cover_size)
            }
            _ => String::new(),
        };
        Track {
            title,
            clean_title: t.title.clone(),
            artist,
            main_artist,
            album: t.album.clone(),
            duration_ms: t.duration,
            cover_url,
            mb_track_id: t.identifier.first().map(|i| i.rsplit('/').next().unwrap_or("").to_string()).unwrap_or_default(),
            mb_album_id: release,
            mb_artist_id: artists.first().map(|a| a.artist_mbid.clone()).unwrap_or_default(),
            ..Default::default()
        }
    }

    fn top_recordings(&self) -> Result<Vec<Track>> {
        let user = self.cfg.user.trim();
        let resp: TopRecordings = self.get(&format!("stats/user/{user}/recordings?count=30&range=month"))?;
        if resp.payload.recordings.is_empty() {
            bail!("no top recordings found for {user}");
        }
        Ok(resp
            .payload
            .recordings
            .into_iter()
            .map(|r| Track {
                title: r.track_name.clone(),
                clean_title: r.track_name,
                artist: r.artist_name.clone(),
                main_artist: r.artist_name,
                album: r.release_name,
                cover_url: if r.release_mbid.is_empty() {
                    String::new()
                } else {
                    format!("https://coverartarchive.org/release/{}/front-{}", r.release_mbid, self.cover_size)
                },
                mb_track_id: r.recording_mbid,
                mb_album_id: r.release_mbid,
                ..Default::default()
            })
            .collect())
    }
}

/// Cover Art Archive only serves a few thumbnail sizes.
pub fn normalize_cover_size(size: u32) -> u32 {
    [250u32, 500, 1200].into_iter().min_by_key(|s| s.abs_diff(size)).unwrap_or(500)
}

/// "Title (feat. A, B & C)"
pub fn build_title(title: &str, feats: &[&str]) -> String {
    match feats {
        [] => title.to_string(),
        [one] => format!("{title} (feat. {one})"),
        many => format!("{title} (feat. {} & {})", many[..many.len() - 1].join(", "), many[many.len() - 1]),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn builds_feat_titles() {
        assert_eq!(build_title("Song", &[]), "Song");
        assert_eq!(build_title("Song", &["A"]), "Song (feat. A)");
        assert_eq!(build_title("Song", &["A", "B", "C"]), "Song (feat. A, B & C)");
    }

    #[test]
    fn snaps_cover_sizes() {
        assert_eq!(normalize_cover_size(0), 250);
        assert_eq!(normalize_cover_size(300), 250);
        assert_eq!(normalize_cover_size(400), 500);
        assert_eq!(normalize_cover_size(5000), 1200);
    }

    #[test]
    fn parses_a_generated_playlist() {
        let json = r#"{"playlist":{"title":"Weekly Exploration for bob","track":[
          {"album":"Alb","creator":"A feat. B","duration":200000,"title":"Song",
           "identifier":["https://musicbrainz.org/recording/11111111-1111-1111-1111-111111111111"],
           "extension":{"https://musicbrainz.org/doc/jspf#track":{"additional_metadata":{
             "artists":[{"artist_credit_name":"A","artist_mbid":"aaa","join_phrase":" feat. "},{"artist_credit_name":"B","artist_mbid":"bbb"}],
             "caa_id":1234567890123,"caa_release_mbid":"rel-1"}}}}]}}"#;
        let resp: PlaylistResp = serde_json::from_str(json).unwrap();
        let mut cfg = Config::default();
        cfg.listenbrainz.single_artist = true;
        let lb = ListenBrainz::new(&cfg);
        let t = lb.convert(&resp.playlist.track[0]);
        assert_eq!(t.title, "Song (feat. B)");
        assert_eq!(t.artist, "A");
        assert_eq!(t.main_artist, "A");
        assert_eq!(t.mb_track_id, "11111111-1111-1111-1111-111111111111");
        assert_eq!(t.mb_artist_id, "aaa");
        assert_eq!(t.cover_url, "https://coverartarchive.org/release/rel-1/1234567890123-500.jpg");
        assert_eq!(t.duration_ms, 200000);
    }

    #[test]
    fn picks_playlist_kind_from_createdfor() {
        let json = r#"{"count":2,"offset":0,"playlist_count":2,"playlists":[
          {"playlist":{"date":"2026-11-16T00:00:00+00:00","identifier":"https://listenbrainz.org/playlist/old",
           "extension":{"https://musicbrainz.org/doc/jspf#playlist":{"additional_metadata":{"algorithm_metadata":{"source_patch":"weekly-jams"}}}}}},
          {"playlist":{"date":"2026-11-23T00:00:00+00:00","identifier":"https://listenbrainz.org/playlist/new",
           "extension":{"https://musicbrainz.org/doc/jspf#playlist":{"additional_metadata":{"algorithm_metadata":{"source_patch":"weekly-jams"}}}}}}]}"#;
        let page: CreatedFor = serde_json::from_str(json).unwrap();
        assert_eq!(page.playlists.len(), 2);
        let patch = &page.playlists[1].playlist.extension.get(JSPF_PLAYLIST).unwrap().additional_metadata.algorithm_metadata.source_patch;
        assert_eq!(patch, "weekly-jams");
    }
}
