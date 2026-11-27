package discovery

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"explo/src/models"
	"explo/src/util"
)

// On Repeat mixes the user's top recordings of three ListenBrainz stats ranges: last month's
// evergreens, this month's rotation and last week's current favourites.
const (
	onRepeatSize      = 30 // playlist length, filled up from last month's ranking
	onRepeatTierDepth = 10 // only the top of each range is considered, skipped duplicates aren't replaced
	onRepeatMinPlays  = 3  // below this, a recording's rank is mostly chance
	onRepeatFetch     = 50 // recordings fetched per range; merging and backfill need more than the top 10
)

// onRepeatRanges are the stats ranges in tier order. this_week is left out: it's thin, and
// ListenBrainz' stats lag behind recent listens. The weights favour recent ranges when ordering,
// so last month's top recordings don't lead the playlist for the whole following month.
var onRepeatRanges = []struct {
	statsRange string
	weight     float64
}{
	{"month", 0.7},
	{"this_month", 0.85},
	{"week", 1.0},
}

type topRecording struct {
	ArtistName    string   `json:"artist_name"`
	ArtistMbids   []string `json:"artist_mbids"`
	ListenCount   int      `json:"listen_count"`
	RecordingMbid string   `json:"recording_mbid"`
	ReleaseMbid   string   `json:"release_mbid"`
	ReleaseName   string   `json:"release_name"`
	TrackName     string   `json:"track_name"`
}

// key identifies a recording across rows and ranges. ListenBrainz splits a recording into
// several rows when its listens were submitted with different release names.
func (r topRecording) key() string {
	if r.RecordingMbid != "" {
		return r.RecordingMbid
	}
	return strings.ToLower(r.ArtistName) + "|" + strings.ToLower(r.TrackName)
}

func (r topRecording) artistKey() string {
	if len(r.ArtistMbids) > 0 && r.ArtistMbids[0] != "" {
		return r.ArtistMbids[0]
	}
	return strings.ToLower(r.ArtistName)
}

// fetchTopRecordings returns the user's top recordings for a stats range. A range ListenBrainz
// hasn't calculated stats for yet (204 No Content) returns no recordings and no error.
func (c *ListenBrainz) fetchTopRecordings(user, statsRange string, count int) ([]topRecording, error) {
	reqURL := fmt.Sprintf("https://api.listenbrainz.org/1/stats/user/%s/recordings?count=%d&range=%s",
		url.PathEscape(user), count, statsRange)
	body, err := c.HttpClient.MakeRequest("GET", reqURL, nil, c.Headers)
	if err != nil {
		return nil, fmt.Errorf("failed to get %s top recordings from ListenBrainz: %s", statsRange, err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var resp TopRecordings
	if err := util.ParseResp(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse %s top recordings: %s", statsRange, err)
	}
	return resp.Payload.Recordings, nil
}

// mergeRecordings sums the listen counts of rows with the same key and sorts the result by
// count, keeping ListenBrainz' order for ties.
func mergeRecordings(recs []topRecording) []topRecording {
	idx := make(map[string]int, len(recs))
	merged := make([]topRecording, 0, len(recs))
	for _, r := range recs {
		if i, ok := idx[r.key()]; ok {
			merged[i].ListenCount += r.ListenCount
			continue
		}
		idx[r.key()] = len(merged)
		merged = append(merged, r)
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].ListenCount > merged[j].ListenCount })
	return merged
}

// pickOnRepeat selects the playlist from the ranges' recordings, given in tier order with
// the first tier being the one to fill up from, and weights[i] being tier i's ordering weight.
// It returns the recordings strongest first.
func pickOnRepeat(tiers [][]topRecording, weights []float64) []topRecording {
	for i := range tiers {
		tiers[i] = mergeRecordings(tiers[i])
	}

	// a recording's strength is its count relative to its range's top count, weighted by the
	// range's recency, at its best range
	score := make(map[string]float64)
	for i, tier := range tiers {
		if len(tier) == 0 || tier[0].ListenCount == 0 {
			continue
		}
		for _, r := range tier {
			if s := weights[i] * float64(r.ListenCount) / float64(tier[0].ListenCount); s > score[r.key()] {
				score[r.key()] = s
			}
		}
	}

	var picked []topRecording
	seen := make(map[string]bool)
	add := func(r topRecording) {
		if len(picked) < onRepeatSize && r.ListenCount >= onRepeatMinPlays && !seen[r.key()] {
			seen[r.key()] = true
			picked = append(picked, r)
		}
	}
	for _, tier := range tiers {
		for _, r := range tier[:min(onRepeatTierDepth, len(tier))] {
			add(r)
		}
	}
	if len(tiers) > 0 {
		for _, r := range tiers[0] {
			add(r)
		}
	}

	sort.SliceStable(picked, func(i, j int) bool { return score[picked[i].key()] > score[picked[j].key()] })
	return spreadArtists(picked)
}

// spreadArtists moves recordings so the same artist isn't played twice in a row, pulling the
// next recording by another artist forward. Order is otherwise kept.
func spreadArtists(recs []topRecording) []topRecording {
	for i := 1; i < len(recs); i++ {
		prev := recs[i-1].artistKey()
		if recs[i].artistKey() != prev {
			continue
		}
		for j := i + 1; j < len(recs); j++ {
			if recs[j].artistKey() != prev {
				r := recs[j]
				copy(recs[i+1:j+1], recs[i:j])
				recs[i] = r
				break
			}
		}
	}
	return recs
}

func (c *ListenBrainz) getTopRecordings(user string) ([]*models.Track, error) {
	tiers := make([][]topRecording, 0, len(onRepeatRanges))
	weights := make([]float64, 0, len(onRepeatRanges))
	for _, r := range onRepeatRanges {
		recs, err := c.fetchTopRecordings(user, r.statsRange, onRepeatFetch)
		if err != nil {
			return nil, fmt.Errorf("getTopRecordings(): %s", err)
		}
		tiers = append(tiers, recs)
		weights = append(weights, r.weight)
	}

	recs := pickOnRepeat(tiers, weights)
	if len(recs) == 0 {
		return nil, fmt.Errorf("no top recordings found for user %s", user)
	}

	tracks := make([]*models.Track, 0, len(recs))
	for _, rec := range recs {
		var coverURL string
		if rec.ReleaseMbid != "" {
			coverURL = fmt.Sprintf("https://coverartarchive.org/release/%s/front-%s", rec.ReleaseMbid, c.coverSize())
		}
		tracks = append(tracks, &models.Track{
			Title:              rec.TrackName,
			CleanTitle:         rec.TrackName,
			Artist:             rec.ArtistName,
			MainArtist:         rec.ArtistName,
			Album:              rec.ReleaseName,
			CoverURL:           coverURL,
			MusicBrainzTrackID: rec.RecordingMbid,
		})
	}
	return tracks, nil
}
