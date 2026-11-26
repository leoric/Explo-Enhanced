package downloader

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// replayGainReference is the ReplayGain 2.0 reference loudness (89 dB in beets' targetlevel terms)
const replayGainReference = -18.0 // LUFS

// formats whose ffmpeg muxers store the REPLAYGAIN_* tags players read (ID3 TXXX, Vorbis comments)
var replayGainFormats = map[string]bool{"mp3": true, "flac": true, "ogg": true}

var (
	integratedRe = regexp.MustCompile(`I:\s*(-?[0-9.]+) LUFS`)
	truePeakRe   = regexp.MustCompile(`Peak:\s*(-?[0-9.]+) dBFS`)
)

// replayGainTags measures file's loudness and returns ffmpeg metadata entries that set its track gain and peak,
// and remove album values that don't apply to a single downloaded track. Returns nil if file can't be measured.
func replayGainTags(file string) []string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))
	if !replayGainFormats[ext] {
		slog.Debug("ReplayGain tags not supported for format", "file", file)
		return nil
	}
	gain, peak, err := measureReplayGain(file)
	if err != nil {
		slog.Warn("could not measure ReplayGain, leaving the file's ReplayGain tags as they are", "file", file, "context", err.Error())
		return nil
	}
	slog.Debug("measured ReplayGain", "file", file, "gain", gain, "peak", peak)
	return []string{
		fmt.Sprintf("REPLAYGAIN_TRACK_GAIN=%.2f dB", gain),
		fmt.Sprintf("REPLAYGAIN_TRACK_PEAK=%.6f", peak),
		"REPLAYGAIN_ALBUM_GAIN=", // empty values delete the tag
		"REPLAYGAIN_ALBUM_PEAK=",
	}
}

// measureReplayGain decodes file with ffmpeg's EBU R128 filter and returns the ReplayGain 2.0 track gain (dB)
// and the linear true peak
func measureReplayGain(file string) (gain, peak float64, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-nostats", "-i", file,
		"-map", "0:a:0", "-af", "ebur128=peak=true:framelog=quiet", "-f", "null", "-")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, 0, fmt.Errorf("ffmpeg loudness scan failed: %w: %s", err, lastLine(stderr.String()))
	}
	return parseEBUR128Summary(stderr.String())
}

// parseEBUR128Summary reads integrated loudness and true peak from the summary ebur128 logs at the end
func parseEBUR128Summary(out string) (gain, peak float64, err error) {
	_, summary, ok := strings.Cut(out, "Summary:")
	if !ok {
		return 0, 0, fmt.Errorf("no ebur128 summary in ffmpeg output")
	}
	i := integratedRe.FindStringSubmatch(summary)
	p := truePeakRe.FindStringSubmatch(summary)
	if i == nil || p == nil {
		return 0, 0, fmt.Errorf("no loudness or peak in ebur128 summary (silent track?)")
	}
	loudness, err := strconv.ParseFloat(i[1], 64)
	if err != nil {
		return 0, 0, err
	}
	peakDB, err := strconv.ParseFloat(p[1], 64)
	if err != nil {
		return 0, 0, err
	}
	if loudness <= -70 { // ebur128's absolute gate: nothing above it, i.e. silence
		return 0, 0, fmt.Errorf("track is silent")
	}
	return replayGainReference - loudness, math.Pow(10, peakDB/20), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
