//! Subsonic-API client (Navidrome and friends): search, library scan, playlists.

use anyhow::{anyhow, bail, Context, Result};
use md5::{Digest, Md5};
use rand::Rng;
use reqwest::blocking::Client;
use serde_json::Value;
use std::time::{Duration, Instant};

use crate::config::ServerCfg;
use crate::events::Ctx;
use crate::http;
use crate::model::SearchResult;

const API_VERSION: &str = "1.16.1";
const CLIENT_NAME: &str = "explo";

pub struct Subsonic {
    http: Client,
    base: String,
    user: String,
    password: String,
}

impl Subsonic {
    pub fn new(cfg: &ServerCfg) -> Self {
        Self::with_credentials(cfg, &cfg.user, &cfg.password)
    }

    /// A client for the admin account, if one is configured (used for library scans).
    pub fn admin(cfg: &ServerCfg) -> Option<Self> {
        (!cfg.admin_user.trim().is_empty() && !cfg.admin_password.is_empty()).then(|| Self::with_credentials(cfg, cfg.admin_user.trim(), &cfg.admin_password))
    }

    fn with_credentials(cfg: &ServerCfg, user: &str, password: &str) -> Self {
        Self { http: http::client(30), base: cfg.url.trim().trim_end_matches('/').to_string(), user: user.to_string(), password: password.to_string() }
    }

    /// Calls `/rest/<endpoint>` and returns the inner `subsonic-response` object.
    fn call(&self, endpoint: &str, params: &[(&str, String)]) -> Result<Value> {
        let salt: String = (0..12).map(|_| char::from(b"abcdefghijklmnopqrstuvwxyz0123456789"[rand::thread_rng().gen_range(0..36)])).collect();
        let token = hex(&Md5::digest(format!("{}{}", self.password, salt)));
        let mut query: Vec<(&str, String)> = vec![
            ("u", self.user.clone()),
            ("t", token),
            ("s", salt),
            ("v", API_VERSION.into()),
            ("c", CLIENT_NAME.into()),
            ("f", "json".into()),
        ];
        query.extend(params.iter().map(|(k, v)| (*k, v.clone())));
        let url = format!("{}/rest/{endpoint}", self.base);
        let resp = http::send_with_retry(|| self.http.get(&url).query(&query)).with_context(|| format!("calling {endpoint}"))?;
        let body: Value = resp.json().with_context(|| format!("{endpoint}: server did not return JSON"))?;
        let inner = body.get("subsonic-response").ok_or_else(|| anyhow!("{endpoint}: not a Subsonic response (is the URL right?)"))?;
        if inner.get("status").and_then(Value::as_str) != Some("ok") {
            let msg = inner.pointer("/error/message").and_then(Value::as_str).unwrap_or("unknown error");
            bail!("{endpoint}: {msg}");
        }
        Ok(inner.clone())
    }

    /// Verifies URL and credentials. Returns a description like "navidrome 0.54 (API 1.16.1)".
    pub fn ping(&self) -> Result<String> {
        let r = self.call("ping", &[])?;
        let s = |k: &str| r.get(k).and_then(Value::as_str).unwrap_or("").to_string();
        let kind = if s("type").is_empty() { "Subsonic server".to_string() } else { s("type") };
        Ok(format!("{kind} {} (API {})", s("serverVersion"), s("version")).replace("  ", " "))
    }

    pub fn search_songs(&self, query: &str, count: u32) -> Result<Vec<SearchResult>> {
        let r = self.call(
            "search3",
            &[("query", query.to_string()), ("songCount", count.max(1).to_string()), ("artistCount", "0".into()), ("albumCount", "0".into())],
        )?;
        let songs = r.pointer("/searchResult3/song").and_then(Value::as_array).cloned().unwrap_or_default();
        let text = |s: &Value, k: &str| s.get(k).and_then(Value::as_str).unwrap_or("").to_string();
        Ok(songs
            .iter()
            .map(|s| SearchResult {
                id: text(s, "id"),
                title: text(s, "title"),
                album: text(s, "album"),
                artist: text(s, "artist"),
                path: text(s, "path"),
                duration: s.get("duration").and_then(Value::as_u64).unwrap_or(0) as u32,
                mbid: text(s, "musicBrainzId"),
            })
            .collect())
    }

    pub fn start_scan(&self) -> Result<()> {
        self.call("startScan", &[]).map(|_| ())
    }

    /// Polls until the server reports it is no longer scanning. Returns false on timeout.
    pub fn wait_for_scan(&self, timeout: Duration, ctx: &Ctx) -> bool {
        std::thread::sleep(Duration::from_millis(800)); // let the scan register
        let start = Instant::now();
        loop {
            match self.call("getScanStatus", &[]) {
                Ok(r) => {
                    if !r.pointer("/scanStatus/scanning").and_then(Value::as_bool).unwrap_or(false) {
                        return true;
                    }
                }
                Err(e) => {
                    ctx.warn(format!("Couldn't read scan status: {e:#}"));
                    return false;
                }
            }
            if start.elapsed() > timeout || ctx.cancelled() {
                return false;
            }
            std::thread::sleep(Duration::from_secs(3));
        }
    }

    /// Id of the user's own playlist with this name (other users' public playlists are ignored).
    pub fn find_playlist(&self, name: &str) -> Result<Option<String>> {
        let r = self.call("getPlaylists", &[])?;
        let lists = r.pointer("/playlists/playlist").and_then(Value::as_array).cloned().unwrap_or_default();
        Ok(lists
            .iter()
            .find(|p| {
                p.get("name").and_then(Value::as_str) == Some(name)
                    && p.get("owner").and_then(Value::as_str).is_some_and(|o| o.eq_ignore_ascii_case(&self.user))
            })
            .and_then(|p| p.get("id").and_then(Value::as_str).map(String::from)))
    }

