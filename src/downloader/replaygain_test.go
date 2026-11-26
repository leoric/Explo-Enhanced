package downloader

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	ffmpeg "github.com/u2takey/ffmpeg-go"
)

const ebur128Output = `[Parsed_ebur128_0 @ 0x1] Summary:

  Integrated loudness:
    I:         -22.2 LUFS
    Threshold: -32.2 LUFS

  Loudness range:
    LRA:        20.0 LU
    Threshold: -42.2 LUFS
    LRA low:   -42.2 LUFS
    LRA high:  -22.2 LUFS

  True peak:
    Peak:       -6.0 dBFS
`

func TestParseEBUR128Summary(t *testing.T) {
	gain, peak, err := parseEBUR128Summary("Input #0, mp3 ...\n" + ebur128Output)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(gain-4.2) > 1e-9 {
		t.Errorf("gain = %v, want 4.2", gain)
	}
	if math.Abs(peak-0.501187) > 1e-6 {
		t.Errorf("peak = %v, want 0.501187", peak)
	}

	for name, out := range map[string]string{
		"no summary": "Input #0, mp3 ...",
		"silence":    strings.Replace(strings.Replace(ebur128Output, "-22.2 LUFS\n    Threshold: -32.2", "-70.0 LUFS\n    Threshold: -70.0", 1), "-6.0 dBFS", "-inf dBFS", 1),
	} {
		if _, _, err := parseEBUR128Summary(out); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

// makeTone writes a 3 s sine tone at the given volume (dB); name's extension picks the format
func makeTone(t *testing.T, dir, name string, volumeDB float64) string {
	t.Helper()
	p := filepath.Join(dir, name)
	runFFmpeg(t, "-f", "lavfi", "-i", "sine=frequency=1000:duration=3", "-af", "volume="+strconv.FormatFloat(volumeDB, 'f', 1, 64)+"dB", p)
	return p
}

func TestMeasureReplayGain(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	quiet, err1 := measureGain(makeTone(t, dir, "quiet.flac", -6))
	loud, err2 := measureGain(makeTone(t, dir, "loud.flac", 0))
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	// 6 dB louder audio needs 6 dB less gain
	if d := quiet - loud; math.Abs(d-6) > 0.2 {
		t.Fatalf("gain difference = %.2f dB, want 6", d)
	}
}

func measureGain(file string) (float64, error) {
	gain, _, err := measureReplayGain(file)
	return gain, err
}

// TestReplayGainTagsReplaceExisting checks the written tags: track values set, stale album values removed
func TestReplayGainTagsReplaceExisting(t *testing.T) {
	requireFFmpeg(t)
	for _, ext := range []string{"mp3", "flac"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			src := makeTone(t, dir, "src."+ext, 0)
			file := filepath.Join(dir, "track."+ext)
			runFFmpeg(t, "-i", src, "-c", "copy", "-metadata", "replaygain_track_gain=+9.00 dB",
				"-metadata", "REPLAYGAIN_ALBUM_GAIN=-3.00 dB", "-metadata", "REPLAYGAIN_ALBUM_PEAK=0.5", file)

			tags := replayGainTags(file)
			if len(tags) == 0 {
				t.Fatal("no ReplayGain tags")
			}
			if err := overwriteMetadata(tags, "", file); err != nil {
				t.Fatal(err)
			}

			got := formatTags(t, file)
			gain := got["replaygain_track_gain"]
			if gain == "" || gain == "+9.00 dB" || !strings.HasSuffix(gain, " dB") {
				t.Errorf("track gain = %q, want a measured value", gain)
			}
			if got["replaygain_track_peak"] == "" {
				t.Error("track peak missing")
			}
			for _, k := range []string{"replaygain_album_gain", "replaygain_album_peak"} {
				if v, ok := got[k]; ok {
					t.Errorf("%s = %q, want removed", k, v)
				}
			}
		})
	}
}

// formatTags returns file's container tags with lowercased keys
func formatTags(t *testing.T, file string) map[string]string {
	t.Helper()
	out, err := ffmpeg.ProbeWithTimeout(file, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal([]byte(out), &probe); err != nil {
		t.Fatal(err)
	}
	tags := make(map[string]string, len(probe.Format.Tags))
	for k, v := range probe.Format.Tags {
		tags[strings.ToLower(k)] = v
	}
	return tags
}
