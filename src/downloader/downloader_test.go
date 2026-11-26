package downloader

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

var testExts = []string{"flac", "mp3"}

func TestCleanPlaylistDirNested(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Weekly-Exploration")
	outside := filepath.Join(root, "keep", "target.flac")

	writeFile(t, filepath.Join(dir, "Artist", "Album", "track.flac"))
	writeFile(t, filepath.Join(dir, "Artist", "Album", "track.tmp.flac"))
	writeFile(t, filepath.Join(dir, "Other", "Single", "song.MP3"))
	writeFile(t, filepath.Join(dir, "Covered", "Album", "track.flac"))
	writeFile(t, filepath.Join(dir, "Covered", "Album", "cover.jpg"))
	writeFile(t, outside)
	if err := os.MkdirAll(filepath.Join(dir, "Linked"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "Linked", "link.flac")); err != nil {
		t.Fatal(err)
	}

	res, err := cleanPlaylistDir(root+"/", dir, testExts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res != (cleanResult{deleted: 4, removedDirs: 4, kept: 2}) {
		t.Fatalf("got %+v, want 4 deleted, 4 removed dirs, 2 kept", res)
	}

	for _, p := range []string{
		filepath.Join(dir, "Artist"),
		filepath.Join(dir, "Other"),
		filepath.Join(dir, "Covered", "Album", "track.flac"),
	} {
		if exists(p) {
			t.Errorf("%s should be removed", p)
		}
	}
	for _, p := range []string{
		dir,
		outside,
		filepath.Join(dir, "Covered", "Album", "cover.jpg"),
		filepath.Join(dir, "Linked", "link.flac"),
	} {
		if !exists(p) {
			t.Errorf("%s should be kept", p)
		}
	}
}

func TestCleanPlaylistDirFlat(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Weekly-Exploration")
	writeFile(t, filepath.Join(dir, "a.flac"))
	writeFile(t, filepath.Join(dir, "b.mp3"))

	res, err := cleanPlaylistDir(root, dir, testExts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res != (cleanResult{deleted: 2}) {
		t.Fatalf("got %+v, want 2 deleted", res)
	}
	if !exists(dir) {
		t.Fatal("playlist dir should be kept")
	}
}

func TestCleanPlaylistDirMissing(t *testing.T) {
	root := t.TempDir()
	res, err := cleanPlaylistDir(root, filepath.Join(root, "Weekly-Exploration"), testExts, nil)
	if err != nil || res.deleted != 0 {
		t.Fatalf("deleted=%d err=%v, want 0/nil", res.deleted, err)
	}
}

func TestCleanPlaylistDirGuard(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "data")
	writeFile(t, filepath.Join(base, "a.flac"))
	writeFile(t, filepath.Join(root, "elsewhere", "b.flac"))

	cases := map[string]struct{ root, dir string }{
		"dir equals root":     {base, base + "/"},
		"empty dir":           {base, ""},
		"empty root":          {"", filepath.Join(base, "x")},
		"dir outside root":    {base, filepath.Join(root, "elsewhere")},
		"dir is parent":       {base, root},
		"filesystem root dir": {"/", "/"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := cleanPlaylistDir(tc.root, tc.dir, testExts, nil)
			if err == nil || res.deleted != 0 {
				t.Fatalf("deleted=%d err=%v, want error and nothing deleted", res.deleted, err)
			}
		})
	}
	if !exists(filepath.Join(base, "a.flac")) || !exists(filepath.Join(root, "elsewhere", "b.flac")) {
		t.Fatal("guard let files be deleted")
	}
}
