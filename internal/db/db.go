package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Job represents a conversion job.
type Job struct {
	ID           int64
	Filepath     string
	Preset       string
	WatchName    string
	OutputPath   string
	Status       string
	Position     *int64
	Attempts     int
	CreatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
	ErrorMessage *string
	LogOutput    *string
}

// DB wraps the SQLite connection.
type DB struct {
	conn *sql.DB
}

// Open opens the SQLite database and initializes the schema.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	conn.SetMaxOpenConns(1)

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		return nil, err
	}

	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    filepath TEXT UNIQUE NOT NULL,
    preset TEXT NOT NULL,
    watch_name TEXT NOT NULL DEFAULT '',
    output_path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    position INTEGER,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    error_message TEXT,
    log_output TEXT
);

CREATE INDEX IF NOT EXISTS idx_jobs_status_position ON jobs(status, position);
CREATE INDEX IF NOT EXISTS idx_jobs_created_at ON jobs(created_at);

CREATE TABLE IF NOT EXISTS service_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`
	_, err := db.conn.Exec(schema)
	if err != nil {
		return err
	}

	// Add watch_name to databases created before the column existed.
	var hasWatchName bool
	rows, err := db.conn.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "watch_name" {
			hasWatchName = true
		}
	}
	rows.Close()
	if !hasWatchName {
		if _, err := db.conn.Exec(`ALTER TABLE jobs ADD COLUMN watch_name TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	// Default encoding and scanning to active.
	if _, err := db.conn.Exec(`INSERT OR IGNORE INTO service_state (key, value) VALUES ('encoding_active', '1'), ('scanning_active', '1')`); err != nil {
		return err
	}

	// Migrate the legacy combined 'active' flag onto both, if present.
	var legacy string
	switch err := db.conn.QueryRow(`SELECT value FROM service_state WHERE key = 'active'`).Scan(&legacy); err {
	case nil:
		if _, err := db.conn.Exec(`UPDATE service_state SET value = ? WHERE key IN ('encoding_active', 'scanning_active')`, legacy); err != nil {
			return err
		}
		if _, err := db.conn.Exec(`DELETE FROM service_state WHERE key = 'active'`); err != nil {
			return err
		}
	case sql.ErrNoRows:
	default:
		return err
	}
	return nil
}

// CreateJob inserts a new job if one does not already exist for the filepath.
func (db *DB) CreateJob(filepath, preset, watchName, outputPath string, position int64) (bool, error) {
	// Check existence first: INSERT OR IGNORE against the UNIQUE constraint
	// still consumes an AUTOINCREMENT id on every ignored attempt, and the
	// scanner calls this for already-queued files on every scan cycle. The
	// transaction makes the check-and-insert atomic; the INSERT stays
	// OR IGNORE as a correctness fallback in case of a race.
	tx, err := db.conn.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM jobs WHERE filepath = ?)`, filepath).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 1 {
		return false, nil
	}

	res, err := tx.Exec(
		`INSERT OR IGNORE INTO jobs (filepath, preset, watch_name, output_path, status, position, attempts) VALUES (?, ?, ?, ?, 'pending', ?, 0)`,
		filepath, preset, watchName, outputPath, position,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

// NextPendingJob returns the pending job with the lowest position.
func (db *DB) NextPendingJob() (*Job, error) {
	row := db.conn.QueryRow(`
		SELECT id, filepath, preset, watch_name, output_path, status, position, attempts, created_at, started_at, completed_at, error_message, log_output
		FROM jobs
		WHERE status = 'pending'
		ORDER BY position ASC, created_at ASC
		LIMIT 1
	`)
	return scanJob(row)
}

// SetJobProcessing marks a job as processing.
func (db *DB) SetJobProcessing(id int64) error {
	_, err := db.conn.Exec(
		`UPDATE jobs SET status = 'processing', started_at = CURRENT_TIMESTAMP WHERE id = ?`,
		id,
	)
	return err
}

// SetJobCompleted marks a job as completed and stores the CLI log output.
func (db *DB) SetJobCompleted(id int64, logOutput string) error {
	_, err := db.conn.Exec(
		`UPDATE jobs SET status = 'completed', position = NULL, completed_at = CURRENT_TIMESTAMP, log_output = ? WHERE id = ?`,
		logOutput, id,
	)
	return err
}

// SetJobFailed marks a job as failed with an error message and log output.
func (db *DB) SetJobFailed(id int64, errorMessage, logOutput string) error {
	_, err := db.conn.Exec(
		`UPDATE jobs SET status = 'failed', position = NULL, completed_at = CURRENT_TIMESTAMP, error_message = ?, log_output = ? WHERE id = ?`,
		errorMessage, logOutput, id,
	)
	return err
}

// IncrementAttempts bumps the attempt counter for a job.
func (db *DB) IncrementAttempts(id int64) error {
	_, err := db.conn.Exec(`UPDATE jobs SET attempts = attempts + 1 WHERE id = ?`, id)
	return err
}

// ResetJobToPending returns a failed job to the pending queue at the end.
func (db *DB) ResetJobToPending(id int64, position int64) error {
	_, err := db.conn.Exec(
		`UPDATE jobs SET status = 'pending', position = ?, attempts = attempts + 1, error_message = NULL, log_output = NULL, completed_at = NULL WHERE id = ?`,
		position, id,
	)
	return err
}

// ListPendingJobs returns all pending jobs ordered by position.
func (db *DB) ListPendingJobs() ([]*Job, error) {
	rows, err := db.conn.Query(`
		SELECT id, filepath, preset, watch_name, output_path, status, position, attempts, created_at, started_at, completed_at, error_message, log_output
		FROM jobs
		WHERE status = 'pending'
		ORDER BY position ASC, created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

