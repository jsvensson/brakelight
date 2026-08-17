package worker

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jsvensson/brakelight/internal/config"
	"github.com/jsvensson/brakelight/internal/db"
)

const encodeTimeout = 4 * time.Hour

// Worker processes jobs from the queue one at a time.
type Worker struct {
	db            *db.DB
	config        *config.Service
	handbrakePath string
	progress      *Progress
}

// New creates a new Worker.
func New(database *db.DB, cfg *config.Service, handbrakePath string) *Worker {
	return &Worker{
		db:            database,
		config:        cfg,
		handbrakePath: handbrakePath,
		progress:      &Progress{},
	}
}

// Run starts the worker loop. It returns when ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	if n, err := w.db.RecoverProcessingJobs(); err != nil {
		log.Printf("Worker error recovering interrupted jobs: %v", err)
	} else if n > 0 {
		log.Printf("Recovered %d interrupted job(s) to pending queue", n)
	}

	pausedLogged := false
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		active, err := w.db.IsEncodingActive()
		if err != nil {
			log.Printf("Worker error reading active state: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if !active {
			if !pausedLogged {
				log.Println("Worker paused: encoding is inactive")
				pausedLogged = true
			}
			time.Sleep(5 * time.Second)
			continue
		}
		pausedLogged = false

		job, err := w.db.NextPendingJob()
		if err != nil {
			log.Printf("Worker error fetching job: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if job == nil {
			time.Sleep(5 * time.Second)
			continue
		}

		if err := w.processJob(ctx, job); err != nil {
			log.Printf("Job %d failed: %v", job.ID, err)
		}
	}
}

// Progress returns the worker's live progress state.
func (w *Worker) Progress() *Progress {
	return w.progress
}

func (w *Worker) processJob(ctx context.Context, job *db.Job) error {
	if _, err := os.Stat(job.Filepath); os.IsNotExist(err) {
		return w.db.SetJobFailed(job.ID, "source file disappeared", "")
	}

	// Ensure stability before encoding.
	if err := waitStable(job.Filepath, 5*time.Second); err != nil {
		log.Printf("Job %d source not stable, will retry: %v", job.ID, err)
		return w.retryOrFail(job, "source file not stable", "")
	}

	if err := w.db.SetJobProcessing(job.ID); err != nil {
		return fmt.Errorf("mark processing: %w", err)
	}

	if size, err := fileSize(job.Filepath); err != nil {
		log.Printf("Job %d: could not stat source file size: %v", job.ID, err)
	} else if err := w.db.SetJobSourceSize(job.ID, int(size)); err != nil {
		log.Printf("Job %d: could not store source file size: %v", job.ID, err)
	}

	partialPath := job.OutputPath + w.config.Config.PartialExtension

	log.Printf("Starting job %d: %s -> %s", job.ID, job.Filepath, partialPath)

	w.progress.Start(job.ID)
	defer w.progress.Stop()

	output, err := w.runHandBrake(ctx, job.Filepath, partialPath, job.Preset)
	if err != nil {
		// Clean up partial file on failure.
		_ = os.Remove(partialPath)
		return w.retryOrFail(job, fmt.Sprintf("%v", err), output)
	}

	if err := os.Rename(partialPath, job.OutputPath); err != nil {
		_ = os.Remove(partialPath)
		return w.retryOrFail(job, fmt.Sprintf("rename output: %v", err), output)
	}

	var outputSize *int
	if size, err := fileSize(job.OutputPath); err != nil {
		log.Printf("Job %d: could not stat output file size: %v", job.ID, err)
	} else {
		s := int(size)
		outputSize = &s
	}

	if err := w.db.SetJobCompleted(job.ID, output, outputSize); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	log.Printf("Completed job %d: %s", job.ID, job.OutputPath)
	return nil
}

func (w *Worker) runHandBrake(ctx context.Context, input, output, preset string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, encodeTimeout)
	defer cancel()

	args := []string{
		"--preset-import-file", w.config.Config.UserPresets,
		"--preset", preset,
		"-i", input,
		"-o", output,
		"--all-audio",
		"--all-subtitles",
	}

	cmd := exec.CommandContext(ctx, w.handbrakePath, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start HandBrakeCLI: %w", err)
	}

	var logBuf logBuffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		w.scanOutput(stdout, &logBuf)
	}()
	go func() {
		defer wg.Done()
		w.scanOutput(stderr, &logBuf)
	}()

	wg.Wait()
	waitErr := cmd.Wait()

	return logBuf.String(), waitErr
}

// scanOutput reads HandBrakeCLI output line by line. Progress lines update
// the live progress state; everything else is appended to the log buffer.
func (w *Worker) scanOutput(r io.Reader, logBuf *logBuffer) {
	scanner := NewProgressScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if w.progress.UpdateLine(line) {
			continue
		}
		logBuf.WriteString(line + "\n")
	}
}

// logBuffer is a mutex-guarded strings.Builder for concurrent stdout/stderr.
type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *logBuffer) WriteString(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.WriteString(s)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (w *Worker) retryOrFail(job *db.Job, message, logOutput string) error {
	if err := w.db.IncrementAttempts(job.ID); err != nil {
		log.Printf("Failed to increment attempts for job %d: %v", job.ID, err)
	}

	if job.Attempts+1 < w.config.Config.MaxAttempts {
		log.Printf("Retrying job %d (attempt %d/%d)", job.ID, job.Attempts+1, w.config.Config.MaxAttempts)
		pos, err := w.db.NextPosition()
		if err != nil {
			return w.db.SetJobFailed(job.ID, message, logOutput)
		}
		return w.db.ResetJobToPending(job.ID, pos)
	}

	return w.db.SetJobFailed(job.ID, message, logOutput)
}

func waitStable(path string, duration time.Duration) error {
	first, err := fileSize(path)
	if err != nil {
		return err
	}

	time.Sleep(duration)

	second, err := fileSize(path)
	if err != nil {
		return err
	}

	if first != second {
		return fmt.Errorf("file still copying")
	}
	if first == 0 {
		return fmt.Errorf("file is empty")
	}

	return nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		return 0, fmt.Errorf("path is a directory")
	}
	return info.Size(), nil
}

