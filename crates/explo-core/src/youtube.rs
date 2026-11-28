//! Finding a track on YouTube Music and downloading it with yt-dlp + ffmpeg.

use anyhow::{anyhow, bail, Context, Result};
use regex::Regex;
use reqwest::blocking::Client;
use serde_json::{json, Value};
use std::ffi::OsString;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::time::Duration;

use crate::config::DownloadCfg;
use crate::events::Ctx;
use crate::http;
use crate::model::Track;
use crate::tools;
use crate::util::{file_stem, hits_filter};

// Same request shape ytmusicapi uses for an unauthenticated "songs" search.
const YTM_SEARCH_URL: &str = "https://music.youtube.com/youtubei/v1/search?alt=json&key=AIzaSyC9XL3ZjWddXya6X74dJoCTL-WEYFDNX30";
const YTM_SONGS_FILTER: &str = "EgWKAQIIAWoMEA4QChADEAQQCRAF";
const YTM_USER_AGENT: &str = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:88.0) Gecko/20100101 Firefox/88.0";

#[derive(Debug, Clone, PartialEq)]
pub struct Hit {
    pub video_id: String,
    pub title: String,
    /// Artist (YouTube Music) or channel (yt-dlp search).
    pub by: String,
}

pub struct Youtube {
    http: Client,
    cfg: DownloadCfg,
    ytdlp: PathBuf,
    ffmpeg: PathBuf,
    ffprobe: Option<PathBuf>,
    visitor_id: Mutex<Option<Option<String>>>,
}

impl Youtube {
    pub fn new(cfg: &DownloadCfg) -> Result<Self> {
        let ytdlp = tools::find("yt-dlp", &cfg.ytdlp_path).ok_or_else(|| anyhow!("yt-dlp not found. Install it with `brew install yt-dlp`, or set its path in Settings"))?;
        let ffmpeg = tools::find("ffmpeg", &cfg.ffmpeg_path).ok_or_else(|| anyhow!("ffmpeg not found. Install it with `brew install ffmpeg`, or set its path in Settings"))?;
        let ffprobe = tools::find_ffprobe(&ffmpeg);
        Ok(Self { http: http::client(20), cfg: cfg.clone(), ytdlp, ffmpeg, ffprobe, visitor_id: Mutex::new(None) })
    }

    /// Best YouTube video id for the track.
    pub fn find_video(&self, track: &Track, ctx: &Ctx) -> Result<String> {
        let query = format!("{} - {}", track.title, track.artist);
        match self.search_ytmusic(&query) {
            Ok(hits) => {
                if let Some(h) = self.pick(track, &hits, false) {
                    return Ok(h.video_id);
                }
                ctx.debug(format!("YouTube Music had no usable result for \"{query}\", trying a plain YouTube search"));
            }
            Err(e) => ctx.debug(format!("YouTube Music search failed ({e:#}), trying a plain YouTube search")),
        }
        let hits = self.search_ytdlp(&query, ctx)?;
        self.pick(track, &hits, true).map(|h| h.video_id).ok_or_else(|| anyhow!("no suitable YouTube result for {} - {}", track.title, track.artist))
    }

    fn pick(&self, track: &Track, hits: &[Hit], prefer_topic: bool) -> Option<Hit> {
        let ok = |h: &&Hit| !h.video_id.is_empty() && !hits_filter(&track.title, &track.artist, &h.title, &self.cfg.filter_words);
        if prefer_topic {
            // auto-generated "Artist - Topic" channels carry the studio audio
            if let Some(h) = hits.iter().filter(ok).find(|h| h.by.ends_with("- Topic") || h.by.eq_ignore_ascii_case(&track.main_artist)) {
                return Some(h.clone());
            }
        }
        hits.iter().find(ok).cloned()
    }

