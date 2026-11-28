package client

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"
	"strings"

	"explo/src/config"
	"explo/src/models"
	"explo/src/util"
)

type SearchResult struct {
	ID string
	Title string
	Album string
	Artist string
	Artists []string
	Path string
	Duration int // seconds
	MBID string
	Score int
}

// normalized track data for matching
type NormalisedTrack struct {
	CleanTitle string
	MainArtist string
	Album string
	File string
	Duration int
	MBTrackID string
	MBReleaseTrackID string
	Language string // "tamil", "telugu", … when the title or album says so
}

// Client manages interactions with the selected music system
type Client struct {
	System string
	Cfg    *config.ClientConfig
	API    APIClient
}

type APIClient interface {
	GetLibrary() error
	GetAuth() error
	AddHeader() error
	AddLibrary() error
	SearchSongs([]*models.Track) error
	RefreshLibrary() error
	CheckRefreshState() bool
	CreatePlaylist([]*models.Track) error
	SearchPlaylist() error
	UpdatePlaylist() error
	DeletePlaylist() error
}

// uploadPlaylistArtwork POSTs raw image bytes to a music app's artwork endpoint.
// Plex, Jellyfin, and Emby all accept the same format — POST + Content-Type: image/jpeg + raw body.
// The only per-client difference is the URL path, which each caller builds before invoking.
func uploadPlaylistArtwork(hc *util.HttpClient, endpoint, localPath string, headers map[string]string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("read artwork: %w", err)
	}
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "image/jpeg")
	req.ContentLength = int64(len(data))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Warn("artwork upload: response body close failed", "err", cerr.Error())
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload artwork: status %d, body: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ArtworkUploader is an optional capability for clients that support setting
// playlist artwork. Use a type assertion: if u, ok := c.API.(client.ArtworkUploader); ok {...}.
type ArtworkUploader interface {
	SetPlaylistArtwork(localPath string) error
}

// InPlaceReplacer is implemented by systems that can replace a playlist's
// songs without deleting it, which keeps its ID stable.
type InPlaceReplacer interface {
	ReplacesInPlace() bool
}

// KeeperFinder is implemented by systems that can list the songs the user wants to keep:
// starred songs and songs in the user's own playlists, except smart playlists and the ones named in exclude.
type KeeperFinder interface {
	KeeperSongs(exclude []string) ([]models.LibrarySong, error)
}

// NewClient initializes a client and sets up authentication
func NewClient(cfg *config.Config) (*Client, error) {
	c := &Client{
		System: cfg.System,
		Cfg:    &cfg.ClientCfg,
	}
	// Create http client with timeout
	httpClient := util.NewHttp(util.HttpClientConfig{
		Timeout: cfg.ClientCfg.HTTPTimeout,
	})

	switch c.System {

	case "emby":
		c.API = NewEmby(cfg.ClientCfg, httpClient)

	case "jellyfin":
		c.API = NewJellyfin(cfg.ClientCfg, httpClient)

	case "mpd":
		c.API = NewMPD(cfg.ClientCfg)

	case "plex":
		c.API = NewPlex(cfg.ClientCfg, httpClient)

	case "subsonic":
		c.API = NewSubsonic(cfg.ClientCfg, httpClient)

	default:
		return nil, fmt.Errorf("unknown system: %s. Use a supported system (emby, jellyfin, mpd, plex, or subsonic)", c.System)
	}

	if err := c.systemSetup(); err != nil { // Run setup automatically
		return nil, fmt.Errorf("setup failed: %w", err)
	}

	return c, nil
}

// TriggerRefresh Runs a trigger to refresh the users app music library
// Useful for one-shot operations
func TriggerRefresh(cfg *config.Config) error {
	c, err := NewClient(cfg)
	if err != nil {
		return fmt.Errorf("client setup: %w", err)
	}
	return c.API.RefreshLibrary()
}

