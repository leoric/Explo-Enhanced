//! The weekly job: ListenBrainz recommendations -> library check -> YouTube downloads ->
//! library scan -> playlist on the server.

use anyhow::{anyhow, bail, Result};
use chrono::{Local, Utc};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Mutex;
use std::time::{Duration, Instant};

use crate::config::{cache_dir, Config, PLAYLIST_KINDS};
use crate::events::{Cancelled, Ctx};
use crate::listenbrainz::ListenBrainz;
use crate::matching::check_tracks;
use crate::model::Track;
use crate::platform::{notify, KeepAwake};
use crate::schedule::{self, Due};
use crate::state::{PlaylistResult, RunLock, RunRecord, State};
use crate::subsonic::Subsonic;
use crate::util::{dir_safe, playlist_name};
use crate::youtube::Youtube;

pub fn kind_label(kind: &str) -> &str {
    PLAYLIST_KINDS.iter().find(|(k, _)| *k == kind).map(|(_, l)| *l).unwrap_or(kind)
}

/// Runs one playlist end to end.
pub fn run_playlist(cfg: &Config, kind: &str, ctx: &Ctx) -> Result<PlaylistResult> {
    let label = kind_label(kind).to_string();
    let today = Local::now().date_naive();
    let custom = cfg.playlists.custom_names.get(kind).map(String::as_str).unwrap_or("");
    let name = playlist_name(kind, custom, cfg.playlists.name_format, cfg.playlists.replace, today);
    // the download folder keeps the default name, so renaming a playlist doesn't move files
    let folder_name = playlist_name(kind, "", cfg.playlists.name_format, cfg.playlists.replace, today);
    let root = PathBuf::from(cfg.download.dir.trim());
    let dir = if cfg.download.use_subdirectory { root.join(dir_safe(&folder_name)) } else { root };
    let mut res = PlaylistResult { kind: kind.to_string(), name: name.clone(), ..Default::default() };

    ctx.stage(format!("{label}: fetching recommendations"));
    ctx.progress(0, 0);
    let (lb_title, mut tracks) = ListenBrainz::new(cfg).tracks_for(kind, ctx)?;
    res.total = tracks.len();
    ctx.info(format!("{label}: {} tracks in \"{lb_title}\"", tracks.len()));
    ctx.check()?;

    ctx.stage(format!("{label}: checking your library"));
    let server = Subsonic::new(&cfg.server);
    let version = server.ping()?;
    ctx.debug(format!("connected to {version}"));
    check_tracks(&server, &mut tracks, cfg.server.match_score, ctx)?;
    res.already_in_library = tracks.iter().filter(|t| t.present).count();
    ctx.info(format!("{label}: {} of {} already in your library", res.already_in_library, tracks.len()));

    // download what's missing
    let mut missing: Vec<usize> = tracks.iter().enumerate().filter(|(_, t)| !t.present).map(|(i, _)| i).collect();
    let limit = cfg.download.max_downloads_per_run as usize;
    if limit > 0 && missing.len() > limit {
        res.deferred = missing.len() - limit;
        missing.truncate(limit);
        ctx.info(format!("Download limit of {limit} reached; {} track(s) wait for the next run", res.deferred));
    }
    if !missing.is_empty() {
        ctx.stage(format!("{label}: downloading {} tracks", missing.len()));
        let (ok, failed) = download_tracks(cfg, &mut tracks, &missing, &dir, ctx)?;
        res.downloaded = ok;
        res.download_failed = failed;
        if ok == 0 && res.already_in_library == 0 {
            bail!("couldn't download any of the {} tracks (see the log for the reasons)", missing.len());
        }
    }
    ctx.check()?;

    // let the server see the new files
    if res.downloaded > 0 {
        refresh_and_recheck(cfg, &server, &mut tracks, ctx)?;
    }

    // the playlist
    ctx.stage(format!("{label}: saving playlist"));
    let mut ids: Vec<String> = vec![];
    for t in tracks.iter().filter(|t| t.present) {
        if !ids.contains(&t.library_id) {
            ids.push(t.library_id.clone());
        }
    }
    for t in tracks.iter().filter(|t| !t.present && !t.file.is_empty()) {
        ctx.warn(format!("\"{}\" was downloaded but the server hasn't indexed it yet; leaving it out of the playlist", t.title));
    }
    if ids.is_empty() {
        bail!("none of the tracks are in your library, so there is nothing to put in a playlist");
    }
    let existing = server.find_playlist(&name)?;
    let id = server.save_playlist(existing.as_deref(), &name, &ids)?;
    let comment = format!("Created for {} by Explo, using ListenBrainz recommendations.", cfg.listenbrainz.user.trim());
    if let Err(e) = server.update_playlist(&id, &comment, cfg.server.public_playlist) {
        ctx.warn(format!("Playlist saved, but updating its description failed: {e:#}"));
    }
    res.in_playlist = ids.len();
    ctx.info(format!("{label}: playlist \"{name}\" now has {} tracks", ids.len()));
    Ok(res)
}

