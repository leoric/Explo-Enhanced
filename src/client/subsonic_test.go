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
	songs     map[string][]map[string]any // search3 results per query
	starred   []map[string]any            // getStarred2 songs
	entries   map[string][]map[string]any // getPlaylist songs per playlist ID
	smart     map[string]bool             // playlists that only report validUntil once getPlaylist evaluates them
	calls     []string                    // endpoint + relevant params, in order
}

func (f *fakeSubsonic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	q := r.URL.Query()
	user := q.Get("u")
	resp := map[string]any{"status": "ok"}
	fail := func(msg string) {
		resp = map[string]any{"status": "failed", "error": map[string]any{"code": 70, "message": msg}}
	}

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
	case "search3":
		f.calls = append(f.calls, "search3 query="+q.Get("query"))
		resp["searchResult3"] = map[string]any{"song": f.songs[q.Get("query")]}
	case "getStarred2":
		f.calls = append(f.calls, endpoint)
		resp["starred2"] = map[string]any{"song": f.starred}
	case "getPlaylist":
		id := q.Get("id")
		f.calls = append(f.calls, "getPlaylist id="+id)
		pl := map[string]any{"id": id, "entry": f.entries[id]}
		if f.smart[id] {
			pl["validUntil"] = "2026-11-27T00:00:00Z"
		}
		resp["playlist"] = pl
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
	cfg := config.ClientConfig{URL: srvURL, PlaylistName: name, MatchScore: 60, Creds: config.Credentials{User: "me", Password: "pw"}}
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

func TestSubsonicSearchSongsOrder(t *testing.T) {
	wanted := map[string]any{"id": "song-wanted", "title": "Common Song", "artist": "Test Band", "album": "Test Album", "musicBrainzId": "mbid-common", "path": "Test Band/Test Album/04 Common Song.mp3"}
	other := map[string]any{"id": "song-other", "title": "Common Song", "artist": "Other Band", "album": "Other Album", "path": "Other Band/Other Album/01 Common Song.mp3"}
	fake := &fakeSubsonic{songs: map[string][]map[string]any{
		"mbid-common":         {wanted},
		"Common Song":         {other}, // the wanted track isn't among the title results
		"Rare Song Test Band": {{"id": "song-rare", "title": "Rare Song", "artist": "Test Band", "album": "Test Album", "path": "Test Band/Test Album/03 Rare Song.mp3"}},
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, sub := newTestClient(t, srv.URL, "x")

	tracks := []*models.Track{
		{CleanTitle: "Common Song", MainArtist: "Test Band", Album: "Test Album", MusicBrainzTrackID: "mbid-common"},
		{CleanTitle: "Rare Song", MainArtist: "Test Band", Album: "Test Album", MusicBrainzTrackID: "mbid-not-tagged"},
		{CleanTitle: "Rare Song", MainArtist: "Test Band", Album: "Test Album"},
		{CleanTitle: "Common Song", MainArtist: "Test Band", Album: "Test Album"},
		{CleanTitle: "Common Song", MainArtist: "Other Band", Album: "Other Album"}, // title + artist finds nothing
	}
	if err := sub.SearchSongs(tracks); err != nil {
		t.Fatal(err)
	}

	assertCalls(t, fake.takeCalls(),
		"search3 query=mbid-common",     // ID matches, nothing else searched
		"search3 query=mbid-not-tagged", // ID not found, falls back to title + artist
		"search3 query=Rare Song Test Band",
		"search3 query=Rare Song Test Band",    // no ID: title + artist matches
		"search3 query=Common Song Test Band",  // no ID, not found by title + artist...
		"search3 query=Common Song",            // ...nor among the title results
		"search3 query=Common Song Other Band", // no results for title + artist...
		"search3 query=Common Song",            // ...found by title alone
	)
	want := []struct {
		id      string
		present bool
	}{{"song-wanted", true}, {"song-rare", true}, {"song-rare", true}, {"", false}, {"song-other", true}}
	for i, w := range want {
		if tracks[i].ID != w.id || tracks[i].Present != w.present {
			t.Errorf("track %d: id=%q present=%v, want %q/%v", i, tracks[i].ID, tracks[i].Present, w.id, w.present)
		}
	}
}

func TestSubsonicKeeperSongs(t *testing.T) {
	song := func(mbid, title string) map[string]any {
		return map[string]any{"id": title, "title": title, "artist": "Test Band", "musicBrainzId": mbid}
	}
	fake := &fakeSubsonic{
		playlists: []Playlist{
			{ID: "mine", Name: "Favourites", Owner: "me"},
			{ID: "explo", Name: "Discover Weekly", Owner: "me"},
			{ID: "theirs", Name: "Party", Owner: "someone-else"},
			{ID: "smart-listed", Name: "Recently Added", Owner: "me", ValidUntil: "2026-11-27T00:00:00Z"},
			{ID: "smart-unevaluated", Name: "Top Rated", Owner: "me"},
		},
		starred: []map[string]any{song("mbid-starred", "Starred Song")},
		entries: map[string][]map[string]any{
			"mine":              {song("mbid-mine", "Common Song"), song("", "Untagged Song")},
			"explo":             {song("mbid-explo", "Explo Song")},
			"theirs":            {song("mbid-theirs", "Their Song")},
			"smart-listed":      {song("mbid-smart", "Smart Song")},
			"smart-unevaluated": {song("mbid-smart2", "Smart Song 2")},
		},
		smart: map[string]bool{"smart-listed": true, "smart-unevaluated": true},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	_, sub := newTestClient(t, srv.URL, "Discover Weekly")
	songs, err := sub.KeeperSongs([]string{"Discover Weekly", "Weekly-Jams"})
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, s := range songs {
		got = append(got, s.RecordingID+"/"+s.Title+"/"+s.Artist)
	}
	want := []string{"mbid-starred/Starred Song/Test Band", "mbid-mine/Common Song/Test Band", "/Untagged Song/Test Band"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("songs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	assertCalls(t, fake.takeCalls(),
		"getStarred2",
		"getPlaylists",
		"getPlaylist id=mine",
		"getPlaylist id=smart-unevaluated",
	)
}

func TestSubsonicKeeperSongsFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"failed","error":{"code":0,"message":"server error"}}}`))
	}))
	defer srv.Close()

	_, sub := newTestClient(t, srv.URL, "Discover Weekly")
	if _, err := sub.KeeperSongs(nil); err == nil {
		t.Fatal("want error")
	}
}