// systemSetup checks needed credentials and initializes the selected system
func (c *Client) systemSetup() error {
	switch c.System {
	case "subsonic":
		if c.Cfg.Creds.User == "" || c.Cfg.Creds.Password == "" {
			return fmt.Errorf("Subsonic USER and PASSWORD are required")
		}
		return c.API.GetAuth()

	case "jellyfin":
		if c.Cfg.Creds.APIKey == "" && c.Cfg.AdminCreds.APIKey == "" {
			return fmt.Errorf("Jellyfin API_KEY or ADMIN_API_KEY is required")
		}
		if c.Cfg.Creds.User == "" {
			slog.Warn("It is recommended to set SYSTEM_USERNAME for Jellyfin")
		}
		if err := c.API.AddHeader(); err != nil {
			return err
		}
		return c.API.GetLibrary()

	case "mpd":
		if c.Cfg.PlaylistDir == "" {
			return fmt.Errorf("MPD PLAYLIST_DIR is required")
		}
		return nil

	case "plex":
		if (c.Cfg.Creds.User == "" || c.Cfg.Creds.Password == "") && c.Cfg.Creds.APIKey == "" {
			return fmt.Errorf("Plex USER/PASSWORD or API_KEY is required")
		}
		if err := c.API.AddHeader(); err != nil {
			return err
		}

		if c.Cfg.Creds.APIKey == "" {
			if err := c.API.GetAuth(); err != nil {
				return err
			}
		}

		if err := c.API.AddHeader(); err != nil {
			return err
		}
		return c.API.GetLibrary()

	case "emby":
		if c.Cfg.Creds.APIKey == "" {
			return fmt.Errorf("Emby API_KEY is required")
		}
		if err := c.API.AddHeader(); err != nil {
			return err
		}
		return c.API.GetLibrary()

	default:
		return fmt.Errorf("unknown system: %s. Use a supported system (emby, jellyfin, mpd, plex, or subsonic)", c.System)
	}
}

func (c *Client) CheckTracks(tracks []*models.Track) error {
	if err := c.API.SearchSongs(tracks); err != nil {
		return fmt.Errorf("SearchSongs failed: %s", err.Error())
	}
	return nil
}

func (c *Client) CreatePlaylist(tracks []*models.Track) error {
	if c.System == "" {
		return fmt.Errorf("could not get music system")
	}

	if err := c.API.RefreshLibrary(); err != nil {
		return fmt.Errorf("[%s] failed to schedule a library scan: %s", c.System, err.Error())
	}
	slog.Info("Refreshing library...", "system", c.System)
	if !c.API.CheckRefreshState() {
		slog.Debug("could not check library refresh state, either the client doesn't support it or threw an error")
		slog.Debug("falling back on SLEEP env variable")
		time.Sleep(time.Duration(c.Cfg.Sleep) * time.Minute)
	}

	if err := c.API.SearchSongs(tracks); err != nil { // search newly added songs
		slog.Warn("SearchSongs failed", "context", err)
	}
	for _, track := range tracks {
		if !track.Present {
			slog.Info("track not found in library, leaving it out of the playlist", "system", c.System, "title", track.CleanTitle, "artist", track.MainArtist)
		}
	}
	if err := c.API.CreatePlaylist(tracks); err != nil {
		return fmt.Errorf("[%s] failed to create playlist: %s", c.System, err.Error())
	}

	if err := c.API.UpdatePlaylist(); err != nil {
		return fmt.Errorf("[%s] failed to update playlist: %s", c.System, err.Error())
	}
	return nil
}

func (c *Client) DeletePlaylist() error {
	if err := c.API.SearchPlaylist(); err != nil {
		return fmt.Errorf("SearchPlaylist failed: %v", err)
	}
	if r, ok := c.API.(InPlaceReplacer); ok && r.ReplacesInPlace() {
		return nil // CreatePlaylist replaces the found playlist's songs
	}
	if err := c.API.DeletePlaylist(); err != nil {
		return fmt.Errorf("[%s] failed to delete playlist: %s", c.System, err.Error())
	}
	return nil
}

func BestMatch(track *models.Track, results []SearchResult, minScore int) (SearchResult, bool) {
	bestScore := -1
	var best SearchResult
	nmTrack := NormalisedTrack{
		CleanTitle: util.NormalizeTitle(track.CleanTitle),
		MainArtist: track.MainArtist,
		Album: track.Album,
		File: filepath.Base(track.File),
		Duration: track.Duration,
		MBTrackID: track.MusicBrainzTrackID,
		MBReleaseTrackID: track.MusicBrainzReleaseTrackID,
		Language: util.Language(track.CleanTitle, track.Title, track.Album)}

	for _, r := range results {
		score := rankResult(nmTrack, r)
		if score > bestScore {
			bestScore = score
			best = r
		}
		if bestScore == definitiveMatchScore {
			break
		}
	}
	if bestScore < minScore {
    	return SearchResult{}, false
	}
	best.Score = bestScore
	return best, true
}