fn download_tracks(cfg: &Config, tracks: &mut [Track], todo: &[usize], dir: &Path, ctx: &Ctx) -> Result<(usize, usize)> {
    let yt = Youtube::new(&cfg.download)?;
    let work = cache_dir().join("downloads");
    std::fs::create_dir_all(&work)?;
    let slots: Vec<Mutex<Track>> = todo.iter().map(|&i| Mutex::new(tracks[i].clone())).collect();
    let (next, done, ok) = (AtomicUsize::new(0), AtomicUsize::new(0), AtomicUsize::new(0));
    let gate = Mutex::new(Instant::now().checked_sub(Duration::from_secs(1)).unwrap_or_else(Instant::now));
    let workers = cfg.download.parallel.clamp(1, 8).min(slots.len());
    ctx.progress(0, slots.len());

    std::thread::scope(|s| {
        for _ in 0..workers {
            s.spawn(|| loop {
                if ctx.cancelled() {
                    break;
                }
                let i = next.fetch_add(1, Ordering::SeqCst);
                if i >= slots.len() {
                    break;
                }
                {
                    // be polite to YouTube: start at most one track per second across all workers
                    let mut last = gate.lock().unwrap();
                    let wait = Duration::from_secs(1).saturating_sub(last.elapsed());
                    if !wait.is_zero() {
                        std::thread::sleep(wait);
                    }
                    *last = Instant::now();
                }
                let mut t = slots[i].lock().unwrap().clone();
                let result = yt.find_video(&t, ctx).and_then(|id| {
                    t.video_id = id;
                    yt.download(&mut t, dir, &work, ctx)
                });
                match result {
                    Ok(()) => {
                        ok.fetch_add(1, Ordering::SeqCst);
                        ctx.info(format!("Downloaded: {} — {}", t.title, t.artist));
                    }
                    Err(e) if e.downcast_ref::<Cancelled>().is_some() => break,
                    Err(e) => {
                        t.file.clear();
                        ctx.warn(format!("Skipped {} — {}: {e:#}", t.title, t.artist));
                    }
                }
                *slots[i].lock().unwrap() = t;
                ctx.progress(done.fetch_add(1, Ordering::SeqCst) + 1, slots.len());
            });
        }
    });
    for (k, &i) in todo.iter().enumerate() {
        tracks[i] = slots[k].lock().unwrap().clone();
    }
    ctx.check()?;
    let ok = ok.load(Ordering::SeqCst);
    Ok((ok, todo.len() - ok))
}

