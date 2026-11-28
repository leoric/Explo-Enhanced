package downloader

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"explo/src/models"
	"explo/src/util"

	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// fileAction is what cleanup does with a downloaded audio file
type fileAction int

const (
	actionDelete fileAction = iota
	actionMove              // keeper: move to KEEPERS_DIR
	actionLeave             // part of the current run: leave in place
)

// fileTags holds the tags used to match a downloaded file against songs
type fileTags struct {
	RecordingID string
	Title       string
	Artist      string
}

// songSet matches files by MusicBrainz recording ID or, as a fallback, by title and main artist
type songSet struct {
	recordingIDs map[string]bool
	artists      map[string][]string // normalized title -> artists of songs with that title
}

func newSongSet(songs []models.LibrarySong) songSet {
	s := songSet{recordingIDs: make(map[string]bool), artists: make(map[string][]string)}
	for _, song := range songs {
		if id := strings.ToLower(strings.TrimSpace(song.RecordingID)); id != "" {
			s.recordingIDs[id] = true
		}
		if title := util.NormalizeTitle(song.Title); title != "" && strings.TrimSpace(song.Artist) != "" {
			s.artists[title] = append(s.artists[title], song.Artist)
		}
	}
	return s
}

func (s songSet) matches(tags fileTags) bool {
	if id := strings.ToLower(strings.TrimSpace(tags.RecordingID)); id != "" && s.recordingIDs[id] {
		return true
	}
	// Explo writes multiple artists as "A; B", match on the main artist
	artist := strings.TrimSpace(strings.Split(tags.Artist, ";")[0])
	if artist == "" {
		return false
	}
	for _, a := range s.artists[util.NormalizeTitle(tags.Title)] {
		if util.ContainsFold(a, artist) || util.ContainsFold(artist, a) {
			return true
		}
	}
	return false
}

// CleanupRules decides what cleanup does with each downloaded file: keepers (starred or in the user's
// own playlists) are moved, tracks of the current run are left in place, everything else is deleted
type CleanupRules struct {
	current  songSet
	keepers  *songSet // nil: KEEPERS_DIR not set, nothing is moved
	readTags func(path string) (fileTags, error)
}

// NewCleanupRules leaves the files of the current run's tracks in place, so they aren't deleted
// right before the library check that decides what to download
func NewCleanupRules(current []*models.Track) *CleanupRules {
	songs := make([]models.LibrarySong, 0, len(current))
	for _, t := range current {
		songs = append(songs, models.LibrarySong{RecordingID: t.MusicBrainzTrackID, Title: t.CleanTitle, Artist: t.MainArtist})
	}
	return &CleanupRules{current: newSongSet(songs), readTags: probeTags}
}

// WithKeepers moves files matching songs to KEEPERS_DIR, including tracks of the current run
func (r *CleanupRules) WithKeepers(songs []models.LibrarySong) *CleanupRules {
	keepers := newSongSet(songs)
	r.keepers = &keepers
	return r
}

func (r *CleanupRules) action(path string) (fileAction, error) {
	tags, err := r.readTags(path)
	if err != nil {
		return actionLeave, err
	}
	switch {
	case r.keepers != nil && r.keepers.matches(tags):
		return actionMove, nil
	case r.current.matches(tags):
		return actionLeave, nil
	default:
		return actionDelete, nil
	}
}

// probeTags reads a file's tags with ffprobe. Vorbis comments of ogg/opus files are stream tags,
// the other formats keep them on the container.
func probeTags(path string) (fileTags, error) {
	out, err := ffmpeg.ProbeWithTimeout(path, 30*time.Second, ffmpeg.KwArgs{"select_streams": "a"})
	if err != nil {
		return fileTags{}, err
	}
	var probe struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal([]byte(out), &probe); err != nil {
		return fileTags{}, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	all := []map[string]string{probe.Format.Tags}
	for _, s := range probe.Streams {
		all = append(all, s.Tags)
	}
	var tags fileTags
	for _, m := range all {
		for key, val := range m {
			// "MusicBrainz Track Id" (ID3/MP4) and "MUSICBRAINZ_TRACKID" (Vorbis) hold the recording ID
			var field *string
			switch util.AlnumOnly(strings.ToLower(key)) {
			case "musicbrainztrackid":
				field = &tags.RecordingID
			case "title":
				field = &tags.Title
			case "artist":
				field = &tags.Artist
			}
			if field != nil && *field == "" {
				*field = strings.TrimSpace(val)
			}
		}
	}
	return tags, nil
}
