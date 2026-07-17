package repo

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
)

const loModulesFile = ".lomodules"

// SubmoduleDef describes a single submodule: the remote URL it points to.
type SubmoduleDef struct {
	URL string `json:"url"`
}

// LoModules is the parsed content of .lomodules.
type LoModules struct {
	Submodules map[string]SubmoduleDef `json:"submodules"`
}

// SubmoduleMode is the mode constant for submodule tree/index entries.
// 0160000 follows git's convention for submodules.
const SubmoduleMode = 0160000

// IsSubmoduleMode returns true if the mode indicates a submodule entry.
func IsSubmoduleMode(mode uint32) bool {
	return mode == SubmoduleMode
}

// DirMode is the mode constant for empty directory entries.
// 040000 follows git's convention for tree entries.
const DirMode = 040000

// IsDirMode returns true if the mode indicates an empty directory entry.
func IsDirMode(mode uint32) bool {
	return mode == DirMode
}

// LoadLoModules reads .lomodules from disk. Returns an empty mapping
// if the file doesn't exist.
func LoadLoModules(r *Repository) (*LoModules, error) {
	path := filepath.Join(r.Path, loModulesFile)
	data, err := ioutil.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &LoModules{Submodules: make(map[string]SubmoduleDef)}, nil
		}
		return nil, fmt.Errorf("read %s: %w", loModulesFile, err)
	}

	var mods LoModules
	if err := json.Unmarshal(data, &mods); err != nil {
		return nil, fmt.Errorf("parse %s: %w", loModulesFile, err)
	}
	if mods.Submodules == nil {
		mods.Submodules = make(map[string]SubmoduleDef)
	}
	return &mods, nil
}

// SaveLoModules writes .lomodules to disk.
func SaveLoModules(r *Repository, mods *LoModules) error {
	data, err := json.MarshalIndent(mods, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", loModulesFile, err)
	}
	if err := ioutil.WriteFile(filepath.Join(r.Path, loModulesFile), data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", loModulesFile, err)
	}
	return nil
}

// AddSubmodule clones a submodule repo at the given path, records its URL
// in .lomodules, and stages both the .lomodules file and the
// submodule entry in the index.
func AddSubmodule(r *Repository, url, path string) error {
	// Clone the submodule repo
	subPath := filepath.Join(r.Path, path)
	sub, err := Clone(url, subPath, false)
	if err != nil {
		return fmt.Errorf("clone submodule: %w", err)
	}

	// Get HEAD commit hash
	headStr, err := sub.ResolveHEAD()
	if err != nil || headStr == "" {
		return fmt.Errorf("submodule has no commits")
	}

	// Update .lomodules
	mods, err := LoadLoModules(r)
	if err != nil {
		return err
	}
	mods.Submodules[path] = SubmoduleDef{URL: url}
	if err := SaveLoModules(r, mods); err != nil {
		return err
	}

	// Stage .lomodules
	modulesPath := filepath.Join(r.Path, loModulesFile)
	if err := r.AddFile(modulesPath); err != nil {
		return err
	}

	// Stage submodule entry
	if err := r.AddSubmodule(path, headStr); err != nil {
		return err
	}

	return nil
}