/// Triggers a library scan, waits for it, then looks the new files up (retrying a few times:
/// some servers need a moment after the scan to make new songs searchable).
fn refresh_and_recheck(cfg: &Config, server: &Subsonic, tracks: &mut [Track], ctx: &Ctx) -> Result<()> {
    ctx.stage("Updating your library");
    ctx.progress(0, 0);
    let admin = Subsonic::admin(&cfg.server);
    let scanner = admin.as_ref().unwrap_or(server);
    match scanner.start_scan() {
        Ok(()) => {
            if !scanner.wait_for_scan(Duration::from_secs(cfg.server.scan_timeout_minutes.max(1) * 60), ctx) {
                ctx.warn("The library scan didn't report completion in time; checking anyway");
            }
        }
        Err(e) => {
            ctx.warn(format!("Couldn't start a library scan ({e:#}). If the server doesn't rescan by itself, set an admin account in Settings"));
            std::thread::sleep(Duration::from_secs(20));
        }
    }
    for round in 0..4 {
        ctx.check()?;
        check_tracks(server, tracks, cfg.server.match_score, ctx)?;
        if tracks.iter().all(|t| t.present || t.file.is_empty()) {
            return Ok(());
        }
        if round < 3 {
            ctx.debug("Some downloaded tracks aren't searchable yet, waiting 10s");
            for _ in 0..10 {
                ctx.check()?;
                std::thread::sleep(Duration::from_secs(1));
            }
        }
    }
    Ok(())
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Trigger {
    Scheduled,
    /// All enabled playlists, started by the user.
    ManualAll,
    /// A single playlist, started by the user.
    ManualOne,
}

impl Trigger {
    fn name(self) -> &'static str {
        if self == Trigger::Scheduled {
            "scheduled"
        } else {
            "manual"
        }
    }
}

/// Runs the given playlists one after another, records the outcome in the state file.
pub fn run_all(cfg: &Config, kinds: &[String], trigger: Trigger, ctx: &Ctx) -> RunRecord {
    let started = Utc::now();
    let mut results = vec![];
    let mut error = None;
    for (n, kind) in kinds.iter().enumerate() {
        if kinds.len() > 1 {
            ctx.info(format!("— Playlist {} of {}: {} —", n + 1, kinds.len(), kind_label(kind)));
        }
        match run_playlist(cfg, kind, ctx) {
            Ok(r) => {
                ctx.info(r.summary());
                results.push(r);
            }
            Err(e) if e.downcast_ref::<Cancelled>().is_some() => {
                error = Some("cancelled".to_string());
                break;
            }
            Err(e) => {
                ctx.error(format!("{}: {e:#}", kind_label(kind)));
                results.push(PlaylistResult { kind: kind.clone(), name: kind_label(kind).into(), error: Some(format!("{e:#}")), ..Default::default() });
            }
        }
    }
    let ok = error.is_none() && !results.is_empty() && results.iter().all(|r| r.error.is_none());
    let record = RunRecord { started, finished: Utc::now(), trigger: trigger.name().into(), ok, playlists: results, error };
    let mut st = State::load();
    st.push_history(record.clone());
    if ok && trigger != Trigger::ManualOne {
        st.last_success = Some(record.finished);
    }
    if let Err(e) = st.save() {
        ctx.warn(format!("Couldn't save run history: {e:#}"));
    }
    record
}

/// Any HTTP response at all counts as "online"; connection errors don't.
fn wait_until_online(cfg: &Config, ctx: &Ctx, max: Duration) {
    let client = crate::http::client(8);
    let targets = ["https://api.listenbrainz.org/1/".to_string(), format!("{}/rest/ping", cfg.server.url.trim().trim_end_matches('/'))];
    let start = Instant::now();
    loop {
        let missing: Vec<&String> = targets.iter().filter(|u| client.get(u.as_str()).send().is_err()).collect();
        if missing.is_empty() {
            return;
        }
        if start.elapsed() > max || ctx.cancelled() {
            ctx.warn(format!("Still can't reach {} — trying anyway", missing.iter().map(|u| crate::http::redact(u)).collect::<Vec<_>>().join(", ")));
            return;
        }
        ctx.info("Waiting for the network (a Mac that just woke up may need a moment)…");
        std::thread::sleep(Duration::from_secs(15));
    }
}

#[derive(Debug, PartialEq, Eq)]
pub enum ScheduledOutcome {
    NotDue(String),
    AlreadyRunning,
    Ran { ok: bool },
}

