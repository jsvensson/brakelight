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
// added. Without track selections, all audio and subtitle tracks are kept.
func TestHandbrakeArgs(t *testing.T) {
	w := &Worker{config: &config.Service{Config: &config.Config{UserPresets: "/presets.json"}}}

	args := w.handbrakeArgs("/in/a.mkv", "/out/a.mkv", "Standard", "", true, nil, true, nil)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--srt") {
		t.Errorf("expected no srt flags without subtitle, got %v", args)
	}
	for _, want := range []string{"--all-audio", "--all-subtitles"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected args to contain %q: %v", args, want)
		}
	}

	args = w.handbrakeArgs("/in/a.mkv", "/out/a.mkv", "Standard", "/in/a.srt", true, nil, true, nil)
	joined = strings.Join(args, " ")
	for _, want := range []string{"--srt-file /in/a.srt", "--srt-lang eng", "--srt-default"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected args to contain %q: %v", want, args)
		}
	}
}

// Explicit track selections replace --all-audio/--all-subtitles with -a/-s
// lists; an empty selection becomes "none".
func TestHandbrakeArgsWithSelection(t *testing.T) {
	w := &Worker{config: &config.Service{Config: &config.Config{UserPresets: "/presets.json"}}}

	args := w.handbrakeArgs("/in/a.mkv", "/out/a.mkv", "Standard", "", false, []int{2}, false, []int{1, 3})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--all-audio") || strings.Contains(joined, "--all-subtitles") {
		t.Errorf("expected no --all flags with explicit selection, got %v", args)
	}
	for _, want := range []string{"-a 2", "-s 1,3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected args to contain %q: %v", want, args)
		}
	}

	args = w.handbrakeArgs("/in/a.mkv", "/out/a.mkv", "Standard", "", false, nil, false, nil)
	joined = strings.Join(args, " ")
	for _, want := range []string{"-a none", "-s none"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected args to contain %q: %v", want, args)
		}
	}
}

// resolveSelection maps stored rules to track numbers using the job's probed
// tracks. A nil or "all" rule, missing probe data, or a rule matching nothing
// falls back to keeping all tracks.
func TestResolveSelection(t *testing.T) {
	tracksJSON := `{"tracks":[
		{"num":1,"type":"audio","language":"ita","default":true,"dub":true},
		{"num":2,"type":"audio","language":"eng","original":true},
		{"num":1,"type":"subtitle","language":"ita","default":true},
		{"num":2,"type":"subtitle","language":"eng"}
	]}`

	// nil rule keeps all
	if all, nums := resolveSelection(nil, &tracksJSON, "audio"); !all || nums != nil {
		t.Errorf("nil rule: expected all=true, got all=%v nums=%v", all, nums)
	}

	// "original" resolves to the English audio track via the original disposition
	rule := "original"
	if all, nums := resolveSelection(&rule, &tracksJSON, "audio"); all || len(nums) != 1 || nums[0] != 2 {
		t.Errorf("original rule: expected all=false nums=[2], got all=%v nums=%v", all, nums)
	}

	// "tracks:1" resolves to the explicit track
	rule = "tracks:1"
	if all, nums := resolveSelection(&rule, &tracksJSON, "subtitle"); all || len(nums) != 1 || nums[0] != 1 {
		t.Errorf("tracks:1 rule: expected all=false nums=[1], got all=%v nums=%v", all, nums)
	}

	// "none" selects nothing but is not the "all" fallback
	rule = "none"
	if all, nums := resolveSelection(&rule, &tracksJSON, "audio"); all || len(nums) != 0 {
		t.Errorf("none rule: expected all=false nums=[], got all=%v nums=%v", all, nums)
	}

	// missing probe data falls back to all
	rule = "original"
	if all, _ := resolveSelection(&rule, nil, "audio"); !all {
		t.Errorf("missing tracks: expected fallback all=true, got all=false")
	}

	// a rule matching nothing falls back to all
	rule = "lang:deu"
	if all, _ := resolveSelection(&rule, &tracksJSON, "audio"); !all {
		t.Errorf("unmatched rule: expected fallback all=true, got all=false")
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

// commaSafeSubtitle passes comma-free paths through unchanged with a no-op
// cleanup. A path containing a comma is replaced with a comma-free temp
// path with the same contents; cleanup removes the temp directory.
func TestCommaSafeSubtitle(t *testing.T) {
	dir := t.TempDir()

	plain := filepath.Join(dir, "plain.srt")
	if err := os.WriteFile(plain, []byte("subs"), 0o644); err != nil {
		t.Fatalf("write srt: %v", err)
	}

	got, cleanup, err := commaSafeSubtitle(plain)
	if err != nil {
		t.Fatalf("commaSafeSubtitle: %v", err)
	}
	if got != plain {
		t.Errorf("expected unchanged path %q, got %q", plain, got)
	}
	cleanup()

	srt := filepath.Join(dir, "Some Movie, Extended Cut.srt")
	if err := os.WriteFile(srt, []byte("subs"), 0o644); err != nil {
		t.Fatalf("write srt: %v", err)
	}

	got, cleanup, err = commaSafeSubtitle(srt)
	if err != nil {
		t.Fatalf("commaSafeSubtitle: %v", err)
	}
	if strings.Contains(got, ",") {
		t.Errorf("expected comma-free path, got %q", got)
	}
	content, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read temp srt: %v", err)
	}
	if string(content) != "subs" {
		t.Errorf("expected temp srt contents %q, got %q", "subs", content)
	}

	tmpDir := filepath.Dir(got)
	cleanup()
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("expected temp dir removed after cleanup, stat err: %v", err)
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
