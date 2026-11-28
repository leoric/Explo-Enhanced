//! Decides whether a recommended track is already in the library.
//! Scoring is a port of the Go implementation's `BestMatch`.

use anyhow::Result;
use std::path::Path;

use crate::events::Ctx;
use crate::model::{SearchResult, Track};
use crate::subsonic::Subsonic;
use crate::util::{clean_search_title, contains_fold, language, normalize_title};

const DEFINITIVE: i32 = 1000;

struct Norm<'a> {
    title: String,
    artist: &'a str,
    album: &'a str,
    file: &'a str,
    duration_ms: u32,
    mb_track_id: &'a str,
    language: String,
}

impl<'a> Norm<'a> {
    fn of(t: &'a Track) -> Self {
        Self {
            title: normalize_title(&t.clean_title),
            artist: &t.main_artist,
            album: &t.album,
            file: &t.file,
            duration_ms: t.duration_ms,
            mb_track_id: &t.mb_track_id,
            language: language(&[&t.clean_title, &t.title, &t.album]),
        }
    }
}

fn rank(t: &Norm, r: &SearchResult) -> i32 {
    if !t.mb_track_id.is_empty() && r.mbid == t.mb_track_id {
        return DEFINITIVE;
    }
    // Dubbed versions share a title: when both sides name a language and they differ, it's another song.
    let lang = language(&[&r.title, &r.album]);
    if !t.language.is_empty() && !lang.is_empty() && lang != t.language {
        return 0;
    }
    let mut score = 0;
    let rt = normalize_title(&r.title);
    if t.title == rt {
        score += 45;
    } else if t.title.len() > 3 && (rt.contains(&t.title) || t.title.contains(&rt)) {
        score += 25;
    }
    // An empty album is "contained" in every album name: only compare two real albums.
    if !t.album.is_empty() && !r.album.is_empty() {
        if t.album.eq_ignore_ascii_case(&r.album) {
            score += 20;
        } else if contains_fold(t.album, &r.album) || contains_fold(&r.album, t.album) {
            score += 15;
        }
    }
    if !t.artist.is_empty() && !r.artist.is_empty() {
        if t.artist.to_lowercase() == r.artist.to_lowercase() {
            score += 30;
        } else if contains_fold(t.artist, &r.artist) || contains_fold(&r.artist, t.artist) {
            score += 15;
        }
    }
    if !t.file.is_empty() {
        let base = Path::new(&r.path).file_name().map(|f| f.to_string_lossy().into_owned()).unwrap_or_default();
        if t.file.eq_ignore_ascii_case(&base) {
            score += 50;
        }
    }
    if t.duration_ms != 0 && r.duration != 0 {
        let diff = (r.duration as i64 - (t.duration_ms / 1000) as i64).abs();
        score += match diff {
            0..=2 => 10,
            3..=9 => 5,
            d if d > 30 => -10,
            _ => 0,
        };
    }
    score
}

/// The best library result for the track, if it clears `min_score`.
pub fn best_match<'r>(track: &Track, results: &'r [SearchResult], min_score: i32) -> Option<(&'r SearchResult, i32)> {
    let norm = Norm::of(track);
    let mut best: Option<(&SearchResult, i32)> = None;
    for r in results {
        let s = rank(&norm, r);
        if best.is_none_or(|(_, b)| s > b) {
            best = Some((r, s));
        }
        if s == DEFINITIVE {
            break;
        }
    }
    best.filter(|(_, s)| *s >= min_score)
}

/// Search queries for a track, most specific first.
pub fn search_queries(track: &Track) -> Vec<String> {
    let title = clean_search_title(&track.clean_title);
    let mut q = vec![];
    if !track.mb_track_id.is_empty() {
        q.push(track.mb_track_id.clone());
    }
    if !track.main_artist.trim().is_empty() {
        q.push(format!("{title} {}", track.main_artist.trim()));
    }
    q.push(title.clone());
    // Search matches every word, so "Hukum - Thalaivar Alappara" misses a library title "Hukum".
    if let Some((base, _)) = title.split_once(" - ") {
        if !base.trim().is_empty() {
            q.push(base.trim().to_string());
        }
    }
    q
}

/// Looks up every track not yet known to be in the library and records `present` / `library_id` on matches.
pub fn check_tracks(server: &Subsonic, tracks: &mut [Track], min_score: i32, ctx: &Ctx) -> Result<()> {
    for t in tracks.iter_mut().filter(|t| !t.present) {
        ctx.check()?;
        for q in search_queries(t) {
            let results = server.search_songs(&q, 20)?;
            if let Some((hit, score)) = best_match(t, &results, min_score) {
                t.library_id = hit.id.clone();
                t.present = true;
                ctx.debug(format!("matched \"{}\" to \"{}\" by {} (score {score}, query \"{q}\")", t.title, hit.title, hit.artist));
                break;
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn track() -> Track {
        Track {
            title: "Hukum".into(),
            clean_title: "Hukum".into(),
            main_artist: "Anirudh Ravichander".into(),
            album: "Jailer".into(),
            duration_ms: 197_000,
            mb_track_id: "mb-1".into(),
            ..Default::default()
        }
    }
    fn result(id: &str, title: &str, artist: &str, album: &str, duration: u32) -> SearchResult {
        SearchResult { id: id.into(), title: title.into(), artist: artist.into(), album: album.into(), duration, ..Default::default() }
    }

    #[test]
    fn mbid_is_definitive() {
        let mut r = result("1", "Something else", "X", "Y", 10);
        r.mbid = "mb-1".into();
        assert_eq!(best_match(&track(), &[r], 60).unwrap().1, 1000);
    }

    #[test]
    fn exact_title_artist_album_duration_scores_high() {
        let r = result("1", "Hukum", "Anirudh Ravichander", "Jailer", 198);
        assert_eq!(best_match(&track(), &[r], 60).unwrap().1, 45 + 20 + 30 + 10);
    }

    #[test]
    fn dubbed_versions_do_not_match() {
        let mut t = track();
        t.clean_title = "Hukum (Tamil)".into();
        let r = result("1", "Hukum", "Anirudh Ravichander", "Jailer (Telugu)", 197);
        assert!(best_match(&t, &[r], 60).is_none());
    }

    #[test]
    fn same_title_other_artist_is_not_enough() {
        let r = result("1", "Hukum", "Someone Else", "Other", 400);
        assert!(best_match(&track(), &[r], 60).is_none()); // 45 - 10 duration penalty
    }

    #[test]
    fn downloaded_file_path_breaks_ties() {
        let mut t = track();
        t.file = "Hukum-Anirudh.mp3".into();
        let mut r = result("1", "Hukum", "Anirudh Ravichander", "", 0);
        r.path = "Explo/Weekly/Hukum-Anirudh.mp3".into();
        assert_eq!(best_match(&t, &[r], 60).unwrap().1, 45 + 30 + 50);
    }

    #[test]
    fn empty_artist_is_not_a_partial_match() {
        let r = result("1", "Hukum", "", "", 0);
        assert_eq!(best_match(&track(), &[r], 0).unwrap().1, 45);
    }

    #[test]
    fn builds_queries_most_specific_first() {
        let mut t = track();
        t.clean_title = "Hukum - Thalaivar Alappara".into();
        let q = search_queries(&t);
        assert_eq!(q, vec!["mb-1", "Hukum - Thalaivar Alappara Anirudh Ravichander", "Hukum - Thalaivar Alappara", "Hukum"]);
    }
}
