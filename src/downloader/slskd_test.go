package downloader

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"explo/src/config"
	"explo/src/models"
)

// fakeSlskdQueue accepts download POSTs except for users in failing, and records the users posted to
type fakeSlskdQueue struct {
	mu      sync.Mutex
	failing map[string]bool
	posts   []string
}

func (f *fakeSlskdQueue) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user, ok := strings.CutPrefix(r.URL.Path, "/api/v0/transfers/downloads/")
	if r.Method != http.MethodPost || !ok {
		http.Error(w, "unexpected request", http.StatusNotFound)
		return
	}
	f.posts = append(f.posts, user)
	if f.failing[user] {
		http.Error(w, "user offline", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func newTestSlskd(t *testing.T, failing ...string) (*Slskd, *fakeSlskdQueue) {
	t.Helper()
	fake := &fakeSlskdQueue{failing: map[string]bool{}}
	for _, u := range failing {
		fake.failing[u] = true
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return NewSlskd(config.Slskd{URL: srv.URL, Timeout: 5}, ""), fake
}

func file(user, name string) File {
	return File{Username: user, Name: name, Size: len(name)}
}

func TestQueueDownloadSkipsFailedUser(t *testing.T) {
	c, fake := newTestSlskd(t, "u1")
	track := &models.Track{ID: "s1", CleanTitle: "Song"}
	files := []File{file("u1", "1.flac"), file("u1", "2.flac"), file("u2", "3.flac"), file("u3", "4.flac")}

	if err := c.queueDownload(files, track); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.posts, ","); got != "u1,u2" {
		t.Fatalf("posts=%s, want u1,u2 (second u1 candidate skipped)", got)
	}
	if track.MainArtistID != "u2" || track.File != "3.flac" || track.Size != len("3.flac") {
		t.Fatalf("track not pointed at queued file: %+v", track)
	}
}

func TestRetryNextSkipsFailedUsers(t *testing.T) {
	c, fake := newTestSlskd(t)
	track := &models.Track{ID: "s1", CleanTitle: "Song"}
	files := []File{file("u1", "1.flac"), file("u1", "2.flac"), file("u2", "3.mp3"), file("u1", "4.mp3")}

	if err := c.queueDownload(files, track); err != nil {
		t.Fatal(err)
	}
	if track.MainArtistID != "u1" || track.File != "1.flac" {
		t.Fatalf("first queue: %+v", track)
	}

	// u1 failed: its other candidates are skipped, u2 is next
	ok, err := c.RetryNext(track, "errored")
	if err != nil || !ok {
		t.Fatalf("RetryNext=%v, %v; want true", ok, err)
	}
	if track.MainArtistID != "u2" || track.File != "3.mp3" {
		t.Fatalf("retry queued %+v, want u2/3.mp3", track)
	}

	// u2 failed, only a u1 candidate is left
	ok, err = c.RetryNext(track, "stalled")
	if err != nil || ok {
		t.Fatalf("RetryNext=%v, %v; want false with nothing left", ok, err)
	}
	if got := strings.Join(fake.posts, ","); got != "u1,u2" {
		t.Fatalf("posts=%s, want u1,u2", got)
	}

	// store entry is gone afterwards
	if ok, _ := c.RetryNext(track, "stalled"); ok {
		t.Fatal("RetryNext after exhaustion should return false")
	}
}

func TestRetryNextPostFailureMovesOn(t *testing.T) {
	c, fake := newTestSlskd(t, "u2")
	track := &models.Track{ID: "s1", CleanTitle: "Song"}
	files := []File{file("u1", "1.flac"), file("u2", "2.flac"), file("u3", "3.flac")}

	if err := c.queueDownload(files, track); err != nil {
		t.Fatal(err)
	}
	ok, err := c.RetryNext(track, "errored")
	if err != nil || !ok || track.MainArtistID != "u3" {
		t.Fatalf("RetryNext=%v, %v, user=%s; want u3 after u2 POST failed", ok, err, track.MainArtistID)
	}
	if got := strings.Join(fake.posts, ","); got != "u1,u2,u3" {
		t.Fatalf("posts=%s", got)
	}
}

func TestRetryNextUnknownTrack(t *testing.T) {
	c, _ := newTestSlskd(t)
	if ok, err := c.RetryNext(&models.Track{ID: "never-queued"}, "missing from queue"); ok || err != nil {
		t.Fatalf("RetryNext=%v, %v; want false, nil", ok, err)
	}
}

func TestFilterFilesKeepsExtensionOrder(t *testing.T) {
	c := Slskd{Cfg: config.Slskd{DownloadAttempts: 3, Filters: config.Filters{Extensions: []string{"flac", "mp3"}}}}
	files := []File{
		{Name: "a.mp3", Extension: "mp3", Username: "u1"},
		{Name: "b.flac", Extension: "flac", Username: "u2"},
		{Name: "c.mp3", Extension: "mp3", Username: "u3"},
		{Name: "d.flac", Extension: "flac", Username: "u4"},
	}
	got, err := c.filterFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range got {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "b.flac,d.flac,a.mp3" {
		t.Fatalf("got %v, want flac first, limited to 3", names)
	}
}
