package discovery

import (
	"fmt"
	"testing"
)

func rec(mbid, artist string, count int) topRecording {
	return topRecording{RecordingMbid: mbid, ArtistName: artist, TrackName: "Song " + mbid, ListenCount: count}
}

// ranked returns n recordings by distinct artists with counts from top down to top-n+1
func ranked(prefix string, n, top int) []topRecording {
	out := make([]topRecording, n)
	for i := range out {
		out[i] = rec(fmt.Sprintf("%s%02d", prefix, i), fmt.Sprintf("Test Band %s%02d", prefix, i), top-i)
	}
	return out
}

func keys(recs []topRecording) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.RecordingMbid
	}
	return out
}

func TestMergeRecordingsSumsSplitRows(t *testing.T) {
	got := mergeRecordings([]topRecording{
		rec("a", "Test Band", 11),
		rec("b", "Other Band", 15),
		{RecordingMbid: "a", ArtistName: "Test Band", ReleaseName: "Test Album (Deluxe)", ListenCount: 9},
		{ArtistName: "No Mbid Band", TrackName: "Common Song", ListenCount: 2},
		{ArtistName: "no mbid band", TrackName: "COMMON SONG", ListenCount: 2},
	})
	if len(got) != 3 {
		t.Fatalf("len=%d want 3: %+v", len(got), got)
	}
	if got[0].RecordingMbid != "a" || got[0].ListenCount != 20 {
		t.Errorf("first=%+v, want a with 20 listens", got[0])
	}
	if got[2].ListenCount != 4 {
		t.Errorf("rows without MBID merged to %d listens, want 4", got[2].ListenCount)
	}
}

func TestPickOnRepeatFillsFromFirstTier(t *testing.T) {
	month := ranked("m", 40, 50)
	thisMonth := ranked("t", 20, 20)
	// last week's top 10 repeats two monthly recordings and drops below the minimum after 5
	week := append([]topRecording{month[0], thisMonth[0]}, ranked("w", 3, 5)...)
	week = append(week, ranked("x", 10, 2)...)
	for i := range week[:2] {
		week[i].ListenCount = 6
	}

	got := pickOnRepeat([][]topRecording{month, thisMonth, week}, []float64{1, 1, 1})
	if len(got) != onRepeatSize {
		t.Fatalf("len=%d want %d", len(got), onRepeatSize)
	}
	in := make(map[string]bool)
	for _, k := range keys(got) {
		if in[k] {
			t.Fatalf("duplicate %s in %v", k, keys(got))
		}
		in[k] = true
	}
	for _, want := range []string{"m09", "t09", "w00", "w02", "m10", "m16"} {
		if !in[want] {
			t.Errorf("missing %s in %v", want, keys(got))
		}
	}
	// thin week entries and anything past the backfill aren't picked
	for _, notWant := range []string{"t10", "x00", "m17"} {
		if in[notWant] {
			t.Errorf("unexpected %s in %v", notWant, keys(got))
		}
	}
}

func TestPickOnRepeatOrdersByBestWeightedRelativeCount(t *testing.T) {
	month := []topRecording{rec("ever", "Test Band", 40), rec("both", "Other Band", 20), rec("low", "Third Band", 10)}
	week := []topRecording{rec("both", "Other Band", 4), rec("new", "New Band", 3)}

	got := keys(pickOnRepeat([][]topRecording{month, nil, week}, []float64{0.7, 0.85, 1}))
	// both: max(0.7*20/40, 4/4) = 1; new: 3/4; ever: 0.7*40/40; low: 0.7*10/40
	want := []string{"both", "new", "ever", "low"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order=%v want %v", got, want)
	}
}

func TestSpreadArtists(t *testing.T) {
	recs := []topRecording{
		rec("a1", "Test Band", 9), rec("a2", "Test Band", 8), rec("a3", "Test Band", 7),
		rec("b1", "Other Band", 6), rec("c1", "Third Band", 5),
	}
	got := keys(spreadArtists(recs))
	want := []string{"a1", "b1", "a2", "c1", "a3"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order=%v want %v", got, want)
	}

	// artist MBIDs take precedence over differently credited names
	feat := []topRecording{
		{RecordingMbid: "f1", ArtistName: "Test Band", ArtistMbids: []string{"x"}},
		{RecordingMbid: "f2", ArtistName: "Test Band feat. Guest", ArtistMbids: []string{"x"}},
		{RecordingMbid: "f3", ArtistName: "Other Band", ArtistMbids: []string{"y"}},
	}
	if got := keys(spreadArtists(feat)); fmt.Sprint(got) != "[f1 f3 f2]" {
		t.Errorf("order=%v want [f1 f3 f2]", got)
	}
}
