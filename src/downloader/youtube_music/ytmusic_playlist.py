#!/usr/bin/env python3
"""Fetch a public YouTube Music playlist and print it as JSON for Explo.

Usage: ytmusic_playlist.py <playlistId> [limit]

Most chart/playlist entries are music or lyric videos with titles like
"Bindaas - Lyric Video | Jailer 2 | Superstar Rajinikanth | Sun TV". For those we
take the words before the first special character as the song name, look it up in
YouTube Music's song search and, only when the result really matches, use the
official audio's clean title, album, duration and videoId instead.
"""
import difflib
import json
import re
import sys

from ytmusicapi import YTMusic

# Indian scripts (Devanagari to Sinhala: Hindi, Tamil, Telugu, Kannada, Malayalam...).
INDIC = "\u0900-\u0DFF"
# The song name ends at the first of these: | ( [ { " # @ •, a dash with a space on either
# side, or a dash/underscore where an Indian script meets Latin ("தூது வளை-Thoothu Valai").
# A plain space between scripts is not a cut: "वर्दी के Power" is one name.
STOP_RE = re.compile(
    rf'\s[-–—]|[-–—]\s|[|｜(\[{{"“”#@•]'
    rf"|(?<=[{INDIC}])\s*[-–—_]+\s*(?=[A-Za-z])|(?<=[A-Za-z])\s*[-–—_]+\s*(?=[{INDIC}])"
)
INDIC_RE = re.compile(f"[{INDIC}]")
# Words that describe the upload, not the song.
JUNK_RE = re.compile(
    r"\b(official|lyric(al)?s?|full|video|audio|songs?|visuali[sz]er|promo|teaser|"
    r"4k|8k|hd|uhd|latest)\b",
    re.I,
)
# Tamil words for "Tamil", "in Tamil", "song lyrics", "song", "lyrics", "video".
# ponytail: Tamil only (the user's playlists); add other scripts' words when they show up.
TAMIL_JUNK_RE = re.compile(r"(?<!\S)(தமிழ்|தமிழில்|பாடல்வரிகள்|பாடல்|வரிகள்|வீடியோ)(?!\S)")
# "(From "Film")", "[From "Film"]", "(From - Film)"
FROM_RE = re.compile(r'\s*[(\[]\s*from\s*[-:]?\s*(?:"([^"]*)"|“([^”]*)”|([^)\]]*))\s*[)\]]', re.I)
OST_RE = re.compile(
    r"\s*[(\[]\s*original\s+(?:motion\s+picture\s+soundtrack|background\s+score|score|soundtrack)\s*[)\]]",
    re.I,
)
# A different recording than the video: never accept these unless the video says so too.
VERSION_RE = re.compile(
    r"\b(female|male|version|remix|reprise|unplugged|lo-?fi|slowed|reverb|instrumental|"
    r"karaoke|cover|acoustic|live|sad|duet|mashup|extended|rendition|reprised?|theme|bgm)\b",
    re.I,
)
# Not a performer: a date that YouTube put in the artist field, or a label / TV channel.
DATE_RE = re.compile(r"^[A-Z][a-z]{2} \d{1,2}, \d{4}$")
LABEL_RE = re.compile(
    r"\b(records?|recordings|music|labels?|entertainment|productions?|studios?|tv|films?|media|"
    r"saregama|t-series|tips|divo|lahari)\b",
    re.I,
)
# One artist field can hold several names: "A & B", "A X B", "A feat. B", "Member (GROUP)".
ARTIST_SEP_RE = re.compile(r",|&|/|\(|\)|\s+(?:and|x|feat\.?|ft\.?|with|y|e|و)\s+", re.I)
LANG_RE = re.compile(r"\b(tamil|telugu|kannada|malayalam|hindi)\b", re.I)
ALNUM_RE = re.compile(r"[\W_]+")
MIN_SIMILARITY = 0.85
# Spelling differences when Tamil etc. is written in English: Thoothu/Thuthu, Valai/Vaalai.
SPELLING = [(r"(.)\1+", r"\1"), ("dh", "d"), ("th", "t"), ("zh", "l"), ("sh", "s"), ("w", "v"), ("ee", "i"), ("oo", "u")]
LOOSE_SIMILARITY = 0.7


def song_name(title):
    """'Kasu Panam Video Song - Extended Version | Soodhu Kavvum' -> 'Kasu Panam'"""
    names = candidates(title)
    return names[0] if names else ""


