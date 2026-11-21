# CLAUDE.md

Guide for working on **Explo-Enhanced**, a fork of [LumePart/Explo](https://github.com/LumePart/Explo). Explo is written in Go, with Python helpers that use ytmusicapi. The fork imports public YouTube Music playlists into a self-hosted library (Navidrome) and downloads only the songs that are missing. It is tuned for Indian film songs. What's done is listed in [README.md](README.md#changes-in-this-fork), and what's next in [ROADMAP.md](ROADMAP.md).

## ⚠️ Read first: what the tests are based on

**The real-world testing is almost all Tamil film music.** The owner's library is mostly Tamil, with some Telugu, Hindi, Malayalam and English. Every rule about titles was designed, tuned and checked by hand against:

- **Public Youtube playlists:** about 430 tracks in 5 YouTube Music playlists (Top Weekly Videos Tamil, Top Weekly Videos Hindi, Top 100 Music Videos India, 10s Tollywood Dance Hits, Bollywood Fire).
- **Real Tamil title patterns.** For example:
  - `Song | Film | Actors | Composer`;
  - Tamil script mixed with English letters: `தூது வளை அரச்சு-Thoothu Valai`;
  - the same word spelled differently in English letters: Andangkaka/Andangakka, Alagaro/Azhagaro, Thoothu/Thuthu;
  - `(From "Film")` singles;
  - dubbed versions that share a title, such as Tamil vs Telugu.

**Other languages were tested, but only offline.** A set of 1,277 chart videos from 30 countries (K-pop, J-pop, Latin, Arabic, African, …) is replayed against search results recorded once. That data was used to find wrong matches and to make sure nothing breaks. **It has never been checked against a real non-Tamil user library.**

If you change a rule for another language, treat it as unproven for that language. If you change a rule that affects Tamil titles, re-check the Tamil cases in the tests.

## Main principle: accuracy over coverage

The owner prefers "very accurate, even if it finds less". Every matching change follows these rules:

- **A track with no confident match keeps its own video audio. It must never become a different song.**
- **A wrong "you already have this" is worse than a duplicate download.** It leaves the right song missing from the playlist and puts another song in its place. A duplicate only wastes some disk space.
- **Rules that only reject a match are safe.** Rules that change the search query or the order of results are risky. For example, removing a date from a query once turned _Daddy Mummy_ (Villu, Tamil) into its Hindi remake.
- **The owner's choices:**
  - covers stay covers;
  - remix, live and instrumental videos keep their own audio;
  - fan edits (8D, slowed, sped up, nightcore, bass boosted) keep their own audio too. They are never swapped for the official song;
  - trailers, teasers and rehearsals are skipped;
  - mashups, jukeboxes and full albums are kept;
  - songs with no album stay "[Unknown Album]" with no cover art, and no album is guessed for them so that it can be easily identified and fixed with correct album.

## How a YouTube Music import flows

1. **Import (web UI):** [`youtube_music.go`](src/web/backend/playlist/youtube_music.go) runs [`ytmusic_playlist.py`](src/downloader/youtube_music/ytmusic_playlist.py). For each video, that script finds the official audio (`resolve()`) or keeps the video. It prints JSON with each track's `source`: `song`, `resolved` or `video`.
2. **Cache:** the tracks are saved to `data/cache/custom-<id>.json` ([`playlist.go`](src/web/backend/playlist/playlist.go)).
3. **Run:** [`main.go`](src/main/main.go) `loadCustomTracks()` reads the cache into `models.Track`.
4. **Library check:** [`subsonic.go`](src/client/subsonic.go) `SearchSongs()` asks Navidrome whether each song is already there.
   - Official songs are scored by `BestMatch()` in [`client.go`](src/client/client.go).
   - **Kept videos** (`VideoTitle != ""`) go through `matchKeptVideo()`, which needs proof (see below).
5. **Download:** [`youtube.go`](src/downloader/youtube.go) downloads that exact video ID and writes the tags. Cover art is added only when the song has an album (`wantsCover`).

**Adding a field to `models.Track` that must survive a run:** it has to be carried through 5 places:

1. the parse struct in `youtube_music.go`;
2. `PlaylistTrack` in `playlist.go`;
3. `cachedPrefetchTrack` in `playlist.go`;
4. `cachedTrack` in `WritePlaylistCache` (`playlist.go`);
5. `cachedTrack` in `main.go`.

Fields have been silently dropped here before. `TestCacheKeepsVideoFacts` in [`main_test.go`](src/main/main_test.go) checks the round trip.

## Library check for kept videos (`keptVideoMatch`)

A kept video's song name is a guess from its title, so a library song counts as "already have it" only with **proof**:

- the singer is named in the video title or listed on the video;
- the length is the same within 3 s;
- or the library album (the film) is named in the video title.

**More proof is needed when:**

- **one proof** is enough when the name is exact or only a filler word was dropped (Tamil, Anthem, Music…);
- **two proofs** are needed when any other word was dropped, the name is one word under 8 letters, or the spelling differs (a spelling difference also needs the singer).

**It is never a match when:**

- the full name is itself a real song on YouTube Music (`NameIsSong`: "Kanne Kalaimaane" is not "Kanne");
- the versions differ (a Live video is not the studio song);
- the video is a fan edit (`Hukum Sped Up` is not your `Hukum`);
- the languages differ;
- the library song is more than 60 s longer than the video. (A video can cut a verse: a 283 s video of a 300 s song is real.)

## Tests

```bash
go test ./src/...                                                        # Go: client, downloader, main, util
python3 -B src/downloader/youtube_music/test_ytmusic_playlist.py         # needs ytmusicapi; makes no network calls
```

- Results on 2026-11-21: **Go 13 tests passing, Python 129 checks passing.**
- The Python test counts its checks instead of stopping at the first failure. It prints `N passed, M failed (lines …)`.
- Use `-B`, or delete `__pycache__/` afterwards: it must not be committed.

**Write the test first.** Use real titles where you can; most tests are real Tamil cases from the owner's library or playlists. Include cases that **must not** match next to the ones that must.

**Before/after measurement (required for any matching change):** the owner wants proof that nothing gets worse.

- Count right matches lost, wrong matches added, and wrong matches removed. Check each changed track by hand.
- **The bar: right matches lost = 0, wrong matches added = 0.**
- **The tools for this are not in the repo.** They lived in a temporary folder:
  - a replay of the 1,277-video corpus with recorded searches;
  - a fake Navidrome (an `httptest` server) that serves a library list to the real `SearchSongs()`;
  - the owner's library export.
- To rebuild them:
  - record the ytmusicapi search results to JSON once, and replay them with a fake `yt.search`;
  - for the library, serve a song list from an `httptest` server that answers `search3`. Every query word must start a word in the title, artist or album, like Navidrome. See `fakeNavidrome` in [`subsonic_test.go`](src/client/subsonic_test.go).
  - Delete any temporary `zz_*_test.go` files afterwards.

## Facts that cost time to learn

- **YouTube throttles bursts.** A search may come back as an empty reply (`Expecting value: line 1 column 1`). `search()` retries 4 times. Without retries, one import left 108 songs as videos with no album.
- **Without a login, YouTube Music** shows no video-to-song link ("counterpart") and no video description. yt-dlp's song and album fields were empty for Indian videos.
- **Navidrome's `search3` needs every query word to match**, which is why shorter names are retried. A word spelled differently can't be found; that's why the code searches the first 4 letters.
- **An empty album is "contained" in every album name.** This used to give free points in `rankResult`; it's fixed now, and it's easy to bring back by accident.
- **ytmusicapi video types:**
  - `ATV` is official audio;
  - `OMV` is an official music video, which lists the song's artists;
  - `UGC` is a user upload, which lists the uploader's channel;
  - `None` also occurs.
- **The fan-edit word list exists twice:** `FAN_EDIT` in `ytmusic_playlist.py` and `fanEditRe` in `client.go`. Change both together.
- **Many upstream Go files are not gofmt-formatted.** Don't reformat whole files; it buries the real change in the diff.
- **Only the Navidrome (Subsonic) client** has the kept-video check. Plex, Jellyfin, Emby and MPD still use `BestMatch` only.

## Working rules

- **The owner reviews and commits.** Don't commit or push unless asked. Work on a feature branch.
- **Keep README entries short:** one bullet per change, with an example. Detail belongs in code comments, tests and the ROADMAP.
- **Open plans** (see ROADMAP): writing MusicBrainz IDs to downloaded songs, song-radio import (`RD<videoId>`), and a play-count dashboard for upgrading downloads to lossless copies.
