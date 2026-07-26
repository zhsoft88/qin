package repo

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	Core struct {
		ChunkMinSize    int `json:"chunk_min_size"`
		ChunkThreshold int `json:"chunk_threshold"`
		ChunkMaxSize    int `json:"chunk_max_size"`
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
	c.Core.ChunkMinSize = 1024 * 1024      // 1MB
	c.Core.ChunkThreshold = 4 * 1024 * 1024  // 4MB
	c.Core.ChunkMaxSize = 8 * 1024 * 1024  // 8MB
	c.Diff.MaxSize = 512 * 1024            // 512KB
	c.Diff.MaxLines = 2000                 // lines
	return c
}

func LoadConfig(repoPath string) (*Config, error) {
	cfgPath := filepath.Join(repoPath, LoDir, "config")
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
	cfgPath := filepath.Join(repoPath, LoDir, "config")
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
	"core.chunk_min_size":    "Minimum chunk size in bytes (default 1048576)",
	"core.chunk_threshold":    "Average chunk size in bytes (default 4194304)",
	"core.chunk_max_size":    "Maximum chunk size in bytes (default 8388608)",
		"diff.max_size":         "Skip content diff if file exceeds this in bytes (default 524288)",
	"diff.max_lines":        "Skip line diff if file exceeds this many lines (default 2000)",
	"user.name":             "User name for commit author",
	"user.email":            "User email for commit author",
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
