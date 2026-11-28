package client

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/url"

	"explo/src/config"
	"explo/src/models"
	"explo/src/util"
)

type FailedResp struct {
	SubsonicResponse struct {
		Status        string `json:"status"`
		Error         struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"subsonic-response"`
}

type SubResponse struct {
	SubsonicResponse struct {
		Status        string        `json:"status"`
		Version       string        `json:"version"`
		Type          string        `json:"type"`
		ServerVersion string        `json:"serverVersion"`
		SearchResult3 struct {
			Song []struct {
				ID            string    `json:"id"`
				Title         string    `json:"title"`
				Artist        string    `json:"artist"`
				Album         string    `json:"album"`
				Duration      int       `json:"duration"`
				MusicBrainzID string    `json:"musicBrainzId"`
				Path          string    `json:"path"`
			} `json:"song"`
		} `json:"searchResult3"`
		Playlists     struct {
			Playlist []Playlist `json:"playlist"`
		} `json:"playlists"`
		Playlist      Playlist `json:"playlist"`
	} `json:"subsonic-response"`
}

type Playlist struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Comment   string    `json:"comment,omitempty"`
	SongCount int       `json:"songCount"`
	Duration  int       `json:"duration"`
	Public    bool      `json:"public"`
	Owner     string    `json:"owner"`
	Created   time.Time `json:"created"`
	Changed   time.Time `json:"changed"`
	CoverArt  string    `json:"coverArt"`
	ValidUntil string   `json:"validUntil,omitempty"` // OpenSubsonic, only set on (evaluated) smart playlists
}

type subsonicSong struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Artist        string `json:"artist"`
	MusicBrainzID string `json:"musicBrainzId"`
}

type keeperResp struct {
	SubsonicResponse struct {
		Starred2 struct {
			Song []subsonicSong `json:"song"`
		} `json:"starred2"`
		Playlists struct {
			Playlist []Playlist `json:"playlist"`
		} `json:"playlists"`
		Playlist struct {
			Playlist
			Entry []subsonicSong `json:"entry"`
		} `json:"playlist"`
	} `json:"subsonic-response"`
}

type ScanState struct {
	SubsonicResponse struct {
		Status        string `json:"status"`
		Version       string `json:"version"`
		Type          string `json:"type"`
		ServerVersion string `json:"serverVersion"`
		OpenSubsonic  bool   `json:"openSubsonic"`
		ScanStatus    struct {
			Scanning    bool      `json:"scanning"`
			Count       int       `json:"count"`
			FolderCount int       `json:"folderCount"`
			LastScan    time.Time `json:"lastScan"`
			ScanType    string    `json:"scanType"`
			ElapsedTime int       `json:"elapsedTime"`
		} `json:"scanStatus"`
	} `json:"subsonic-response"`
}

type Subsonic struct {
	Token string
	Salt string
	HttpClient *util.HttpClient
	Cfg config.ClientConfig
}

func NewSubsonic(cfg config.ClientConfig, httpClient *util.HttpClient) *Subsonic {
	return &Subsonic{Cfg: cfg,
		HttpClient: httpClient}
}

func (c *Subsonic) AddHeader() error {
	return nil
}

func (c *Subsonic) GetAuth() error { // Generate salt and token
	var salt = make([]byte, 6)


	_, err := rand.Read(salt)
	if err != nil {
		return fmt.Errorf("failed to read salt: %s", err.Error())
	}

	saltStr := base64.RawURLEncoding.EncodeToString(salt)
	passSalt := fmt.Sprintf("%s%s", c.Cfg.Creds.Password, saltStr)

	token := fmt.Sprintf("%x", md5.Sum([]byte(passSalt)))

	c.Token = url.PathEscape(token)
	c.Salt = url.PathEscape(saltStr)
	return nil
}

func (c *Subsonic) GetLibrary() error {
	return nil
}

func (c *Subsonic) AddLibrary() error {
	return nil
}

func (c *Subsonic) SearchSongs(tracks []*models.Track) error {
	for _, track := range tracks {
		searchQuery := util.CleanSearchTitle(track.CleanTitle)

		if track.VideoTitle != "" { // a kept video: its name is a guess, so it needs proof
			if err := c.searchKeptVideo(track, searchQuery); err != nil {
				return err
			}
			continue
		}

		// most specific first: the MusicBrainz recording ID matches exactly, while a title search
		// only returns the server's first results, which can miss the track for common titles.
		// Title + artist narrows those results; the bare title catches differently spelled artists.
		for _, query := range searchQueries(track, searchQuery) {
			matched, err := c.searchSong(track, query)
			if err != nil {
				return err
			}
			if matched {
				break
			}
		}
	}
	return nil
}

// searchQueries lists the search3 queries for a track, most specific first.
func searchQueries(track *models.Track, title string) []string {
	var queries []string
	if track.MusicBrainzTrackID != "" {
		queries = append(queries, track.MusicBrainzTrackID)
	}
	if artist := strings.TrimSpace(track.MainArtist); artist != "" {
		queries = append(queries, title+" "+artist)
	}
	queries = append(queries, title)

	// Search matches every word, so "Hukum - Thalaivar Alappara" misses a library title "Hukum".
	if base, _, found := strings.Cut(title, " - "); found && strings.TrimSpace(base) != "" {
		queries = append(queries, strings.TrimSpace(base))
	}
	return queries
}

// searchSong runs one search3 query and, on a match above the score threshold, marks track as present
func (c *Subsonic) searchSong(track *models.Track, searchQuery string) (bool, error) {
	songs, err := c.searchSongs(searchQuery, 0)
	if err != nil {
		return false, err
	}
	if len(songs) == 0 {
		slog.Debug("no results returned for query", "query", searchQuery, "trackArtist", track.MainArtist, "trackAlbum", track.Album)
		return false, nil
	}
	trackMatch, ok := BestMatch(track, songs, c.Cfg.MatchScore)
	if !ok {
		slog.Debug("failed to find match, no results returned above threshold score", "threshold", c.Cfg.MatchScore, "searchQuery", searchQuery)
		return false, nil
	}
	track.ID = trackMatch.ID
	track.Present = true
	slog.Debug("matched track", "searchQuery", searchQuery, "matchScore", trackMatch.Score, "trackArtist", trackMatch.Artist, "trackAlbum", trackMatch.Album)
	return true, nil
}

// searchKeptVideo looks for the library song a kept video stands for. A video's name is only a
// guess, so a match needs proof (see keptVideoMatch) instead of a plain score threshold.
func (c *Subsonic) searchKeptVideo(track *models.Track, searchQuery string) error {
	songs, err := c.searchSongs(searchQuery, 0)
	if err != nil {
		return err
	}

	if base, _, found := strings.Cut(searchQuery, " - "); len(songs) == 0 && found && strings.TrimSpace(base) != "" {
		slog.Debug("[subsonic] retrying search without subtitle", "query", base)
		if songs, err = c.searchSongs(strings.TrimSpace(base), 0); err != nil {
			return err
		}
	}

	if len(songs) == 0 && track.MusicBrainzTrackID != "" {
		slog.Debug("[subsonic] using fallback MB TrackID search", "mbid", track.MusicBrainzTrackID)
		if songs, err = c.searchSongs(track.MusicBrainzTrackID, 0); err != nil {
			return err
		}
	}

	trackMatch, ok, err := c.matchKeptVideo(track, searchQuery, songs)
	if err != nil {
		return err
	}
	if !ok {
		slog.Debug("no library match", "threshold", c.Cfg.MatchScore, "searchQuery", searchQuery, "results", len(songs))
		return nil
	}
	track.ID = trackMatch.ID
	track.Present = true
	slog.Debug("matched track", "searchQuery", searchQuery, "matchScore", trackMatch.Score, "trackArtist", trackMatch.Artist, "trackAlbum", trackMatch.Album)
	return nil
}

// matchKeptVideo checks a kept video's name against the songs found for it, then retries the name
// without its last words ("Kovai Gethu Anthem" -> "Kovai Gethu"); see keptVideoMatch.
func (c *Subsonic) matchKeptVideo(track *models.Track, name string, songs []SearchResult) (SearchResult, bool, error) {
	for i, try := range append([]string{name}, shorterNames(name)...) {
		if i > 0 {
			var err error
			if songs, err = c.searchSongs(try, 50); err != nil { // a shorter name is more common: look further
				return SearchResult{}, false, err
			}
		}
		for _, r := range songs {
			if keptVideoMatch(track, try, r) {
				slog.Info("library already has this video's song",
					"video", track.VideoTitle, "library", r.Title, "artist", r.Artist, "album", r.Album)
				return r, true, nil
			}
		}
	}
	// Spelled differently ("Andangkaka" vs "Andangakka"), search can't find it: look at every song
	// starting with the name's first letters.
	if fields := strings.Fields(name); len(fields) > 0 && len([]rune(fields[0])) >= 4 {
		songs, err := c.searchSongs(string([]rune(fields[0])[:4]), 100)
		if err != nil {
			return SearchResult{}, false, err
		}
		for _, try := range append([]string{name}, shorterNames(name)...) {
			for _, r := range songs {
				if keptVideoMatch(track, try, r) {
					slog.Info("library already has this video's song, spelled differently",
						"video", track.VideoTitle, "library", r.Title, "artist", r.Artist, "album", r.Album)
					return r, true, nil
				}
			}
		}
	}
	return SearchResult{}, false, nil
}

// searchSongs runs a Subsonic search3; count 0 keeps the server's default (20).
func (c *Subsonic) searchSongs(query string, count int) ([]SearchResult, error) {
	reqParam := fmt.Sprintf("search3?query=%s&f=json", url.QueryEscape(query))
	if count > 0 {
		reqParam += fmt.Sprintf("&songCount=%d&artistCount=0&albumCount=0", count)
	}
	body, err := c.subsonicRequest(reqParam)
	if err != nil {
		return nil, err
	}
	var resp SubResponse
	if err := util.ParseResp(body, &resp); err != nil {
		return nil, err
	}
	songs := resp.SubsonicResponse.SearchResult3.Song
	out := make([]SearchResult, 0, len(songs))
	for _, song := range songs {
		out = append(out, SearchResult{
			ID:       song.ID,
			Title:    song.Title,
			Album:    song.Album,
			Artist:   song.Artist,
			Path:     song.Path,
			Duration: song.Duration,
			MBID:     song.MusicBrainzID,
		})
	}
	return out, nil
}

func (c *Subsonic) RefreshLibrary() error {
	if c.Cfg.AdminCreds.User != "" && c.Cfg.AdminCreds.Password != "" {
		adminCfg := c.Cfg
		adminCfg.Creds = config.Credentials{User: c.Cfg.AdminCreds.User, Password: c.Cfg.AdminCreds.Password}
		adminClient := NewSubsonic(adminCfg, c.HttpClient)

		if err := adminClient.GetAuth(); err != nil {
			return err
		}
		return adminClient.startScan()
	}

	return c.startScan()
}

func (c *Subsonic) startScan() error {
	reqParam := "startScan?f=json"
	if _, err := c.subsonicRequest(reqParam); err != nil {
		return err
	}
	return nil
}

func (c *Subsonic) CheckRefreshState() bool {
	time.Sleep(500 * time.Millisecond) // small sleep to avoid calling scan status too fast
	var state ScanState
	reqParam := "getScanStatus?f=json"

	for {
		body, err := c.subsonicRequest(reqParam)
		if err != nil {
			slog.Warn("could not check scan status", "err", err)
			return false
		}
		if err = json.Unmarshal(body, &state); err != nil {
			slog.Warn("failed to unmarshal scan status response", "err", err)
			return false
		}
		if !state.SubsonicResponse.ScanStatus.Scanning {
			return true
		}
		slog.Debug("Library scan still ongoing")
		time.Sleep(15 * time.Second)
	}
}

// ReplacesInPlace reports that an existing playlist's songs are replaced instead of deleting it, keeping its ID
func (c *Subsonic) ReplacesInPlace() bool {
	return true
}

func (c *Subsonic) CreatePlaylist(tracks []*models.Track) error {
	var trackIDs strings.Builder
	for _, track := range tracks { // build songID parameters
		fmt.Fprintf(&trackIDs, "&songId=%s", track.ID)
	}

	if c.Cfg.PlaylistID != "" { // replace songs of the existing playlist
		err := c.createPlaylist("playlistId="+url.QueryEscape(c.Cfg.PlaylistID), trackIDs.String())
		if err == nil {
			return nil
		}
		slog.Warn("[subsonic] failed to replace playlist songs, creating a new playlist", "playlist", c.Cfg.PlaylistName, "id", c.Cfg.PlaylistID, "err", err.Error())
		c.Cfg.PlaylistID = ""
	}

	return c.createPlaylist("name="+url.QueryEscape(c.Cfg.PlaylistName), trackIDs.String())
}

// createPlaylist sends createPlaylist with target (name=... or playlistId=...) and stores the returned ID
func (c *Subsonic) createPlaylist(target, trackIDs string) error {
	reqParam := fmt.Sprintf("createPlaylist?%s%s&f=json", target, trackIDs)

	body, err := c.subsonicRequest(reqParam)
	if err != nil {
		return err
	}

	var resp SubResponse
	if err := util.ParseResp(body, &resp); err != nil {
        return err
    }

	if id := resp.SubsonicResponse.Playlist.ID; id != "" {
		c.Cfg.PlaylistID = id
	}
	return nil
}

func (c *Subsonic) SearchPlaylist() error {
	reqParam := "getPlaylists?f=json"

	body, err := c.subsonicRequest(reqParam)
	if err != nil {
		return err
	}

	var resp SubResponse
	if err := util.ParseResp(body, &resp); err != nil {
        return err
    }

	for _, playlist := range resp.SubsonicResponse.Playlists.Playlist {
		// getPlaylists also returns other users' public playlists, only use our own
		if playlist.Name == c.Cfg.PlaylistName && strings.EqualFold(playlist.Owner, c.Cfg.Creds.User) {
			c.Cfg.PlaylistID = playlist.ID
			return nil

		}
	}
	return nil
}

func (c *Subsonic) UpdatePlaylist() error {
	reqParam := fmt.Sprintf("updatePlaylist?playlistId=%s&comment=%s&f=json&public=%t",c.Cfg.PlaylistID, url.QueryEscape(c.Cfg.PlaylistDescr), c.Cfg.PublicPlaylist)

	if _, err := c.subsonicRequest(reqParam); err != nil {
		return err
	}
	return nil
}

func (c *Subsonic) DeletePlaylist() error {
	if c.Cfg.PlaylistID == "" {
		return nil
	}
	reqParam := fmt.Sprintf("deletePlaylist?id=%s&f=json", c.Cfg.PlaylistID)

	if _, err := c.subsonicRequest(reqParam); err != nil {
		return err
	}
	return nil
}

// KeeperSongs returns the user's starred songs and the songs in their own playlists,
// skipping smart playlists and the playlists named in exclude
func (c *Subsonic) KeeperSongs(exclude []string) ([]models.LibrarySong, error) {
	var starred keeperResp
	if err := c.getKeeperResp("getStarred2?f=json", &starred); err != nil {
		return nil, fmt.Errorf("getStarred2: %w", err)
	}
	songs := toLibrarySongs(starred.SubsonicResponse.Starred2.Song)
	slog.Debug("found starred songs", "songs", len(songs))

	var lists keeperResp
	if err := c.getKeeperResp("getPlaylists?f=json", &lists); err != nil {
		return nil, fmt.Errorf("getPlaylists: %w", err)
	}
	for _, p := range lists.SubsonicResponse.Playlists.Playlist {
		// getPlaylists also returns other users' public playlists
		if !strings.EqualFold(p.Owner, c.Cfg.Creds.User) || slices.Contains(exclude, p.Name) || p.ValidUntil != "" {
			continue
		}
		var list keeperResp
		if err := c.getKeeperResp("getPlaylist?id="+url.QueryEscape(p.ID)+"&f=json", &list); err != nil {
			return nil, fmt.Errorf("getPlaylist %q: %w", p.Name, err)
		}
		if list.SubsonicResponse.Playlist.ValidUntil != "" { // smart playlist, evaluated by this request
			continue
		}
		entries := list.SubsonicResponse.Playlist.Entry
		slog.Debug("found playlist songs", "playlist", p.Name, "songs", len(entries))
		songs = append(songs, toLibrarySongs(entries)...)
	}
	return songs, nil
}

func (c *Subsonic) getKeeperResp(reqParams string, resp *keeperResp) error {
	body, err := c.subsonicRequest(reqParams)
	if err != nil {
		return err
	}
	return util.ParseResp(body, resp)
}

func toLibrarySongs(songs []subsonicSong) []models.LibrarySong {
	out := make([]models.LibrarySong, 0, len(songs))
	for _, s := range songs {
		out = append(out, models.LibrarySong{RecordingID: s.MusicBrainzID, Title: s.Title, Artist: s.Artist})
	}
	return out
}

func (c *Subsonic) subsonicRequest(reqParams string) ([]byte, error) {

	reqURL := fmt.Sprintf("%s/rest/%s&u=%s&t=%s&s=%s&v=%s&c=%s",c.Cfg.URL, reqParams, c.Cfg.Creds.User, c.Token, c.Salt, c.Cfg.Subsonic.Version, c.Cfg.ClientID)
	body, err := c.HttpClient.MakeRequest("GET", reqURL, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to make request %s", err.Error())
	}

	var checkResp FailedResp
	if err = util.ParseResp(body, &checkResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal request %s", err.Error())
	} else if checkResp.SubsonicResponse.Status == "failed" {
		return nil, fmt.Errorf("%s", checkResp.SubsonicResponse.Error.Message)
	}
	return body, nil
}