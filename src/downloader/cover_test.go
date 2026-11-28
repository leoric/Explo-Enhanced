package downloader

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"explo/src/models"

	"github.com/wader/goutubedl"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
}

func runFFmpeg(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", append([]string{"-y", "-loglevel", "error"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// makeAudio writes a short sine tone; name's extension picks the format
func makeAudio(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	runFFmpeg(t, "-f", "lavfi", "-i", "sine=duration=0.2", p)
	return p
}

// makeCover writes a solid-color JPEG of the given size, e.g. "64x64"
func makeCover(t *testing.T, dir, name, size string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	runFFmpeg(t, "-f", "lavfi", "-i", "color=c=red:s="+size, "-frames:v", "1", p)
	return p
}

type probedStream struct {
	CodecType string `json:"codec_type"`
	Width     int    `json:"width"`
}

func probeStreams(t *testing.T, file string) []probedStream {
	t.Helper()
	out, err := ffmpeg.ProbeWithTimeout(file, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Streams []probedStream `json:"streams"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	return p.Streams
}

// coverWidths returns the widths of all picture streams, and fails if the audio stream is gone
func coverWidths(t *testing.T, file string) []int {
	t.Helper()
	var widths []int
	audio := false
	for _, s := range probeStreams(t, file) {
		switch s.CodecType {
		case "video":
			widths = append(widths, s.Width)
		case "audio":
			audio = true
		}
	}
	if !audio {
		t.Fatalf("%s lost its audio stream", file)
	}
	return widths
}

func TestEmbedAndReplaceCover(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	small := makeCover(t, dir, "small.jpg", "32x32")
	large := makeCover(t, dir, "large.jpg", "64x64")

	for _, name := range []string{"track.flac", "track.mp3", "track.m4a"} {
		t.Run(name, func(t *testing.T) {
			file := makeAudio(t, dir, name)

			if has, err := hasEmbeddedCover(file); err != nil || has {
				t.Fatalf("fresh file: hasEmbeddedCover=%v, %v", has, err)
			}

			if err := overwriteMetadata(nil, small, file); err != nil {
				t.Fatal(err)
			}
			if got := coverWidths(t, file); len(got) != 1 || got[0] != 32 {
				t.Fatalf("after embed: cover widths %v, want [32]", got)
			}
			if has, err := hasEmbeddedCover(file); err != nil || !has {
				t.Fatalf("after embed: hasEmbeddedCover=%v, %v", has, err)
			}

			// tags only: existing cover is kept
			if err := overwriteMetadata([]string{"title=Song"}, "", file); err != nil {
				t.Fatal(err)
			}
			if got := coverWidths(t, file); len(got) != 1 || got[0] != 32 {
				t.Fatalf("after tag rewrite: cover widths %v, want [32]", got)
			}

			// replace: old cover is dropped, not duplicated
			if err := overwriteMetadata([]string{"title=Song"}, large, file); err != nil {
				t.Fatal(err)
			}
			if got := coverWidths(t, file); len(got) != 1 || got[0] != 64 {
				t.Fatalf("after replace: cover widths %v, want [64]", got)
			}
		})
	}
}

func TestCoverToEmbed(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	coversDir := filepath.Join(dir, "covers")
	cover := makeCover(t, dir, "cover.jpg", "16x16")
	data, err := os.ReadFile(cover)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/release/abc/front-500" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	newTrack := func() *models.Track {
		return &models.Track{CleanTitle: "Song", CoverURL: srv.URL + "/release/abc/front-500"}
	}

	bare := makeAudio(t, dir, "bare.flac")
	withCover := makeAudio(t, dir, "covered.flac")
	if err := overwriteMetadata(nil, cover, withCover); err != nil {
		t.Fatal(err)
	}

	// not cached yet: downloaded into the shared cache, named by release like the web UI does
	track := newTrack()
	got := coverToEmbed(bare, track, coversDir, false)
	if want := filepath.Join(coversDir, "abc.jpg"); got != want || track.CoverPath != want {
		t.Fatalf("cover %q, CoverPath %q; want %q", got, track.CoverPath, want)
	}
	if b, err := os.ReadFile(got); err != nil || len(b) != len(data) {
		t.Fatalf("cached cover not written correctly: %v", err)
	}

	// cached: reused without another request
	track = newTrack()
	if got := coverToEmbed(bare, track, coversDir, false); got != filepath.Join(coversDir, "abc.jpg") || requests != 1 {
		t.Fatalf("cover %q after %d requests; want cached file after 1 request", got, requests)
	}

	// existing cover kept unless replacing
	if got := coverToEmbed(withCover, newTrack(), coversDir, false); got != "" {
		t.Fatal("existing cover should be kept without replace")
	}
	if got := coverToEmbed(withCover, newTrack(), coversDir, true); got == "" {
		t.Fatal("expected a cover when replacing")
	}

	// unsupported format, remote error, no cover at all
	if got := coverToEmbed(filepath.Join(dir, "x.opus"), newTrack(), coversDir, true); got != "" {
		t.Fatal("opus should be skipped")
	}
	missing := &models.Track{CoverURL: srv.URL + "/release/missing/front-500"}
	if got := coverToEmbed(bare, missing, coversDir, false); got != "" {
		t.Fatal("404 cover should be skipped")
	}
	if got := coverToEmbed(bare, &models.Track{}, coversDir, false); got != "" {
		t.Fatal("track without cover should be skipped")
	}
}

func TestCachedCover(t *testing.T) {
	dir := t.TempDir()
	cached := filepath.Join(dir, "abc.jpg")
	if err := os.WriteFile(cached, []byte("jpeg"), 0644); err != nil {
		t.Fatal(err)
	}

	// already cached by the playlist run (CoverURL is then the UI's API path)
	got, err := cachedCover(&models.Track{CoverURL: "/api/covers/abc.jpg", CoverPath: cached}, dir)
	if err != nil || got != cached {
		t.Fatalf("got %q, %v; want cached file", got, err)
	}

	// no remote URL to fetch a missing file from
	_, err = cachedCover(&models.Track{CoverURL: "/api/covers/abc.jpg", CoverPath: filepath.Join(dir, "gone.jpg")}, dir)
	if !errors.Is(err, errNoCover) {
		t.Fatalf("err = %v, want errNoCover", err)
	}

	// oversized original in the cache is not embedded
	original := filepath.Join(dir, "orig.jpg")
	f, err := os.Create(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxEmbedCoverBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedCover(&models.Track{CoverPath: original}, dir); err == nil {
		t.Fatal("oversized cover should be rejected")
	}
}

func TestWriteFolderCover(t *testing.T) {
	root := t.TempDir()
	cover := filepath.Join(root, "art.jpg")
	if err := os.WriteFile(cover, []byte("jpeg"), 0644); err != nil {
		t.Fatal(err)
	}
	album := filepath.Join(root, "Sushin Shyam", "Scene")
	if err := os.MkdirAll(album, 0755); err != nil {
		t.Fatal(err)
	}

	writeFolderCover(cover, album, root)
	if b, err := os.ReadFile(filepath.Join(album, "cover.jpg")); err != nil || string(b) != "jpeg" {
		t.Fatalf("album folder should get cover.jpg, got %q %v", b, err)
	}

	writeFolderCover(cover, root, root) // flat download dir: one cover.jpg would cover every song
	if _, err := os.Stat(filepath.Join(root, "cover.jpg")); err == nil {
		t.Fatal("must not write cover.jpg into the flat download dir")
	}
}

func TestCopyAudio(t *testing.T) {
	aac := goutubedl.Format{FormatID: "140", ACodec: "mp4a.40.2", VCodec: "none"}
	opus := goutubedl.Format{FormatID: "251", ACodec: "opus", VCodec: "none"}
	muxed := goutubedl.Format{FormatID: "18", ACodec: "opus", VCodec: "avc1"}
	if copyableCodec("opus") != "opus" || copyableCodec(".M4A") != "mp4a" || copyableCodec("mp3") != "" {
		t.Error("wrong codec for extension")
	}
	if hasAudio([]goutubedl.Format{aac, muxed}, "opus") {
		t.Error("only AAC audio-only formats: an opus target must re-encode")
	}
	if !hasAudio([]goutubedl.Format{aac, opus}, "opus") || !hasAudio([]goutubedl.Format{aac, opus}, "mp4a") {
		t.Error("matching audio-only format available: should copy")
	}
}

func TestNoCoverWithoutAlbum(t *testing.T) {
	withAlbum := models.Track{Album: "Scene", CoverURL: "https://x/art.jpg"}
	noAlbum := models.Track{CoverURL: "https://x/art.jpg"} // a kept video: stays "[Unknown Album]", easy to find
	if !wantsCover(true, withAlbum) {
		t.Error("a song with an album should get its cover")
	}
	if wantsCover(true, noAlbum) {
		t.Error("a song with no album must not get a cover")
	}
	if wantsCover(false, withAlbum) || wantsCover(true, models.Track{Album: "Scene"}) {
		t.Error("no cover when EMBED_COVER_ART is off or there is no cover URL")
	}
}
