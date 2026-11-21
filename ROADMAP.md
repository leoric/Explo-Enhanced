# Roadmap

What this fork has done and what it plans next. The fork adds YouTube Music playlists and better matching for Indian film songs to [LumePart/Explo](https://github.com/LumePart/Explo). See the [README](README.md#changes-in-this-fork) for how to use what's done.

**Status:** ✅ Done · 🚧 In progress · 📋 Planned · 💡 Idea · ⛔ Not planned

---

## ✅ Done

### YouTube Music playlist import
- Import any **public** YouTube Music playlist, album or curated playlist from the web UI. No account or cookies needed.
- Music and lyric videos are swapped for their **official audio** when a search finds a confident match. Otherwise the video's own audio is used.
- Tracks are deduplicated by video ID. The exact video is downloaded without searching again.
- Settings: `YOUTUBE_PLAYLIST_LIMIT` (tracks read per playlist) and `MAX_DOWNLOADS_PER_RUN` (new downloads per run; the rest wait for later runs).
- Imported playlists download into a folder named after the playlist.

### Downloads
- **Audio is copied without re-encoding** when YouTube's audio already fits `TRACK_EXTENSION` (Opus for `opus`/`ogg`, AAC for `m4a`).
- **Cover art for Opus/Ogg**: a `cover.jpg` in the album folder, since ffmpeg can't embed art in those files.

### Web UI
- YouTube Music added as an import source.
- Imported playlist cards scroll sideways in one row.

---

## 🚧 In progress

### Title cleanup for better matching
Goal: match YouTube videos to the right song and to songs already in your library, **favouring accuracy over coverage**. A track with no confident match is downloaded from its video instead. It never becomes a wrong song.

Done so far:
- Film-song tags: `(From "Film")`, soundtrack tags and language tags are removed before searching.
- Dubbed versions kept apart: a Telugu track never matches a library song tagged Tamil.
- Search is retried with the part before ` - `.
- Tamil-script titles (`தூது வளை அரச்சு-Thoothu Valai`, `ஏக் தோ தீன் தமிழ் பாடல்வரிகள்`) are handled, and differently spelled matches are accepted only when the artist confirms them.
- "Theme" and "BGM" tracks are not taken for the video's song.
- Rules that can only **reject** a wrong match, found by testing 1,277 chart videos from 30 countries:
  - A song name mixing scripts (`वर्दी के Power`) is not split in two.
  - The artist's name at the start of a title is never searched as a song (`COOLKID - QUÁ ĐỦ RỒI`).
  - Same title, different artist: rejected when no listed artist matches *and* the length differs by more than 30 s (`Hong Kong` by The Weeknd ≠ `Hong Kong` by Sgija Keys; also `JAILER - Hukum` ≠ a song called "Jailer"). Labels, "official"/VEVO/Topic suffixes and group names are accounted for.
  - A cover video stays a cover, or becomes the cover singer's own release of it.
  - Version words count in both directions and in more languages: a `(Remix)`, `(Live)`, `(Instrumental)`, `(En Vivo)` or `(伴奏)` video keeps its own audio.
  - A 10+ minute video (jukebox, full album, background score) is never swapped for a short song.
- Fan edits (8D, slowed, sped up, nightcore) keep their own audio, like remixes and live videos.
- Trailers, teasers, glimpses, snippets and rehearsals are skipped; mashups and jukeboxes are kept.
- Invisible characters are ignored, and so are dates shown as the artist when comparing artists.
- Upload words are left out of song names: `Music Video`, a language before `Video`/`Song`, `ft. …`. Measured: 8 more right matches, no new wrong one.
- **Library check for videos kept as-is**: a same-titled library song counts only with proof (singer, length or film), versions must agree, and the name may be shortened (`Kovai Gethu Anthem` → `Kovai Gethu`) or spelled differently (`Andangkaka` → `Andangakka`) under stricter proof. Also fixes an old bug where a song with no album got free album points. Fixes duplicate downloads of songs you own. Tested against a simulated 18,873-song library: kept videos wrongly marked "already have it" went from about 15 to 0. Against a real 5,575-song library and 424 playlist tracks: 2 wrong matches removed, none lost; 8 of 10 known duplicates now found.
- Songs with no album stay "[Unknown Album]" without cover art.
- Failed YouTube searches are retried: in one import of 5 playlists, 108 throttled searches had left songs as videos with no album.

Measured on the 1,277-track test: 43 wrong matches removed, 20 right matches added, no right match lost.

Next: rules that find *more* matches, each only if a rerun of the 1,277-track test shows no new wrong match.
- Better song names for videos titled like `New Song 2026 | New Hindi Song | Tere Ishq Ki Kasam` (today: "New 2026").
- Prefer the result whose album is the film named in the video (`Daddy Mummy … Villu` must not become the Hindi remake from *Bhaag Johnny*; today it's right only because of how YouTube orders results).

---

## 📋 Planned

### Listening history from Navidrome
Read how often each song is played, so the fork can act on what you actually listen to.
- Source: Navidrome's Subsonic API reports a play count and last-played date per song (`getSong`, `getAlbumList2?type=frequent`).
- Fallback: ListenBrainz listen counts, since plays are already scrobbled there.
- Used by the quality dashboard below. It could later feed discovery too.

### Dashboard: low-quality downloads and upgrades
A page listing the songs this fork downloaded in lossy quality (YouTube Opus/AAC), with each song's play count. Songs you play a lot get **replaced with a lossless copy** automatically.
- **List:** each downloaded song with format, bitrate, source (YouTube video / official audio), play count, last played, and which playlist brought it in.
- **Upgrade rule:** when the play count reaches a threshold (setting, e.g. `UPGRADE_PLAY_COUNT=5`), search for a lossless copy and replace the low-quality file.
- **Sources:** Soulseek (slskd) for a FLAC of the track, or Lidarr for the album. Both already exist in Explo.
- **Safety:**
  - Accept a replacement only if title, artist and length match.
  - Keep the old file until the new one is verified.
  - Allow manual "upgrade now" and "never upgrade" per song.
- **Open question:** a file with a new extension (`.opus` → `.flac`) may look like a new song to Navidrome, losing its play count and playlist entries. Check whether Navidrome's persistent track IDs keep them across the swap. Otherwise, copy them over through the API.

### MusicBrainz IDs for imported songs
Look up each imported song on MusicBrainz and write its ID (MBID) into the file. Scrobbles then map exactly in ListenBrainz, which improves recommendations.
- Measured on 121 Indian tracks: an MBID was found for **62%**. A match requires a similar title, length within 5 s and no language conflict. One doubtful match was seen.
- Explo already writes MusicBrainz tags once a track has an ID. Only the lookup is new.
- Cost: about 1–3 s per track at import, because MusicBrainz allows one request per second.

---

## 💡 Ideas

- **Song radio import:** accept YouTube "Mix" links built from one song (`list=RD<videoId>`). They give related songs with no login, and YouTube refreshes them. Needs a different read call; tested to return about 50 songs.
- **More languages:** quoted K-pop song names, Japanese/Chinese brackets and separators, filler words in other languages, Simplified/Traditional Chinese, artist names in other scripts. Deliberately later: each one helps coverage but carries some accuracy risk.
- **Offer changes back to upstream Explo** as pull requests: the matching fixes, the YouTube Music importer, audio copying, cover art.

---

## ⛔ Not planned (and why)

- **Personal mixes (My Mix, Supermix, Replay, Discover) read directly:** Google sign-in no longer works for YouTube Music's API. ytmusicapi has had issue #813 open since Sep 2025, and its maintainer sees no path forward. Browser cookies expire unpredictably. **Workaround:** in YouTube Music, use *Save to playlist* on the mix, make that playlist public, and import it. Re-save it now and then.
- **Staggered schedule times** for imported playlists: all imports keep the time the user set.
- **Cover art embedded inside Opus files:** the folder `cover.jpg` is enough for Navidrome.