def candidates(title):
    """The first two non-empty segments: 'Love Insurance Kompany - Dheema Promo' tries both.
    English spellings first: library titles and YouTube Music song titles use them."""
    names = []
    for part in STOP_RE.split(title):
        name = " ".join(TAMIL_JUNK_RE.sub(" ", JUNK_RE.sub(" ", part)).split())
        if name and name not in names:
            names.append(name)
        if len(names) == 2:
            break
    return sorted(names, key=lambda n: bool(INDIC_RE.search(n)))


def norm(s):
    return ALNUM_RE.sub("", s.lower())


def spelled(s):
    s = norm(s)
    for pattern, repl in SPELLING:
        s = re.sub(pattern, repl, s)
    return s


def names(artists):
    """'Mano, S Janaki' -> ['mano', 's janaki']"""
    out = []
    for artist in artists:
        for name in re.split(r",|&|\band\b", artist.lower()):
            name = " ".join(re.sub(r"[\W_]+", " ", name).split())
            if len(name) >= 4:
                out.append(name)
    return out


def singers_named(video_title, found_artists, video_artists=()):
    """True when one of the found song's artists is named in the video title or listed on the video
    (YouTube lists the song's artists on official videos; 'Yuvan Shankar Raja' = 'Yuvanshankar Raja')."""
    title = " ".join(re.sub(r"[\W_]+", " ", video_title.lower()).split())
    listed = {norm(n) for n in names(video_artists)}
    return any(re.search(rf"\b{re.escape(n)}\b", title) or norm(n) in listed for n in names(found_artists))


def same_name(wanted, found_title, found_artists, video_title, video_artists=()):
    a, b = norm(wanted), norm(song_name(found_title))
    if not b:
        return False
    if a and difflib.SequenceMatcher(None, a, b).ratio() >= MIN_SIMILARITY:
        return True
    # Spelled differently (or in Tamil script): only when the video names the song's singer.
    if not singers_named(video_title, found_artists, video_artists):
        return False
    if INDIC_RE.search(wanted):
        return True
    a, b = spelled(wanted), spelled(song_name(found_title))
    return difflib.SequenceMatcher(None, a, b).ratio() >= LOOSE_SIMILARITY or (len(a) >= 5 and b.startswith(a))


def is_match(video_title, wanted, found_title, found_artists=(), video_artists=()):
    """True when found_title is the same song as `wanted` (not a cover, remix, other song)."""
    if not same_name(wanted, found_title, found_artists, video_title, video_artists):
        return False
    for word in VERSION_RE.findall(found_title):
        if not re.search(rf"\b{re.escape(word)}\b", video_title, re.I):
            return False
    # A Telugu video must not resolve to the Tamil audio (dubbed versions share names).
    want, got = LANG_RE.findall(video_title), LANG_RE.findall(found_title)
    return not (want and got and want[0].lower() != got[0].lower())


def performers(artists):
    """Normalised performer names, without dates and labels: ['Jeyma Monica X Dini Kurnia'] -> ['jeymamonica', 'dinikurnia']"""
    return [n for a in artists if not DATE_RE.match(a) and not LABEL_RE.search(a)
            for n in map(norm, ARTIST_SEP_RE.split(a)) if len(n) >= 4]


def same_artist(a, b):
    """'bigass' / 'jengbigass', 'taylorswift' / 'taylorswiftvevo', 'fadelshaker' / 'fadlshaker'"""
    return a in b or b in a or difflib.SequenceMatcher(None, a, b).ratio() >= MIN_SIMILARITY


def other_song(item, found):
    """True when an official video's performers are not on the found song and the lengths differ by
    more than 30 s: the same title by someone else ('Hong Kong' by The Weeknd vs by Sgija Keys).
    Either alone is normal for the right song (composer-credited videos, long intros).
    "Big Ass" = "Jeng Big Ass" and "XVEVO" = "X": a name inside the other counts as the same artist."""
    if item.get("videoType") != "MUSIC_VIDEO_TYPE_OMV":
        return False  # user uploads list the uploader's channel, not the performer
    mine = performers(a["name"] for a in item.get("artists") or [] if a.get("name"))
    theirs = performers(a["name"] for a in found.get("artists") or [] if a.get("name"))
    if not mine or any(same_artist(a, b) for a in mine for b in theirs):
        return False
    a, b = item.get("duration_seconds"), found.get("duration_seconds")
    return bool(a and b and abs(a - b) > 30)


