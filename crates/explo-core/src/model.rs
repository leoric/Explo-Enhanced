//! Data shared across the pipeline.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct Track {
    pub title: String,
    /// Title as ListenBrainz returns it (without "(feat. ...)" additions).
    pub clean_title: String,
    pub artist: String,
    pub main_artist: String,
    pub album: String,
    pub album_artist: String,
    pub duration_ms: u32,
    pub cover_url: String,
    pub mb_track_id: String,
    pub mb_album_id: String,
    pub mb_artist_id: String,

    /// File name (no directory) once downloaded.
    pub file: String,
    /// Song id on the music server, once the track is in the library.
    pub library_id: String,
    pub present: bool,
    pub video_id: String,
}

/// A song as returned by the music server's search.
#[derive(Debug, Clone, Default)]
pub struct SearchResult {
    pub id: String,
    pub title: String,
    pub album: String,
    pub artist: String,
    pub path: String,
    /// Seconds.
    pub duration: u32,
    pub mbid: String,
}