// ListProcessingJobs returns all currently processing jobs (should be 0 or 1).
func (db *DB) ListProcessingJobs() ([]*Job, error) {
	rows, err := db.conn.Query(`
		SELECT id, filepath, preset, watch_name, output_path, status, position, attempts, created_at, started_at, completed_at, error_message, log_output
		FROM jobs
		WHERE status = 'processing'
		ORDER BY started_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

// ListRecentHistory returns the most recent completed or failed jobs.
func (db *DB) ListRecentHistory(limit int) ([]*Job, error) {
	rows, err := db.conn.Query(`
		SELECT id, filepath, preset, watch_name, output_path, status, position, attempts, created_at, started_at, completed_at, error_message, log_output
		FROM jobs
		WHERE status IN ('completed', 'failed')
		ORDER BY completed_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

// ListCompletedJobs returns all completed jobs, regardless of age.
func (db *DB) ListCompletedJobs() ([]*Job, error) {
	rows, err := db.conn.Query(`
		SELECT id, filepath, preset, watch_name, output_path, status, position, attempts, created_at, started_at, completed_at, error_message, log_output
		FROM jobs
		WHERE status = 'completed'
		ORDER BY completed_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

// MoveJobToPosition reorders a pending job to a new zero-based index.
func (db *DB) MoveJobToPosition(id int64, newIndex int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT id FROM jobs
		WHERE status = 'pending'
		ORDER BY position ASC, created_at ASC
	`)
	if err != nil {
		return err
	}

	var ids []int64
	var found bool
	for rows.Next() {
		var jid int64
		if err := rows.Scan(&jid); err != nil {
			rows.Close()
			return err
		}
		if jid == id {
			found = true
		}
		ids = append(ids, jid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("job %d not found or not pending", id)
	}

	if newIndex < 0 {
		newIndex = 0
	}
	if newIndex >= int64(len(ids)) {
		newIndex = int64(len(ids)) - 1
	}

	// Remove the moved id and reinsert at newIndex.
	filtered := make([]int64, 0, len(ids))
	for _, jid := range ids {
		if jid != id {
			filtered = append(filtered, jid)
		}
	}
	reordered := make([]int64, 0, len(ids))
	reordered = append(reordered, filtered[:newIndex]...)
	reordered = append(reordered, id)
	reordered = append(reordered, filtered[newIndex:]...)

	for i, jid := range reordered {
		if _, err := tx.Exec(`UPDATE jobs SET position = ? WHERE id = ?`, int64(i)+1, jid); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// CancelJob deletes a pending job from the queue.
func (db *DB) CancelJob(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM jobs WHERE id = ? AND status = 'pending'`, id)
	return err
}

// GetJobLog returns the source filepath and stored CLI log output for a
// completed or failed job. The third return value reports whether the job
// exists in history.
func (db *DB) GetJobLog(id int64) (string, string, bool, error) {
	var filepath string
	var logOut sql.NullString
	err := db.conn.QueryRow(
		`SELECT filepath, log_output FROM jobs WHERE id = ? AND status IN ('completed', 'failed')`,
		id,
	).Scan(&filepath, &logOut)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return filepath, logOut.String, true, nil
}

// ClearHistory deletes all completed and failed jobs.
func (db *DB) ClearHistory() error {
	_, err := db.conn.Exec(`DELETE FROM jobs WHERE status IN ('completed', 'failed')`)
	return err
}

// DeleteJob removes a single completed or failed job from history. Removing
// the row makes its source file eligible for re-encoding on the next scan.
func (db *DB) DeleteJob(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM jobs WHERE id = ? AND status IN ('completed', 'failed')`, id)
	return err
}

// RecoverProcessingJobs returns interrupted processing jobs to the pending queue.
func (db *DB) RecoverProcessingJobs() (int64, error) {
	res, err := db.conn.Exec(`UPDATE jobs SET status = 'pending', attempts = attempts + 1 WHERE status = 'processing'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// NextPosition returns the next available position value for new pending jobs.
func (db *DB) NextPosition() (int64, error) {
	var pos sql.NullInt64
	if err := db.conn.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM jobs WHERE status = 'pending'`).Scan(&pos); err != nil {
		return 0, err
	}
	return pos.Int64, nil
}

// IsEncodingActive returns whether encoding is currently active.
func (db *DB) IsEncodingActive() (bool, error) {
	return db.isActive("encoding_active")
}

// SetEncodingActive sets whether encoding is currently active.
func (db *DB) SetEncodingActive(active bool) error {
	return db.setActive("encoding_active", active)
}

// IsScanningActive returns whether file scanning is currently active.
func (db *DB) IsScanningActive() (bool, error) {
	return db.isActive("scanning_active")
}

// SetScanningActive sets whether file scanning is currently active.
func (db *DB) SetScanningActive(active bool) error {
	return db.setActive("scanning_active", active)
}

func (db *DB) isActive(key string) (bool, error) {
	var value string
	if err := db.conn.QueryRow(`SELECT value FROM service_state WHERE key = ?`, key).Scan(&value); err != nil {
		if err == sql.ErrNoRows {
			return true, nil
		}
		return true, err
	}
	return value == "1" || value == "true", nil
}

func (db *DB) setActive(key string, active bool) error {
	value := "0"
	if active {
		value = "1"
	}
	_, err := db.conn.Exec(`INSERT INTO service_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func scanJob(row *sql.Row) (*Job, error) {
	j := &Job{}
	var pos sql.NullInt64
	var started, completed sql.NullTime
	var errMsg, logOut sql.NullString

	err := row.Scan(
		&j.ID, &j.Filepath, &j.Preset, &j.WatchName, &j.OutputPath, &j.Status, &pos,
		&j.Attempts, &j.CreatedAt, &started, &completed, &errMsg, &logOut,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if pos.Valid {
		p := pos.Int64
		j.Position = &p
	}
	if started.Valid {
		j.StartedAt = &started.Time
	}
	if completed.Valid {
		j.CompletedAt = &completed.Time
	}
	if errMsg.Valid {
		j.ErrorMessage = &errMsg.String
	}
	if logOut.Valid {
		j.LogOutput = &logOut.String
	}

	return j, nil
}

func scanJobs(rows *sql.Rows) ([]*Job, error) {
	var jobs []*Job
	for rows.Next() {
		j := &Job{}
		var pos sql.NullInt64
		var started, completed sql.NullTime
		var errMsg, logOut sql.NullString

		if err := rows.Scan(
			&j.ID, &j.Filepath, &j.Preset, &j.WatchName, &j.OutputPath, &j.Status, &pos,
			&j.Attempts, &j.CreatedAt, &started, &completed, &errMsg, &logOut,
		); err != nil {
			return nil, err
		}

		if pos.Valid {
			p := pos.Int64
			j.Position = &p
		}
		if started.Valid {
			j.StartedAt = &started.Time
		}
		if completed.Valid {
			j.CompletedAt = &completed.Time
		}
		if errMsg.Valid {
			j.ErrorMessage = &errMsg.String
		}
		if logOut.Valid {
			j.LogOutput = &logOut.String
		}

		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}
