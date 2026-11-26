package downloader

import (
	"testing"
	"time"

	"explo/src/models"
)

// fakeMonitor reports a fixed status per file name; files not in states are missing from the queue
type fakeMonitor struct {
	cfg        MonitorConfig
	states     map[string]FileStatus
	candidates []string // files RetryNext queues, in order
	retries    []string // reasons passed to RetryNext
	cleanups   int
}

func (f *fakeMonitor) GetDownloadStatus(tracks []*models.Track) (map[string]FileStatus, error) {
	out := make(map[string]FileStatus)
	for _, t := range tracks {
		if st, ok := f.states[t.File]; ok {
			st.Filename = t.File
			out[t.ID] = st
		}
	}
	return out, nil
}

func (f *fakeMonitor) GetConf() (MonitorConfig, error)                      { return f.cfg, nil }
func (f *fakeMonitor) MoveDownload(string, string, string, *models.Track) error { return nil }
func (f *fakeMonitor) Cleanup(models.Track, string) error                   { f.cleanups++; return nil }

func (f *fakeMonitor) RetryNext(track *models.Track, reason string) (bool, error) {
	f.retries = append(f.retries, reason)
	if len(f.candidates) == 0 {
		return false, nil
	}
	track.File = f.candidates[0]
	f.candidates = f.candidates[1:]
	return true, nil
}

var (
	errored   = FileStatus{State: "Errored"}
	succeeded = FileStatus{State: "Completed, Succeeded", BytesTransferred: 10}
	queued    = FileStatus{State: "Queued, Remotely"}
)

func testMonitorConfig() MonitorConfig {
	return MonitorConfig{CheckInterval: 5 * time.Millisecond, StallDuration: time.Hour, MaxDuration: time.Hour, Service: "fake"}
}

func runMonitor(t *testing.T, f *fakeMonitor, track *models.Track) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- (&DownloadClient{}).MonitorDownloads([]*models.Track{track}, f) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not finish")
	}
}

func newTrack() *models.Track {
	return &models.Track{ID: "search-1", CleanTitle: "Song", MainArtist: "Artist", File: "a.flac"}
}

func TestMonitorRetriesErroredDownload(t *testing.T) {
	f := &fakeMonitor{
		cfg:        testMonitorConfig(),
		states:     map[string]FileStatus{"a.flac": errored, "b.flac": succeeded},
		candidates: []string{"b.flac"},
	}
	track := newTrack()
	runMonitor(t, f, track)

	if !track.Present || track.File != "b.flac" {
		t.Fatalf("present=%v file=%q, want downloaded b.flac", track.Present, track.File)
	}
	if len(f.retries) != 1 || f.retries[0] != "errored" {
		t.Fatalf("retries=%v, want [errored]", f.retries)
	}
}

func TestMonitorSkipsWhenNoCandidateLeft(t *testing.T) {
	f := &fakeMonitor{
		cfg:        testMonitorConfig(),
		states:     map[string]FileStatus{"a.flac": errored, "b.flac": errored},
		candidates: []string{"b.flac"},
	}
	track := newTrack()
	runMonitor(t, f, track)

	if track.Present {
		t.Fatal("track should be skipped")
	}
	if len(f.retries) != 2 || f.cleanups != 2 {
		t.Fatalf("retries=%v cleanups=%d, want 2 retries and 2 cleanups", f.retries, f.cleanups)
	}
}

func TestMonitorRetriesMissingDownload(t *testing.T) {
	f := &fakeMonitor{
		cfg:        testMonitorConfig(),
		states:     map[string]FileStatus{"b.flac": succeeded}, // a.flac vanished from the queue
		candidates: []string{"b.flac"},
	}
	track := newTrack()
	runMonitor(t, f, track)

	if !track.Present || track.File != "b.flac" {
		t.Fatalf("present=%v file=%q, want downloaded b.flac", track.Present, track.File)
	}
	if len(f.retries) != 1 || f.retries[0] != "missing from queue" {
		t.Fatalf("retries=%v, want [missing from queue]", f.retries)
	}
}

func TestMonitorRetriesStalledDownload(t *testing.T) {
	cfg := testMonitorConfig()
	cfg.StallDuration = 20 * time.Millisecond
	f := &fakeMonitor{
		cfg:        cfg,
		states:     map[string]FileStatus{"a.flac": queued, "b.flac": succeeded},
		candidates: []string{"b.flac"},
	}
	track := newTrack()
	runMonitor(t, f, track)

	if !track.Present || len(f.retries) != 1 || f.retries[0] != "stalled" {
		t.Fatalf("present=%v retries=%v, want downloaded after one stalled retry", track.Present, f.retries)
	}
}

func TestMonitorMaxDurationSkipsWithoutRetry(t *testing.T) {
	cfg := testMonitorConfig()
	cfg.MaxDuration = 20 * time.Millisecond
	f := &fakeMonitor{
		cfg:        cfg,
		states:     map[string]FileStatus{"a.flac": queued, "b.flac": succeeded},
		candidates: []string{"b.flac"},
	}
	track := newTrack()
	runMonitor(t, f, track)

	if track.Present || len(f.retries) != 0 || f.cleanups != 1 {
		t.Fatalf("present=%v retries=%v cleanups=%d, want skipped without retry", track.Present, f.retries, f.cleanups)
	}
}

func TestTryNextCandidateResetsTracker(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	tracker := &DownloadMonitor{LastBytesTransferred: 500, Counter: 2, LastUpdated: started, StartedAt: started}
	f := &fakeMonitor{candidates: []string{"b.flac"}}
	now := time.Now()

	if !tryNextCandidate(f, newTrack(), tracker, now, "errored") {
		t.Fatal("expected a retry")
	}
	if tracker.LastBytesTransferred != 0 || tracker.Counter != 0 || !tracker.LastUpdated.Equal(now) || !tracker.StartedAt.Equal(started) {
		t.Fatalf("tracker not reset correctly: %+v", tracker)
	}
	if tryNextCandidate(f, newTrack(), tracker, now, "errored") {
		t.Fatal("expected no retry with no candidates left")
	}
}

func TestTryNextCandidateWithoutRetrier(t *testing.T) {
	// embedding only the Monitor interface hides RetryNext
	var m Monitor = struct{ Monitor }{&fakeMonitor{candidates: []string{"b.flac"}}}
	if tryNextCandidate(m, newTrack(), &DownloadMonitor{}, time.Now(), "errored") {
		t.Fatal("monitor without Retrier must not retry")
	}
}
