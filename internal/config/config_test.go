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

func TestWatchByName(t *testing.T) {
	svc := &Service{Watch: []Watch{{Name: "general"}, {Name: "animated"}}}

	if w := svc.WatchByName("animated"); w == nil || w.Name != "animated" {
		t.Errorf("expected to find watch 'animated', got %v", w)
	}
	if w := svc.WatchByName("missing"); w != nil {
		t.Errorf("expected nil for unknown watch, got %v", w)
	}
}

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
