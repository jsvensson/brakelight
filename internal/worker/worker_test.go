package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jsvensson/brakelight/internal/config"
)

// The HandBrakeCLI arguments embed a sidecar SRT as an English default
// subtitle track when one is given; without a sidecar, no SRT flags are
// added.
func TestHandbrakeArgs(t *testing.T) {
	w := &Worker{config: &config.Service{Config: &config.Config{UserPresets: "/presets.json"}}}

	args := w.handbrakeArgs("/in/a.mkv", "/out/a.mkv", "Standard", "")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--srt") {
		t.Errorf("expected no srt flags without subtitle, got %v", args)
	}

	args = w.handbrakeArgs("/in/a.mkv", "/out/a.mkv", "Standard", "/in/a.srt")
	joined = strings.Join(args, " ")
	for _, want := range []string{"--srt-file /in/a.srt", "--srt-lang eng", "--srt-default"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected args to contain %q: %v", want, args)
		}
	}
}

// sidecarSubtitle finds an .srt file with the same base name adjacent to the
// media file. It returns an empty string when no sidecar exists or only a
// differently named .srt is present.
func TestSidecarSubtitle(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "a.mkv")
	if err := os.WriteFile(media, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}

	if got := sidecarSubtitle(media); len(got) > 0 {
		t.Errorf("expected empty string without sidecar, got %q", got)
	}

	other := filepath.Join(dir, "b.srt")
	if err := os.WriteFile(other, []byte("subs"), 0o644); err != nil {
		t.Fatalf("write other srt: %v", err)
	}
	if got := sidecarSubtitle(media); len(got) > 0 {
		t.Errorf("expected empty string for differently named srt, got %q", got)
	}

	srt := filepath.Join(dir, "a.srt")
	if err := os.WriteFile(srt, []byte("subs"), 0o644); err != nil {
		t.Fatalf("write srt: %v", err)
	}
	if got := sidecarSubtitle(media); got != srt {
		t.Errorf("expected %q, got %q", srt, got)
	}
}

// The {output}, {output_file}, and {output_path} placeholders in
// pre/post commands are replaced with the full output path, its basename,
// and its directory. Commands without placeholders pass through unchanged.
func TestSubstituteOutput(t *testing.T) {
	const outputPath = "/media/encoded/My Movie.mkv"

	got := substituteOutput("cp {output} /archive/{output_file}; cd {output_path}", outputPath)
	want := "cp /media/encoded/My Movie.mkv /archive/My Movie.mkv; cd /media/encoded"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}

	got = substituteOutput("echo no placeholders", outputPath)
	if got != "echo no placeholders" {
		t.Errorf("expected unchanged command, got %q", got)
	}
}

// Pre-commands run in order through the shell with placeholders substituted,
// and their stdout is captured in the job log.
func TestRunPreCommands(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "movie.mkv")

	marker := filepath.Join(dir, "marker")
	cmds := []string{
		"echo starting: {output_file}",
		"echo {output} > " + marker,
		"echo ok | tr a-z A-Z",
	}

	var buf logBuffer
	w := &Worker{}
	failures := w.runPreCommands(context.Background(), 1, cmds, outputPath, &buf)

	if len(failures) > 0 {
		t.Errorf("expected no failures, got %v", failures)
	}

	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.TrimSpace(string(content)) != outputPath {
		t.Errorf("expected marker to contain %q, got %q", outputPath, content)
	}

	log := buf.String()
	for _, want := range []string{"starting: movie.mkv", "OK"} {
		if !strings.Contains(log, want) {
			t.Errorf("expected log to contain %q:\n%s", want, log)
		}
	}
}

// A failing pre-command is recorded in the job log and returned in the
// failures list identifying the command; the remaining commands still run.
func TestRunPreCommandsFailureIsLogged(t *testing.T) {
	var buf logBuffer
	w := &Worker{}
	failures := w.runPreCommands(context.Background(), 1, []string{"echo hi", "exit 1"}, "/tmp/out.mkv", &buf)

	if !strings.Contains(buf.String(), "pre-command failed") {
		t.Errorf("expected failure to be recorded in log:\n%s", buf.String())
	}

	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d: %v", len(failures), failures)
	}
	if !strings.Contains(failures[0], "pre-command failed: exit 1") {
		t.Errorf("expected failure to describe the command, got %q", failures[0])
	}
}

// Post-commands run in order through the shell with placeholders
// substituted, and their stdout is captured in the job log.
func TestRunPostCommands(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(outputPath, []byte("data"), 0o644); err != nil {
		t.Fatalf("write output file: %v", err)
	}

	marker := filepath.Join(dir, "marker")
	cmds := []string{
		"echo encoded: {output_file}",
		"echo {output_file} > " + marker,
		"echo ok | tr a-z A-Z",
	}

	var buf logBuffer
	w := &Worker{}
	failures := w.runPostCommands(context.Background(), 1, cmds, outputPath, &buf)

	if len(failures) > 0 {
		t.Errorf("expected no failures, got %v", failures)
	}

	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.TrimSpace(string(content)) != "movie.mkv" {
		t.Errorf("expected marker to contain movie.mkv, got %q", content)
	}

	log := buf.String()
	for _, want := range []string{"encoded: movie.mkv", "OK"} {
		if !strings.Contains(log, want) {
			t.Errorf("expected log to contain %q:\n%s", want, log)
		}
	}
}

// A failing post-command is recorded in the job log and returned in the
// failures list identifying the command; the remaining commands still run.
func TestRunPostCommandsFailureIsLogged(t *testing.T) {
	var buf logBuffer
	w := &Worker{}
	failures := w.runPostCommands(context.Background(), 1, []string{"echo hi", "exit 1"}, "/tmp/out.mkv", &buf)

	if !strings.Contains(buf.String(), "post-command failed") {
		t.Errorf("expected failure to be recorded in log:\n%s", buf.String())
	}

	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d: %v", len(failures), failures)
	}
	if !strings.Contains(failures[0], "post-command failed: exit 1") {
		t.Errorf("expected failure to describe the command, got %q", failures[0])
	}
}
