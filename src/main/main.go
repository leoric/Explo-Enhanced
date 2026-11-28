package main

import (
	"encoding/json"
	"explo/src/logging"
	"explo/src/models"
	"explo/src/web/backend"
	"explo/src/web/backend/playlist"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"explo/src/client"
	"explo/src/config"
	"explo/src/discovery"
	"explo/src/downloader"
	"explo/src/util"
)

type Song struct {
	Title  string
	Artist string
	Album  string
}

// loadCustomTracks reads a custom playlist's track cache and returns them as
// models.Track slices, bypassing the LB discovery step entirely.
func loadCustomTracks(dataDir, playlistID string) ([]*models.Track, string, error) {
	type cachedTrack struct {
		Title      string `json:"title"`
		Artist     string `json:"artist"`
		MainArtist string `json:"mainArtist"`
		Release    string `json:"release"`
		CoverURL   string `json:"coverUrl"`
		CoverPath          string `json:"coverPath"`
		MusicBrainzTrackID string `json:"mbTrackId,omitempty"`
		VideoID            string `json:"videoId,omitempty"`
		DurationMs         int    `json:"durationMs,omitempty"`
		VideoTitle         string `json:"videoTitle,omitempty"`
		VideoDurationMs    int    `json:"videoDurationMs,omitempty"`
		NameIsSong         bool   `json:"nameIsSong,omitempty"`
	}
	type cacheFile struct {
		Tracks []cachedTrack `json:"tracks"`
	}
	type customPlaylist struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	data, err := os.ReadFile(filepath.Join(dataDir, "cache", playlistID+".json"))
	if err != nil {
		return nil, "", fmt.Errorf("custom playlist %q not found in cache: %w", playlistID, err)
	}
	var c cacheFile
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, "", fmt.Errorf("failed to parse custom playlist cache: %w", err)
	}

	// Look up the human-readable name from metadata
	name := playlistID
	if meta, err := os.ReadFile(filepath.Join(dataDir, "custom-playlists.json")); err == nil {
		var all []customPlaylist
		if json.Unmarshal(meta, &all) == nil {
			for _, p := range all {
				if p.ID == playlistID {
					name = p.Name
					break
				}
			}
		}
	}

	tracks := make([]*models.Track, len(c.Tracks))
	for i, t := range c.Tracks {
		mainArtist := t.MainArtist
		if mainArtist == "" {
			mainArtist = t.Artist
		}
		tracks[i] = &models.Track{
			CleanTitle: t.Title,
			Title:      t.Title,
			Artist:     t.Artist,
			MainArtist: mainArtist,
			Album:      t.Release,
			CoverURL:   t.CoverURL,
			CoverPath:          t.CoverPath,
			MusicBrainzTrackID: t.MusicBrainzTrackID,
			VideoID:            t.VideoID,
			Duration:           t.DurationMs,
			VideoTitle:         t.VideoTitle,
			VideoDuration:      t.VideoDurationMs,
			NameIsSong:         t.NameIsSong,
		}
	}
	return tracks, name, nil
}

func initHttpClient() *util.HttpClient {
	return util.NewHttp(util.HttpClientConfig{
		Timeout: 10,
	})
}

