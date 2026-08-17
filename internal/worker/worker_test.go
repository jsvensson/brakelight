package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	w.runPreCommands(context.Background(), cmds, outputPath, &buf)

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

func TestRunPreCommandsFailureIsLogged(t *testing.T) {
	var buf logBuffer
	w := &Worker{}
	w.runPreCommands(context.Background(), []string{"exit 1"}, "/tmp/out.mkv", &buf)

	if !strings.Contains(buf.String(), "pre-command failed") {
		t.Errorf("expected failure to be recorded in log:\n%s", buf.String())
	}
}

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
	w.runPostCommands(context.Background(), cmds, outputPath, &buf)

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

func TestRunPostCommandsFailureIsLogged(t *testing.T) {
	var buf logBuffer
	w := &Worker{}
	w.runPostCommands(context.Background(), []string{"exit 1"}, "/tmp/out.mkv", &buf)

	if !strings.Contains(buf.String(), "post-command failed") {
		t.Errorf("expected failure to be recorded in log:\n%s", buf.String())
	}
}
