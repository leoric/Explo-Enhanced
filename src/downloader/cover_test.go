package downloader

import (

	"testing"

	"github.com/wader/goutubedl"
)

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

