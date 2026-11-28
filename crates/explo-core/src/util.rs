//! Text helpers: title normalisation, file names, playlist names.

use chrono::{Datelike, NaiveDate};
use regex::Regex;
use std::sync::LazyLock;

use crate::config::NameFormat;

macro_rules! re {
    ($name:ident, $pat:expr) => {
        static $name: LazyLock<Regex> = LazyLock::new(|| Regex::new($pat).expect("valid regex"));
    };
}

re!(FILENAME_RE, r"[^\p{L}\d._,\-]+");
re!(ALNUM_RE, r"[^\p{L}\d]+");
re!(FEAT_TAIL_RE, r"(?i)\s*[(\[{]\s*(?:feat\.?|featuring|ft\.?|with)\s[^)\]}]*[)\]}]\s*$");
re!(REMASTER_TAIL_RE, r"(?i)\s*(?:[-–—]|\(|\[)\s*(?:\d{4}\s+|digital\s+)?remaster(?:ed)?\)?\]?\s*$");
// Indian film-song tags: (From "Film"), [Original Motion Picture Soundtrack], (Tamil), - Telugu
re!(FROM_TAG_RE, r#"(?i)\s*[(\[]\s*from\s*[-:]?\s*(?:"[^"]*"|“[^”]*”|[^)\]]*)\s*[)\]]"#);
re!(SOUNDTRACK_RE, r"(?i)\s*[(\[]\s*original\s+(?:motion\s+picture\s+soundtrack|background\s+score|score|soundtrack)\s*[)\]]");
re!(
    LANGUAGE_TAG_RE,
    r"(?i)\s*(?:[(\[]\s*(tamil|telugu|kannada|malayalam|hindi)\s*[)\]]|[-–]\s*(tamil|telugu|kannada|malayalam|hindi)\b)"
);

/// Removes feat./remaster tails and film-song tags that a music library usually doesn't have.
fn strip_tags(s: &str) -> String {
    let s = FROM_TAG_RE.replace_all(s, "");
    let s = SOUNDTRACK_RE.replace_all(&s, "");
    let s = LANGUAGE_TAG_RE.replace_all(&s, "");
    let s = FEAT_TAIL_RE.replace_all(&s, "");
    REMASTER_TAIL_RE.replace_all(&s, "").into_owned()
}

/// Title for search queries: tags stripped, still human-readable.
pub fn clean_search_title(s: &str) -> String {
    strip_tags(s).trim().to_string()
}

/// Tags stripped, lower-cased, letters and digits only: for fuzzy title comparison.
pub fn normalize_title(s: &str) -> String {
    ALNUM_RE.replace_all(&strip_tags(s).to_lowercase(), "").into_owned()
}

/// "tamil", "telugu", ... when one of the texts carries a language tag, else "".
pub fn language(texts: &[&str]) -> String {
    for t in texts {
        if let Some(c) = LANGUAGE_TAG_RE.captures(t) {
            if let Some(m) = c.get(1).or_else(|| c.get(2)) {
                return m.as_str().to_lowercase();
            }
        }
    }
    String::new()
}

pub fn contains_fold(s: &str, sub: &str) -> bool {
    s.to_lowercase().contains(&sub.to_lowercase())
}

/// `{title}-{artist}`, unsafe characters replaced, capped at 240 bytes.
pub fn file_stem(title: &str, artist: &str) -> String {
    const MAX_BYTES: usize = 240;
    let t = FILENAME_RE.replace_all(title, "_");
    let a = FILENAME_RE.replace_all(artist, "_");
    let mut out = format!("{t}-{a}");
    while out.len() > MAX_BYTES {
        out.pop();
    }
    out
}

/// Folder-name-safe version of a playlist name.
pub fn dir_safe(s: &str) -> String {
    let cleaned: String = s
        .trim()
        .chars()
        .map(|c| if matches!(c, '/' | '\\' | ':') { '-' } else { c })
        .filter(|c| !matches!(c, '*' | '?' | '"' | '<' | '>' | '|'))
        .collect();
    match cleaned.as_str() {
        "" | "." | ".." | ". ." => "_".to_string(),
        _ => cleaned,
    }
}

/// "weekly-exploration" -> "Weekly-Exploration"
pub fn title_case_kind(kind: &str) -> String {
    let mut out = String::new();
    let mut start = true;
    for c in kind.chars() {
        if c.is_alphabetic() {
            if start {
                out.extend(c.to_uppercase());
            } else {
                out.extend(c.to_lowercase());
            }
            start = false;
        } else {
            out.push(c);
            start = true;
        }
    }
    out
}

/// Playlist name for a run. `base` overrides the title-cased kind when non-empty.
pub fn playlist_name(kind: &str, base: &str, format: NameFormat, replace: bool, today: NaiveDate) -> String {
    let base = if base.trim().is_empty() { title_case_kind(kind) } else { base.trim().to_string() };
    if replace {
        return base;
    }
    if format == NameFormat::Date {
        return format!("{base}-{}", today.format("%Y-%m-%d"));
    }
    if kind == "daily-jams" {
        return format!("{base}-{}-Day{}", today.year(), today.ordinal());
    }
    let w = today.iso_week();
    format!("{base}-{}-Week{}", w.year(), w.week())
}

/// Does any filter word appear in the candidate title without appearing in the wanted track?
pub fn hits_filter(track_title: &str, track_artist: &str, candidate_title: &str, filters: &[String]) -> bool {
    let (t, a, c) = (track_title.to_lowercase(), track_artist.to_lowercase(), candidate_title.to_lowercase());
    filters.iter().map(|k| k.trim().to_lowercase()).filter(|k| !k.is_empty()).any(|k| !t.contains(&k) && !a.contains(&k) && c.contains(&k))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalizes_titles() {
        assert_eq!(normalize_title("Hukum (From \"Jailer\") (Tamil)"), "hukum");
        assert_eq!(normalize_title("Song (feat. Someone)"), "song");
        assert_eq!(normalize_title("Song - 2011 Remaster"), "song");
        assert_eq!(normalize_title("Don't Stop!"), "dontstop");
        assert_eq!(clean_search_title("Powerhouse (From \"Coolie\") (Tamil)"), "Powerhouse");
    }

    #[test]
    fn detects_language() {
        assert_eq!(language(&["Hukum - Telugu"]), "telugu");
        assert_eq!(language(&["KGF Chapter 1 (Tamil)"]), "tamil");
        assert_eq!(language(&["Hukum", "Album"]), "");
    }

    #[test]
    fn names_files() {
        assert_eq!(file_stem("AC/DC: Thunder?", "Some Band"), "AC_DC_Thunder_-Some_Band");
        let long = "x".repeat(500);
        assert!(file_stem(&long, &long).len() <= 240);
    }

    #[test]
    fn names_playlists() {
        let d = NaiveDate::from_ymd_opt(2026, 1, 31).unwrap();
        assert_eq!(playlist_name("weekly-exploration", "", NameFormat::Week, true, d), "Weekly-Exploration");
        assert_eq!(playlist_name("weekly-exploration", "", NameFormat::Week, false, d), "Weekly-Exploration-2026-Week5");
        assert_eq!(playlist_name("weekly-jams", "Jams", NameFormat::Date, false, d), "Jams-2026-01-31");
        assert_eq!(playlist_name("daily-jams", "", NameFormat::Week, false, d), "Daily-Jams-2026-Day31");
        // ISO week year differs from calendar year around New Year
        let d = NaiveDate::from_ymd_opt(2027, 1, 1).unwrap();
        assert_eq!(playlist_name("weekly-jams", "", NameFormat::Week, false, d), "Weekly-Jams-2026-Week53");
    }

    #[test]
    fn filter_words() {
        let f = vec!["live".to_string(), "remix".to_string()];
        assert!(hits_filter("Song", "Band", "Song (Live at X)", &f));
        assert!(!hits_filter("Song (Live)", "Band", "Song (Live at X)", &f));
        assert!(!hits_filter("Song", "Band", "Song (Official Audio)", &f));
    }

    #[test]
    fn safe_dir_names() {
        assert_eq!(dir_safe("A/B: C?"), "A-B- C");
        assert_eq!(dir_safe(".."), "_");
    }
}
