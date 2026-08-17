package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2/hclsimple"
)

// Watch maps a directory to a HandBrake preset.
type Watch struct {
	Name         string   `hcl:",label"`
	Path         string   `hcl:"path"`
	Preset       string   `hcl:"preset,optional"`
	OutputDir    string   `hcl:"output_dir,optional"`
	PreCommands  []string `hcl:"pre_commands,optional"`
	PostCommands []string `hcl:"post_commands,optional"`
}

// Config holds top-level service settings.
type Config struct {
	OutputDir        string `hcl:"output_dir"`
	UserPresets      string `hcl:"user_presets"`
	DefaultPreset    string `hcl:"default_preset,optional"`
	HandBrakeCLI     string `hcl:"handbrake_cli,optional"`
	LogFile          string `hcl:"log_file,optional"`
	ScanInterval     string `hcl:"scan_interval,optional"`
	PartialExtension string `hcl:"partial_extension,optional"`
	MaxAttempts      int    `hcl:"max_attempts,optional"`
	ListenAddr       string `hcl:"listen_addr,optional"`
}

// Root is the top-level HCL structure.
type Root struct {
	Config *Config `hcl:"config,block"`
	Watch  []Watch `hcl:"watch,block"`
}

// Service wraps the parsed configuration with defaults applied.
type Service struct {
	Config *Config
	Watch  []Watch
}

// ScanIntervalDefault is the default directory scan interval.
const ScanIntervalDefault = "30s"

// PartialExtensionDefault is the extension used while encoding.
const PartialExtensionDefault = ".partial"

// MaxAttemptsDefault is the default retry limit.
const MaxAttemptsDefault = 3

// Load reads and parses an HCL config file.
func Load(path string) (*Service, error) {
	var root Root
	if err := hclsimple.DecodeFile(path, nil, &root); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	if root.Config == nil {
		return nil, fmt.Errorf("missing required 'config' block")
	}

	if root.Config.OutputDir == "" {
		return nil, fmt.Errorf("config.output_dir is required")
	}
	root.Config.OutputDir = expandPath(root.Config.OutputDir)

	if root.Config.UserPresets == "" {
		return nil, fmt.Errorf("config.user_presets is required")
	}
	root.Config.UserPresets = expandPath(root.Config.UserPresets)

	if root.Config.LogFile != "" {
		root.Config.LogFile = expandPath(root.Config.LogFile)
	}

	if root.Config.HandBrakeCLI != "" {
		root.Config.HandBrakeCLI = expandPath(root.Config.HandBrakeCLI)
	}

	if root.Config.ScanInterval == "" {
		root.Config.ScanInterval = ScanIntervalDefault
	}

	if root.Config.PartialExtension == "" {
		root.Config.PartialExtension = PartialExtensionDefault
	}

	if root.Config.MaxAttempts == 0 {
		root.Config.MaxAttempts = MaxAttemptsDefault
	}

	if root.Config.ListenAddr == "" {
		root.Config.ListenAddr = ":8080"
	}

	if len(root.Watch) == 0 {
		return nil, fmt.Errorf("at least one 'watch' block is required")
	}

	for i, w := range root.Watch {
		root.Watch[i].Path = expandPath(w.Path)
		if w.Preset == "" {
			if root.Config.DefaultPreset == "" {
				return nil, fmt.Errorf("watch %q: preset is required and config.default_preset is not set", w.Name)
			}
			root.Watch[i].Preset = root.Config.DefaultPreset
		}
		if w.OutputDir == "" {
			root.Watch[i].OutputDir = root.Config.OutputDir
		} else {
			root.Watch[i].OutputDir = expandPath(w.OutputDir)
		}
	}

	return &Service{Config: root.Config, Watch: root.Watch}, nil
}

// DBPath returns the path to the SQLite database.
func (c *Config) DBPath() string {
	supportDir := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "brakelight")
	_ = os.MkdirAll(supportDir, 0o755)
	return filepath.Join(supportDir, "queue.db")
}

// PresetForDir returns the preset for a given watch directory.
func (s *Service) PresetForDir(dir string) (string, bool) {
	for _, w := range s.Watch {
		if w.Path == dir {
			return w.Preset, true
		}
	}
	return "", false
}

// WatchByName returns the watch block with the given name, or nil.
func (s *Service) WatchByName(name string) *Watch {
	for i := range s.Watch {
		if s.Watch[i].Name == name {
			return &s.Watch[i]
		}
	}
	return nil
}

func expandPath(path string) string {
	if path == "~" || (len(path) > 1 && path[:2] == "~/") {
		home := os.Getenv("HOME")
		if home == "" {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