    fn visitor_id(&self) -> Option<String> {
        let mut slot = self.visitor_id.lock().ok()?;
        slot.get_or_insert_with(|| {
            let body = self.http.get("https://music.youtube.com").header("User-Agent", YTM_USER_AGENT).send().ok()?.text().ok()?;
            let re = Regex::new(r#""VISITOR_DATA"\s*:\s*"([^"]+)""#).ok()?;
            re.captures(&body).map(|c| c[1].to_string())
        })
        .clone()
    }

    fn search_ytmusic(&self, query: &str) -> Result<Vec<Hit>> {
        let version = format!("1.{}.01.00", chrono::Utc::now().format("%Y%m%d"));
        let body = json!({
            "context": {"client": {"clientName": "WEB_REMIX", "clientVersion": version, "hl": "en"}, "user": {}},
            "query": query,
            "params": YTM_SONGS_FILTER,
        });
        let visitor = self.visitor_id();
        let mut rb = self.http.post(YTM_SEARCH_URL).header("User-Agent", YTM_USER_AGENT).header("Origin", "https://music.youtube.com").json(&body);
        if let Some(v) = &visitor {
            rb = rb.header("X-Goog-Visitor-Id", v);
        }
        let resp = http::send_once(rb)?;
        let v: Value = resp.json().context("YouTube Music returned something that isn't JSON")?;
        Ok(parse_ytmusic_songs(&v))
    }

    fn search_ytdlp(&self, query: &str, ctx: &Ctx) -> Result<Vec<Hit>> {
        let mut c = tools::command(&self.ytdlp);
        c.args(["--flat-playlist", "--dump-json", "--no-warnings", "--no-config"]);
        self.add_cookies(&mut c);
        c.arg(format!("ytsearch5:{query}"));
        let out = tools::run_capture(c, Duration::from_secs(60), Some(&ctx.cancel_handle()))?;
        if !out.success {
            bail!("yt-dlp search failed: {}", tools::last_line(&out.stderr));
        }
        Ok(out
            .stdout
            .lines()
            .filter_map(|l| serde_json::from_str::<Value>(l).ok())
            .map(|v| Hit {
                video_id: v["id"].as_str().unwrap_or("").to_string(),
                title: v["title"].as_str().unwrap_or("").to_string(),
                by: v["channel"].as_str().or_else(|| v["uploader"].as_str()).unwrap_or("").to_string(),
            })
            .collect())
    }

    fn add_cookies(&self, c: &mut std::process::Command) {
        let p = self.cfg.cookies_path.trim();
        if !p.is_empty() && Path::new(p).is_file() {
            c.arg("--cookies").arg(p);
        }
    }

    /// Downloads `track.video_id` into `dir`, tags it, and records `track.file`.
    pub fn download(&self, track: &mut Track, dir: &Path, work_dir: &Path, ctx: &Ctx) -> Result<()> {
        if track.video_id.is_empty() {
            bail!("no video id for {}", track.title);
        }
        let ext = self.cfg.extension.trim().trim_start_matches('.').to_lowercase();
        let ext = if ext.is_empty() { "mp3".to_string() } else { ext };
        track.file = format!("{}.{ext}", file_stem(&track.title, &track.artist));
        std::fs::create_dir_all(dir).with_context(|| format!("creating {}", dir.display()))?;
        let final_path = dir.join(&track.file);
        if std::fs::metadata(&final_path).map(|m| m.len() > 0).unwrap_or(false) {
            ctx.debug(format!("{} already downloaded", track.file));
            return Ok(());
        }

        let tmp = work_dir.join(format!("{}-{}", track.video_id, std::process::id()));
        std::fs::create_dir_all(&tmp)?;
        let result = self.download_into(track, &tmp, &final_path, &ext, ctx);
        let _ = std::fs::remove_dir_all(&tmp);
        result
    }

    fn download_into(&self, track: &Track, tmp: &Path, final_path: &Path, ext: &str, ctx: &Ctx) -> Result<()> {
        // 1. fetch the audio stream, preferring the codec that fits the target extension as-is
        let format = match ext {
            "opus" | "ogg" => "bestaudio[acodec^=opus]/bestaudio",
            "m4a" => "bestaudio[acodec^=mp4a]/bestaudio",
            _ => "bestaudio",
        };
        let mut c = tools::command(&self.ytdlp);
        c.args(["-f", format, "--no-playlist", "--no-part", "--no-progress", "--quiet", "--no-warnings", "--no-config"]);
        c.arg("-o").arg(tmp.join("audio.%(ext)s"));
        self.add_cookies(&mut c);
        c.arg(format!("https://www.youtube.com/watch?v={}", track.video_id));
        let out = tools::run_capture(c, Duration::from_secs(600), Some(&ctx.cancel_handle()))?;
        if !out.success {
            bail!("yt-dlp failed for {}: {}", track.video_id, tools::last_line(&out.stderr));
        }
        let input = std::fs::read_dir(tmp)?
            .filter_map(|e| e.ok().map(|e| e.path()))
            .find(|p| p.file_name().is_some_and(|n| n.to_string_lossy().starts_with("audio.")))
            .ok_or_else(|| anyhow!("yt-dlp produced no file for {}", track.video_id))?;

        // 2. optional cover art
        let cover = self.fetch_cover(track, ext, tmp, ctx);

        // 3. tag + convert with ffmpeg (copy the stream when the codec already fits)
        let copy = self.can_copy(&input, ext);
        let partial = final_path.with_file_name(format!(".{}.partial.{ext}", file_stem(&track.title, &track.artist)));
        let args = ffmpeg_args(&input, cover.as_deref(), &partial, ext, copy, &metadata(track, ext));
        let mut f = tools::command(&self.ffmpeg);
        f.args(&args);
        let out = tools::run_capture(f, Duration::from_secs(300), Some(&ctx.cancel_handle()))?;
        if !out.success {
            let _ = std::fs::remove_file(&partial);
            bail!("ffmpeg failed for {}: {}", track.title, tools::last_line(&out.stderr));
        }
        std::fs::rename(&partial, final_path)?;
        Ok(())
    }

    fn can_copy(&self, input: &Path, ext: &str) -> bool {
        let want = match ext {
            "opus" | "ogg" => "opus",
            "m4a" => "aac",
            _ => return false,
        };
        let Some(probe) = &self.ffprobe else { return false };
        let mut c = tools::command(probe);
        c.args(["-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "default=nw=1:nk=1"]).arg(input);
        tools::run_capture(c, Duration::from_secs(20), None).map(|o| o.success && o.stdout.trim() == want).unwrap_or(false)
    }

    /// Cover art is embedded only for songs with an album; Ogg/Opus can't carry attached pictures through ffmpeg.
    fn fetch_cover(&self, track: &Track, ext: &str, tmp: &Path, ctx: &Ctx) -> Option<PathBuf> {
        if !self.cfg.embed_cover_art || track.cover_url.is_empty() || track.album.is_empty() || matches!(ext, "opus" | "ogg") {
            return None;
        }
        let path = tmp.join("cover.jpg");
        let res = http::send_with_retry(|| self.http.get(&track.cover_url)).and_then(|r| Ok(r.bytes()?)).and_then(|b| Ok(std::fs::write(&path, b)?));
        match res {
            Ok(()) => Some(path),
            Err(e) => {
                ctx.warn(format!("No cover art for {}: {e:#}", track.title));
                None
            }
        }
    }
}

/// Pulls (video id, title, artist) out of a YouTube Music search response. The nesting is deep and
/// changes now and then, so this walks the whole document looking for list-item renderers.
pub fn parse_ytmusic_songs(v: &Value) -> Vec<Hit> {
    fn walk<'a>(v: &'a Value, out: &mut Vec<&'a Value>) {
        match v {
            Value::Object(m) => {
                for (k, child) in m {
                    if k == "musicResponsiveListItemRenderer" {
                        out.push(child);
                    } else {
                        walk(child, out);
                    }
                }
            }
            Value::Array(a) => a.iter().for_each(|c| walk(c, out)),
            _ => {}
        }
    }
    let mut items = vec![];
    walk(v, &mut items);
    items
        .into_iter()
        .filter_map(|item| {
            let video_id = item
                .pointer("/playlistItemData/videoId")
                .or_else(|| item.pointer("/overlay/musicItemThumbnailOverlayRenderer/content/musicPlayButtonRenderer/playNavigationEndpoint/watchEndpoint/videoId"))
                .and_then(Value::as_str)?
                .to_string();
            let col = |i: usize| -> Vec<String> {
                item.pointer(&format!("/flexColumns/{i}/musicResponsiveListItemFlexColumnRenderer/text/runs"))
                    .and_then(Value::as_array)
                    .map(|runs| runs.iter().filter_map(|r| r["text"].as_str().map(String::from)).collect())
                    .unwrap_or_default()
            };
            let title = col(0).into_iter().next().unwrap_or_default();
            let mut sub = col(1);
            if sub.len() > 1 && matches!(sub[0].as_str(), "Song" | "Video") {
                sub.drain(..2.min(sub.len())); // "Song", " • "
            }
            Some(Hit { video_id, title, by: sub.into_iter().next().unwrap_or_default() })
        })
        .collect()
}

/// Tag names differ per container: ID3 for mp3, Vorbis comments for flac/opus/ogg.
pub fn metadata(t: &Track, ext: &str) -> Vec<String> {
    let (album_artist, mb_track, mb_album, mb_artist) = match ext {
        "mp3" => ("album_artist", "MusicBrainz Track Id", "MusicBrainz Album Id", "MusicBrainz Artist Id"),
        "flac" | "opus" | "ogg" => ("albumartist", "MusicBrainz_TrackId", "MusicBrainz_AlbumId", "MusicBrainz_ArtistId"),
        _ => ("album_artist", "", "", ""),
    };
    let mut m = vec![];
    let mut add = |k: &str, v: &str| {
        if !k.is_empty() && !v.is_empty() {
            m.push(format!("{k}={v}"));
        }
    };
    add("artist", &t.artist);
    add("title", &t.title);
    add("album", &t.album);
    add(album_artist, &t.album_artist);
    add(mb_track, &t.mb_track_id);
    add(mb_album, &t.mb_album_id);
    add(mb_artist, &t.mb_artist_id);
    m
}

pub fn ffmpeg_args(input: &Path, cover: Option<&Path>, out: &Path, ext: &str, copy_audio: bool, meta: &[String]) -> Vec<OsString> {
    let mut a: Vec<OsString> = ["-y", "-loglevel", "error", "-i"].iter().map(OsString::from).collect();
    a.push(input.into());
    if let Some(c) = cover {
        a.extend(["-i".into(), c.into()]);
    }
    a.extend(["-map".into(), "0:a".into()]);
    if cover.is_some() {
        for s in ["-map", "1:v", "-c:v", "mjpeg", "-disposition:v:0", "attached_pic", "-metadata:s:v", "title=Album cover", "-metadata:s:v", "comment=Cover (front)"] {
            a.push(s.into());
        }
    }
    if copy_audio {
        a.extend(["-c:a".into(), "copy".into()]);
    }
    if ext == "mp3" {
        a.extend(["-id3v2_version".into(), "3".into()]);
    }
    for m in meta {
        a.extend(["-metadata".into(), m.into()]);
    }
    a.push(out.into());
    a
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_ytmusic_response() {
        let v = json!({"contents":{"tabbedSearchResultsRenderer":{"tabs":[{"tabRenderer":{"content":{"sectionListRenderer":{"contents":[
          {"musicShelfRenderer":{"contents":[
            {"musicResponsiveListItemRenderer":{"playlistItemData":{"videoId":"vid1"},"flexColumns":[
              {"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Hukum"}]}}},
              {"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Song"},{"text":" • "},{"text":"Anirudh"},{"text":" • "},{"text":"Jailer"}]}}}]}},
            {"musicResponsiveListItemRenderer":{"flexColumns":[]}},
            {"musicResponsiveListItemRenderer":{"overlay":{"musicItemThumbnailOverlayRenderer":{"content":{"musicPlayButtonRenderer":{"playNavigationEndpoint":{"watchEndpoint":{"videoId":"vid2"}}}}}},
              "flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Other"}]}}},
                             {"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Someone"},{"text":" • "},{"text":"Album"}]}}}]}}
          ]}}]}}}}]}}});
        let hits = parse_ytmusic_songs(&v);
        assert_eq!(hits, vec![
            Hit { video_id: "vid1".into(), title: "Hukum".into(), by: "Anirudh".into() },
            Hit { video_id: "vid2".into(), title: "Other".into(), by: "Someone".into() },
        ]);
    }

    #[test]
    fn picks_first_unfiltered_hit() {
        let cfg = DownloadCfg::default();
        let yt = Youtube { http: http::client(1), cfg, ytdlp: "yt-dlp".into(), ffmpeg: "ffmpeg".into(), ffprobe: None, visitor_id: Mutex::new(None) };
        let track = Track { title: "Song".into(), artist: "Band".into(), main_artist: "Band".into(), ..Default::default() };
        let hits = vec![
            Hit { video_id: "a".into(), title: "Song (Live)".into(), by: "Band".into() },
            Hit { video_id: "b".into(), title: "Song".into(), by: "Some Channel".into() },
            Hit { video_id: "c".into(), title: "Song".into(), by: "Band - Topic".into() },
        ];
        assert_eq!(yt.pick(&track, &hits, false).unwrap().video_id, "b");
        assert_eq!(yt.pick(&track, &hits, true).unwrap().video_id, "c");
    }

    #[test]
    fn tags_per_container() {
        let t = Track { title: "T".into(), artist: "A".into(), album: "Al".into(), mb_track_id: "m1".into(), ..Default::default() };
        let mp3 = metadata(&t, "mp3");
        assert!(mp3.contains(&"MusicBrainz Track Id=m1".to_string()) && mp3.contains(&"title=T".to_string()));
        assert!(metadata(&t, "opus").contains(&"MusicBrainz_TrackId=m1".to_string()));
        assert!(!metadata(&t, "m4a").iter().any(|m| m.contains("MusicBrainz")));
    }

    #[test]
    fn builds_ffmpeg_command() {
        let args = ffmpeg_args(Path::new("in.webm"), Some(Path::new("c.jpg")), Path::new("out.mp3"), "mp3", false, &["title=T".into()]);
        let s: Vec<String> = args.iter().map(|a| a.to_string_lossy().into_owned()).collect();
        let joined = s.join(" ");
        assert!(joined.starts_with("-y -loglevel error -i in.webm -i c.jpg -map 0:a -map 1:v -c:v mjpeg"));
        assert!(joined.contains("-id3v2_version 3 -metadata title=T out.mp3"));
        assert!(!joined.contains("-c:a copy"));
        let s2: Vec<String> = ffmpeg_args(Path::new("in.webm"), None, Path::new("o.opus"), "opus", true, &[]).iter().map(|a| a.to_string_lossy().into_owned()).collect();
        assert_eq!(s2.join(" "), "-y -loglevel error -i in.webm -map 0:a -c:a copy o.opus");
    }
}
