package db

import (
	"path/filepath"
	"testing"
)

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

func TestMoveJobToPosition(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	for i, name := range []string{"a.mkv", "b.mkv", "c.mkv"} {
		if _, err := d.CreateJob("/tmp/"+name, "preset", "general", "/tmp/out/"+name, int64(i+1)); err != nil {
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

func TestCreateJobIgnoresDuplicates(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	created, err := d.CreateJob("/tmp/a.mkv", "preset", "general", "/tmp/out/a.mkv", 1)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if !created {
		t.Error("expected first insert to succeed")
	}

	created, err = d.CreateJob("/tmp/a.mkv", "preset", "general", "/tmp/out/a.mkv", 2)
	if err != nil {
		t.Fatalf("duplicate create job: %v", err)
	}
	if created {
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

	if _, err := d.CreateJob("/tmp/a.mkv", "preset", "general", "/tmp/out/a.mkv", 1); err != nil {
		t.Fatalf("create job: %v", err)
	}
	for range 100 {
		if _, err := d.CreateJob("/tmp/a.mkv", "preset", "general", "/tmp/out/a.mkv", 1); err != nil {
			t.Fatalf("duplicate create job: %v", err)
		}
	}

	if _, err := d.CreateJob("/tmp/b.mkv", "preset", "general", "/tmp/out/b.mkv", 2); err != nil {
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

func TestDeleteJobOnlyRemovesHistoryRows(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if _, err := d.CreateJob("/tmp/pending.mkv", "preset", "general", "/tmp/out/pending.mkv", 1); err != nil {
		t.Fatalf("create pending job: %v", err)
	}
	if _, err := d.CreateJob("/tmp/done.mkv", "preset", "general", "/tmp/out/done.mkv", 2); err != nil {
		t.Fatalf("create completed job: %v", err)
	}

	pending, err := d.NextPendingJob()
	if err != nil {
		t.Fatalf("next pending job: %v", err)
	}
	if err := d.SetJobProcessing(pending.ID); err != nil {
		t.Fatalf("mark processing: %v", err)
	}
	if err := d.SetJobCompleted(pending.ID); err != nil {
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

func TestJobWatchNameRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if _, err := d.CreateJob("/tmp/a.mkv", "preset", "animated", "/tmp/out/a.mkv", 1); err != nil {
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

func TestListCompletedJobs(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	for _, name := range []string{"done.mkv", "failed.mkv", "pending.mkv"} {
		if _, err := d.CreateJob("/tmp/"+name, "preset", "general", "/tmp/out/"+name, 1); err != nil {
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
			if err := d.SetJobCompleted(j.ID); err != nil {
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
