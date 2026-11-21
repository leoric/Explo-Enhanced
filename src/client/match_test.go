package client

import (
	"testing"

	"explo/src/models"
	"explo/src/util"
)

// Indian film-song titles from YouTube Music vs. a library tagged with plain song names.
func TestFilmSongTitles(t *testing.T) {
	search := map[string]string{
		`Hukum - Thalaivar Alappara - Tamil (From "Jailer")`:                                "Hukum - Thalaivar Alappara",
		`Powerhouse (From "Coolie") (Tamil)`:                                                "Powerhouse",
		`Chola Chola (From "Ponniyin Selvan, Part 1") [Original Motion Picture Soundtrack]`: "Chola Chola",
		`Kar Gayi Chull (From "Kapoor & Sons (Since 1921)")`:                                "Kar Gayi Chull",
		`Annakili (From - Tamizh Kirukkan)`:                                                 "Annakili",
		`Vibe Undi (From "Mirai") [Telugu]`:                                                 "Vibe Undi",
		`KALYANI (Remix)`:                                                                   "KALYANI (Remix)", // a different recording: keep
		`Gun-in Kadhal`:                                                                     "Gun-in Kadhal",
	}
	for in, want := range search {
		if got := util.CleanSearchTitle(in); got != want {
			t.Errorf("CleanSearchTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if util.NormalizeTitle("Hukum (Tamil)") != util.NormalizeTitle("Hukum") {
		t.Error("a language tag in the library title should not change the normalized title")
	}

	langs := map[string]string{
		"Hukum - Telugu":                  "telugu",
		"KGF Chapter 1 (Tamil)":           "tamil",
		`Tum Tum (From "Enemy - Telugu")`: "telugu",
		"Animal- Hindi":                   "hindi",
		"Classic Tamil Film Melodies":     "", // a word in a name, not a tag
		"Kaavaalaa":                       "",
	}
	for in, want := range langs {
		if got := util.Language(in); got != want {
			t.Errorf("Language(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDubbedVersionsDoNotMatch(t *testing.T) {
	tamil := SearchResult{ID: "ta", Title: "Hukum (Tamil)", Artist: "Anirudh Ravichander", Album: "Jailer"}
	plain := SearchResult{ID: "plain", Title: "Hukum", Artist: "Anirudh Ravichander", Album: "Jailer"}

	telugu := &models.Track{CleanTitle: "Hukum - Telugu", MainArtist: "Anirudh Ravichander", Album: "Jailer"}
	if _, ok := BestMatch(telugu, []SearchResult{tamil}, 60); ok {
		t.Error("a Telugu track must not match a library song tagged Tamil")
	}
	if m, ok := BestMatch(telugu, []SearchResult{plain}, 60); !ok || m.ID != "plain" {
		t.Error("with no language in the library title, the song should still match")
	}

	fromYouTube := &models.Track{CleanTitle: `Powerhouse (From "Coolie") (Tamil)`, MainArtist: "Anirudh Ravichander", Album: "Coolie"}
	lib := SearchResult{ID: "p", Title: "Powerhouse", Artist: "Anirudh Ravichander", Album: "Coolie"}
	if m, ok := BestMatch(fromYouTube, []SearchResult{lib}, 60); !ok || m.Score < 90 {
		t.Errorf("film-song title should fully match the clean library title, got score %d ok=%v", m.Score, ok)
	}
}