    /// Creates the playlist, or replaces the songs of `existing` in place (keeps its id). Returns the id.
    pub fn save_playlist(&self, existing: Option<&str>, name: &str, song_ids: &[String]) -> Result<String> {
        let target = |existing: Option<&str>| match existing {
            Some(id) => ("playlistId", id.to_string()),
            None => ("name", name.to_string()),
        };
        let attempt = |existing: Option<&str>| -> Result<String> {
            let (k, v) = target(existing);
            let mut params = vec![(k, v)];
            params.extend(song_ids.iter().map(|id| ("songId", id.clone())));
            let r = self.call("createPlaylist", &params)?;
            Ok(r.pointer("/playlist/id").and_then(Value::as_str).or(existing).unwrap_or("").to_string())
        };
        match existing {
            Some(id) => attempt(Some(id)).or_else(|_| attempt(None)),
            None => attempt(None),
        }
    }

    pub fn update_playlist(&self, id: &str, comment: &str, public: bool) -> Result<()> {
        self.call("updatePlaylist", &[("playlistId", id.to_string()), ("comment", comment.to_string()), ("public", public.to_string())]).map(|_| ())
    }
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

#[cfg(test)]
pub(crate) mod mock {
    //! A tiny in-process HTTP server that records requests and answers with canned Subsonic JSON.
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::sync::{Arc, Mutex};

    pub struct Mock {
        pub url: String,
        pub requests: Arc<Mutex<Vec<String>>>,
    }

    pub fn serve(handler: impl Fn(&str, &str) -> String + Send + 'static) -> Mock {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let requests = Arc::new(Mutex::new(Vec::new()));
        let log = requests.clone();
        std::thread::spawn(move || {
            for stream in listener.incoming() {
                let Ok(mut stream) = stream else { continue };
                let mut buf = [0u8; 8192];
                let n = stream.read(&mut buf).unwrap_or(0);
                let head = String::from_utf8_lossy(&buf[..n]).to_string();
                let target = head.lines().next().unwrap_or("").split_whitespace().nth(1).unwrap_or("").to_string();
                log.lock().unwrap().push(target.clone());
                let (path, query) = target.split_once('?').unwrap_or((&target, ""));
                let body = handler(path, query);
                let resp = format!("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}", body.len(), body);
                let _ = stream.write_all(resp.as_bytes());
            }
        });
        Mock { url, requests }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn cfg(url: &str) -> ServerCfg {
        ServerCfg { url: url.into(), user: "bob".into(), password: "sesame".into(), ..Default::default() }
    }

    #[test]
    fn md5_token_matches_the_subsonic_spec_example() {
        // From the Subsonic API docs: password "sesame", salt "c19b2d" -> "26719a1196d2a940705a59634eb18eab"
        assert_eq!(hex(&Md5::digest("sesamec19b2d")), "26719a1196d2a940705a59634eb18eab");
    }

    #[test]
    fn talks_to_a_server() {
        let m = mock::serve(|path, query| {
            assert!(query.contains("u=bob") && query.contains("&t=") && query.contains("&s=") && query.contains("f=json"));
            match path {
                "/rest/ping" => r#"{"subsonic-response":{"status":"ok","version":"1.16.1","type":"navidrome","serverVersion":"0.54.0"}}"#.into(),
                "/rest/search3" => r#"{"subsonic-response":{"status":"ok","searchResult3":{"song":[
                    {"id":"s1","title":"Song","artist":"A","album":"Alb","duration":200,"path":"Explo/Weekly/Song-A.mp3","musicBrainzId":"mb1"}]}}}"#.into(),
                "/rest/getPlaylists" => r#"{"subsonic-response":{"status":"ok","playlists":{"playlist":[
                    {"id":"p0","name":"Weekly","owner":"someone-else"},{"id":"p1","name":"Weekly","owner":"Bob"}]}}}"#.into(),
                "/rest/createPlaylist" => r#"{"subsonic-response":{"status":"ok","playlist":{"id":"p1"}}}"#.into(),
                "/rest/updatePlaylist" | "/rest/startScan" => r#"{"subsonic-response":{"status":"ok"}}"#.into(),
                "/rest/getScanStatus" => r#"{"subsonic-response":{"status":"ok","scanStatus":{"scanning":false}}}"#.into(),
                _ => r#"{"subsonic-response":{"status":"failed","error":{"code":70,"message":"nope"}}}"#.into(),
            }
        });
        let s = Subsonic::new(&cfg(&m.url));
        assert_eq!(s.ping().unwrap(), "navidrome 0.54.0 (API 1.16.1)");
        let songs = s.search_songs("Song A", 20).unwrap();
        assert_eq!(songs.len(), 1);
        assert_eq!(songs[0].path, "Explo/Weekly/Song-A.mp3");
        assert_eq!(songs[0].duration, 200);
        assert_eq!(s.find_playlist("Weekly").unwrap().as_deref(), Some("p1")); // own playlist, not someone else's
        let id = s.save_playlist(Some("p1"), "Weekly", &["s1".into(), "s2".into()]).unwrap();
        assert_eq!(id, "p1");
        s.update_playlist("p1", "made by Explo", false).unwrap();
        s.start_scan().unwrap();
        assert!(s.wait_for_scan(Duration::from_secs(5), &Ctx::silent()));

        let reqs = m.requests.lock().unwrap().clone();
        let create = reqs.iter().find(|r| r.contains("createPlaylist")).unwrap();
        assert!(create.contains("playlistId=p1") && create.contains("songId=s1") && create.contains("songId=s2"), "{create}");
        let err = s.call("bogus", &[]).unwrap_err();
        assert!(format!("{err:#}").contains("nope"));
    }
}
