package repo

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/zhsoft88/lo/internal/core"
)

// SwitchBranch switches to an existing branch: updates HEAD, restores files.
func (r *Repository) SwitchBranch(name string) error {
	hashStr, err := r.ReadRef("refs/heads/" + name)
	if err != nil {
		return fmt.Errorf("branch not found: %s", name)
	}

	hash, err := core.HashFromHex(hashStr)
	if err != nil {
		return fmt.Errorf("parse commit hash: %w", err)
	}

	if err := r.SetHEAD("ref: refs/heads/" + name); err != nil {
		return fmt.Errorf("update HEAD: %w", err)
	}

	return r.restoreCommit(hash)
}

// restoreCommit restores files and updates index but does NOT touch HEAD.
func (r *Repository) restoreCommit(hash core.Hash) error {
	oldFiles, err := r.ListFiles()
	if err != nil {
		return fmt.Errorf("list current files: %w", err)
	}
	// Remove old files using deduplicated paths (handles OS-tagged composite keys)
	for _, path := range collectPaths(oldFiles) {
		os.Remove(filepath.Join(r.Path, path))
	}

	commit, err := r.LoadCommit(hash)
	if err != nil {
		return fmt.Errorf("load commit: %w", err)
	}

	tree, err := r.LoadTree(commit.Tree)
	if err != nil {
		return fmt.Errorf("load tree: %w", err)
	}

	newIndex := &Index{Entries: make(map[string]IndexEntry, len(tree.Entries))}
	cOS := currentOS()

	// Group tree entries by name for OS-aware filtering
	type namedEntry struct {
		entries []TreeEntry
	}
	groups := make(map[string]*namedEntry)
	for i := range tree.Entries {
		e := &tree.Entries[i]
		if groups[e.Name] == nil {
			groups[e.Name] = &namedEntry{}
		}
		groups[e.Name].entries = append(groups[e.Name].entries, *e)
	}

	for name, group := range groups {
		// Determine the winning entry for the current OS
		var winner *TreeEntry
		for _, e := range group.entries {
			if osMatch(e.OSS, cOS) {
				winner = &e
				if len(e.OSS) == 1 && e.OSS[0] == cOS {
					break // exact OS match is the best possible
				}
			}
		}
		if winner == nil {
			// No matching OS variant — still add to index, skip working tree
			for _, e := range group.entries {
				key := entryKey(name, osIDForKey(e.OSS))
				newIndex.Entries[key] = IndexEntry{
					Hash: e.Hash,
					Size: e.Size,
					Mode: e.Mode,
					OSS:    e.OSS,
				}
			}
			continue
		}

		// Write winning entry to disk
		fullPath := filepath.Join(r.Path, name)

		// Submodule entries: create directory, store in index, skip file content
		if IsSubmoduleMode(winner.Mode) {
			if err := os.MkdirAll(fullPath, 0755); err != nil {
				return fmt.Errorf("create submodule dir %s: %w", name, err)
			}
			for _, e := range group.entries {
				key := entryKey(name, osIDForKey(e.OSS))
				newIndex.Entries[key] = IndexEntry{
					Hash: e.Hash,
					Size: e.Size,
					Mode: e.Mode,
					OSS:    e.OSS,
				}
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return fmt.Errorf("create directory for %s: %w", name, err)
		}

		objType, _, err := r.LoadObject(winner.Hash)
		if err != nil {
			return fmt.Errorf("load object %s: %w", winner.Hash.Short(), err)
		}

		var fileData []byte
		var isLazy bool
		if objType == core.ObjectChunkManifest {
			if r.hasAllChunks(winner.Hash) {
				fileData, err = r.LoadChunkedFile(winner.Hash)
				if err != nil {
					return fmt.Errorf("load chunked file %s: %w", name, err)
				}
			} else {
				fileData = []byte("lo-lfs")
				isLazy = true
			}
		} else {
			_, blobData, err := r.LoadObject(winner.Hash)
			if err != nil {
				return fmt.Errorf("load blob %s: %w", name, err)
			}
			fileData = blobData
		}

		if err := writeFileFromEntry(fullPath, fileData, winner.Mode); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}

		// Add all OS variants to index (default + OS-specific)
		for _, e := range group.entries {
			key := entryKey(name, osIDForKey(e.OSS))
			var contentHash core.Hash
			if e.Hash == winner.Hash {
				contentHash = core.HashFromBytes(fileData)
			} else {
				_, bd, _ := r.LoadObject(e.Hash)
				if bd != nil {
					contentHash = core.HashFromBytes(bd)
				}
			}
			newIndex.Entries[key] = IndexEntry{
				Hash:        e.Hash,
				ContentHash: contentHash,
				Size:        e.Size,
				Mode:        e.Mode,
				Lazy:        e.Hash == winner.Hash && isLazy,
				OSS:         e.OSS,
			}
		}
	}

	if err := r.SaveIndex(newIndex); err != nil {
		return fmt.Errorf("save index: %w", err)
	}

	return nil
}

// ListBranches returns all branch names and marks the current one.
func (r *Repository) ListBranches() ([]string, string, error) {
	headsDir := filepath.Join(r.RefsDir(), "heads")
	entries, err := ioutil.ReadDir(headsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}

	current := r.CurrentBranch()
	currentValid := false
	var branches []string
	for _, entry := range entries {
		if !entry.IsDir() {
			branches = append(branches, entry.Name())
			if entry.Name() == current {
				currentValid = true
			}
		}
	}
	if !currentValid {
		current = ""
	}
	return branches, current, nil
}

// CreateBranch creates a new branch pointing at the current HEAD.
func (r *Repository) CreateBranch(name string) error {
	headHash, err := r.ResolveHEAD()
	if err != nil {
		return fmt.Errorf("resolve HEAD: %w", err)
	}
	if headHash == "" {
		return fmt.Errorf("no commits to branch from")
	}
	return r.WriteRef("refs/heads/"+name, headHash)
}

// DeleteBranch removes a branch ref. Returns an error if the branch
// is the currently checked-out branch.
func (r *Repository) DeleteBranch(name string) error {
	if name == r.CurrentBranch() {
		return fmt.Errorf("cannot delete branch '%s' (currently on it)", name)
	}
	ref := "refs/heads/" + name
	_, err := r.ReadRef(ref)
	if err != nil {
		return fmt.Errorf("branch not found: %s", name)
	}
	return os.Remove(filepath.Join(r.RefsDir(), "heads", name))
}