// Inits debug, gets playlist name, if needed, handles deprecation
func setup(cfg *config.Config) {
	cfg.HandleDeprecation()
	logging.Init(cfg)
	cfg.GenPlaylistDetails()
}
func runSearchTest(cfg *config.Config, httpClient *util.HttpClient) {
	lb := discovery.NewListenBrainz(cfg.DiscoveryCfg, httpClient)
	track, err := lb.LookupRecording(cfg.Flags.SearchMBID)
	if err != nil {
		log.Fatalf("failed to resolve MBID %s from ListenBrainz: %s", cfg.Flags.SearchMBID, err)
	}
	slog.Info("resolved recording", "title", track.CleanTitle, "artist", track.MainArtist, "album", track.Album, "duration_ms", track.Duration)

	c, err := client.NewClient(cfg)
	if err != nil {
		log.Fatalf("failed to init client: %s", err)
	}
	tracks := []*models.Track{track}
	if err := c.CheckTracks(tracks); err != nil {
		slog.Warn("CheckTracks error", "err", err)
	}

	if track.Present {
		slog.Info("FOUND in library", "system", cfg.System, "key", track.ID)
	} else {
		slog.Info("NOT FOUND in library", "system", cfg.System)
	}
}
func main() {
	var cfg config.Config
	if err := cfg.GetFlags(); err != nil {
		log.Fatal(err)
	}
	cfg.ReadEnv()
	cfg.MergeFlags()
	setup(&cfg)

	httpClient := initHttpClient()

	if cfg.Flags.SearchMBID != "" {
		runSearchTest(&cfg, httpClient)
		return
	}

	if cfg.Flags.RefreshOnly {
		if err := client.TriggerRefresh(&cfg); err != nil {
			slog.Error("refresh-only failed", "err", err.Error())
			os.Exit(1)
		}
		slog.Info("library refresh triggered")
		return
	}

	slog.Info("Starting Explo...")

	if err := os.MkdirAll(cfg.DownloadCfg.Youtube.CoversDir, 0755); err != nil {
		slog.Error("failed making directory", "msg", err.Error())
	}

	if cfg.ServerCfg.Enabled && !cfg.Flags.PlaylistSet {

		exploPath, err := os.Executable()
		if err != nil {
			log.Fatal("could not determine executable path: ", err)
		}

		cfg.ServerCfg.ExploPath = exploPath
		srv := backend.NewServer(cfg.ServerCfg)
		log.Fatal(srv.Start())
	}

	slog.Info("Pulling playlist", "playlist", cfg.Flags.Playlist)

	var tracks []*models.Track
	var err error
	if strings.HasPrefix(cfg.Flags.Playlist, "custom-") {
		var playlistName string
		tracks, playlistName, err = loadCustomTracks(cfg.ServerCfg.WebDataDir, cfg.Flags.Playlist)
		if err == nil {
			cfg.ClientCfg.PlaylistName = playlistName
			if cfg.DownloadCfg.UseSubDir { // folder named after the playlist instead of Custom-<id>
				cfg.DownloadCfg.DownloadDir = filepath.Join(filepath.Dir(cfg.DownloadCfg.DownloadDir), util.CustomDirName(playlistName, cfg.Flags.Playlist))
			}
		}
	} else {
		disc := discovery.NewDiscoverer(cfg.DiscoveryCfg, httpClient)
		tracks, err = disc.Discover()
	}

	if err != nil {
		slog.Error(err.Error(), "notify", true)
		os.Exit(1)
	}
	allTracks := append([]*models.Track(nil), tracks...)
	if cfg.ServerCfg.WebDataDir != "" {
		playlist.WritePlaylistCache(cfg.ServerCfg.WebDataDir, cfg.Flags.Playlist, allTracks, nil)
		slog.Info("Saved playlist", "playlist", cfg.Flags.Playlist, "name", cfg.ClientCfg.PlaylistName, "tracks", len(allTracks), "folder", cfg.DownloadCfg.DownloadDir)
	}

	client, err := client.NewClient(&cfg)
	if err != nil {
		slog.Error(err.Error(), "notify", true)
		os.Exit(1)
	}
	downloader, err := downloader.NewDownloader(&cfg.DownloadCfg, httpClient, cfg.Flags.ExcludeLocal)
	if err != nil {
		slog.Error(err.Error(), "notify", true)
		os.Exit(1)
	}
	if cfg.ReplacePlaylist {
		err := client.DeletePlaylist()
		if err != nil {
			slog.Warn(err.Error(), "notify", true)
		}
	}
	if cfg.Flags.CleanDownloads && cfg.DownloadCfg.UseSubDir {
		cleanDownloads(&cfg, client, downloader, tracks)
	}
	if cfg.Flags.DownloadMode != "force" {
		if err := client.CheckTracks(tracks); err != nil { // Check if tracks exist on system before downloading
			slog.Warn(err.Error(), "notify", true)
		}
	}

	if cfg.Flags.DownloadMode != "skip" {
		enrichMissingTracks(&cfg, tracks, func(t []*models.Track) ([]*models.Track, error) {
			return discovery.EnrichTracks(httpClient, cfg.DiscoveryCfg.Listenbrainz, t)
		})
		downloader.StartDownload(&tracks)
		if len(tracks) == 0 {
			slog.Error("couldn't download any tracks", "notify", true)
			os.Exit(1)
		}
	}

	if err := client.CreatePlaylist(tracks); err != nil {
		slog.Warn(err.Error())
	} else {
		slog.Info("playlist created successfully", "system", cfg.System, "playlistName", cfg.ClientCfg.PlaylistName, "notify", true)
		uploadCustomPlaylistArtwork(&cfg, client)
	}
}

