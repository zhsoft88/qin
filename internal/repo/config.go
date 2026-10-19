package repo

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Core struct {
		ChunkMinSize   int `json:"chunk_min_size"`
		ChunkThreshold int `json:"chunk_threshold"`
		ChunkMaxSize   int `json:"chunk_max_size"`
		// Fsmonitor is "false" (default, off) or "true" (use the built-in
		// change monitor). Empty is treated as off.
		Fsmonitor string `json:"fsmonitor,omitempty"`
		// Bare marks a repository with no working tree: a push target. It is
		// the one thing that separates "push target" from "checkout", so it
		// defaults to false — a plain Init is a checkout, and a push to the
		// branch it has checked out is refused. omitempty keeps the config of
		// a non-bare repository byte-identical to what it was before the field
		// existed.
		Bare bool `json:"bare,omitempty"`
		// Symlinks controls whether a stored symlink is materialized as a real
		// link. nil means the platform default: git-compatible, false on
		// Windows and true elsewhere. A pointer is required because a plain
		// bool with omitempty could not distinguish "unset" (use the platform
		// default) from an explicit false.
		Symlinks *bool `json:"symlinks,omitempty"`
	} `json:"core"`
	Diff struct {
		MaxSize  int `json:"max_size"`  // skip content diff if file exceeds this (bytes)
		MaxLines int `json:"max_lines"` // skip line diff if file exceeds this (lines)
	} `json:"diff"`
	User struct {
		Name  string `json:"name,omitempty"`
		Email string `json:"email,omitempty"`
	} `json:"user"`
}

func DefaultConfig() *Config {
	c := &Config{}
	c.Core.ChunkMinSize = 1024 * 1024       // 1MB
	c.Core.ChunkThreshold = 4 * 1024 * 1024 // 4MB
	c.Core.ChunkMaxSize = 8 * 1024 * 1024   // 8MB
	c.Core.Fsmonitor = FsmonitorOff
	c.Diff.MaxSize = 512 * 1024 // 512KB
	c.Diff.MaxLines = 2000      // lines
	return c
}

// symlinksDefault is the platform default for core.symlinks: git treats
// Windows as unable to create symlinks for an ordinary user, so a stored link
// is checked out there as a regular file holding the target text. hostGOOS is
// the testable platform hook; a real Windows host reports "windows".
func symlinksDefault() bool {
	return hostGOOS() != "windows"
}

// symlinksEnabled reports whether this repository materializes stored symlinks
// as real links. An unset key follows the platform default.
func (r *Repository) symlinksEnabled() bool {
	if r == nil || r.Config == nil || r.Config.Core.Symlinks == nil {
		return symlinksDefault()
	}
	return *r.Config.Core.Symlinks
}

