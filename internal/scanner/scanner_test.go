package scanner

import (
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

func TestScanQueuesAllStableFilesOnSecondPass(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)

	for _, name := range []string{"a.mkv", "b.mp4", "c.avi"} {
		writeFile(t, filepath.Join(watchDir, name), 1024)
	}

	start := time.Now()
	s.scan()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("scan blocked for %v with 3 new files", elapsed)
	}
	if got := pendingCount(t, d); got != 0 {
		t.Errorf("expected 0 jobs after first scan, got %d", got)
	}

	s.scan()
	if got := pendingCount(t, d); got != 3 {
		t.Errorf("expected 3 jobs after second scan, got %d", got)
	}
}

func TestScanSkipsGrowingFile(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)

	path := filepath.Join(watchDir, "copying.mkv")
	writeFile(t, path, 1024)

	s.scan()
	writeFile(t, path, 2048)

	s.scan()
	if got := pendingCount(t, d); got != 0 {
		t.Errorf("expected growing file to stay unqueued, got %d jobs", got)
	}

	s.scan()
	if got := pendingCount(t, d); got != 1 {
		t.Errorf("expected stable file to be queued, got %d jobs", got)
	}
}

func TestScanUsesWatchOutputDir(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)

	writeFile(t, filepath.Join(watchDir, "movie.mkv"), 1024)

	s.scan()
	s.scan()

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

func TestScanSkipsEmptyFile(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)

	writeFile(t, filepath.Join(watchDir, "empty.mkv"), 0)

	s.scan()
	s.scan()
	if got := pendingCount(t, d); got != 0 {
		t.Errorf("expected empty file to stay unqueued, got %d jobs", got)
	}
}

func TestScanDoesNotRequeueCompletedJobWithMissingOutput(t *testing.T) {
	watchDir := t.TempDir()
	s, d := newTestScanner(t, watchDir)

	writeFile(t, filepath.Join(watchDir, "movie.mkv"), 1024)

	s.scan()
	s.scan()

	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list pending jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if err := d.SetJobCompleted(jobs[0].ID, ""); err != nil {
		t.Fatalf("complete job: %v", err)
	}

	// Output file is "moved away" (never existed in the test). The completed
	// job row must keep the source from being re-queued.
	s.scan()
	if got := pendingCount(t, d); got != 0 {
		t.Errorf("expected completed job to block re-queue, got %d pending jobs", got)
	}

	// Clearing history makes the file fair game again.
	if err := d.ClearHistory(); err != nil {
		t.Fatalf("clear history: %v", err)
	}
	s.scan()
	if got := pendingCount(t, d); got != 1 {
		t.Errorf("expected file to be re-queued after history clear, got %d pending jobs", got)
	}
}
