package downloader

import (
	"os"
	"path/filepath"

	"explo/src/models"
	"testing"

	"github.com/wader/goutubedl"
)

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
