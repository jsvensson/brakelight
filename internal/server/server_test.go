package server

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"
	"testing"

	"github.com/jsvensson/brakelight/internal/config"
	"github.com/jsvensson/brakelight/internal/db"
	"github.com/jsvensson/brakelight/internal/worker"
)

// The vendored htmx 2.0.10 and htmx-ext-sse 2.2.4 files must be byte-exact.
// A corrupted vendored asset silently breaks all htmx interactions in the UI.
func TestEmbeddedJSIntegrity(t *testing.T) {
	files := map[string]string{
		"templates/htmx.min.js": "71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de",
		"templates/sse.js":      "3b5992a541619babefc4c169505af474df5c3039da51e59b96ccf9241ecd61d2",
	}

	for name, wantSHA256 := range files {
		f, err := assets.Open(name)
		if err != nil {
			t.Fatalf("open embedded %s: %v", name, err)
		}

		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}

		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != wantSHA256 {
			t.Errorf("%s sha256 mismatch:\ngot:  %s\nwant: %s", name, got, wantSHA256)
		}
	}
}

// The progress fragment carries a data-title attribute used to update the
// browser tab title with live encoding progress and queue size.
func TestProgressFragmentTitle(t *testing.T) {
	render := func(view progressView) string {
		t.Helper()
		var buf strings.Builder
		if err := renderProgressFragment(&buf, view); err != nil {
			t.Fatalf("render progress fragment: %v", err)
		}
		return buf.String()
	}

	active := render(progressView{Snapshot: worker.Snapshot{Active: true, Percent: 12.34}, Pending: 17})
	if want := `data-title="Brakelight: 12.3% (17 pending)"`; !strings.Contains(active, want) {
		t.Errorf("active fragment missing %q:\n%s", want, active)
	}

	idle := render(progressView{Snapshot: worker.Snapshot{Active: false}, Pending: 3})
	if want := `data-title="Brakelight"`; !strings.Contains(idle, want) {
		t.Errorf("idle fragment missing %q:\n%s", want, idle)
	}
}

// The history log endpoint serves a job's stored CLI output as an HTML fragment.
func TestHistoryLogEndpoint(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	s := New(d, &config.Service{Config: &config.Config{}}, &worker.Progress{})

	if _, err := d.CreateJob("/media/movie.mkv", "preset", "general", "/media/out/movie.mkv", 1); err != nil {
		t.Fatalf("create job: %v", err)
	}
	job, err := d.NextPendingJob()
	if err != nil {
		t.Fatalf("next pending job: %v", err)
	}
	const logText = "muxing: done"
	if err := d.SetJobCompleted(job.ID, logText, nil); err != nil {
		t.Fatalf("set job completed: %v", err)
	}

	req := httptest.NewRequest("GET", fmt.Sprintf("/history/%d/log", job.ID), nil)
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, logText) {
		t.Errorf("expected log text in response:\n%s", body)
	}
	if !strings.Contains(body, `class="log-output"`) {
		t.Errorf("expected log-output div in response:\n%s", body)
	}
	if !strings.Contains(body, `id="log-modal-title" hx-swap-oob="true">movie.mkv`) {
		t.Errorf("expected OOB modal title with filename in response:\n%s", body)
	}

	req = httptest.NewRequest("GET", "/history/9999/log", nil)
	rec = httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown job, got %d", rec.Code)
	}
}

func TestFormatDuration(t *testing.T) {
	start := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		end      time.Time
		expected string
	}{
		{"hours", start.Add(83 * time.Minute), "1h23m"},
		{"minutes", start.Add(5*time.Minute + 30*time.Second), "5m30s"},
		{"seconds", start.Add(42 * time.Second), "42s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatDuration(&start, &c.end); got != c.expected {
				t.Errorf("expected %q, got %q", c.expected, got)
			}
		})
	}

	if got := formatDuration(nil, &start); got != "-" {
		t.Errorf("expected - for nil start, got %q", got)
	}
}

func TestSizeChange(t *testing.T) {
	jobWithSizes := func(source, output int) *db.Job {
		return &db.Job{SourceSize: &source, OutputSize: &output}
	}

	if got := sizeChange(jobWithSizes(1000, 850)); got != "-15%" {
		t.Errorf("expected -15%%, got %q", got)
	}
	if got := sizeChange(jobWithSizes(1000, 1200)); got != "+20%" {
		t.Errorf("expected +20%%, got %q", got)
	}
	if got := sizeChange(&db.Job{}); got != "-" {
		t.Errorf("expected - for nil sizes, got %q", got)
	}

	if got := sizeChangeClass(jobWithSizes(1000, 850)); got != "size-down" {
		t.Errorf("expected size-down, got %q", got)
	}
	if got := sizeChangeClass(jobWithSizes(1000, 1200)); got != "size-up" {
		t.Errorf("expected size-up, got %q", got)
	}
	if got := sizeChangeClass(&db.Job{}); len(got) > 0 {
		t.Errorf("expected empty class for nil sizes, got %q", got)
	}
}

func TestReencodeRisk(t *testing.T) {
	watchDir := t.TempDir()
	outDir := t.TempDir()

	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	cfg := &config.Service{
		Config: &config.Config{OutputDir: outDir},
		Watch:  []config.Watch{{Name: "test", Path: watchDir, Preset: "preset", OutputDir: outDir}},
	}
	s := &Server{db: d, config: cfg}

	completeJob := func(t *testing.T, source, output string) {
		t.Helper()
		if _, err := d.CreateJob(source, "preset", "test", output, 1); err != nil {
			t.Fatalf("create job: %v", err)
		}
		jobs, err := d.ListPendingJobs()
		if err != nil {
			t.Fatalf("list pending jobs: %v", err)
		}
		for _, j := range jobs {
			if j.Filepath == source {
				if err := d.SetJobCompleted(j.ID, "", nil); err != nil {
					t.Fatalf("complete job: %v", err)
				}
				return
			}
		}
		t.Fatalf("job for %s not found", source)
	}

	writeFile := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	// Completed job, source in a watch dir, output gone: at risk.
	source := filepath.Join(watchDir, "movie.mkv")
	output := filepath.Join(outDir, "movie.mkv")
	writeFile(t, source)
	completeJob(t, source, output)
	if got := s.reencodeRisk(); got != 1 {
		t.Errorf("expected risk 1 with source present and output gone, got %d", got)
	}

	// Output exists: scanner would skip the file anyway.
	writeFile(t, output)
	if got := s.reencodeRisk(); got != 0 {
		t.Errorf("expected risk 0 with output present, got %d", got)
	}

	// Source gone: nothing to re-encode.
	os.Remove(output)
	os.Remove(source)
	if got := s.reencodeRisk(); got != 0 {
		t.Errorf("expected risk 0 with source gone, got %d", got)
	}

	// Source outside any watch dir: not picked up by the scanner.
	outside := filepath.Join(t.TempDir(), "other.mkv")
	writeFile(t, outside)
	completeJob(t, outside, filepath.Join(outDir, "other.mkv"))
	if got := s.reencodeRisk(); got != 0 {
		t.Errorf("expected risk 0 for source outside watch dirs, got %d", got)
	}
}
