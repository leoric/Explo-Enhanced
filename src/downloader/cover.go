package downloader

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"explo/src/models"

	ffmpeg "github.com/u2takey/ffmpeg-go"
)

var errNoCover = errors.New("track has no cover art")

// formats whose ffmpeg muxers can store an attached cover picture
var coverFormats = map[string]bool{"mp3": true, "flac": true, "m4a": true}

// coverToEmbed returns the path of a cover image to embed into file, or "" if none should be embedded.
// Files that already have a cover keep it unless replace is set. The returned func removes temp files.
func coverToEmbed(client *http.Client, file string, track *models.Track, replace bool) (string, func()) {
	noop := func() {}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))
	if !coverFormats[ext] {
		slog.Debug("cover embedding not supported for format", "file", file)
		return "", noop
	}

	if !replace {
		hasCover, err := hasEmbeddedCover(file)
		if err != nil {
			slog.Warn("could not check for embedded cover, leaving it as is", "file", file, "context", err.Error())
			return "", noop
		}
		if hasCover {
			slog.Debug("file already has a cover", "file", file)
			return "", noop
		}
	}

	coverPath, cleanup, err := fetchCover(client, track)
	if errors.Is(err, errNoCover) {
		slog.Debug("no cover art to embed", "title", track.CleanTitle, "artist", track.MainArtist)
		return "", noop
	}
	if err != nil {
		slog.Warn("failed to fetch cover art", "title", track.CleanTitle, "artist", track.MainArtist, "context", err.Error())
		return "", noop
	}
	return coverPath, cleanup
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

// fetchCover downloads the track's remote cover (sized by COVER_ART_SIZE) to a temp file.
// It falls back to an already cached cover file when there's no remote URL (custom playlists).
func fetchCover(client *http.Client, track *models.Track) (string, func(), error) {
	noop := func() {}

	if strings.HasPrefix(track.CoverURL, "http://") || strings.HasPrefix(track.CoverURL, "https://") {
		resp, err := client.Get(track.CoverURL) //nolint:noctx
		if err != nil {
			return "", noop, err
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				slog.Debug("failed to close cover response", "context", err.Error())
			}
		}()
		if resp.StatusCode != http.StatusOK {
			return "", noop, fmt.Errorf("got %d from %s", resp.StatusCode, track.CoverURL)
		}

		f, err := os.CreateTemp("", "explo-cover-*.jpg")
		if err != nil {
			return "", noop, err
		}
		cleanup := func() {
			if err := os.Remove(f.Name()); err != nil {
				slog.Debug("failed to remove temp cover", "context", err.Error())
			}
		}
		_, err = io.Copy(f, resp.Body)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			cleanup()
			return "", noop, fmt.Errorf("failed to save cover: %w", err)
		}
		return f.Name(), cleanup, nil
	}

	if track.CoverPath != "" {
		if info, err := os.Stat(track.CoverPath); err == nil && info.Size() > 0 {
			return track.CoverPath, noop, nil
		}
	}
	return "", noop, errNoCover
}
