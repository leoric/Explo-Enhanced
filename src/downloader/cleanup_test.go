package downloader

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"explo/src/models"
	"explo/src/util"
)

const (
	keeperMBID  = "11111111-2222-3333-4444-555555555555"
	currentMBID = "22222222-3333-4444-5555-666666666666"
	otherMBID   = "66666666-7777-8888-9999-000000000000"
)

// withTags makes rules read tags from a map keyed by file name instead of probing files
func withTags(r *CleanupRules, tags map[string]fileTags) *CleanupRules {
	r.readTags = func(path string) (fileTags, error) {
		t, ok := tags[filepath.Base(path)]
		if !ok {
			return fileTags{}, errors.New("unreadable")
		}
		return t, nil
	}
	return r
}

var testTags = map[string]fileTags{
	"keeper.flac":         {RecordingID: keeperMBID, Title: "Renamed", Artist: "Someone"},
	"keeper-title.flac":   {Title: "Quiet Tune", Artist: "Test Band; Guest Singer"},
	"current-keeper.flac": {RecordingID: currentMBID, Title: "Starred Again", Artist: "Test Band"},
	"current.flac":        {RecordingID: otherMBID, Title: "Common Song", Artist: "Test Band"},
	"current-title.mp3":   {Title: "Fresh Song (feat. Guest Singer)", Artist: "Test Band; Guest Singer"},
	"other.flac":          {RecordingID: "33333333-0000-0000-0000-000000000000", Title: "Other Song", Artist: "Test Band"},
	"other-artist.mp3":    {Title: "Quiet Tune", Artist: "Another Band"},
	"no-artist.mp3":       {Title: "Quiet Tune"},
}

func testCurrentTracks() []*models.Track {
	return []*models.Track{
		{MusicBrainzTrackID: strings.ToUpper(otherMBID), CleanTitle: "Common Song", MainArtist: "Test Band"},
		{CleanTitle: "Fresh Song", MainArtist: "Test Band"},
		{MusicBrainzTrackID: currentMBID, CleanTitle: "Starred Again", MainArtist: "Test Band"},
	}
}

func testKeeperSongs() []models.LibrarySong {
	return []models.LibrarySong{
		{RecordingID: strings.ToUpper(keeperMBID), Title: "Common Name", Artist: "Test Band"},
		{Title: "Quiet Tune (feat. Guest Singer)", Artist: "Test Band"},
		{RecordingID: currentMBID, Title: "Starred Again", Artist: "Test Band"},
	}
}

func TestCleanupRulesAction(t *testing.T) {
	cases := map[string]struct{ withKeepers, withoutKeepers fileAction }{
		"keeper.flac":         {actionMove, actionDelete}, // recording ID, case-insensitive
		"keeper-title.flac":   {actionMove, actionDelete}, // title + main artist fallback
		"current-keeper.flac": {actionMove, actionLeave},  // keepers move even when in the current run
		"current.flac":        {actionLeave, actionLeave}, // recording ID
		"current-title.mp3":   {actionLeave, actionLeave}, // title + main artist fallback
		"other.flac":          {actionDelete, actionDelete},
		"other-artist.mp3":    {actionDelete, actionDelete}, // same title, different artist
		"no-artist.mp3":       {actionDelete, actionDelete},
	}

	keeping := withTags(NewCleanupRules(testCurrentTracks()).WithKeepers(testKeeperSongs()), testTags)
	plain := withTags(NewCleanupRules(testCurrentTracks()), testTags)
	for file, want := range cases {
		for _, tc := range []struct {
			rules *CleanupRules
			want  fileAction
		}{{keeping, want.withKeepers}, {plain, want.withoutKeepers}} {
			got, err := tc.rules.action(file)
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			if got != tc.want {
				t.Errorf("%s (keepers=%v): action=%d, want %d", file, tc.rules.keepers != nil, got, tc.want)
			}
		}
	}
	if got, err := keeping.action("unreadable.flac"); err == nil || got != actionLeave {
		t.Errorf("unreadable file: action=%d err=%v, want leave and an error", got, err)
	}
}

