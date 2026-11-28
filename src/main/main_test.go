package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"explo/src/config"
	"explo/src/models"
	"explo/src/web/backend/playlist"
)

func enrichCfg(playlist string, enabled bool) *config.Config {
	var cfg config.Config
	cfg.Flags.Playlist = playlist
	cfg.DiscoveryCfg.Listenbrainz.EnrichTrackMetadata = enabled
	return &cfg
}

func TestEnrichMissingTracksOnlyMissing(t *testing.T) {
	tracks := []*models.Track{
		{CleanTitle: "present", MusicBrainzTrackID: "m1", Present: true},
		{CleanTitle: "missing", MusicBrainzTrackID: "m2"},
		{CleanTitle: "no mbid"},
		{CleanTitle: "missing too", MusicBrainzTrackID: "m4"},
	}
	var got []string
	enrich := func(in []*models.Track) ([]*models.Track, error) {
		out := make([]*models.Track, len(in))
		for i, tr := range in {
			got = append(got, tr.MusicBrainzTrackID)
			out[i] = &models.Track{CleanTitle: tr.CleanTitle + " (enriched)", MusicBrainzTrackID: tr.MusicBrainzTrackID}
		}
		return out, nil
	}

	enrichMissingTracks(enrichCfg("weekly-exploration", true), tracks, enrich)

	if len(got) != 2 || got[0] != "m2" || got[1] != "m4" {
		t.Fatalf("enriched %v, want [m2 m4]", got)
	}
	want := []string{"present", "missing (enriched)", "no mbid", "missing too (enriched)"}
	for i, w := range want {
		if tracks[i].CleanTitle != w {
			t.Errorf("tracks[%d] = %q, want %q", i, tracks[i].CleanTitle, w)
		}
	}
}

func TestEnrichMissingTracksSkips(t *testing.T) {
	cases := map[string]struct {
		cfg    *config.Config
		tracks []*models.Track
	}{
		"disabled":       {enrichCfg("weekly-exploration", false), []*models.Track{{MusicBrainzTrackID: "m1"}}},
		"fresh-releases": {enrichCfg("fresh-releases", true), []*models.Track{{MusicBrainzTrackID: "m1"}}},
		"all present":    {enrichCfg("weekly-exploration", true), []*models.Track{{MusicBrainzTrackID: "m1", Present: true}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			enrichMissingTracks(tc.cfg, tc.tracks, func([]*models.Track) ([]*models.Track, error) {
				t.Fatal("enrich should not be called")
				return nil, nil
			})
		})
	}
}

func TestEnrichMissingTracksErrorKeepsTracks(t *testing.T) {
	orig := &models.Track{CleanTitle: "missing", MusicBrainzTrackID: "m1"}
	tracks := []*models.Track{orig}
	enrichMissingTracks(enrichCfg("weekly-exploration", true), tracks, func([]*models.Track) ([]*models.Track, error) {
		return nil, errors.New("listenbrainz down")
	})
	if tracks[0] != orig {
		t.Fatal("track replaced despite enrichment error")
	}
}

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
