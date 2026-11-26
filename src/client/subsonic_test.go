package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"explo/src/config"
	"explo/src/models"
	"explo/src/util"
)

// fakeSubsonic is a minimal in-memory Subsonic playlist API
type fakeSubsonic struct {
	mu        sync.Mutex
	playlists []Playlist
	nextID    int
	calls     []string // endpoint + relevant params, in order
}

func (f *fakeSubsonic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	q := r.URL.Query()
	user := q.Get("u")
	resp := map[string]any{"status": "ok"}
	fail := func(msg string) { resp = map[string]any{"status": "failed", "error": map[string]any{"code": 70, "message": msg}} }

	switch endpoint := strings.TrimPrefix(r.URL.Path, "/rest/"); endpoint {
	case "getPlaylists":
		f.calls = append(f.calls, endpoint)
		resp["playlists"] = map[string]any{"playlist": f.playlists}
	case "createPlaylist":
		songs := strings.Join(q["songId"], ",")
		if id := q.Get("playlistId"); id != "" {
			f.calls = append(f.calls, fmt.Sprintf("createPlaylist playlistId=%s songs=%s", id, songs))
			found := false
			for _, p := range f.playlists {
				if p.ID == id && p.Owner == user {
					found = true
					resp["playlist"] = p
				}
			}
			if !found {
				fail("playlist not found")
			}
		} else {
			f.calls = append(f.calls, fmt.Sprintf("createPlaylist name=%s songs=%s", q.Get("name"), songs))
			f.nextID++
			p := Playlist{ID: fmt.Sprintf("pl-%d", f.nextID), Name: q.Get("name"), Owner: user}
			f.playlists = append(f.playlists, p)
			resp["playlist"] = p
		}
	case "deletePlaylist":
		f.calls = append(f.calls, "deletePlaylist id="+q.Get("id"))
	case "updatePlaylist":
		f.calls = append(f.calls, "updatePlaylist playlistId="+q.Get("playlistId"))
	default:
		fail("unexpected endpoint " + endpoint)
	}

	if err := json.NewEncoder(w).Encode(map[string]any{"subsonic-response": resp}); err != nil {
		panic(err)
	}
}

func (f *fakeSubsonic) takeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.calls
	f.calls = nil
	return calls
}

// newTestClient returns a fresh client, as in a new explo run
func newTestClient(t *testing.T, srvURL, name string) (*Client, *Subsonic) {
	t.Helper()
	cfg := config.ClientConfig{URL: srvURL, PlaylistName: name, Creds: config.Credentials{User: "me", Password: "pw"}}
	sub := NewSubsonic(cfg, util.NewHttp(util.HttpClientConfig{Timeout: 5}))
	if err := sub.GetAuth(); err != nil {
		t.Fatal(err)
	}
	return &Client{System: "subsonic", Cfg: &cfg, API: sub}, sub
}

// replaceRun mimics main.go with --replace-playlist: DeletePlaylist, then create + update
func replaceRun(t *testing.T, srvURL, name string, songs ...string) string {
	t.Helper()
	c, sub := newTestClient(t, srvURL, name)
	if err := c.DeletePlaylist(); err != nil {
		t.Fatal(err)
	}
	var tracks []*models.Track
	for _, s := range songs {
		tracks = append(tracks, &models.Track{ID: s})
	}
	if err := sub.CreatePlaylist(tracks); err != nil {
		t.Fatal(err)
	}
	if err := sub.UpdatePlaylist(); err != nil {
		t.Fatal(err)
	}
	return sub.Cfg.PlaylistID
}

func assertCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSubsonicReplaceKeepsPlaylistID(t *testing.T) {
	fake := &fakeSubsonic{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	id1 := replaceRun(t, srv.URL, "Weekly-Exploration", "a", "b")
	assertCalls(t, fake.takeCalls(),
		"getPlaylists",
		"createPlaylist name=Weekly-Exploration songs=a,b",
		"updatePlaylist playlistId=pl-1",
	)

	id2 := replaceRun(t, srv.URL, "Weekly-Exploration", "c")
	assertCalls(t, fake.takeCalls(),
		"getPlaylists",
		"createPlaylist playlistId=pl-1 songs=c",
		"updatePlaylist playlistId=pl-1",
	)
	if id1 != "pl-1" || id2 != id1 {
		t.Fatalf("ids %q, %q: want stable pl-1", id1, id2)
	}
}

func TestSubsonicReplaceIgnoresOtherOwner(t *testing.T) {
	fake := &fakeSubsonic{playlists: []Playlist{{ID: "theirs", Name: "Discover Weekly", Owner: "someone-else"}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	id := replaceRun(t, srv.URL, "Discover Weekly", "a")
	assertCalls(t, fake.takeCalls(),
		"getPlaylists",
		"createPlaylist name=Discover Weekly songs=a",
		"updatePlaylist playlistId=pl-1",
	)
	if id != "pl-1" {
		t.Fatalf("id %q, want pl-1", id)
	}
}

func TestSubsonicReplaceFallsBackToCreate(t *testing.T) {
	fake := &fakeSubsonic{playlists: []Playlist{{ID: "old", Name: "Weekly-Exploration", Owner: "me"}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c, sub := newTestClient(t, srv.URL, "Weekly-Exploration")
	if err := c.DeletePlaylist(); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.playlists = nil // deleted between search and create
	fake.mu.Unlock()

	if err := sub.CreatePlaylist([]*models.Track{{ID: "a"}}); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, fake.takeCalls(),
		"getPlaylists",
		"createPlaylist playlistId=old songs=a",
		"createPlaylist name=Weekly-Exploration songs=a",
	)
	if sub.Cfg.PlaylistID != "pl-1" {
		t.Fatalf("id %q, want pl-1", sub.Cfg.PlaylistID)
	}
}

func TestSubsonicDeleteWithoutIDIsNoop(t *testing.T) {
	fake := &fakeSubsonic{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	_, sub := newTestClient(t, srv.URL, "Weekly-Exploration")
	if err := sub.DeletePlaylist(); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, fake.takeCalls())
}