def clean_title(title):
    """Drop (From "Film") and soundtrack tags; keep language and version tags."""
    return " ".join(OST_RE.sub("", FROM_RE.sub("", title)).split())


def film_of(*texts):
    for text in texts:
        m = FROM_RE.search(text or "")
        if m:
            film = next(g for g in m.groups() if g is not None).strip(" -")
            if film:
                return film
    return ""


def cover(item):
    thumbs = item.get("thumbnails") or []
    if not thumbs:
        return ""
    # YouTube Music art URLs end in "=w60-h60-..."; ask for a bigger square.
    return re.sub(r"=w\d+-h\d+", "=w544-h544", thumbs[-1]["url"])


def track_from(item, source):
    artists = [a["name"] for a in item.get("artists") or [] if a.get("name")]
    album = (item.get("album") or {}).get("name") or ""
    title = item.get("title") or ""
    # Singles are released as 'Song (From "Film")': the film is the real album.
    album = film_of(title, album) or album
    return {
        "title": clean_title(title) if source != "video" else song_name(title) or title,
        "artist": ", ".join(artists),
        "mainArtist": artists[0] if artists else "",
        "album": clean_title(album),
        # A video's length includes intros/dialogue, so only trust official audio durations.
        "durationMs": (item.get("duration_seconds") or 0) * 1000 if source != "video" else 0,
        "videoId": item["videoId"],
        "coverUrl": cover(item),
        "source": source,
    }


def resolve(yt, item):
    """Return the official audio for a music/lyric video, or None."""
    title = item.get("title") or ""
    artists = item.get("artists") or []
    video_artists = [a["name"] for a in artists if a.get("name")]
    # UGC uploads list the channel (e.g. "Sun TV") as artist; that only confuses search.
    artist = artists[0]["name"] if artists and item.get("videoType") != "MUSIC_VIDEO_TYPE_UGC" else ""
    # "COOLKID - Song": the artist's own name is not a song name to search for.
    listed = {norm(a) for a in video_artists}
    names = candidates(title)
    names = [n for n in names if norm(n) not in listed] or names
    for i, name in enumerate(names):
        try:
            results = yt.search(f"{name} {artist}".strip(), filter="songs", limit=5)
        except Exception as e:  # one failed lookup must not fail the whole playlist
            print(f"search failed for {name!r}: {e}", file=sys.stderr)
            return None
        indic = bool(INDIC_RE.search(name))
        # A Tamil-script name can't be compared letter by letter: trust only the top result.
        pool = [r for r in (results[:1] if indic else results[:3]) if r.get("videoId")]
        # Exact spelling first. Looser spelling (singer named in the video) only for the song
        # name itself: a later segment is often the film, and songs get named after films.
        passes = (False, True) if i == 0 else (False,)
        for loose in passes:
            for r in pool:
                found_artists = [a["name"] for a in r.get("artists") or [] if a.get("name")] if loose else ()
                if is_match(title, name, r.get("title") or "", found_artists, video_artists) and not other_song(item, r):
                    return r
    return None


def main():
    if len(sys.argv) < 2:
        print("Usage: ytmusic_playlist.py <playlistId> [limit]", file=sys.stderr)
        sys.exit(1)
    playlist_id = sys.argv[1]
    limit = int(sys.argv[2]) if len(sys.argv) > 2 else 100

    yt = YTMusic()
    playlist = yt.get_playlist(playlist_id, limit=limit)

    tracks, seen = [], set()
    for item in (playlist.get("tracks") or [])[:limit]:
        if not item.get("videoId") or item.get("isAvailable") is False:
            continue
        if item.get("videoType") == "MUSIC_VIDEO_TYPE_ATV":
            track = track_from(item, "song")
        else:
            found = resolve(yt, item)
            track = track_from(found, "resolved") if found else track_from(item, "video")
        if track["videoId"] in seen:  # two uploads of the same song resolve to one videoId
            continue
        seen.add(track["videoId"])
        tracks.append(track)

    print(json.dumps({
        "name": playlist.get("title") or "",
        "artwork": cover(playlist),
        "tracks": tracks,
    }, ensure_ascii=False))


if __name__ == "__main__":
    main()