func LoadConfig(repoPath string) (*Config, error) {
	cfgPath := filepath.Join(repoPath, QinDir, "config")
	data, err := ioutil.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func SaveConfig(repoPath string, cfg *Config) error {
	cfgPath := filepath.Join(repoPath, QinDir, "config")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := ioutil.WriteFile(cfgPath, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// configKeys maps dotted key names to their descriptions.
var configKeys = map[string]string{
	"core.chunk_min_size":  "Minimum chunk size in bytes (default 1048576)",
	"core.chunk_threshold": "Average chunk size in bytes (default 4194304)",
	"core.chunk_max_size":  "Maximum chunk size in bytes (default 8388608)",
	"core.fsmonitor":       "Skip unchanged files in status using the built-in change monitor: false | true",
	"core.bare":            "No working tree: a push target, which protects no branch from being pushed to: false | true",
	"core.symlinks":        "Check out symlinks as real links; false writes the target as a regular file (default: true except on windows): false | true",
	"diff.max_size":        "Skip content diff if file exceeds this in bytes (default 524288)",
	"diff.max_lines":       "Skip line diff if file exceeds this many lines (default 2000)",
	"user.name":            "User name for commit author",
	"user.email":           "User email for commit author",
}

// ConfigGet returns the value of a config key as a string.
func ConfigGet(cfg *Config, key string) (string, error) {
	switch key {
	case "core.chunk_min_size":
		return strconv.Itoa(cfg.Core.ChunkMinSize), nil
	case "core.chunk_threshold":
		return strconv.Itoa(cfg.Core.ChunkThreshold), nil
	case "core.chunk_max_size":
		return strconv.Itoa(cfg.Core.ChunkMaxSize), nil
	case "core.fsmonitor":
		if cfg.Core.Fsmonitor == "" {
			return FsmonitorOff, nil
		}
		return cfg.Core.Fsmonitor, nil
	case "core.bare":
		return strconv.FormatBool(cfg.Core.Bare), nil
	case "core.symlinks":
		if cfg.Core.Symlinks == nil {
			return strconv.FormatBool(symlinksDefault()), nil
		}
		return strconv.FormatBool(*cfg.Core.Symlinks), nil
	case "diff.max_size":
		return strconv.Itoa(cfg.Diff.MaxSize), nil
	case "diff.max_lines":
		return strconv.Itoa(cfg.Diff.MaxLines), nil
	case "user.name":
		return cfg.User.Name, nil
	case "user.email":
		return cfg.User.Email, nil
	default:
		return "", fmt.Errorf("unknown config key: %s", key)
	}
}

// ConfigSet sets a config key to a string value and returns the updated config.
func ConfigSet(cfg *Config, key, value string) error {
	switch key {
	case "core.chunk_min_size":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %s", value)
		}
		cfg.Core.ChunkMinSize = v
	case "core.chunk_threshold":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %s", value)
		}
		cfg.Core.ChunkThreshold = v
	case "core.chunk_max_size":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %s", value)
		}
		cfg.Core.ChunkMaxSize = v
	case "core.fsmonitor":
		if !ValidFsmonitorValue(value) {
			return fmt.Errorf("invalid core.fsmonitor value: %s (use false or true)", value)
		}
		cfg.Core.Fsmonitor = strings.ToLower(strings.TrimSpace(value))
		if cfg.Core.Fsmonitor == "" {
			cfg.Core.Fsmonitor = FsmonitorOff
		}
	case "core.bare":
		v, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid core.bare value: %s (use false or true)", value)
		}
		cfg.Core.Bare = v
	case "core.symlinks":
		v, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid core.symlinks value: %s (use false or true)", value)
		}
		cfg.Core.Symlinks = &v
	case "diff.max_size":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %s", value)
		}
		cfg.Diff.MaxSize = v
	case "diff.max_lines":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %s", value)
		}
		cfg.Diff.MaxLines = v
	case "user.name":
		cfg.User.Name = value
	case "user.email":
		cfg.User.Email = value
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}

// ConfigUnset resets a config key to its default/empty value.
func ConfigUnset(cfg *Config, key string) error {
	switch key {
	case "core.chunk_min_size":
		cfg.Core.ChunkMinSize = 1024 * 1024
	case "core.chunk_threshold":
		cfg.Core.ChunkThreshold = 4 * 1024 * 1024
	case "core.chunk_max_size":
		cfg.Core.ChunkMaxSize = 8 * 1024 * 1024
	case "core.fsmonitor":
		cfg.Core.Fsmonitor = FsmonitorOff
	case "core.bare":
		cfg.Core.Bare = false
	case "core.symlinks":
		cfg.Core.Symlinks = nil
	case "diff.max_size":
		cfg.Diff.MaxSize = 512 * 1024
	case "diff.max_lines":
		cfg.Diff.MaxLines = 2000
	case "user.name":
		cfg.User.Name = ""
	case "user.email":
		cfg.User.Email = ""
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}

// ConfigKeys returns a copy of known config keys and their descriptions.
func ConfigKeys() map[string]string {
	cp := make(map[string]string, len(configKeys))
	for k, v := range configKeys {
		cp[k] = v
	}
	return cp
}
