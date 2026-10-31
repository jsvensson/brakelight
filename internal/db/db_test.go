package db

import (
	"path/filepath"
	"testing"
)

// Source and output sizes are nil on a new job and are stored once known, so
// history entries can show how much the encode shrank the file.
func TestJobSizeRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if _, err := d.CreateJob(NewJob{Filepath: "/media/movie.mkv", Preset: "preset", WatchName: "general", OutputPath: "/media/out/movie.mkv", Position: 1}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	job, err := d.NextPendingJob()
	if err != nil {
		t.Fatalf("next pending job: %v", err)
	}

	// New jobs have no sizes.
	if job.SourceSize != nil || job.OutputSize != nil {
		t.Errorf("expected nil sizes on new job, got source=%v output=%v", job.SourceSize, job.OutputSize)
	}

	if err := d.SetJobSourceSize(job.ID, 1000); err != nil {
		t.Fatalf("set source size: %v", err)
	}
	outputSize := 850
	if err := d.SetJobCompleted(job.ID, "", &outputSize); err != nil {
		t.Fatalf("set job completed: %v", err)
	}

	history, err := d.ListRecentHistory(10)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 history job, got %d", len(history))
	}
	got := history[0]
	if got.SourceSize == nil || *got.SourceSize != 1000 {
		t.Errorf("expected source size 1000, got %v", got.SourceSize)
	}
	if got.OutputSize == nil || *got.OutputSize != 850 {
		t.Errorf("expected output size 850, got %v", got.OutputSize)
	}
}

// GetJobLog returns a completed job's stored CLI output and source path.
// Pending jobs and unknown ids return no result rather than an error.
func TestJobLogOutputRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if _, err := d.CreateJob(NewJob{Filepath: "/media/movie.mkv", Preset: "preset", WatchName: "general", OutputPath: "/media/out/movie.mkv", Position: 1}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	job, err := d.NextPendingJob()
	if err != nil {
		t.Fatalf("next pending job: %v", err)
	}

	// Pending jobs are not history yet.
	if jobLog, err := d.GetJobLog(job.ID); err != nil || jobLog != nil {
		t.Errorf("expected pending job to have no history log, jobLog=%v err=%v", jobLog, err)
	}

	const want = "Encoding: done\nmuxing: done\n"
	if err := d.SetJobCompleted(job.ID, want, nil); err != nil {
		t.Fatalf("set job completed: %v", err)
	}

	jobLog, err := d.GetJobLog(job.ID)
	if err != nil {
		t.Fatalf("get job log: %v", err)
	}
	if jobLog == nil {
		t.Fatal("expected completed job to be found")
	}
	if jobLog.LogOutput != want {
		t.Errorf("expected log %q, got %q", want, jobLog.LogOutput)
	}
	if jobLog.Filepath != "/media/movie.mkv" {
		t.Errorf("expected filepath /media/movie.mkv, got %q", jobLog.Filepath)
	}

	if jobLog, err := d.GetJobLog(9999); err != nil || jobLog != nil {
		t.Errorf("expected unknown id to be not found, jobLog=%v err=%v", jobLog, err)
	}
}

// Pre/post command failures accumulate on the job and are readable from
// history, and retrying the job clears them for the next attempt.
func TestJobCommandErrorsRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if _, err := d.CreateJob(NewJob{Filepath: "/media/movie.mkv", Preset: "preset", WatchName: "general", OutputPath: "/media/out/movie.mkv", Position: 1}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	job, err := d.NextPendingJob()
	if err != nil {
		t.Fatalf("next pending job: %v", err)
	}

	// New jobs have no command errors.
	if job.CommandErrors != nil {
		t.Errorf("expected nil command errors on new job, got %q", *job.CommandErrors)
	}

	if err := d.SetJobCompleted(job.ID, "", nil); err != nil {
		t.Fatalf("set job completed: %v", err)
	}
	if err := d.AppendJobCommandError(job.ID, "post-command failed: logger x: exit status 1\n"); err != nil {
		t.Fatalf("append command error: %v", err)
	}
	if err := d.AppendJobCommandError(job.ID, "post-command failed: logger y: exit status 2\n"); err != nil {
		t.Fatalf("append command error: %v", err)
	}

	history, err := d.ListRecentHistory(10)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 history job, got %d", len(history))
	}
	want := "post-command failed: logger x: exit status 1\npost-command failed: logger y: exit status 2\n"
	if history[0].CommandErrors == nil || *history[0].CommandErrors != want {
		t.Errorf("expected command errors %q, got %v", want, history[0].CommandErrors)
	}

	// Retrying the job clears the command errors.
	if err := d.ResetJobToPending(job.ID, 1); err != nil {
		t.Fatalf("reset job to pending: %v", err)
	}
	job, err = d.NextPendingJob()
	if err != nil {
		t.Fatalf("next pending job: %v", err)
	}
	if job.CommandErrors != nil {
		t.Errorf("expected command errors cleared on retry, got %q", *job.CommandErrors)
	}
}