// cleanDownloads deletes the previous run's tracks, leaving the files of this run's tracks in place.
// With KEEPERS_DIR set, starred tracks and tracks in the user's own playlists are moved there instead;
// cleanup is skipped when they can't be determined.
func cleanDownloads(cfg *config.Config, c *client.Client, d *downloader.DownloadClient, tracks []*models.Track) {
	rules := downloader.NewCleanupRules(tracks)
	if strings.TrimSpace(cfg.DownloadCfg.KeepersDir) == "" {
		d.DeleteSongs(rules)
		return
	}
	finder, ok := c.API.(client.KeeperFinder)
	if !ok {
		slog.Warn("KEEPERS_DIR is only supported for subsonic, skipping cleanup of downloaded tracks", "system", cfg.System, "notify", true)
		return
	}
	songs, err := finder.KeeperSongs(cfg.ExploPlaylistNames())
	if err != nil {
		slog.Warn("could not get starred and playlisted songs, skipping cleanup of downloaded tracks", "context", err.Error(), "notify", true)
		return
	}
	slog.Info("checking downloaded tracks against starred and playlisted songs", "songs", len(songs))
	d.DeleteSongs(rules.WithKeepers(songs))
}

// enrichMissingTracks adds MusicBrainz metadata (ENRICH_TRACK_METADATA) to the tracks that will be
// downloaded. It runs after the library check, so tracks already in the library aren't looked up.
func enrichMissingTracks(cfg *config.Config, tracks []*models.Track, enrich func([]*models.Track) ([]*models.Track, error)) {
	// fresh-releases tracks already carry MusicBrainz metadata
	if !cfg.DiscoveryCfg.Listenbrainz.EnrichTrackMetadata || cfg.Flags.Playlist == "fresh-releases" {
		return
	}

	var idx []int
	var missing []*models.Track
	for i, t := range tracks {
		if !t.Present && t.MusicBrainzTrackID != "" {
			idx = append(idx, i)
			missing = append(missing, t)
		}
	}
	if len(missing) == 0 {
		return
	}

	enriched, err := enrich(missing)
	if err != nil {
		slog.Warn("failed to enrich track metadata", "error", err)
		return
	}
	for j, i := range idx { // enrichment returns new track structs, put them back in place
		tracks[i] = enriched[j]
	}
}

// uploadCustomPlaylistArtwork pushes a custom playlist's cached artwork to the music app
// after first successful creation. No-op for non-custom playlists, playlists without
// artwork, or clients that don't support artwork upload (Subsonic, MPD).
func uploadCustomPlaylistArtwork(cfg *config.Config, c *client.Client) {
	if !strings.HasPrefix(cfg.Flags.Playlist, "custom-") {
		return
	}
	cp := playlist.GetCustomPlaylist(cfg.ServerCfg.WebDataDir, cfg.Flags.Playlist)
	if cp == nil || cp.ArtworkURL == "" || cp.ArtworkUploaded {
		return
	}
	uploader, ok := c.API.(client.ArtworkUploader)
	if !ok {
		return
	}
	path := playlist.CustomPlaylistArtworkPath(cfg.ServerCfg.WebDataDir, cp.ID)
	if _, err := os.Stat(path); err != nil {
		slog.Warn("custom-playlists: artwork not cached locally, skipping upload", "id", cp.ID, "path", path)
		return
	}
	if err := uploader.SetPlaylistArtwork(path); err != nil {
		slog.Warn("custom-playlists: failed to upload playlist artwork", "id", cp.ID, "err", err.Error())
		return
	}
	if err := playlist.MarkCustomPlaylistArtworkUploaded(cfg.ServerCfg.WebDataDir, cp.ID); err != nil {
		slog.Warn("custom-playlists: artwork upload succeeded but flag not persisted", "id", cp.ID, "err", err.Error())
		return
	}
	slog.Info("custom-playlists: playlist artwork uploaded", "id", cp.ID, "system", cfg.System)
}
