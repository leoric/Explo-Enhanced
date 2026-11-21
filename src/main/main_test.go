package main

import (
	"os"
	"path/filepath"
	"testing"

	"explo/src/web/backend/playlist"
)

// A run reads the imported playlist's cache and writes it back: the kept-video facts used for
// library matching must survive both, or the next run silently loses them.
func TestCacheKeepsVideoFacts(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0755); err != nil {
		t.Fatal(err)
	}
	raw := `{"tracks":[{"title":"Kovai Gethu Anthem","artist":"The Times Of India","videoId":"abc",` +
		`"videoTitle":"Kovai Gethu Anthem | Hiphop Tamizha","videoDurationMs":199000,"nameIsSong":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "cache", "custom-x.json"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		tracks, _, err := loadCustomTracks(dir, "custom-x")
		if err != nil {
			t.Fatal(err)
		}
		tr := tracks[0]
		if tr.VideoTitle != "Kovai Gethu Anthem | Hiphop Tamizha" || tr.VideoDuration != 199000 || !tr.NameIsSong {
			t.Fatalf("run %d: video facts lost: %+v", run, tr)
		}
		playlist.WritePlaylistCache(dir, "custom-x", tracks, nil)
	}
}
