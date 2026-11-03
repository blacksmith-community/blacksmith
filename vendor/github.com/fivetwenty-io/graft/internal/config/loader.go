package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

// Load loads configuration from a YAML file at the specified path.
// It returns an error if the file cannot be read or parsed.
func Load(path string) (*Config, error) {
	expandedPath, err := expandPath(path)
	if err != nil {
		return nil, fmt.Errorf("expanding config path: %w", err)
	}

	cleanPath := filepath.Clean(expandedPath)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Start with defaults and overlay only the fields the file explicitly
	// sets (see mergeFileOntoConfig: a naive yaml.Unmarshal onto a
	// prepopulated struct would zero every field the file omits).
	cfg := DefaultConfig()
	if err := mergeFileOntoConfig(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// Validate the loaded configuration.
	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("validating configuration: %w", err)
	}

	return cfg, nil
}

// mergeFileOntoConfig overlays onto cfg only the fields explicitly present
// in the YAML document in data, leaving every field the document omits at
// its current value in cfg (typically a DefaultConfig()). This works around
// goccy/go-yaml's Unmarshal-onto-a-prepopulated-struct behavior, which
// zeroes every field absent from the source document rather than leaving it
// untouched, contradicting Load's default-preserving contract: an operator
// config file that only sets e.g. logging.level must not silently disable
// caching or parallel evaluation by zeroing Cache.Enabled/Parallel.Enabled.
//
// It works by decoding the document twice: once into a generic
// map-of-maps to discover which top-level/nested keys are actually present,
// and once into a scratch Config to get correctly-typed values (bool, int,
// time.Duration, string) via goccy's normal decoding. Only the (section,
// field) pairs found present in the first pass are copied from the second
// pass onto cfg.
func mergeFileOntoConfig(data []byte, cfg *Config) error {
	var present map[string]map[string]any
	if err := yaml.Unmarshal(data, &present); err != nil {
		return err
	}

	var parsed Config
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return err
	}

	for section, keys := range present {
		setters, known := fieldSetters[section]
		if !known {
			continue
		}
		for key := range keys {
			if set, known := setters[key]; known {
				set(cfg, &parsed)
			}
		}
	}

	return nil
}

// fieldSetters maps each config section, and each key within it, to the
// copy of that one field from a parsed document onto the config being
// built. mergeFileOntoConfig walks it with the keys the document actually
// contains; a key with no entry here is one the file may spell but this
// merge does not carry over.
var fieldSetters = map[string]map[string]func(cfg, parsed *Config){
	"engine": {
		"strict_mode":   func(cfg, parsed *Config) { cfg.Engine.StrictMode = parsed.Engine.StrictMode },
		"max_recursion": func(cfg, parsed *Config) { cfg.Engine.MaxRecursion = parsed.Engine.MaxRecursion },
		"timeout":       func(cfg, parsed *Config) { cfg.Engine.Timeout = parsed.Engine.Timeout },
	},
	"cache": {
		"enabled":    func(cfg, parsed *Config) { cfg.Cache.Enabled = parsed.Cache.Enabled },
		"max_size":   func(cfg, parsed *Config) { cfg.Cache.MaxSize = parsed.Cache.MaxSize },
		"ttl":        func(cfg, parsed *Config) { cfg.Cache.TTL = parsed.Cache.TTL },
		"l2_enabled": func(cfg, parsed *Config) { cfg.Cache.L2Enabled = parsed.Cache.L2Enabled },
		"l2_path":    func(cfg, parsed *Config) { cfg.Cache.L2Path = parsed.Cache.L2Path },
	},
	"parallel": {
		"enabled":     func(cfg, parsed *Config) { cfg.Parallel.Enabled = parsed.Parallel.Enabled },
		"min_workers": func(cfg, parsed *Config) { cfg.Parallel.MinWorkers = parsed.Parallel.MinWorkers },
		"max_workers": func(cfg, parsed *Config) { cfg.Parallel.MaxWorkers = parsed.Parallel.MaxWorkers },
	},
	"metrics": {
		"enabled":  func(cfg, parsed *Config) { cfg.Metrics.Enabled = parsed.Metrics.Enabled },
		"format":   func(cfg, parsed *Config) { cfg.Metrics.Format = parsed.Metrics.Format },
		"endpoint": func(cfg, parsed *Config) { cfg.Metrics.Endpoint = parsed.Metrics.Endpoint },
	},
	"logging": {
		"level":  func(cfg, parsed *Config) { cfg.Logging.Level = parsed.Logging.Level },
		"format": func(cfg, parsed *Config) { cfg.Logging.Format = parsed.Logging.Format },
	},
}

