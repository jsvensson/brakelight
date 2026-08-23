package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.hcl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// A watch's post_commands list is parsed in order, and a watch without the
// key gets nil rather than an empty list.
func TestWatchPostCommands(t *testing.T) {
	path := writeConfig(t, `
config {
  output_dir   = "/media/encoded"
  user_presets = "/presets.json"
}

watch "general" {
  path   = "/media/watch"
  preset = "Standard"
  post_commands = [
    "logger 'Encoded: {output_file}'",
    "cp {output} /archive/{output_file}",
  ]
}

watch "plain" {
  path   = "/media/other"
  preset = "Standard"
}
`)

	svc, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	got := svc.Watch[0].PostCommands
	want := []string{"logger 'Encoded: {output_file}'", "cp {output} /archive/{output_file}"}
	if len(got) != len(want) {
		t.Fatalf("expected %d post commands, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("post_commands[%d]: expected %q, got %q", i, want[i], got[i])
		}
	}

	if svc.Watch[1].PostCommands != nil {
		t.Errorf("expected nil post_commands when absent, got %v", svc.Watch[1].PostCommands)
	}
}

// A watch's pre_commands list is parsed in order, and a watch without the
// key gets nil rather than an empty list.
func TestWatchPreCommands(t *testing.T) {
	path := writeConfig(t, `
config {
  output_dir   = "/media/encoded"
  user_presets = "/presets.json"
}

watch "general" {
  path   = "/media/watch"
  preset = "Standard"
  pre_commands = [
    "touch /staging/{output_file}.lock",
    "logger 'Starting: {output_file}'",
  ]
}

watch "plain" {
  path   = "/media/other"
  preset = "Standard"
}
`)

	svc, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	got := svc.Watch[0].PreCommands
	want := []string{"touch /staging/{output_file}.lock", "logger 'Starting: {output_file}'"}
	if len(got) != len(want) {
		t.Fatalf("expected %d pre commands, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pre_commands[%d]: expected %q, got %q", i, want[i], got[i])
		}
	}

	if svc.Watch[1].PreCommands != nil {
		t.Errorf("expected nil pre_commands when absent, got %v", svc.Watch[1].PreCommands)
	}
}

// fail_on_pre_command_error is honored when set on a watch and defaults to
// false when absent.
func TestWatchFailOnPreCommandError(t *testing.T) {
	path := writeConfig(t, `
config {
  output_dir   = "/media/encoded"
  user_presets = "/presets.json"
}

watch "strict" {
  path   = "/media/watch"
  preset = "Standard"
  fail_on_pre_command_error = true
}

watch "plain" {
  path   = "/media/other"
  preset = "Standard"
}
`)

	svc, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if !svc.Watch[0].FailOnPreCommandError {
		t.Error("expected fail_on_pre_command_error to be true when set")
	}
	if svc.Watch[1].FailOnPreCommandError {
		t.Error("expected fail_on_pre_command_error to default to false")
	}
}

// A watch without an explicit preset inherits the config's default_preset.
func TestWatchPresetDefaultsToConfigDefaultPreset(t *testing.T) {
	path := writeConfig(t, `
config {
  output_dir     = "/media/encoded"
  user_presets   = "/presets.json"
  default_preset = "Standard"
}

watch "general" {
  path = "/media/watch"
}
`)

	svc, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if got := svc.Watch[0].Preset; got != "Standard" {
		t.Errorf("expected watch preset to default to Standard, got %q", got)
	}
}

// A watch's explicit preset wins over the config's default_preset.
func TestWatchPresetOverridesConfigDefaultPreset(t *testing.T) {
	path := writeConfig(t, `
config {
  output_dir     = "/media/encoded"
  user_presets   = "/presets.json"
  default_preset = "Standard"
}

watch "animated" {
  path   = "/media/watch"
  preset = "Animated"
}
`)

	svc, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if got := svc.Watch[0].Preset; got != "Animated" {
		t.Errorf("expected watch preset override Animated, got %q", got)
	}
}

// Loading fails when a watch has no preset and the config provides no
// default_preset to fall back on.
func TestWatchPresetRequiredWithoutDefaultPreset(t *testing.T) {
	path := writeConfig(t, `
config {
  output_dir   = "/media/encoded"
  user_presets = "/presets.json"
}

watch "general" {
  path = "/media/watch"
}
`)

	if _, err := Load(path); err == nil {
		t.Error("expected error when watch has no preset and config.default_preset is not set")
	}
}