/// Entry point for `explo --scheduled` (started by launchd). `force` skips the due-check.
pub fn scheduled(ctx: &Ctx, force: bool) -> Result<ScheduledOutcome> {
    let cfg = Config::load()?;
    let problems = cfg.problems();
    if !problems.is_empty() {
        bail!("not configured: {}", problems.join("; "));
    }
    let mut state = State::load();
    let slot = match schedule::is_due(&state, Local::now(), &cfg.schedule) {
        Due::Yes(slot) => Some(slot),
        Due::No(reason) if !force => {
            ctx.info(format!("Nothing to do: {reason}"));
            return Ok(ScheduledOutcome::NotDue(reason));
        }
        Due::No(_) => None,
    };
    let Some(_lock) = RunLock::try_acquire()? else {
        ctx.info("Another Explo run is already in progress");
        return Ok(ScheduledOutcome::AlreadyRunning);
    };
    if let Some(slot) = slot {
        let key = slot.to_rfc3339();
        state.attempts = if state.slot == key { state.attempts + 1 } else { 1 };
        state.slot = key;
        state.save()?;
        ctx.info(format!("Starting scheduled run (attempt {} for this week's slot)", state.attempts));
    } else {
        ctx.info("Starting forced run");
    }
    let _awake = KeepAwake::new();
    wait_until_online(&cfg, ctx, Duration::from_secs(300));
    let record = run_all(&cfg, &cfg.playlists.enabled, Trigger::Scheduled, ctx);
    if cfg.notify {
        let body = if record.ok { record.summary() } else { format!("Run failed: {}", record.summary().lines().next().unwrap_or("see the log")) };
        notify("Explo", &body);
    }
    if !record.ok {
        return Err(anyhow!("run failed: {}", record.summary().replace('\n', "; ")));
    }
    Ok(ScheduledOutcome::Ran { ok: true })
}

/// Quick connectivity check used by the Settings screen.
pub fn test_connections(cfg: &Config) -> Vec<(String, Result<String>)> {
    vec![("ListenBrainz".into(), ListenBrainz::new(cfg).check_user()), ("Music server".into(), Subsonic::new(&cfg.server).ping())]
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use crate::state::State;
    use crate::subsonic::mock;
    use serde_json::{json, Value};
    use std::os::unix::fs::PermissionsExt;
    use std::process::Command;
    use std::sync::Arc;

    // Tests that point EXPLO_HOME at a temp dir must not run concurrently.
    static ENV_LOCK: Mutex<()> = Mutex::new(());

    const FAKE_YTDLP: &str = r#"#!/bin/bash
last="${@: -1}"
case "$last" in
  ytsearch5:*) echo '{"id":"FAKEVID001","title":"A fine song","channel":"Someone - Topic"}'; exit 0;;