// A fresh database reports both encoding and scanning as active, so the
// service runs without requiring an explicit unpause.
func TestServiceStateDefaultsToActive(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	encoding, err := d.IsEncodingActive()
	if err != nil {
		t.Fatalf("IsEncodingActive: %v", err)
	}
	if !encoding {
		t.Error("expected encoding to be active by default")
	}

	scanning, err := d.IsScanningActive()
	if err != nil {
		t.Fatalf("IsScanningActive: %v", err)
	}
	if !scanning {
		t.Error("expected scanning to be active by default")
	}
}

// The encoding and scanning pause flags are independent: pausing one must
// not affect the other, and resuming restores the active state.
func TestServiceStateRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if err := d.SetEncodingActive(false); err != nil {
		t.Fatalf("SetEncodingActive(false): %v", err)
	}

	encoding, err := d.IsEncodingActive()
	if err != nil {
		t.Fatalf("IsEncodingActive: %v", err)
	}
	if encoding {
		t.Error("expected encoding to be inactive after SetEncodingActive(false)")
	}

	scanning, err := d.IsScanningActive()
	if err != nil {
		t.Fatalf("IsScanningActive: %v", err)
	}
	if !scanning {
		t.Error("expected scanning to be unaffected by SetEncodingActive(false)")
	}

	if err := d.SetScanningActive(false); err != nil {
		t.Fatalf("SetScanningActive(false): %v", err)
	}

	scanning, err = d.IsScanningActive()
	if err != nil {
		t.Fatalf("IsScanningActive: %v", err)
	}
	if scanning {
		t.Error("expected scanning to be inactive after SetScanningActive(false)")
	}

	if err := d.SetEncodingActive(true); err != nil {
		t.Fatalf("SetEncodingActive(true): %v", err)
	}

	encoding, err = d.IsEncodingActive()
	if err != nil {
		t.Fatalf("IsEncodingActive: %v", err)
	}
	if !encoding {
		t.Error("expected encoding to be active after SetEncodingActive(true)")
	}
}

// The paused state is stored in the database and survives a service restart.
func TestServiceStatePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := d.SetEncodingActive(false); err != nil {
		t.Fatalf("SetEncodingActive(false): %v", err)
	}
	d.Close()

	d, err = Open(path)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer d.Close()

	encoding, err := d.IsEncodingActive()
	if err != nil {
		t.Fatalf("IsEncodingActive: %v", err)
	}
	if encoding {
		t.Error("expected inactive encoding state to persist across reopen")
	}

	scanning, err := d.IsScanningActive()
	if err != nil {
		t.Fatalf("IsScanningActive: %v", err)
	}
	if !scanning {
		t.Error("expected scanning state to be unaffected across reopen")
	}
}

