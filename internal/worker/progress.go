package worker

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var progressRe = regexp.MustCompile(`Encoding: task (\d+) of (\d+), ([\d.]+) % \(([\d.]+) fps, avg [\d.]+ fps, ETA (\S+)\)`)

// Progress holds live encoding progress for the currently running job.
type Progress struct {
	mu        sync.RWMutex
	active    bool
	jobID     int64
	task      int
	taskCount int
	percent   float64
	fps       float64
	eta       string
}

// Snapshot is a point-in-time copy of the current progress.
type Snapshot struct {
	Active    bool
	JobID     int64
	Task      int
	TaskCount int
	Percent   float64
	FPS       float64
	ETA       string
}

// Start marks a job as actively encoding.
func (p *Progress) Start(jobID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = true
	p.jobID = jobID
	p.task = 0
	p.taskCount = 0
	p.percent = 0
	p.fps = 0
	p.eta = ""
}

// Stop clears the active state.
func (p *Progress) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = false
}

// Snapshot returns a copy of the current progress.
func (p *Progress) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return Snapshot{
		Active:    p.active,
		JobID:     p.jobID,
		Task:      p.task,
		TaskCount: p.taskCount,
		Percent:   p.percent,
		FPS:       p.fps,
		ETA:       p.eta,
	}
}

// UpdateLine parses one output line from HandBrakeCLI. It returns true if the
// line was a progress update (and should be excluded from stored logs).
func (p *Progress) UpdateLine(line string) bool {
	m := progressRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return false
	}

	task, _ := strconv.Atoi(m[1])
	taskCount, _ := strconv.Atoi(m[2])
	percent, _ := strconv.ParseFloat(m[3], 64)
	fps, _ := strconv.ParseFloat(m[4], 64)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.task = task
	p.taskCount = taskCount
	p.percent = percent
	p.fps = fps
	p.eta = m[5]
	return true
}

// SplitLines splits on \n and \r. HandBrakeCLI rewrites progress lines in
// place using carriage returns, so a newline-only scanner never sees updates
// until the process ends.
func SplitLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, c := range data {
		if c == '\n' || c == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// NewProgressScanner returns a bufio.Scanner using SplitLines.
func NewProgressScanner(r interface{ Read([]byte) (int, error) }) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Split(SplitLines)
	return s
}