esac
out=""; prev=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done
out="${out//%(ext)s/m4a}"
exec ffmpeg -loglevel error -y -f lavfi -i "sine=frequency=440:duration=1" -c:a aac "$out"
"#;

    fn lb_track(title: &str, artist: &str, album: &str, mbid: &str) -> Value {
        json!({"album": album, "creator": artist, "duration": 120000, "title": title,
               "identifier": [format!("https://musicbrainz.org/recording/{mbid}")], "extension": {}})
    }

    #[test]
    fn full_run_against_mock_services() {
        let _env = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = std::env::temp_dir().join(format!("explo-e2e-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&tmp);
        std::fs::create_dir_all(&tmp).unwrap();
        std::env::set_var("EXPLO_HOME", tmp.join("home"));
        let music = tmp.join("music");

        // --- mock ListenBrainz
        let lb = mock::serve(|path, _| match path {
            "/user/bob/playlists/createdfor" => json!({"count": 1, "offset": 0, "playlist_count": 1, "playlists": [
                {"playlist": {"date": "2026-11-23T00:00:00+00:00", "identifier": "https://listenbrainz.org/playlist/abc",
                  "extension": {"https://musicbrainz.org/doc/jspf#playlist": {"additional_metadata": {"algorithm_metadata": {"source_patch": "weekly-exploration"}}}}}}]}).to_string(),
            "/playlist/abc" => json!({"playlist": {"title": "Weekly Exploration for bob", "track": [
                lb_track("Already Here", "Band A", "Alb A", "m1"),
                lb_track("Song Two", "Band B", "Alb B", "m2"),
                lb_track("Song Three", "Band C", "Alb C", "m3")]}}).to_string(),
            _ => "{}".into(),
        });
        std::env::set_var("EXPLO_LISTENBRAINZ_API", &lb.url);

        // --- mock Navidrome: a library that grows when "scanned"
        let library: Arc<Mutex<Vec<Value>>> = Arc::new(Mutex::new(vec![
            json!({"id": "lib1", "title": "Already Here", "artist": "Band A", "album": "Alb A", "path": "Elsewhere/Already.mp3", "duration": 120, "musicBrainzId": "m1"}),
        ]));
        let created: Arc<Mutex<Vec<Vec<String>>>> = Arc::new(Mutex::new(vec![]));
        let (lib, made, scan_dir) = (library.clone(), created.clone(), music.join("Weekly-Exploration"));
        let server = mock::serve(move |path, query| {
            let url = reqwest::Url::parse(&format!("http://x{path}?{query}")).unwrap();
            let ok = |extra: Value| {
                let mut v = json!({"status": "ok"});
                v.as_object_mut().unwrap().extend(extra.as_object().unwrap().clone());
                json!({"subsonic-response": v}).to_string()
            };
            match path {
                "/rest/ping" => ok(json!({"version": "1.16.1", "type": "navidrome", "serverVersion": "test"})),
                "/rest/search3" => ok(json!({"searchResult3": {"song": *lib.lock().unwrap()}})),
                "/rest/startScan" => {
                    let mut lib = lib.lock().unwrap();
                    if let Ok(rd) = std::fs::read_dir(&scan_dir) {
                        for e in rd.flatten() {
                            let file = e.file_name().to_string_lossy().into_owned();
                            if file.starts_with('.') || lib.iter().any(|s| s["path"].as_str().is_some_and(|p| p.ends_with(&file))) {
                                continue;
                            }
                            let stem = file.rsplit_once('.').unwrap().0;
                            let (t, a) = stem.rsplit_once('-').unwrap();
                            let n = lib.len();
                            lib.push(json!({"id": format!("new{n}"), "title": t.replace('_', " "), "artist": a.replace('_', " "), "album": "", "path": format!("Explo/Weekly-Exploration/{file}"), "duration": 1}));
                        }
                    }
                    ok(json!({}))
                }
                "/rest/getScanStatus" => ok(json!({"scanStatus": {"scanning": false}})),
                "/rest/getPlaylists" => {
                    let has = !made.lock().unwrap().is_empty();
                    ok(json!({"playlists": {"playlist": if has { vec![json!({"id": "pl1", "name": "Weekly-Exploration", "owner": "bob"})] } else { vec![] }}}))
                }
                "/rest/createPlaylist" => {
                    made.lock().unwrap().push(url.query_pairs().filter(|(k, _)| k == "songId").map(|(_, v)| v.into_owned()).collect());
                    ok(json!({"playlist": {"id": "pl1"}}))
                }
                "/rest/updatePlaylist" => ok(json!({})),
                _ => json!({"subsonic-response": {"status": "failed", "error": {"code": 70, "message": "unknown"}}}).to_string(),
            }
        });

        // --- fake yt-dlp (real ffmpeg makes the audio)
        let ytdlp = tmp.join("yt-dlp");
        std::fs::write(&ytdlp, FAKE_YTDLP).unwrap();
        std::fs::set_permissions(&ytdlp, std::fs::Permissions::from_mode(0o755)).unwrap();

        let mut cfg = Config::default();
        cfg.listenbrainz.user = "bob".into();
        cfg.server.url = server.url.clone();
        cfg.server.user = "bob".into();
        cfg.server.password = "pw".into();
        cfg.download.dir = music.to_string_lossy().into_owned();
        cfg.download.extension = "m4a".into();
        cfg.download.ytdlp_path = ytdlp.to_string_lossy().into_owned();
        cfg.download.parallel = 2;
        assert!(cfg.problems().is_empty());

        // first run: 1 track already in the library, 2 get downloaded
        let ctx = Ctx::new(|e| {
            if let crate::events::Event::Log(l, m) = e {
                eprintln!("[{l:?}] {m}");
            }
        });
        let r = run_playlist(&cfg, "weekly-exploration", &ctx).expect("first run");
        assert_eq!((r.total, r.already_in_library, r.downloaded, r.download_failed, r.in_playlist), (3, 1, 2, 0, 3), "{r:?}");
        assert_eq!(r.name, "Weekly-Exploration");

        let file = music.join("Weekly-Exploration/Song_Two-Band_B.m4a");
        assert!(file.is_file(), "missing {}", file.display());
        let out = Command::new("ffprobe").args(["-v", "error", "-show_entries", "format_tags=title,artist,album", "-of", "default=nw=1"]).arg(&file).output().unwrap();
        let tags = String::from_utf8_lossy(&out.stdout).to_string();
        assert!(tags.contains("title=Song Two") && tags.contains("artist=Band B") && tags.contains("album=Alb B"), "{tags}");
        // no partial files left behind
        assert!(std::fs::read_dir(music.join("Weekly-Exploration")).unwrap().flatten().all(|e| !e.file_name().to_string_lossy().contains("partial")));
        assert_eq!(created.lock().unwrap().len(), 1);
        let ids = created.lock().unwrap()[0].clone();
        assert_eq!(ids.len(), 3);
        assert_eq!(ids[0], "lib1");

        // second run (through run_all): everything is in the library now, nothing downloaded, playlist replaced in place
        let rec = run_all(&cfg, &["weekly-exploration".to_string()], Trigger::ManualAll, &ctx);
        assert!(rec.ok, "{rec:?}");
        assert_eq!((rec.playlists[0].downloaded, rec.playlists[0].already_in_library, rec.playlists[0].in_playlist), (0, 3, 3));
        assert_eq!(created.lock().unwrap().len(), 2);
        let st = State::load();
        assert!(st.last_success.is_some());
        assert_eq!(st.history.len(), 1);
        let _ = std::fs::remove_dir_all(&tmp);
    }

    #[test]
    fn scheduled_does_nothing_when_not_due() {
        let _env = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = std::env::temp_dir().join(format!("explo-sched-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&tmp);
        std::env::set_var("EXPLO_HOME", tmp.join("home"));

        // not configured: a clear error, no run
        let err = scheduled(&Ctx::silent(), false).unwrap_err();
        assert!(format!("{err:#}").contains("not configured"), "{err:#}");

        let mut cfg = Config::default();
        cfg.listenbrainz.user = "bob".into();
        cfg.server.user = "bob".into();
        cfg.server.password = "pw".into();
        cfg.save().unwrap();
        // installed long ago, and this week's slot already succeeded
        let st = State { installed_at: Some(Utc::now() - chrono::Duration::days(30)), last_success: Some(Utc::now()), ..Default::default() };
        st.save().unwrap();
        match scheduled(&Ctx::silent(), false).unwrap() {
            ScheduledOutcome::NotDue(reason) => assert!(reason.contains("already ran"), "{reason}"),
            other => panic!("expected NotDue, got {other:?}"),
        }
        assert!(State::load().history.is_empty());

        // a run holding the lock blocks a forced run
        let _held = RunLock::try_acquire().unwrap().expect("lock is free");
        assert_eq!(scheduled(&Ctx::silent(), true).unwrap(), ScheduledOutcome::AlreadyRunning);
        let _ = std::fs::remove_dir_all(&tmp);
    }
}
