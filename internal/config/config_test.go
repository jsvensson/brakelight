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