func TestCleanPlaylistDirActions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Weekly-Exploration")
	keepersDir := filepath.Join(root, "Keepers")

	keeper := filepath.Join("Test Band", "Test Album", "keeper.flac")
	taken := filepath.Join("Test Band", "Test Album", "keeper-title.flac")
	current := filepath.Join("Test Band", "Current Album", "current.flac")
	other := filepath.Join("Test Band", "Other Album", "other.flac")
	unreadable := filepath.Join("Mystery", "unreadable.mp3")
	for _, f := range []string{keeper, taken, current, other, unreadable} {
		writeFile(t, filepath.Join(dir, f))
	}
	writeFile(t, filepath.Join(keepersDir, taken)) // already moved by an earlier run

	rules := withTags(NewCleanupRules(testCurrentTracks()).WithKeepers(testKeeperSongs()), testTags)
	res, err := cleanPlaylistDir(root, dir, testExts, &cleanupPlan{keepersDir: keepersDir, action: rules.action})
	if err != nil {
		t.Fatal(err)
	}
	if res != (cleanResult{deleted: 1, moved: 1, left: 1, removedDirs: 1, kept: 2}) {
		t.Fatalf("got %+v, want 1 deleted, 1 moved, 1 left, 1 removed dir, 2 kept", res)
	}

	for _, p := range []string{
		filepath.Join(keepersDir, keeper),
		filepath.Join(keepersDir, taken),
		filepath.Join(dir, taken),      // target exists, never overwritten
		filepath.Join(dir, current),    // in the current run
		filepath.Join(dir, unreadable), // tags unreadable
	} {
		if !exists(p) {
			t.Errorf("%s should exist", p)
		}
	}
	for _, p := range []string{
		filepath.Join(dir, keeper),
		filepath.Join(dir, "Test Band", "Other Album"),
	} {
		if exists(p) {
			t.Errorf("%s should be gone", p)
		}
	}
}

func TestCleanPlaylistDirMoveWithoutKeepersDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Weekly-Exploration")
	writeFile(t, filepath.Join(dir, "a.flac"))
	moveAll := func(string) (fileAction, error) { return actionMove, nil }

	res, err := cleanPlaylistDir(root, dir, testExts, &cleanupPlan{action: moveAll})
	if err != nil {
		t.Fatal(err)
	}
	if res != (cleanResult{kept: 1}) || !exists(filepath.Join(dir, "a.flac")) {
		t.Fatalf("got %+v, want the file kept in place", res)
	}
}

func TestCleanPlaylistDirKeepersDirGuard(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Weekly-Exploration")
	writeFile(t, filepath.Join(dir, "a.flac"))
	moveAll := func(string) (fileAction, error) { return actionMove, nil }

	for name, keepersDir := range map[string]string{
		"same as dir":   dir,
		"inside of dir": filepath.Join(dir, "Keepers"),
	} {
		t.Run(name, func(t *testing.T) {
			res, err := cleanPlaylistDir(root, dir, testExts, &cleanupPlan{keepersDir: keepersDir, action: moveAll})
			if err == nil || res != (cleanResult{}) {
				t.Fatalf("got %+v, err=%v, want error and nothing touched", res, err)
			}
		})
	}
	if !exists(filepath.Join(dir, "a.flac")) {
		t.Fatal("guard let files be touched")
	}
}

// TestProbeTagsReadsExploTags checks that the tags Explo writes on download are read back
func TestProbeTagsReadsExploTags(t *testing.T) {
	requireFFmpeg(t)
	for _, ext := range []string{"mp3", "flac", "opus"} {
		t.Run(ext, func(t *testing.T) {
			file := makeAudio(t, t.TempDir(), "track."+ext)
			track := models.Track{
				File:               file,
				Title:              "Common Song",
				Artist:             "Test Band",
				Artists:            []string{"Test Band", "Guest Singer"},
				MusicBrainzTrackID: keeperMBID,
			}
			if err := overwriteMetadata(util.BuildffmpegMetadata(track), "", file); err != nil {
				t.Fatal(err)
			}

			got, err := probeTags(file)
			if err != nil {
				t.Fatal(err)
			}
			want := fileTags{RecordingID: keeperMBID, Title: "Common Song", Artist: "Test Band; Guest Singer"}
			if got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
}
