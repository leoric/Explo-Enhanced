package downloader

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"explo/src/models"
	"explo/src/util"

	ffmpeg "github.com/u2takey/ffmpeg-go"
)

var errNoCover = errors.New("track has no cover art")

// formats whose ffmpeg muxers can store an attached cover picture
var coverFormats = map[string]bool{"mp3": true, "flac": true, "m4a": true}

// cached covers above this size are full-size originals (e.g. cached before COVER_ART_SIZE was
// validated) and aren't embedded. Cover Art Archive thumbnails stay well below it, even at 1200px.
const maxEmbedCoverBytes = 4 << 20

// coverToEmbed returns the path of a cover image to embed into file, or "" if none should be embedded.
// Files that already have a cover keep it unless replace is set.
func coverToEmbed(file string, track *models.Track, coversDir string, replace bool) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))
	if !coverFormats[ext] {
		slog.Debug("cover embedding not supported for format", "file", file)
		return ""
	}

	if !replace {
		hasCover, err := hasEmbeddedCover(file)
		if err != nil {
			slog.Warn("could not check for embedded cover, leaving it as is", "file", file, "context", err.Error())
			return ""
		}
		if hasCover {
			slog.Debug("file already has a cover", "file", file)
			return ""
		}
	}

	coverPath, err := cachedCover(track, coversDir)
	if errors.Is(err, errNoCover) {
		slog.Debug("no cover art to embed", "title", track.CleanTitle, "artist", track.MainArtist)
		return ""
	}
	if err != nil {
		slog.Warn("not embedding cover art", "title", track.CleanTitle, "artist", track.MainArtist, "context", err.Error())
		return ""
	}
	return coverPath
}

// hasEmbeddedCover reports whether file contains a picture stream
func hasEmbeddedCover(file string) (bool, error) {
	out, err := ffmpeg.ProbeWithTimeout(file, 30*time.Second, ffmpeg.KwArgs{"select_streams": "v"})
	if err != nil {
		return false, err
	}
	var probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}
	if err := json.Unmarshal([]byte(out), &probe); err != nil {
		return false, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}
	return len(probe.Streams) > 0, nil
}

// cachedCover returns the track's cover from the shared covers cache (the same file the web UI uses),
// downloading it into the cache if it isn't there yet
func cachedCover(track *models.Track, coversDir string) (string, error) {
	if !fileExists(track.CoverPath) {
		if !strings.HasPrefix(track.CoverURL, "http://") && !strings.HasPrefix(track.CoverURL, "https://") {
			return "", errNoCover
		}
		if coversDir == "" {
			return "", fmt.Errorf("no covers directory configured")
		}
		if err := os.MkdirAll(coversDir, 0755); err != nil {
			return "", err
		}
		_, track.CoverPath = util.DownloadCover(track.CoverURL, coversDir)
		if !fileExists(track.CoverPath) {
			return "", fmt.Errorf("failed to download %s", track.CoverURL)
		}
	}

	info, err := os.Stat(track.CoverPath)
	if err != nil {
		return "", err
	}
	if info.Size() > maxEmbedCoverBytes {
		return "", fmt.Errorf("cached cover %s is %d MB, likely a full-size original; delete it from the covers cache to fetch it at COVER_ART_SIZE", track.CoverPath, info.Size()>>20)
	}
	return track.CoverPath, nil
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}
