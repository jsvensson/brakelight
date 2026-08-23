package worker

import (
	"strings"
	"testing"
)

const sampleProgressLine = "Encoding: task 1 of 2, 34.52 % (123.45 fps, avg 98.76 fps, ETA 00h12m03s)"

// A HandBrakeCLI "Encoding: task ..." line is recognized as progress and its
// task, percent, fps, and ETA fields are parsed into the snapshot.
func TestUpdateLineParsesProgress(t *testing.T) {
	p := &Progress{}
	p.Start(42)

	if !p.UpdateLine(sampleProgressLine) {
		t.Fatal("expected line to be recognized as progress")
	}

	snap := p.Snapshot()
	if !snap.Active {
		t.Error("expected active")
	}
	if snap.JobID != 42 {
		t.Errorf("JobID = %d, want 42", snap.JobID)
	}
	if snap.Task != 1 || snap.TaskCount != 2 {
		t.Errorf("Task = %d of %d, want 1 of 2", snap.Task, snap.TaskCount)
	}
	if snap.Percent != 34.52 {
		t.Errorf("Percent = %v, want 34.52", snap.Percent)
	}
	if snap.FPS != 123.45 {
		t.Errorf("FPS = %v, want 123.45", snap.FPS)
	}
	if snap.ETA != "00h12m03s" {
		t.Errorf("ETA = %q, want %q", snap.ETA, "00h12m03s")
	}
}

// Ordinary HandBrakeCLI output lines are not progress and must leave the
// snapshot untouched.
func TestUpdateLineIgnoresNonProgress(t *testing.T) {
	p := &Progress{}
	p.Start(1)

	for _, line := range []string{
		"[12:00:00] hb_init: starting libhb thread",
		"Scanning new sources...",
		"Muxing: this may take a while",
		"",
	} {
		if p.UpdateLine(line) {
			t.Errorf("expected %q to not be progress", line)
		}
	}

	if snap := p.Snapshot(); snap.Percent != 0 {
		t.Errorf("Percent = %v, want 0 after non-progress lines", snap.Percent)
	}
}

// Start resets any stale progress from a previous job, and Stop marks the
// snapshot inactive.
func TestProgressStartStop(t *testing.T) {
	p := &Progress{}
	p.UpdateLine(sampleProgressLine)
	p.Start(7)
	if snap := p.Snapshot(); snap.Percent != 0 {
		t.Errorf("Start should reset percent, got %v", snap.Percent)
	}
	p.Stop()
	if snap := p.Snapshot(); snap.Active {
		t.Error("expected inactive after Stop")
	}
}

// The progress scanner must split tokens on \r, \n, and \r\n, since
// HandBrakeCLI uses all three as line terminators.
func TestSplitLinesHandlesCRandLF(t *testing.T) {
	input := "line one\rline two\nline three\r\nline four"
	s := NewProgressScanner(strings.NewReader(input))

	var got []string
	for s.Scan() {
		got = append(got, s.Text())
	}
	if err := s.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	want := []string{"line one", "line two", "line three", "", "line four"}
	if len(got) != len(want) {
		t.Fatalf("got %d tokens %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// End to end: feeding a \r-separated stream through the scanner yields each
// in-place progress rewrite as its own line, so the snapshot ends up with the
// last reported percent.
func TestScannerFeedsProgressFromCRStream(t *testing.T) {
	// Simulates HandBrakeCLI rewriting progress in place with \r.
	stream := "Scanning new sources...\n" +
		"Encoding: task 1 of 1, 1.00 % (10.0 fps, avg 10.0 fps, ETA 01h00m00s)\r" +
		"Encoding: task 1 of 1, 50.00 % (60.0 fps, avg 55.0 fps, ETA 00h30m00s)\r" +
		"Encode done!\n"

	p := &Progress{}
	p.Start(1)

	s := NewProgressScanner(strings.NewReader(stream))
	for s.Scan() {
		p.UpdateLine(s.Text())
	}

	snap := p.Snapshot()
	if snap.Percent != 50.0 {
		t.Errorf("Percent = %v, want 50", snap.Percent)
	}
}
