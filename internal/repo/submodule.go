package repo

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
)

const qinModulesFile = ".qinmodules"

// SubmoduleDef describes a single submodule: the remote URL it points to.
type SubmoduleDef struct {
	URL string `json:"url"`
}

// QinModules is the parsed content of .qinmodules.
type QinModules struct {
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

// LoadQinModules reads .qinmodules from disk. Returns an empty mapping
// if the file doesn't exist.
func LoadQinModules(r *Repository) (*QinModules, error) {
	path := filepath.Join(r.Path, qinModulesFile)
	data, err := ioutil.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &QinModules{Submodules: make(map[string]SubmoduleDef)}, nil
		}
		return nil, fmt.Errorf("read %s: %w", qinModulesFile, err)
	}

	var mods QinModules
	if err := json.Unmarshal(data, &mods); err != nil {
		return nil, fmt.Errorf("parse %s: %w", qinModulesFile, err)
	}
	if mods.Submodules == nil {
		mods.Submodules = make(map[string]SubmoduleDef)
	}
	return &mods, nil
}

// SaveQinModules writes .qinmodules to disk.
func SaveQinModules(r *Repository, mods *QinModules) error {
	data, err := json.MarshalIndent(mods, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", qinModulesFile, err)
	}
	if err := ioutil.WriteFile(filepath.Join(r.Path, qinModulesFile), data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", qinModulesFile, err)
	}
	return nil
}

// AddSubmodule clones a submodule repo at the given path, records its URL
// in .qinmodules, and stages both the .qinmodules file and the
// submodule entry in the index.
func AddSubmodule(r *Repository, url, path string) error {
	// The path is untrusted on the clone --recursive and `submodule update
	// --init` routes, where it is a key read from .qinmodules — a tracked file
	// delivered by the clone. Joined unchecked, "../.." would clone
	// attacker-chosen content to an arbitrary location outside the working
	// tree, with no prompt. git likewise refuses submodule paths outside it.
	//
	// The slash form is both what gets validated and what is recorded, so the
	// entry means the same thing on every OS.
	rel := filepath.ToSlash(path)
	if !safeRepoPath(rel) {
		return fmt.Errorf("submodule path outside repository: %s", path)
	}

	// Clone the submodule repo
	subPath := filepath.Join(r.Path, rel)
	sub, err := Clone(url, subPath, false)
	if err != nil {
		return fmt.Errorf("clone submodule: %w", err)
	}

	// Get HEAD commit hash
	headStr, err := sub.ResolveHEAD()
	if err != nil || headStr == "" {
		return fmt.Errorf("submodule has no commits")
	}

	// Update .qinmodules
	mods, err := LoadQinModules(r)
	if err != nil {
		return err
	}
	mods.Submodules[rel] = SubmoduleDef{URL: url}
	if err := SaveQinModules(r, mods); err != nil {
		return err
	}

	// Stage .qinmodules
	modulesPath := filepath.Join(r.Path, qinModulesFile)
	if err := r.AddFile(modulesPath); err != nil {
		return err
	}

	// Stage submodule entry
	if err := r.AddSubmodule(rel, headStr); err != nil {
		return err
	}

	return nil
}