// LoadOrDefault attempts to load configuration from the specified path.
// If the file does not exist or cannot be loaded, it returns the default configuration.
func LoadOrDefault(path string) *Config {
	cfg, err := Load(path)
	if err != nil {
		return DefaultConfig()
	}
	return cfg
}

// LoadWithSearch searches for configuration files in standard locations
// and loads the first one found. Search order:
//
// 1. ./graft.yaml (current directory)
//
// 2. $HOME/.graft/config.yaml (user config directory)
//
// 3. /etc/graft/config.yaml (system config)
//
// If no configuration file is found, it returns the default configuration.
func LoadWithSearch() (*Config, string, error) {
	searchPaths := getSearchPaths()

	for _, path := range searchPaths {
		expandedPath, err := expandPath(path)
		if err != nil {
			continue
		}

		if _, err := os.Stat(expandedPath); err == nil {
			cfg, err := Load(expandedPath)
			if err != nil {
				return nil, "", fmt.Errorf("loading config from %s: %w", expandedPath, err)
			}
			return cfg, expandedPath, nil
		}
	}

	return DefaultConfig(), "", nil
}

// SaveAs saves the configuration to a YAML file at the specified path.
func SaveAs(cfg *Config, path string) error {
	expandedPath, err := expandPath(path)
	if err != nil {
		return fmt.Errorf("expanding config path: %w", err)
	}

	// Ensure the parent directory exists.
	dir := filepath.Dir(expandedPath)
	if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
		return fmt.Errorf("creating config directory: %w", mkdirErr)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling configuration: %w", err)
	}

	if err := os.WriteFile(expandedPath, data, 0o600); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	return nil
}

// LoadAndApplyEnv loads configuration from a file and applies environment variable overrides.
func LoadAndApplyEnv(path string) (*Config, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}

	ApplyEnv(cfg)

	// Re-validate after applying environment overrides.
	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("validating configuration after env overrides: %w", err)
	}

	return cfg, nil
}

// Load loads configuration into a Manager from the specified path.
func (m *Manager) Load(path string) error {
	cfg, err := LoadAndApplyEnv(path)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.config = cfg
	m.configPath = path
	hooks := make([]func(*Config), len(m.changeHooks))
	copy(hooks, m.changeHooks)
	m.mu.Unlock()

	// Notify change hooks.
	for _, hook := range hooks {
		go hook(cfg.Clone())
	}

	return nil
}

// LoadOrDefault loads configuration from path or uses defaults.
func (m *Manager) LoadOrDefault(path string) {
	if err := m.Load(path); err != nil {
		m.mu.Lock()
		m.config = DefaultConfig()
		m.configPath = ""
		m.mu.Unlock()
	}
}

// getSearchPaths returns the list of paths to search for configuration files.
func getSearchPaths() []string {
	paths := []string{
		"./graft.yaml",
		"~/.graft/config.yaml",
	}

	// Add system config path on Unix-like systems.
	if _, err := os.Stat("/etc"); err == nil {
		paths = append(paths, "/etc/graft/config.yaml")
	}

	return paths
}

// expandPath expands ~ and environment variables in paths.
func expandPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	// Expand ~ to home directory.
	if path != "" && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("getting home directory: %w", err)
		}
		path = filepath.Join(home, path[1:])
	}

	// Expand environment variables.
	path = os.ExpandEnv(path)

	return path, nil
}
