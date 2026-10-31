package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jsvensson/brakelight/internal/config"
	"github.com/jsvensson/brakelight/internal/db"
)

func newTestScanner(t *testing.T, watchDir string) (*Scanner, *db.DB) {
	t.Helper()

	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cfg := &config.Service{
		Config: &config.Config{OutputDir: t.TempDir()},
		Watch: []config.Watch{{
			Name:      "test",
			Path:      watchDir,
			Preset:    "Fast 1080p30",
			OutputDir: t.TempDir(),
		}},
	}

	return New(d, cfg), d
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func pendingCount(t *testing.T, d *db.DB) int {
	t.Helper()
	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list pending jobs: %v", err)
	}
	return len(jobs)
}

// A file must be seen unchanged in two consecutive scans before it is
// queued, so the first scan only records sizes and queues nothing. The first
// scan must also not block waiting for files to settle.
func TestScanQueuesAllStableFilesOnSecondPass(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)

	for _, name := range []string{"a.mkv", "b.mp4", "c.avi", "d.m4v", "e.flv", "f.webm", "g.wmv", "h.vob"} {
		writeFile(t, filepath.Join(watchDir, name), 1024)
	}

	start := time.Now()
	s.scan(context.Background())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("scan blocked for %v with 8 new files", elapsed)
	}
	if got := pendingCount(t, d); got != 0 {
		t.Errorf("expected 0 jobs after first scan, got %d", got)
	}

	s.scan(context.Background())
	if got := pendingCount(t, d); got != 8 {
		t.Errorf("expected 8 jobs after second scan, got %d", got)
	}
}

// A file whose size grows between scans is still being copied and must stay
// unqueued until its size is stable across two scans.
func TestScanSkipsGrowingFile(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)

	path := filepath.Join(watchDir, "copying.mkv")
	writeFile(t, path, 1024)

	s.scan(context.Background())
	writeFile(t, path, 2048)

	s.scan(context.Background())
	if got := pendingCount(t, d); got != 0 {
		t.Errorf("expected growing file to stay unqueued, got %d jobs", got)
	}

	s.scan(context.Background())
	if got := pendingCount(t, d); got != 1 {
		t.Errorf("expected stable file to be queued, got %d jobs", got)
	}
}

// The queued job's output path must be placed in the watch's configured
// output directory, not the global default.
func TestScanUsesWatchOutputDir(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)

	writeFile(t, filepath.Join(watchDir, "movie.mkv"), 1024)

	s.scan(context.Background())
	s.scan(context.Background())

	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list pending jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	wantDir := s.config.Watch[0].OutputDir
	if dir := filepath.Dir(jobs[0].OutputPath); dir != wantDir {
		t.Errorf("expected output in %s, got %s", wantDir, dir)
	}
}

// Zero-byte files are placeholders or failed copies and must never be
// queued.
func TestScanSkipsEmptyFile(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)

	writeFile(t, filepath.Join(watchDir, "empty.mkv"), 0)

	s.scan(context.Background())
	s.scan(context.Background())
	if got := pendingCount(t, d); got != 0 {
		t.Errorf("expected empty file to stay unqueued, got %d jobs", got)
	}
}

// A completed history row prevents the source file from being re-queued even
// when its output file has been moved away; only clearing history makes the
// file eligible again.
func TestScanDoesNotRequeueCompletedJobWithMissingOutput(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)

	writeFile(t, filepath.Join(watchDir, "movie.mkv"), 1024)

	s.scan(context.Background())
	s.scan(context.Background())

	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list pending jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if err := d.SetJobCompleted(jobs[0].ID, "", nil); err != nil {
		t.Fatalf("complete job: %v", err)
	}

	// Output file is "moved away" (never existed in the test). The completed
	// job row must keep the source from being re-queued.
	s.scan(context.Background())
	if got := pendingCount(t, d); got != 0 {
		t.Errorf("expected completed job to block re-queue, got %d pending jobs", got)
	}

	// Clearing history makes the file fair game again.
	if err := d.ClearHistory(); err != nil {
		t.Fatalf("clear history: %v", err)
	}
	s.scan(context.Background())
	if got := pendingCount(t, d); got != 1 {
		t.Errorf("expected file to be re-queued after history clear, got %d pending jobs", got)
	}
}