// Without a db_file setting, the database lives in the per-user application
// support directory, which is created if missing.
func TestDBPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &Config{}
	got, err := cfg.DBPath()
	if err != nil {
		t.Fatalf("DBPath: %v", err)
	}

	want := filepath.Join(home, "Library", "Application Support", "brakelight", "queue.db")
	if got != want {
		t.Errorf("expected DBPath %q, got %q", want, got)
	}

	if info, err := os.Stat(filepath.Dir(got)); err != nil || !info.IsDir() {
		t.Errorf("expected app support dir to be created: %v", err)
	}
}

// A db_file setting overrides the default database location, and its parent
// directories are created if missing.
func TestDBPathCustomFile(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "nested", "myqueue.db")

	path := writeConfig(t, `
config {
  output_dir   = "/media/encoded"
  user_presets = "/presets.json"
  db_file      = "`+dbFile+`"
}

watch "general" {
  path   = "/media/watch"
  preset = "Standard"
}
`)

	svc, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	got, err := svc.Config.DBPath()
	if err != nil {
		t.Fatalf("DBPath: %v", err)
	}
	if got != dbFile {
		t.Errorf("expected custom db_file %q, got %q", dbFile, got)
	}

	if info, err := os.Stat(filepath.Dir(dbFile)); err != nil || !info.IsDir() {
		t.Errorf("expected database dir to be created: %v", err)
	}
}

// A leading ~/ in db_file is expanded to the user's home directory at load
// time.
func TestDBFileExpandsHome(t *testing.T) {
	path := writeConfig(t, `
config {
  output_dir   = "/media/encoded"
  user_presets = "/presets.json"
  db_file      = "~/db/custom.db"
}

watch "general" {
  path   = "/media/watch"
  preset = "Standard"
}
`)

	svc, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	want := filepath.Join(os.Getenv("HOME"), "db", "custom.db")
	if got := svc.Config.DBFile; got != want {
		t.Errorf("expected expanded db_file %q, got %q", want, got)
	}
}

// WatchByName finds a watch by its block label and returns nil for names
// that are not configured.
func TestWatchByName(t *testing.T) {
	svc := &Service{Watch: []Watch{{Name: "general"}, {Name: "animated"}}}

	if w := svc.WatchByName("animated"); w == nil || w.Name != "animated" {
		t.Errorf("expected to find watch 'animated', got %v", w)
	}
	if w := svc.WatchByName("missing"); w != nil {
		t.Errorf("expected nil for unknown watch, got %v", w)
	}
}

// A watch without an explicit output_dir inherits the config's output_dir.
func TestWatchOutputDirDefaultsToConfigOutputDir(t *testing.T) {
	path := writeConfig(t, `
config {
  output_dir   = "/media/encoded"
  user_presets = "/presets.json"
}

watch "general" {
  path   = "/media/watch"
  preset = "Standard"
}
`)

	svc, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if got := svc.Watch[0].OutputDir; got != "/media/encoded" {
		t.Errorf("expected watch output_dir to default to /media/encoded, got %q", got)
	}
}

// A watch's explicit output_dir wins over the config's output_dir.
func TestWatchOutputDirOverridesConfigOutputDir(t *testing.T) {
	path := writeConfig(t, `
config {
  output_dir   = "/media/encoded"
  user_presets = "/presets.json"
}

watch "general" {
  path       = "/media/watch"
  preset     = "Standard"
  output_dir = "/media/animated-out"
}
`)

	svc, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if got := svc.Watch[0].OutputDir; got != "/media/animated-out" {
		t.Errorf("expected watch output_dir override /media/animated-out, got %q", got)
	}
}

// A leading ~/ in a watch's output_dir is expanded to the user's home
// directory at load time.
func TestWatchOutputDirExpandsHome(t *testing.T) {
	path := writeConfig(t, `
config {
  output_dir   = "/media/encoded"
  user_presets = "/presets.json"
}

watch "general" {
  path       = "/media/watch"
  preset     = "Standard"
  output_dir = "~/encoded"
}
`)

	svc, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	want := filepath.Join(os.Getenv("HOME"), "encoded")
	if got := svc.Watch[0].OutputDir; got != want {
		t.Errorf("expected expanded watch output_dir %q, got %q", want, got)
	}
}
