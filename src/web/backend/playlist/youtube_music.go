package playlist

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Public YouTube Music playlists only: PL… (user and YouTube Charts playlists),
// OLAK5uy… (albums) and RDCLAK… (YouTube's curated playlists).
// Personal mixes (RDTMAK…, RDMM, LM) need a signed-in account and are not supported yet.
var (
	ytMusicListRe         = regexp.MustCompile(`^https?://(?:music\.|www\.|m\.)?youtube\.com/(?:playlist|watch)\?(?:.*&)?list=([A-Za-z0-9_-]+)`)
	ytMusicPublicPrefixes = []string{"PL", "OLAK5uy", "RDCLAK"}
)

const defaultYTPlaylistLimit = 100

// extractYouTubeMusicID pulls the playlist ID out of a music.youtube.com or youtube.com URL.
func extractYouTubeMusicID(raw string) (string, error) {
	m := ytMusicListRe.FindStringSubmatch(strings.TrimSpace(raw))
	if len(m) < 2 {
		return "", fmt.Errorf("not a valid YouTube Music playlist URL")
	}
	id := strings.TrimPrefix(m[1], "VL")
	for _, prefix := range ytMusicPublicPrefixes {
		if strings.HasPrefix(id, prefix) {
			return id, nil
		}
	}
	return "", fmt.Errorf("only public YouTube Music playlists are supported: personal mixes need a signed-in account")
}

// ytPlaylistLimit reads YOUTUBE_PLAYLIST_LIMIT from the config file, else the container env;
// every non-official-audio track costs one search.
func ytPlaylistLimit(env map[string]string) int {
	v := env["YOUTUBE_PLAYLIST_LIMIT"]
	if v == "" {
		v = os.Getenv("YOUTUBE_PLAYLIST_LIMIT")
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	return defaultYTPlaylistLimit
}

type ytMusicPlaylist struct {
	Name    string `json:"name"`
	Artwork string `json:"artwork"`
	Tracks  []struct {
		Title           string `json:"title"`
		Artist          string `json:"artist"`
		MainArtist      string `json:"mainArtist"`
		Album           string `json:"album"`
		DurationMs      int    `json:"durationMs"`
		VideoID         string `json:"videoId"`
		VideoTitle      string `json:"videoTitle"`
		VideoDurationMs int    `json:"videoDurationMs"`
		NameIsSong      bool   `json:"nameIsSong"`
		CoverURL        string `json:"coverUrl"`
		Source          string `json:"source"` // "song" | "resolved" | "video"
	} `json:"tracks"`
}

// fetchYouTubeMusicPlaylist runs ytmusic_playlist.py (next to search_ytmusic.py), which fetches the
// playlist and swaps music/lyric videos for their official audio when it finds a confident match.
// Returns (playlistName, artworkURL, tracks, error).
func fetchYouTubeMusicPlaylist(pageURL string, limit int) (string, string, []PlaylistTrack, error) {
	id, err := extractYouTubeMusicID(pageURL)
	if err != nil {
		return "", "", nil, err
	}

	out, err := exec.Command("python3", "ytmusic_playlist.py", id, strconv.Itoa(limit)).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			lines := strings.Split(strings.TrimSpace(string(exitErr.Stderr)), "\n")
			return "", "", nil, fmt.Errorf("youtube music: fetch failed: %s", lines[len(lines)-1])
		}
		return "", "", nil, fmt.Errorf("youtube music: fetch failed: %w", err)
	}

	var pl ytMusicPlaylist
	if err := json.Unmarshal(out, &pl); err != nil {
		return "", "", nil, fmt.Errorf("youtube music: failed to parse helper output: %w", err)
	}
	if len(pl.Tracks) == 0 {
		return "", "", nil, fmt.Errorf("youtube music: playlist has no playable tracks")
	}

	tracks := make([]PlaylistTrack, len(pl.Tracks))
	sources := map[string]int{}
	for i, t := range pl.Tracks {
		sources[t.Source]++
		tracks[i] = PlaylistTrack{
			Title:           t.Title,
			Artist:          t.Artist,
			MainArtist:      t.MainArtist,
			Album:           t.Album,
			CoverURL:        t.CoverURL,
			VideoID:         t.VideoID,
			DurationMs:      t.DurationMs,
			VideoTitle:      t.VideoTitle,
			VideoDurationMs: t.VideoDurationMs,
			NameIsSong:      t.NameIsSong,
		}
	}
	slog.Info("youtube music: parsed playlist", "name", pl.Name, "tracks", len(tracks),
		"official_audio", sources["song"], "resolved_from_video", sources["resolved"], "video_only", sources["video"])
	return pl.Name, pl.Artwork, tracks, nil
}
