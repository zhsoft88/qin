package repo

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/zhsoft88/lo/internal/core"
)

// ResetCommit moves HEAD to the given commit and resets index/working tree
// according to the mode:
//   "soft"  — only move HEAD
//   "mixed" — move HEAD + reset index (default)
//   "hard"  — move HEAD + reset index + reset working tree
func (r *Repository) ResetCommit(hash core.Hash, mode string) error {
	// Resolve HEAD to get current branch
	head, err := r.ReadHEAD()
	if err != nil {
		return fmt.Errorf("read HEAD: %w", err)
	}
	if len(head) < 5 || head[:4] != "ref:" {
		return fmt.Errorf("reset requires a branch (detached HEAD not supported)")
	}
	ref := head[5:]

	commit, err := r.LoadCommit(hash)
	if err != nil {
		return fmt.Errorf("load commit: %w", err)
	}

	tree, err := r.LoadTree(commit.Tree)
	if err != nil {
		return fmt.Errorf("load tree: %w", err)
	}

	// Collect old index paths BEFORE any index writes (for hard mode)
	var oldPaths []string
	if mode == "hard" {
		oldIdx, err := r.LoadIndex()
		if err != nil {
			return err
		}
		oldPaths = collectPaths(oldIdx.Entries)
	}

	// Build new entries from tree
	newEntries := make(map[string]TreeEntry)
	for _, e := range tree.Entries {
		key := entryKey(e.Name, osIDForKey(e.OSS))
		newEntries[key] = e
	}

	// For mixed and hard: replace index
	if mode == "mixed" || mode == "hard" {
		if err := r.buildIndexFromTreeEntries(newEntries); err != nil {
			return err
		}
	}

	// For hard: rewrite working tree
	if mode == "hard" {
		// Remove all files currently on disk
		for _, path := range oldPaths {
			os.Remove(filepath.Join(r.Path, path))
		}

		// Write new files with OS filtering (like restoreCommit)
		cOS := currentOS()
		groups := make(map[string][]TreeEntry)
		for _, e := range tree.Entries {
			groups[e.Name] = append(groups[e.Name], e)
		}
		for name, group := range groups {
			var winner *TreeEntry
			for _, e := range group {
				if osMatch(e.OSS, cOS) {
					winner = &e
					if len(e.OSS) == 1 && e.OSS[0] == cOS {
						break
					}
				}
			}
			if winner != nil {
				if err := r.writeTreeEntryToDisk(name, *winner); err != nil {
					return err
				}
			}
		}
	}

	// Update branch ref
	if err := r.WriteRef(ref, hash.String()); err != nil {
		return fmt.Errorf("update ref: %w", err)
	}

	return nil
}

// buildIndexFromTreeEntries creates an index from tree entries without writing files.
func (r *Repository) buildIndexFromTreeEntries(entries map[string]TreeEntry) error {
	newIndex := &Index{Entries: make(map[string]IndexEntry, len(entries))}
	for key, e := range entries {
		var contentHash core.Hash
		_, bd, _ := r.LoadObject(e.Hash)
		if bd != nil {
			contentHash = core.HashFromBytes(bd)
		}
		newIndex.Entries[key] = IndexEntry{
			Hash:        e.Hash,
			ContentHash: contentHash,
			Size:        e.Size,
			Mode:        e.Mode,
			OSS:              e.OSS,
		}
	}
	return r.SaveIndex(newIndex)
}

// writeTreeEntryToDisk writes a single tree entry to the working tree.
func (r *Repository) writeTreeEntryToDisk(name string, entry TreeEntry) error {
	fullPath := filepath.Join(r.Path, name)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return fmt.Errorf("create directory for %s: %w", name, err)
	}

	objType, _, err := r.LoadObject(entry.Hash)
	if err != nil {
		return fmt.Errorf("load object %s: %w", entry.Hash.Short(), err)
	}

	var fileData []byte
	if objType == core.ObjectChunkManifest {
		if r.hasAllChunks(entry.Hash) {
			fileData, err = r.LoadChunkedFile(entry.Hash)
			if err != nil {
				return fmt.Errorf("load chunked file %s: %w", name, err)
			}
		} else {
			fileData = []byte("lo-lfs")
		}
	} else {
		_, blobData, err := r.LoadObject(entry.Hash)
		if err != nil {
			return fmt.Errorf("load blob %s: %w", name, err)
		}
		fileData = blobData
	}

	return writeFileFromEntry(fullPath, fileData, entry.Mode)
}

// collectPathsFromTree deduplicates clean paths from tree entries (composite keys).
func collectPathsFromTree(entries map[string]TreeEntry) []string {
	seen := make(map[string]bool)
	var paths []string
	for key := range entries {
		path, _ := parseKey(key)
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}