// Files from a review watch are queued with review=1 and the probed track
// listing stored as JSON; the prober runs once the file is stable. A failed
// probe still queues the job, just without track details.
func TestScanReviewWatchProbesAndStoresTracks(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)
	s.config.Watch[0].Review = true

	probes := 0
	s.prober = func(ctx context.Context, path string) (string, error) {
		probes++
		return `{"tracks":[{"num":1,"type":"audio","language":"ita","dub":true},{"num":2,"type":"audio","language":"eng","original":true}]}`, nil
	}

	writeFile(t, filepath.Join(watchDir, "ep01.mkv"), 1024)
	s.scan(context.Background())
	if got := pendingCount(t, d); got != 0 {
		t.Fatalf("expected no jobs on first scan, got %d", got)
	}
	s.scan(context.Background())

	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list pending jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if !jobs[0].Review {
		t.Error("expected review=1 on the queued job")
	}
	if jobs[0].TracksJSON == nil || len(*jobs[0].TracksJSON) == 0 {
		t.Error("expected tracks_json to be stored")
	}
	if probes != 1 {
		t.Errorf("expected prober to run once, got %d", probes)
	}
	if jobs[0].AudioSelection != nil {
		t.Error("expected no selection before UI review")
	}
}

// A failing prober must not block queueing of review-watch files.
func TestScanReviewWatchProbeFailureStillQueues(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)
	s.config.Watch[0].Review = true
	s.prober = func(ctx context.Context, path string) (string, error) {
		return "", errProbeFailed
	}

	writeFile(t, filepath.Join(watchDir, "ep02.mkv"), 1024)
	s.scan(context.Background())
	s.scan(context.Background())

	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list pending jobs: %v", err)
	}
	if len(jobs) != 1 || !jobs[0].Review {
		t.Fatalf("expected 1 review job, got %v", jobs)
	}
	if jobs[0].TracksJSON != nil {
		t.Errorf("expected no tracks_json on probe failure, got %v", *jobs[0].TracksJSON)
	}
}

var errProbeFailed = errors.New("probe failed")

// An already-queued review file must not be re-probed on later scans: the
// job-existence check runs before the prober, so the prober fires exactly
// once per file even across many scan cycles.
func TestScanDoesNotReprobeQueuedReviewFile(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)
	s.config.Watch[0].Review = true

	probes := 0
	s.prober = func(ctx context.Context, path string) (string, error) {
		probes++
		return `{"tracks":[]}`, nil
	}

	writeFile(t, filepath.Join(watchDir, "ep01.mkv"), 1024)
	for i := 0; i < 4; i++ {
		s.scan(context.Background())
	}

	if got := pendingCount(t, d); got != 1 {
		t.Fatalf("expected 1 job, got %d", got)
	}
	if probes != 1 {
		t.Errorf("expected prober to run exactly once, got %d", probes)
	}
}

// The scan context is threaded into the prober, so a cancelled scan (e.g.
// shutdown) cancels a hung ffprobe instead of blocking the scan loop.
func TestScanProberReceivesScanContext(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)
	s.config.Watch[0].Review = true

	s.prober = func(ctx context.Context, path string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}

	writeFile(t, filepath.Join(watchDir, "ep01.mkv"), 1024)
	s.scan(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.scan(ctx)

	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list pending jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected the job to still be queued despite probe failure, got %d jobs", len(jobs))
	}
	if jobs[0].TracksJSON != nil {
		t.Errorf("expected no tracks_json when the probe was cancelled, got %v", *jobs[0].TracksJSON)
	}
}
