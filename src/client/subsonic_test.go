package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"explo/src/config"
	"explo/src/models"
	"explo/src/util"
)

// fakeNavidrome answers search3 like Navidrome: every query word must be in the song.
func fakeNavidrome(t *testing.T, library map[string][]map[string]any) *Subsonic {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		songs := library[r.URL.Query().Get("query")]
		resp := map[string]any{"subsonic-response": map[string]any{"status": "ok", "searchResult3": map[string]any{"song": songs}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return NewSubsonic(config.ClientConfig{URL: srv.URL}, util.NewHttp(util.HttpClientConfig{Timeout: 5}))
}

func TestSearchSongsShorterName(t *testing.T) {
	kovai := map[string]any{"id": "lib-kovai", "title": "Kovai Gethu", "artist": "Hiphop Tamizha", "album": "Kovai Gethu", "duration": 178}
	c := fakeNavidrome(t, map[string][]map[string]any{"Kovai Gethu": {kovai}})

	kept := &models.Track{CleanTitle: "Kovai Gethu Anthem", Title: "Kovai Gethu Anthem",
		VideoTitle: "Kovai Gethu Anthem | The Times Of India | Hiphop Tamizha", VideoDuration: 199000}
	official := &models.Track{CleanTitle: "Kovai Gethu Anthem", Title: "Kovai Gethu Anthem", MainArtist: "Hiphop Tamizha"}
	if err := c.SearchSongs([]*models.Track{kept, official}); err != nil {
		t.Fatal(err)
	}
	if !kept.Present || kept.ID != "lib-kovai" {
		t.Error("a kept video should find the library song under its shorter name")
	}
	if official.Present {
		t.Error("only kept videos are shortened: an official song's title is already exact")
	}

	// a kept video with the same title as another artist's song in the library: not "already have it"
	other := map[string]any{"id": "lib-other", "title": "Pattampoochi", "artist": "Malgudi Shuba", "album": "Yenna Paaru", "duration": 290, "path": "x/y.flac"}
	c = fakeNavidrome(t, map[string][]map[string]any{"Pattampoochi": {other}})
	pattam := &models.Track{CleanTitle: "Pattampoochi", Title: "Pattampoochi", Artist: "Some Channel", MainArtist: "Some Channel",
		VideoTitle: "Pattampoochi (feat. Suriya & Mamitha Baiju)", VideoDuration: 230000}
	if err := c.SearchSongs([]*models.Track{pattam}); err != nil {
		t.Fatal(err)
	}
	if pattam.Present {
		t.Error("same title but no proof it's the same song: must be downloaded")
	}
}

// Navidrome can't find "Andangakka" when asked for "Andangkaka": the first letters of the name find it.
func TestSearchSongsSpelling(t *testing.T) {
	andang := map[string]any{"id": "lib-andang", "title": "Andangakka", "artist": "Harris Jayaraj, Jassie Gift", "album": "Anniyan", "duration": 337, "path": "a/b.flac"}
	c := fakeNavidrome(t, map[string][]map[string]any{"Anda": {andang}})
	kept := &models.Track{CleanTitle: "Andangkaka Kondakari", Title: "Andangkaka Kondakari", Artist: "Some Channel",
		VideoTitle: "Andangkaka Kondakari - 4K Video Song | Anniyan | Vikram | Harris Jayaraj", VideoDuration: 339000}
	if err := c.SearchSongs([]*models.Track{kept}); err != nil {
		t.Fatal(err)
	}
	if !kept.Present || kept.ID != "lib-andang" {
		t.Error("a differently spelled library song should be found through the name's first letters")
	}
}