const definitiveMatchScore = 1000
func rankResult(track NormalisedTrack, r SearchResult) int {
	score := 0

	resultTitle := util.NormalizeTitle(r.Title)

	if (track.MBTrackID != "" && r.MBID == track.MBTrackID) || (track.MBReleaseTrackID != "" && r.MBID == track.MBReleaseTrackID) {
		return definitiveMatchScore // definitive match
	}
	// Dubbed versions share a title: when both sides name a language and they differ, it's another song.
	if lang := util.Language(r.Title, r.Album); track.Language != "" && lang != "" && lang != track.Language {
		return 0
	}
	if track.CleanTitle == resultTitle {
		score += 45
	} else if len(track.CleanTitle) > 3 && (strings.Contains(resultTitle, track.CleanTitle) || strings.Contains(track.CleanTitle, resultTitle)) {
		score += 25
	}
	// An empty album is "contained" in every album name: only compare two real albums.
	if track.Album == "" || r.Album == "" {
	} else if strings.EqualFold(track.Album, r.Album) {
		score += 20
	} else if util.ContainsFold(track.Album, r.Album) || util.ContainsFold(r.Album, track.Album) {
		score += 15
	}
	if strings.EqualFold(track.MainArtist, r.Artist) || (len(r.Artists) > 0 && strings.EqualFold(r.Artists[0], track.MainArtist)) {
		score += 30
	} else if (util.ContainsFold(track.MainArtist, r.Artist) || util.ContainsFold(r.Artist, track.MainArtist)) || (len(r.Artists) > 0 && util.ContainsFold(r.Artists[0], track.MainArtist)) {
		score += 15
	}

	resultFile := filepath.Base(r.Path)

	if track.File != "" && strings.EqualFold(track.File, resultFile) {
		score += 50
	}
	durationSet := track.Duration != 0 && r.Duration != 0
	if durationSet {
		durationDiff := util.Abs(r.Duration-track.Duration/1000)
		switch {
		case durationDiff < 3:
			score += 10
		case durationDiff < 10:
			score += 5
		case durationDiff > 30:
			score -= 10
		}
}
	return score
}
// A video kept as-is (no official audio found) has a song name guessed from its title, which can
// carry extra words ("Kovai Gethu Anthem" vs the library's "Kovai Gethu"). keptVideoMatch accepts a
// library song for that name, or a shorter one, only with proof, because a wrong "already have it"
// leaves the playlist song missing, which is worse than a duplicate download.
var (
	// Words that describe the upload, not the song: dropping one needs one proof.
	uploadWords = wordSet("tamil telugu hindi malayalam kannada anthem music song songs video lyric lyrics lyrical official audio full promo hd 4k 8k")
	// Words that make it a different recording: video and library song must name the same ones.
	versionWords = wordSet("female male remix reprise unplugged lofi instrumental karaoke cover acoustic live sad duet mashup extended rendition theme bgm slowed reverb nightcore 8d")
	sameVersion  = map[string]string{"vivo": "live", "現場": "live", "现场": "live", "ライブ": "live", "伴奏": "instrumental", "리믹스": "remix"}
	// Fan edits are kept as their video: never the official song in the library (same list as ytmusic_playlist.py).
	fanEditRe = regexp.MustCompile(`(?i)\b(?:slowed|reverb|sped ?up|speed ?(?:up|down)|nightcore|8d|bass ?boosted)\b`)
	bracketRe    = regexp.MustCompile(`[(\[（【][^)\]）】]*[)\]）】]`)
	languages    = wordSet("tamil telugu hindi malayalam kannada")
	artistSepRe  = regexp.MustCompile(`(?i),|;|&|/|\s+(?:feat\.?|ft\.?|x|and|with)\s+`)
	titlePartRe  = regexp.MustCompile(`[|｜]|\s[-–—]\s`)
	nonWordRe    = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

const maxDroppedWords = 4

// Spelling differences when Tamil etc. is written in English ("Andangkaka"/"Andangakka",
// "Alagaro"/"Azhagaro"): the same list as the YouTube Music importer (ytmusic_playlist.py).
var spellingRules = strings.NewReplacer("dh", "d", "th", "t", "zh", "l", "sh", "s", "w", "v", "ee", "i", "oo", "u")

func spelled(s string) string {
	var b strings.Builder
	var last rune
	for _, r := range util.NormalizeTitle(s) {
		if r != last { // double letters count once
			b.WriteRune(r)
		}
		last = r
	}
	return spellingRules.Replace(b.String())
}

// sameSpelling: the two titles are the same words spelled differently (85% alike after the rules).
func sameSpelling(a, b string) bool {
	x, y := []rune(spelled(a)), []rune(spelled(b))
	if len(x) == 0 || len(y) == 0 {
		return false
	}
	// Levenshtein distance
	prev := make([]int, len(y)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(x); i++ {
		cur := make([]int, len(y)+1)
		cur[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return float64(prev[len(y)]) <= 0.15*float64(max(len(x), len(y)))
}

func wordSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.Fields(s) {
		set[w] = true
	}
	return set
}

func words(s string) []string {
	return strings.Fields(nonWordRe.ReplaceAllString(strings.ToLower(s), " "))
}

// containsWords reports whether needle appears as consecutive whole words in hay, and has at
// least minLetters letters, so "Mano" is not found in "Manoj" and "A" proves nothing.
func containsWords(hay, needle []string, minLetters int) bool {
	if len(needle) == 0 || len(strings.Join(needle, "")) < minLetters {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if slices.Equal(hay[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

// shorterNames drops words from the end, longest name first: "Kovai Gethu Anthem" -> "Kovai Gethu", "Kovai".
func shorterNames(name string) []string {
	w := strings.Fields(name)
	var out []string
	for n := len(w) - 1; n >= 1 && len(w)-n <= maxDroppedWords; n-- {
		out = append(out, strings.Join(w[:n], " "))
	}
	return out
}

// versions lists the version words in text: "Lego House (Live)" -> {live}.
func versions(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range words(text) {
		if v, ok := sameVersion[w]; ok {
			w = v
		}
		if versionWords[w] {
			out[w] = true
		}
	}
	return out
}

// keptVideoMatch: is library song r the song of kept video track, searched as name (its song name,
// or that name without its last words)?
func keptVideoMatch(track *models.Track, name string, r SearchResult) bool {
	full := strings.TrimSpace(track.CleanTitle)
	dropped := words(strings.TrimPrefix(full, strings.TrimSpace(name)))
	exact := util.NormalizeTitle(r.Title) == util.NormalizeTitle(name)
	if track.VideoTitle == "" || fanEditRe.MatchString(track.VideoTitle) || (len(dropped) > 0 && track.NameIsSong) || !(exact || sameSpelling(r.Title, name)) {
		return false
	}
	// the video's own version (its name and brackets; "| Love Duet Melody" describes the upload)
	// must be the library song's version, both ways
	video := versions(full + " " + strings.Join(bracketRe.FindAllString(track.VideoTitle, -1), " "))
	if !maps.Equal(video, versions(r.Title)) {
		return false
	}
	needed, videoLang := 1, util.Language(track.VideoTitle)
	for _, w := range dropped {
		if languages[w] {
			videoLang = w
		}
		if !uploadWords[w] {
			needed = 2
		}
	}
	// a short common word ("Kanne", "Dil") is part of many song names
	if sw := words(name); len(sw) == 1 && len(sw[0]) < 8 {
		needed = 2
	}
	if lang := util.Language(r.Title, r.Album); videoLang != "" && lang != "" && lang != videoLang {
		return false
	}
	videoSecs := track.VideoDuration / 1000
	// A video edit can cut a verse (283 s video, 300 s song), but not a whole minute.
	if videoSecs > 0 && r.Duration > videoSecs+60 {
		return false
	}

	title := words(track.VideoTitle)
	length := videoSecs > 0 && r.Duration > 0 && util.Abs(r.Duration-videoSecs) <= 3
	listed := map[string]bool{}
	for _, a := range artistSepRe.Split(track.Artist, -1) {
		listed[strings.Join(words(a), " ")] = true
	}
	singer := false
	for _, a := range artistSepRe.Split(strings.Join(append([]string{r.Artist}, r.Artists...), ","), -1) {
		aw := words(a)
		if containsWords(title, aw, 4) || (len(aw) > 0 && listed[strings.Join(aw, " ")]) {
			singer = true
			break
		}
	}
	// the film: the library album named in the video title, or a title part naming the album.
	// A single's album is the song name itself, which proves nothing.
	film := false
	if album := words(util.CleanSearchTitle(r.Album)); !slices.Equal(album, words(util.CleanSearchTitle(r.Title))) {
		film = containsWords(title, album, 5)
		for _, part := range titlePartRe.Split(track.VideoTitle, -1)[1:] {
			film = film || containsWords(album, words(part), 5)
		}
	}
	if !exact { // spelled differently: only when the video names the singer, and one more proof
		if !singer {
			return false
		}
		needed = max(needed, 2)
	}
	proofs := 0
	for _, p := range []bool{length, singer, film} {
		if p {
			proofs++
		}
	}
	return proofs >= needed
}
