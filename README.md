# Explo - Music Discovery for Self-Hosted Music Systems

## Changes in this fork

This fork of [LumePart/Explo](https://github.com/LumePart/Explo) adds YouTube Music playlist import and better matching for Indian film songs.
See the [roadmap](ROADMAP.md) for what's in progress and planned.

**YouTube Music playlists**
- Import any public YouTube Music playlist (`PL…`), album (`OLAK5uy…`) or YouTube curated playlist (`RDCLAK…`) from the web UI. No account or cookies needed. Personal mixes are not supported.
- Music and lyric videos are swapped for their official audio track when a search finds a confident match (whole-title similarity, matching version words such as "Female Version", "Remix" or "Theme", and no language conflict). Otherwise the video's own audio is used.
- Titles in Tamil or other Indian scripts are handled: `தூது வளை அரச்சு-Thoothu Valai -Mano S Janaki` is searched as `Thoothu Valai`, and `ஏக் தோ தீன் தமிழ் பாடல்வரிகள்` as `ஏக் தோ தீன்`. A differently spelled match (`Thuthu Valaielle`) or a Tamil-script match is accepted only when the song's artist is named in the video title or listed on the video.
- A match is rejected, and the video kept, when it can't be the same recording:
  - the official video's artists aren't on the found song *and* the lengths differ by more than 30 s (`Hong Kong` by The Weeknd is not `Hong Kong` by Sgija Keys); labels such as Sun TV don't count as artists;
  - the video is a cover (`Cover by`, `원곡`) and the song isn't the cover singer's own release;
  - the video names a version in brackets that the song lacks (`(Remix)`, `(Live)`, `(Instrumental)`, `(伴奏)`). `Live`, `Ao Vivo` and `En Vivo` count as the same version;
  - the video runs 10 minutes or more and the song is under half as long (jukeboxes and full albums stay whole).
- The artist's own name at the start of a title (`COOLKID - QUÁ ĐỦ RỒI`) is not searched as a song, unless it is the whole title.
- Fan edits (8D, slowed + reverb, sped up, nightcore, bass boosted) keep their own audio: never swapped for the official song, and never counted as the official song in your library.
- Upload words are left out of the song name: `Kutty Pattas Music Video` → `Kutty Pattas`, `Soppanasundari Tamil Video` → `Soppanasundari`, `Taki Taki ft. Selena Gomez` → `Taki Taki`.
- Trailers, teasers, glimpses, snippets and rehearsals are left out unless they match a real song. "Promo songs" are kept.
- Failed searches are retried (YouTube throttles bursts), so songs aren't kept as videos just because YouTube was busy.
- Tracks are deduplicated by YouTube video ID, so several uploads of the same song appear once.
- The exact video ID is passed to the downloader, so it downloads that track without searching again.
- `YOUTUBE_PLAYLIST_LIMIT` (default `100`) caps how many tracks are read from a playlist. It can be set in the Settings config file or the container environment.

**Matching against your library**
- Song titles are cleaned before searching and comparing: `(From "Film")`, soundtrack tags and language tags are removed, so `Powerhouse (From "Coolie") (Tamil)` matches a library song titled `Powerhouse`.
- Dubbed versions are kept apart: a Telugu track never matches a library song tagged Tamil. A library title with no language still matches.
- If a search like `Hukum - Thalaivar Alappara` finds nothing, it is retried with the part before ` - `.
- A song with no album no longer gets free album points against every library song (that let a same-titled song by another artist match).
- A YouTube video kept as-is counts as already in your library only with proof: the singer, the length (±3 s) or the film must match, and the version too (a `(Live)` video is not the studio song). Shorter names (`Kovai Gethu Anthem` → `Kovai Gethu`) and other spellings (`Andangkaka` → `Andangakka`) need stronger proof. Each such match is logged.

**Downloads**
- `MAX_DOWNLOADS_PER_RUN` (default `0` = no limit) caps new downloads per run. Remaining tracks are downloaded on later runs.
- Audio is copied from YouTube as-is when it already matches `TRACK_EXTENSION` (Opus for `opus`/`ogg`, AAC for `m4a`) instead of being encoded a second time.
- With `EMBED_COVER_ART=true`, Opus/Ogg downloads get a `cover.jpg` in their album folder, because ffmpeg can't embed art in those files. This needs a `PATH_TEMPLATE` with an album folder.
- A song with no album gets no cover art, so it stays easy to find as "[Unknown Album]".
- With `USE_SUBDIRECTORY=true`, imported playlists download into a folder named after the playlist instead of `Custom-<id>`.

**Other fixes**
- Imported playlist caches keep the main artist, MusicBrainz ID, video ID and duration after a run (they were being dropped).
- The run log shows the playlist's name and download folder next to its `custom-…` ID.

**Web UI**
- YouTube Music added as an import source, with a weekly refresh by default.
- Imported playlist cards scroll sideways in one row instead of growing the page.

---


[![Discord](https://img.shields.io/discord/1497141529696014409?style=flat&logo=Discord&labelColor=white&color=black&link=https%3A%2F%2Fdiscord.gg%2FuFWWPaN2zk)](https://discord.gg/uFWWPaN2zk)

<p align="center">
  <img src="visual/dashboard.png" width="850" />
</p>

**Explo** bridges the gap between music discovery and self-hosted music systems. Its main function is to act as a self-hosted alternative to Spotify’s *Discover Weekly*, automating music discovery based on your listening history.

Explo uses the [ListenBrainz](https://listenbrainz.org/) recommendation engine to retrieve personalized tracks and automatically imports them into your music library.

---

## Features

- Automatic music discovery powered by ListenBrainz
- Web UI for setup, scheduling, and playlist import management
- Fetch personalized playlists from ListenBrainz:
  - Weekly Exploration
  - Weekly Jams
  - Daily Jams
  - Fresh Releases
- Import custom playlists from:
  - Apple Music
  - ListenBrainz
  - Spotify
- Automatically request playlist tracks from YouTube and Soulseek, or albums through Lidarr
- Add metadata to downloaded tracks
- Create playlists in your music system
- Optionally keep previous playlists for later listening
---

## Documentation

See the [Wiki Home](https://github.com/LumePart/Explo/wiki) for an overview of supported systems and next steps.

Or jump directly to:
- [Quick Start](https://github.com/LumePart/Explo/wiki/2.-Quick-Start) – Get Explo running with docker
- [Getting Started](https://github.com/LumePart/Explo/wiki/3.-Getting-Started) – Installation and setup guide  
- [Configuration Parameters](https://github.com/LumePart/Explo/wiki/5.-Configuration-Parameters) – Environment variable and flag reference  
- [System Notes](https://github.com/LumePart/Explo/wiki/6.-System-Notes) – Known issues and system-specific tips  
- [FAQ](https://github.com/LumePart/Explo/wiki/8.-FAQ) – Common questions


## Kubernetes

```bash
helm repo add explo https://lumepart.github.io/Explo && helm repo update
helm install explo explo/explo --namespace explo --create-namespace
```

See [charts/explo/README.md](charts/explo/README.md).

## Acknowledgements

Explo uses the following 3rd-party libraries:

- [ffmpeg-go](https://github.com/u2takey/ffmpeg-go): Go wrapper for FFmpeg

- [goutubedl](https://github.com/wader/goutubedl): Go wrapper for yt-dlp

- [godotenv](https://github.com/joho/godotenv): Load configuration from `.env` files

- [ytmusicapi](https://github.com/sigma67/ytmusicapi): Unofficial Youtube Music API

- [notify](https://github.com/nikoksr/notify): Module for sending notifications to different services

- [gocron](https://github.com/go-co-op/gocron): Internal cron scheduling

## Contributing

Contributions are welcome! For suggestions, bug reports, or feature requests, please open an issue. Before opening a pull request, please read the [contributing guide](https://github.com/LumePart/Explo?tab=contributing-ov-file)

For discussion regarding development or help, join our [Discord!](https://discord.gg/uFWWPaN2zk)