# Explo-RS

A native desktop rewrite of [Explo / Explo-Enhanced](https://github.com/leoric/ExploEnhanced): your
ListenBrainz weekly recommendations, downloaded from YouTube and turned into playlists on your
Navidrome (or any Subsonic-compatible) server. No Docker, no web server, no browser: one app with a GUI,
and an optional weekly background job.

```
ListenBrainz ──► tracks ──► already in your library? ──► yt-dlp + ffmpeg (tagged files)
                                                   └────► library scan ──► playlist on the server
```

## Install

Requirements: `brew install yt-dlp ffmpeg`, and a Rust toolchain (≥ 1.89) to build.

```sh
scripts/bundle-macos.sh          # builds target/Explo.app (ad-hoc signed)
mv target/Explo.app /Applications
```

First launch: right-click → Open (the app isn't notarized). Then:

1. **Settings**: ListenBrainz username, server address and login, download folder (inside your music library). *Test connections* checks both.
2. **Dashboard**: tick the playlists you want, press **Run all** (or *Run now* on a single one).
3. **Schedule**: pick a weekday and time, press **Install weekly schedule**.

Settings save automatically to `~/Library/Application Support/Explo/config.json` (mode 0600).

## How the weekly job copes with a sleeping Mac

The schedule is a per-user **LaunchAgent** (`~/Library/LaunchAgents/com.explo.weekly.plist`) that starts
`Explo.app/Contents/MacOS/explo --scheduled`:

* **Asleep at the scheduled time** — launchd runs a missed calendar job when the Mac wakes (cron would skip it).
* **Powered off / logged out** — `RunAtLoad` and an hourly trigger catch up afterwards.
* Every start asks *"is a run due?"*: a slot (e.g. Monday 09:00) is run **at most once** after it passes; slots from before you installed are not backfilled; failures retry hourly, at most 6 times per slot.
* It waits up to 5 minutes for the network after wake, and keeps the Mac awake (`caffeinate`) while running.
* A lock file prevents the GUI and the background job from running at the same time.

Install the schedule **after** moving the app to `/Applications` — the job remembers the executable's path
(the app warns about macOS "translocated" paths and moved binaries).
If your music folder is on an external/network volume, macOS may ask for permission the first time.

## Command line

```
explo                 open the app
explo --scheduled     run only if this week's run is due (what launchd calls)
explo --run           run all enabled playlists now
explo --install / --uninstall / --status
```
Logs: `~/Library/Application Support/Explo/logs/explo.log` (also shown in the Log tab).
Set `EXPLO_HOME=/some/folder` to keep config, state, logs and cache elsewhere.

## What is (and isn't) ported from the Go version

| | |
|---|---|
| ✅ | ListenBrainz: Weekly Exploration, Weekly Jams, Daily Jams, On Repeat; artist blacklist; `(feat. …)` handling |
| ✅ | Navidrome/Subsonic: library matching (same scoring as the Go code), library scan, create/replace playlist in place |
| ✅ | YouTube Music song search (falls back to yt-dlp search), yt-dlp download, codec copy when possible, ffmpeg tagging, optional cover art, filter words, parallel downloads, per-run cap |
| ✅ | Playlist naming (week/date/custom), per-playlist download folders |
| ❌ | YouTube Music / Spotify / Apple Music **playlist import** and its Tamil/Indic title heuristics |
| ❌ | Fresh Releases, MusicBrainz metadata enrichment, `--clean-downloads` / keepers |
| ❌ | Plex, Jellyfin, Emby, MPD, slskd, Lidarr, notifications other than the macOS one |
| ❌ | Scheduling on Linux/Windows (the `scheduler` module is the place to add systemd timers / Task Scheduler) |

## Layout

* `crates/explo-core` — everything except the UI: config, ListenBrainz, Subsonic, matching, YouTube, pipeline, schedule logic, launchd integration. Cross-platform.
* `crates/explo-app` — the egui window and the command-line entry point.

`cargo test --workspace` runs the unit tests plus an end-to-end test (mock ListenBrainz, mock Navidrome, fake yt-dlp, real ffmpeg).
