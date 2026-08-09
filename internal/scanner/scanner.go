package scanner

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jsvensson/brakelight/internal/config"
	"github.com/jsvensson/brakelight/internal/db"
)

var mediaExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true,
	".ts": true, ".m2ts": true, ".mts": true,
}

// Scanner periodically scans watch directories for new media files.
type Scanner struct {
	db           *db.DB
	config       *config.Service
	pausedLogged bool
	seen         map[string]int64
}

// New creates a new Scanner.
func New(database *db.DB, cfg *config.Service) *Scanner {
	return &Scanner{db: database, config: cfg}
}

// Run starts the scanner loop. It returns when ctx is cancelled.
func (s *Scanner) Run(ctx context.Context) {
	interval, err := time.ParseDuration(s.config.Config.ScanInterval)
	if err != nil {
		log.Printf("Invalid scan_interval %q, using default: %v", s.config.Config.ScanInterval, err)
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.scan()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scan()
		}
	}
}

func (s *Scanner) scan() {
	active, err := s.db.IsScanningActive()
	if err != nil {
		log.Printf("Scanner error reading active state: %v", err)
		return
	}
	if !active {
		if !s.pausedLogged {
			log.Println("Scanner paused: file scanning is inactive")
			s.pausedLogged = true
		}
		return
	}
	s.pausedLogged = false

	current := make(map[string]int64)
	for _, watch := range s.config.Watch {
		if err := s.scanDir(watch, current); err != nil {
			log.Printf("Scanner error for %s: %v", watch.Path, err)
		}
	}
	s.seen = current

	s.pruneStaleJobs()
}

// pruneStaleJobs removes pending jobs whose source file no longer exists.
func (s *Scanner) pruneStaleJobs() {
	jobs, err := s.db.ListPendingJobs()
	if err != nil {
		log.Printf("Scanner error listing pending jobs: %v", err)
		return
	}

	for _, job := range jobs {
		if _, err := os.Stat(job.Filepath); os.IsNotExist(err) {
			if err := s.db.CancelJob(job.ID); err != nil {
				log.Printf("Failed to remove stale job %d (%s): %v", job.ID, job.Filepath, err)
				continue
			}
			log.Printf("Removed stale job %d: %s", job.ID, job.Filepath)
		}
	}

	processing, err := s.db.ListProcessingJobs()
	if err != nil {
		log.Printf("Scanner error listing processing jobs: %v", err)
		return
	}

	for _, job := range processing {
		if _, err := os.Stat(job.Filepath); os.IsNotExist(err) {
			if err := s.db.SetJobFailed(job.ID, "source file no longer exists", ""); err != nil {
				log.Printf("Failed to fail stale job %d (%s): %v", job.ID, job.Filepath, err)
				continue
			}
			log.Printf("Failed stale processing job %d: %s", job.ID, job.Filepath)
		}
	}
}

func (s *Scanner) scanDir(watch config.Watch, current map[string]int64) error {
	info, err := os.Stat(watch.Path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("Watch directory does not exist, skipping: %s", watch.Path)
			return nil
		}
		return err
	}
	if !info.IsDir() {
		log.Printf("Watch path is not a directory, skipping: %s", watch.Path)
		return nil
	}

	entries, err := os.ReadDir(watch.Path)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !mediaExtensions[ext] {
			continue
		}

		path := filepath.Join(watch.Path, name)

		size, err := fileSize(path)
		if err != nil {
			log.Printf("Failed to stat %s: %v", path, err)
			continue
		}
		current[path] = size

		// Only queue files with a non-zero size that was unchanged since
		// the previous scan, so files still being copied are not enqueued.
		if prev, ok := s.seen[path]; !ok || prev != size || size == 0 {
			continue
		}

		if err := s.queueFile(path, watch); err != nil {
			log.Printf("Failed to queue %s: %v", path, err)
		}
	}

	return nil
}

func (s *Scanner) queueFile(path string, watch config.Watch) error {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if base == "" {
		base = fmt.Sprintf("video_%d", time.Now().Unix())
	}

	outputPath := filepath.Join(watch.OutputDir, base+".mkv")
	if _, err := os.Stat(outputPath); err == nil {
		// Target file already exists; source was likely already encoded.
		return nil
	}

	pos, err := s.db.NextPosition()
	if err != nil {
		return err
	}

	// Any existing job row for this filepath (pending, processing, completed,
	// or failed) blocks re-queueing. A file whose output was moved away is
	// only re-encoded once its history row is removed.
	created, err := s.db.CreateJob(path, watch.Preset, watch.Name, outputPath, pos)
	if err != nil {
		return err
	}
	if created {
		log.Printf("Queued: %s [%s]", path, watch.Preset)
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

