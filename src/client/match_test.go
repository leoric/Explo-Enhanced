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

// A kept video's name can carry extra words the library title doesn't have ("Kovai Gethu Anthem" vs
// "Kovai Gethu"). A shorter name may match only with enough proof; a wrong "already have it" would
// leave the playlist song missing, which is worse than downloading a duplicate.
func TestShorterNames(t *testing.T) {
	got := shorterNames("Kovai Gethu Anthem")
	if len(got) != 2 || got[0] != "Kovai Gethu" || got[1] != "Kovai" {
		t.Errorf("shorterNames = %q, want longest first", got)
	}
	if len(shorterNames("Taki")) != 0 {
		t.Error("a one-word name has nothing to drop")
	}
	if got := shorterNames("A B C D E F G"); len(got) != 4 || got[3] != "A B C" {
		t.Errorf("drop at most 4 words, got %q", got)
	}
}

func TestShortNameMatch(t *testing.T) {
	video := func(name, title string, seconds int) *models.Track {
		return &models.Track{CleanTitle: name, Title: name, VideoTitle: title, VideoDuration: seconds * 1000}
	}
	lib := func(title, artist, album string, seconds int) SearchResult {
		return SearchResult{Title: title, Artist: artist, Album: album, Duration: seconds}
	}
	cases := []struct {
		name  string
		track *models.Track
		short string
		lib   SearchResult
		want  bool
	}{
		// known extra word + singer named in the video title: one proof is enough
		{"Tamil + singer", video("Soppanasundari Tamil", "Soppanasundari Tamil Video | D. Imman | Vikram Prabhu", 270),
			"Soppanasundari", lib(`Soppanasundari (From "Veera Sivaji")`, "D. Imman; Vaikom Vijayalakshmi", "Veera Sivaji", 263), true},
		{"Anthem + singer", video("Kovai Gethu Anthem", "Kovai Gethu Anthem | The Times Of India | Hiphop Tamizha", 199),
			"Kovai Gethu", lib("Kovai Gethu", "Hiphop Tamizha", "Kovai Gethu", 178), true},
		{"Music + singer", video("Kutty Pattas Music", "Kutty Pattas Music Video | Ashwin | Reba John | Santhosh Dhayanidhi", 243),
			"Kutty Pattas", lib("Kutty Pattas", "Santhosh Dhayanidhi, Rakshita Suresh", "Kutty Pattas", 240), true},
		{"known word + same length", video("Kovai Gethu Anthem", "Kovai Gethu Anthem | The Times Of India", 179),
			"Kovai Gethu", lib("Kovai Gethu", "Hiphop Tamizha", "Kovai Gethu", 178), true},
		// unknown extra word: two proofs
		{"unknown word, singer + film", video("Nagada Sang Dhol Baaje", "Nagada Sang Dhol Baaje | Ram-Leela | Shreya Ghoshal", 280),
			"Nagada Sang Dhol", lib("Nagada Sang Dhol", "Shreya Ghoshal, Osman Mir", "Goliyon Ki Raasleela Ram-Leela", 274), true},
		{"unknown word, singer + length", video("Thottu Thottu Pesum Sulthana", "Thottu Thottu Pesum Sulthana | Swarnalatha", 284),
			"Thottu Thottu Pesum", lib("Thottu Thottu Pesum", "Pushpavanam Kuppusamy, Swarnalatha", "Ethirum Pudhirum", 286), true},
		{"unknown word, singer only", video("Thottu Thottu Pesum Sulthana", "Thottu Thottu Pesum Sulthana | Swarnalatha", 300),
			"Thottu Thottu Pesum", lib("Thottu Thottu Pesum", "Pushpavanam Kuppusamy, Swarnalatha", "Ethirum Pudhirum", 286), false},
		// the full name is a real song of its own: never shortened
		{"full name is a song", &models.Track{CleanTitle: "Kanne Kalaimaane", VideoTitle: "Kanne Kalaimaane | Yesudas | Moondram Pirai", VideoDuration: 260000, NameIsSong: true},
			"Kanne", lib("Kanne", "K. J. Yesudas", "Other Film", 258), false},
		// a short common word needs two proofs
		{"short word, singer only", video("Kanne Kalaimaane", "Kanne Kalaimaane | Yesudas", 300),
			"Kanne", lib("Kanne", "Yesudas", "Other Film", 250), false},
		{"short word, singer + length", video("Mascara Pottu", "Mascara Pottu | Salim | Vijay Antony", 297),
			"Mascara", lib("Mascara", "Supriya Joshi, Vijay Antony", "Salim", 297), true},
		// a version word is never dropped: a remix or female version is not the original
		{"remix", video("Kadhal Vaibogame Remix", "Kadhal vaibogame remix | S. Janaki", 249),
			"Kadhal Vaibogame", lib("Kadhal Vaibogame", "S. Janaki", "Perumal", 249), false},
		{"female version", video("Rathinamo Female Version", "Rathinamo Female Version | Saurav Srisan", 200),
			"Rathinamo", lib("Rathinamo", "Saurav Srisan", "Rathinamo", 200), false},
		// other safeguards
		{"other language", video("Soppanasundari Telugu", "Soppanasundari Telugu Video | D. Imman", 263),
			"Soppanasundari", lib("Soppanasundari (Tamil)", "D. Imman", "Veera Sivaji", 263), false},
		{"library song much longer than the video", video("Kovai Gethu Anthem", "Kovai Gethu Anthem | Hiphop Tamizha", 110),
			"Kovai Gethu", lib("Kovai Gethu", "Hiphop Tamizha", "Kovai Gethu", 178), false},
		// a video edit can cut part of the song (real case: 283 s video, 300 s library song)
		{"video shorter than the song", &models.Track{CleanTitle: "Koodamela Koodavechi /", Artist: "VANDANA SRINIVASH, Prasanna, Yugabharathi, V.V.Prasanna",
			VideoTitle: "Koodamela Koodavechi 8K/4K Video Song | Rummy | D. Imman | Vijay Sethupathi | Iyshwarya Rajesh", VideoDuration: 283000},
			"Koodamela Koodavechi /", lib("Koodamela Koodavechi", "D. Imman, VV Prassanna, Vandana Srinivasan, Yugabharathi", "Rummy", 300), true},
		{"title must be exact", video("Kovai Gethu Anthem", "Kovai Gethu Anthem | Hiphop Tamizha", 199),
			"Kovai Gethu", lib("Kovai Gethu Remix", "Hiphop Tamizha", "Kovai Gethu", 178), false},
		{"artist is a whole word", video("Soppanasundari Tamil", "Soppanasundari Tamil Video | Manoj Kumar", 270),
			"Soppanasundari", lib("Soppanasundari", "Mano", "Veera Sivaji", 250), false},
		{"no proof", video("Soppanasundari Tamil", "Soppanasundari Tamil Video", 300),
			"Soppanasundari", lib("Soppanasundari", "D. Imman", "Veera Sivaji", 263), false},
		// the full name gets the same rule: same title alone is not proof (another artist's song)
		{"full name + singer", video("Soppanasundari", "Soppanasundari Video | D. Imman | Vikram Prabhu", 270),
			"Soppanasundari", lib("Soppanasundari", "D. Imman", "Veera Sivaji", 263), true},
		{"full name, other artist", video("Pattampoochi", "Pattampoochi (feat. Suriya & Mamitha Baiju)", 230),
			"Pattampoochi", lib("Pattampoochi", "Malgudi Shuba", "Yenna Paaru", 290), false},
		{"listed artist is proof", &models.Track{CleanTitle: "Hong Kong", Artist: "The Weeknd", VideoTitle: "Hong Kong (feat. 王菲季候風)", VideoDuration: 214000},
			"Hong Kong", lib("Hong Kong", "The Weeknd", "Hurry Up Tomorrow", 205), true},
		{"other artist, same title", &models.Track{CleanTitle: "Hong Kong", Artist: "The Weeknd", VideoTitle: "Hong Kong (feat. 王菲季候風)", VideoDuration: 214000},
			"Hong Kong", lib("Hong Kong", "Sgija Keys, M00tion", "Each Of Us", 160), false},
		// versions must agree both ways: a live video is not the studio song you own, and back
		{"live video", &models.Track{CleanTitle: "Lego House", Artist: "Ed Sheeran", VideoTitle: "Lego House (Live)", VideoDuration: 200000},
			"Lego House", lib("Lego House", "Ed Sheeran", "+", 185), false},
		{"instrumental video", &models.Track{CleanTitle: "甲乙丙丁Strangers", Artist: "Li Jiawei", VideoTitle: "甲乙丙丁Strangers (伴奏)", VideoDuration: 200000},
			"甲乙丙丁Strangers", lib("甲乙丙丁Strangers", "Li Jiawei", "甲乙丙丁Strangers", 199), false},
		{"library has the extended mix", &models.Track{CleanTitle: "What If?", Artist: "Neptunica", VideoTitle: "What If?", VideoDuration: 400000},
			"What If?", lib("What If? (Extended Mix)", "Neptunica", "What If?", 380), false},
		// spelled differently (Tamil in English letters): the singer plus one more proof
		{"spelling: singer + film", video("Andangkaka Kondakari", "Andangkaka Kondakari - 4K Video Song | Anniyan | Vikram | Harris Jayaraj", 339),
			"Andangkaka", lib("Andangakka", "Harris Jayaraj, Jassie Gift", "Anniyan", 337), true},
		{"spelling: zh/l, singer + film", video("Marutha Alagaro", "Marutha Alagaro | Sundara Purushan | K. S. Chithra", 290),
			"Marutha Alagaro", lib("Marutha Azhagaro", "K.S. Chithra", "Sundara Purushan", 285), true},
		{"spelling without the singer", video("Marutha Alagaro", "Marutha Alagaro | Sundara Purushan", 285),
			"Marutha Alagaro", lib("Marutha Azhagaro", "K.S. Chithra", "Sundara Purushan", 285), false},
		{"spelling, singer only: rain is not hill", video("Mazhai Varuthu", "Mazhai Varuthu | K. S. Chithra", 300),
			"Mazhai Varuthu", lib("Malai Varuthu", "K.S. Chithra", "Other Film", 250), false},
		{"not just similar", video("Kannamma", "Kannamma | Anirudh Ravichander | Kaala", 250),
			"Kannamma", lib("Kanmani", "Anirudh Ravichander", "Kaala", 250), false},
		// a fan edit (slowed, sped up, 8D…) is kept as its video: it is not the official song you own
		{"slowed video", video("Hukum", "Hukum (Slowed + Reverb) | Jailer | Anirudh Ravichander", 260),
			"Hukum", lib("Hukum", "Anirudh Ravichander", "Jailer", 260), false},
		{"sped up video", video("Hukum", "Hukum Sped Up | Jailer | Anirudh Ravichander", 205),
			"Hukum", lib("Hukum", "Anirudh Ravichander", "Jailer", 205), false},
		{"both extended", &models.Track{CleanTitle: "Verappa Extended", Artist: "Sai Abhyankkar", VideoTitle: "Verappa Extended", VideoDuration: 250000},
			"Verappa Extended", lib(`Verappa - Extended (From "Karuppu")`, "Sai Abhyankkar, Arivu", "Karuppu", 248), true},
	}
	for _, c := range cases {
		if got := keptVideoMatch(c.track, c.short, c.lib); got != c.want {
			t.Errorf("%s: keptVideoMatch = %v, want %v", c.name, got, c.want)
		}
	}
}

// A song with no album must not score album points against every library song: "" is "contained" in
// any album name, which used to lift a title-only match (45) to the threshold (60).
func TestEmptyAlbumScoresNothing(t *testing.T) {
	track := &models.Track{CleanTitle: "Pattampoochi", MainArtist: "Sublahshini"}
	other := SearchResult{Title: "Pattampoochi", Artist: "Malgudi Shuba", Album: "Yenna Paaru", Path: "a/b/Pattampoochi.flac"}
	if m, ok := BestMatch(track, []SearchResult{other}, 60); ok {
		t.Errorf("same title, other artist, no album: score %d must stay under 60", m.Score)
	}
	withAlbum := &models.Track{CleanTitle: "Pattampoochi", MainArtist: "Sublahshini", Album: "Yenna Paaru"}
	if m, _ := BestMatch(withAlbum, []SearchResult{other}, 0); m.Score != 65 {
		t.Errorf("a real album match still counts: score %d, want 65", m.Score)
	}
}