// Databases written before the pause state was split carry a single legacy
// 'active' key; opening them migrates it to the per-feature encoding and
// scanning flags and removes the legacy key.
func TestLegacyActiveStateMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := d.conn.Exec(`INSERT INTO service_state (key, value) VALUES ('active', '0')`); err != nil {
		t.Fatalf("insert legacy state: %v", err)
	}
	d.Close()

	d, err = Open(path)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer d.Close()

	encoding, err := d.IsEncodingActive()
	if err != nil {
		t.Fatalf("IsEncodingActive: %v", err)
	}
	if encoding {
		t.Error("expected legacy inactive state to migrate to encoding")
	}

	scanning, err := d.IsScanningActive()
	if err != nil {
		t.Fatalf("IsScanningActive: %v", err)
	}
	if scanning {
		t.Error("expected legacy inactive state to migrate to scanning")
	}

	var count int
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM service_state WHERE key = 'active'`).Scan(&count); err != nil {
		t.Fatalf("count legacy keys: %v", err)
	}
	if count != 0 {
		t.Error("expected legacy 'active' key to be removed after migration")
	}
}

// Moving a pending job to a new queue position shifts the jobs in between
// while preserving their relative order.
func TestMoveJobToPosition(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	for i, name := range []string{"a.mkv", "b.mkv", "c.mkv"} {
		if _, err := d.CreateJob(NewJob{Filepath: "/tmp/" + name, Preset: "preset", WatchName: "general", OutputPath: "/tmp/out/" + name, Position: int64(i + 1)}); err != nil {
			t.Fatalf("create job: %v", err)
		}
	}

	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(jobs))
	}

	// Move c.mkv (id 3) to index 0.
	if err := d.MoveJobToPosition(jobs[2].ID, 0); err != nil {
		t.Fatalf("move job: %v", err)
	}

	jobs, err = d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if jobs[0].Filepath != "/tmp/c.mkv" {
		t.Errorf("expected c.mkv first, got %s", jobs[0].Filepath)
	}
	if jobs[1].Filepath != "/tmp/a.mkv" {
		t.Errorf("expected a.mkv second, got %s", jobs[1].Filepath)
	}
	if jobs[2].Filepath != "/tmp/b.mkv" {
		t.Errorf("expected b.mkv third, got %s", jobs[2].Filepath)
	}
}

// Creating a job for an already-queued filepath is silently ignored (id 0,
// no error) instead of inserting a duplicate.
func TestCreateJobIgnoresDuplicates(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	id, err := d.CreateJob(NewJob{Filepath: "/tmp/a.mkv", Preset: "preset", WatchName: "general", OutputPath: "/tmp/out/a.mkv", Position: 1})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if id == 0 {
		t.Error("expected first insert to succeed")
	}

	id, err = d.CreateJob(NewJob{Filepath: "/tmp/a.mkv", Preset: "preset", WatchName: "general", OutputPath: "/tmp/out/a.mkv", Position: 2})
	if err != nil {
		t.Fatalf("duplicate create job: %v", err)
	}
	if id > 0 {
		t.Error("expected duplicate insert to be ignored")
	}
}

// Repeated duplicate inserts must not consume AUTOINCREMENT ids; the scanner
// calls CreateJob for already-queued files on every scan cycle.
func TestCreateJobDuplicatesDoNotBurnIDs(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if _, err := d.CreateJob(NewJob{Filepath: "/tmp/a.mkv", Preset: "preset", WatchName: "general", OutputPath: "/tmp/out/a.mkv", Position: 1}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	for range 100 {
		if _, err := d.CreateJob(NewJob{Filepath: "/tmp/a.mkv", Preset: "preset", WatchName: "general", OutputPath: "/tmp/out/a.mkv", Position: 1}); err != nil {
			t.Fatalf("duplicate create job: %v", err)
		}
	}

	if _, err := d.CreateJob(NewJob{Filepath: "/tmp/b.mkv", Preset: "preset", WatchName: "general", OutputPath: "/tmp/out/b.mkv", Position: 2}); err != nil {
		t.Fatalf("create second job: %v", err)
	}

	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list pending jobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
	if jobs[1].ID != jobs[0].ID+1 {
		t.Errorf("expected consecutive ids after duplicates, got %d and %d", jobs[0].ID, jobs[1].ID)
	}
}

// DeleteJob removes history rows but refuses to touch pending jobs, so the
// UI's delete action cannot be used to cancel queued work.
func TestDeleteJobOnlyRemovesHistoryRows(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if _, err := d.CreateJob(NewJob{Filepath: "/tmp/pending.mkv", Preset: "preset", WatchName: "general", OutputPath: "/tmp/out/pending.mkv", Position: 1}); err != nil {
		t.Fatalf("create pending job: %v", err)
	}
	if _, err := d.CreateJob(NewJob{Filepath: "/tmp/done.mkv", Preset: "preset", WatchName: "general", OutputPath: "/tmp/out/done.mkv", Position: 2}); err != nil {
		t.Fatalf("create completed job: %v", err)
	}

	pending, err := d.NextPendingJob()
	if err != nil {
		t.Fatalf("next pending job: %v", err)
	}
	if err := d.SetJobProcessing(pending.ID); err != nil {
		t.Fatalf("mark processing: %v", err)
	}
	if err := d.SetJobCompleted(pending.ID, "", nil); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	// Deleting a pending job via DeleteJob must not touch it.
	remaining, err := d.NextPendingJob()
	if err != nil {
		t.Fatalf("next pending job: %v", err)
	}
	if err := d.DeleteJob(remaining.ID); err != nil {
		t.Fatalf("delete pending job: %v", err)
	}
	if got := len(mustListPending(t, d)); got != 1 {
		t.Errorf("expected pending job to survive DeleteJob, got %d pending", got)
	}

	// Deleting the completed job removes it from history.
	if err := d.DeleteJob(pending.ID); err != nil {
		t.Fatalf("delete completed job: %v", err)
	}
	completed, err := d.ListCompletedJobs()
	if err != nil {
		t.Fatalf("list completed jobs: %v", err)
	}
	if len(completed) != 0 {
		t.Errorf("expected completed job to be deleted, got %d", len(completed))
	}
}

func mustListPending(t *testing.T, d *DB) []*Job {
	t.Helper()
	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list pending jobs: %v", err)
	}
	return jobs
}

// The watch name recorded at job creation is stored and returned with the
// job, so the UI can show which watch directory produced it.
func TestJobWatchNameRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if _, err := d.CreateJob(NewJob{Filepath: "/tmp/a.mkv", Preset: "preset", WatchName: "animated", OutputPath: "/tmp/out/a.mkv", Position: 1}); err != nil {
		t.Fatalf("create job: %v", err)
	}

	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].WatchName != "animated" {
		t.Errorf("expected watch name 'animated', got %q", jobs[0].WatchName)
	}
}

// ListCompletedJobs returns only successfully completed jobs, excluding
// failed and still-pending ones.
func TestListCompletedJobs(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	for _, name := range []string{"done.mkv", "failed.mkv", "pending.mkv"} {
		if _, err := d.CreateJob(NewJob{Filepath: "/tmp/" + name, Preset: "preset", WatchName: "general", OutputPath: "/tmp/out/" + name, Position: 1}); err != nil {
			t.Fatalf("create job: %v", err)
		}
	}

	jobs, err := d.ListPendingJobs()
	if err != nil {
		t.Fatalf("list pending jobs: %v", err)
	}
	for _, j := range jobs {
		switch j.Filepath {
		case "/tmp/done.mkv":
			if err := d.SetJobCompleted(j.ID, "", nil); err != nil {
				t.Fatalf("complete job: %v", err)
			}
		case "/tmp/failed.mkv":
			if err := d.SetJobFailed(j.ID, "boom", ""); err != nil {
				t.Fatalf("fail job: %v", err)
			}
		}
	}

	completed, err := d.ListCompletedJobs()
	if err != nil {
		t.Fatalf("list completed jobs: %v", err)
	}
	if len(completed) != 1 {
		t.Fatalf("expected 1 completed job, got %d", len(completed))
	}
	if completed[0].Filepath != "/tmp/done.mkv" {
		t.Errorf("expected done.mkv, got %s", completed[0].Filepath)
	}
}

// SetJobTrackSelection stores the audio/subtitle rules on a pending job and
// they round-trip through GetJob; a review job becomes eligible for
// NextPendingJob only once a selection exists.
func TestJobTrackSelectionRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	pos, err := d.NextPosition()
	if err != nil {
		t.Fatalf("next position: %v", err)
	}
	id, err := d.CreateJob(NewJob{
		Filepath:   "/media/review/ep01.mkv",
		Preset:     "Standard",
		WatchName:  "review",
		OutputPath: "/media/encoded/ep01.mkv",
		Position:   pos,
		Review:     true,
		TracksJSON: `{"tracks":[{"num":1,"type":"audio","language":"ita","dub":true},{"num":2,"type":"audio","language":"eng","original":true}]}`,
	})
	if err != nil || id == 0 {
		t.Fatalf("create job: id=%d err=%v", id, err)
	}

	// Review job without selection is skipped by the worker's queue query.
	if job, err := d.NextPendingJob(); err != nil || job != nil {
		t.Errorf("expected no eligible job before selection, got %v err=%v", job, err)
	}

	if err := d.SetJobTrackSelection(id, "original", "all"); err != nil {
		t.Fatalf("set selection: %v", err)
	}

	job, err := d.GetJob(id)
	if err != nil || job == nil {
		t.Fatalf("get job: %v", err)
	}
	if !job.Review {
		t.Error("expected review=true")
	}
	if job.AudioSelection == nil || *job.AudioSelection != "original" {
		t.Errorf("expected audio_selection original, got %v", job.AudioSelection)
	}
	if job.SubtitleSelection == nil || *job.SubtitleSelection != "all" {
		t.Errorf("expected subtitle_selection all, got %v", job.SubtitleSelection)
	}
	if job.TracksJSON == nil || len(*job.TracksJSON) == 0 {
		t.Error("expected tracks_json to be stored")
	}

	// Now the job is eligible for encoding.
	next, err := d.NextPendingJob()
	if err != nil || next == nil || next.ID != id {
		t.Errorf("expected job %d to be next pending after selection, got %v err=%v", id, next, err)
	}
}

// A non-review pending job is eligible for NextPendingJob without any track
// selection.
func TestNextPendingJobIncludesNonReviewJobs(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	pos, _ := d.NextPosition()
	id, err := d.CreateJob(NewJob{Filepath: "/media/a.mkv", Preset: "P", WatchName: "w", OutputPath: "/out/a.mkv", Position: pos})
	if err != nil || id == 0 {
		t.Fatalf("create job: id=%d err=%v", id, err)
	}
	next, err := d.NextPendingJob()
	if err != nil || next == nil || next.ID != id {
		t.Errorf("expected job %d to be next pending, got %v err=%v", id, next, err)
	}
}

// HasJobForFilepath reports whether any job row exists for a filepath,
// regardless of status.
func TestHasJobForFilepath(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	exists, err := d.HasJobForFilepath("/media/a.mkv")
	if err != nil {
		t.Fatalf("has job: %v", err)
	}
	if exists {
		t.Error("expected no job row for unknown filepath")
	}

	pos, _ := d.NextPosition()
	id, err := d.CreateJob(NewJob{Filepath: "/media/a.mkv", Preset: "P", WatchName: "w", OutputPath: "/out/a.mkv", Position: pos})
	if err != nil || id == 0 {
		t.Fatalf("create job: id=%d err=%v", id, err)
	}
	exists, err = d.HasJobForFilepath("/media/a.mkv")
	if err != nil || !exists {
		t.Errorf("expected job row to exist, got exists=%v err=%v", exists, err)
	}

	// The row still counts after the job leaves the pending state.
	if err := d.SetJobCompleted(id, "", nil); err != nil {
		t.Fatalf("complete job: %v", err)
	}
	exists, err = d.HasJobForFilepath("/media/a.mkv")
	if err != nil || !exists {
		t.Errorf("expected completed job row to still count, got exists=%v err=%v", exists, err)
	}
}

// An empty audio_selection string does not release a review job for
// encoding; only a non-empty selection makes it eligible.
func TestNextPendingJobSkipsEmptySelection(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	pos, _ := d.NextPosition()
	id, err := d.CreateJob(NewJob{Filepath: "/media/review/ep01.mkv", Preset: "P", WatchName: "review", OutputPath: "/out/ep01.mkv", Position: pos, Review: true})
	if err != nil || id == 0 {
		t.Fatalf("create job: id=%d err=%v", id, err)
	}

	if err := d.SetJobTrackSelection(id, "", "all"); err != nil {
		t.Fatalf("set selection: %v", err)
	}
	if job, err := d.NextPendingJob(); err != nil || job != nil {
		t.Errorf("expected empty selection to keep the job ineligible, got %v err=%v", job, err)
	}
}
