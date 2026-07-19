package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

// Stash saves the current index state to a stash and resets to HEAD.
func (r *Repository) Stash() error {
	idx, err := r.LoadIndex()
	if err != nil {
		return fmt.Errorf("load index: %w", err)
	}
	if len(idx.Entries) == 0 {
		return fmt.Errorf("no changes to stash")
	}

	headStr, err := r.ResolveHEAD()
	if err != nil || headStr == "" {
		return fmt.Errorf("no commits to stash against")
	}
	head, _ := core.HashFromHex(headStr)

	// Build tree from current index
	treeEntries := make([]TreeEntry, 0, len(idx.Entries))
	for path, entry := range idx.Entries {
		cleanPath, _ := parseKey(path)
		treeEntries = append(treeEntries, TreeEntry{
			Name:   cleanPath,
			Hash:   entry.Hash,
			Size:   entry.Size,
			Mode:   entry.Mode,
			OSS:    entry.OSS,
		})
	}

	tree := &Tree{Entries: treeEntries}
	treeContent, err := core.SerializeJSON(tree)
	if err != nil {
		return fmt.Errorf("serialize tree: %w", err)
	}
	treeHash, err := r.StoreObject(core.ObjectTree, treeContent)
	if err != nil {
		return fmt.Errorf("store tree: %w", err)
	}

	// Check for existing stash to link as second parent
	var parents []core.Hash
	parents = append(parents, head)
	existingStash, err := r.ReadRef("refs/stash")
	if err == nil && existingStash != "" {
		existingHash, err := core.HashFromHex(existingStash)
		if err == nil {
			parents = append(parents, existingHash)
		}
	}

	stashCommit := Commit{
		Tree:    treeHash,
		Parents: parents,
		Author:  "stash <stash>",
		Message: "stash",
		Time:    time.Now(),
	}
	content, err := core.SerializeJSON(stashCommit)
	if err != nil {
		return fmt.Errorf("serialize stash: %w", err)
	}
	stashHash, err := r.StoreObject(core.ObjectCommit, content)
	if err != nil {
		return fmt.Errorf("store stash: %w", err)
	}

	if err := r.WriteRef("refs/stash", stashHash.String()); err != nil {
		return fmt.Errorf("write stash ref: %w", err)
	}

	// Reset working tree to HEAD
	if err := r.restoreCommit(head); err != nil {
		return fmt.Errorf("reset working tree: %w", err)
	}

	return nil
}

// StashPop restores the latest stash and removes it.
func (r *Repository) StashPop() error {
	stashStr, err := r.ReadRef("refs/stash")
	if err != nil {
		return fmt.Errorf("no stash found")
	}
	stashHash, err := core.HashFromHex(stashStr)
	if err != nil {
		return err
	}

	stash, err := r.LoadCommit(stashHash)
	if err != nil {
		return fmt.Errorf("load stash: %w", err)
	}

	tree, err := r.LoadTree(stash.Tree)
	if err != nil {
		return fmt.Errorf("load stash tree: %w", err)
	}

	// Group tree entries by name for OS-aware filtering
	type stashNamedEntry struct {
		entries []TreeEntry
	}
	groups := make(map[string]*stashNamedEntry)
	for i := range tree.Entries {
		e := &tree.Entries[i]
		if groups[e.Name] == nil {
			groups[e.Name] = &stashNamedEntry{}
		}
		groups[e.Name].entries = append(groups[e.Name].entries, *e)
	}

	// Write stash files to working tree and update index
	cOS := currentOS()
	newIndex := &Index{Entries: make(map[string]IndexEntry, len(tree.Entries))}
	for name, group := range groups {
		// Determine the winning entry for the current OS
		var winner *TreeEntry
		for _, e := range group.entries {
			if osMatch(e.OSS, cOS) {
				winner = &e
				if len(e.OSS) == 1 && e.OSS[0] == cOS {
					break
				}
			}
		}
		if winner == nil {
			// No matching OS variant — add to index, skip working tree
			for _, e := range group.entries {
				key := entryKey(name, osIDForKey(e.OSS))
				newIndex.Entries[key] = IndexEntry{
					Hash: e.Hash,
					Size: e.Size,
					Mode: e.Mode,
					OSS:              e.OSS,
				}
			}
			continue
		}

		// Write winning entry to disk
		fullPath := filepath.Join(r.Path, name)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return fmt.Errorf("create directory for %s: %w", name, err)
		}

		objType, _, err := r.LoadObject(winner.Hash)
		if err != nil {
			return fmt.Errorf("load object %s: %w", winner.Hash.Short(), err)
		}

		var fileData []byte
		if objType == core.ObjectChunkManifest {
			fileData, err = r.LoadChunkedFile(winner.Hash)
			if err != nil {
				return fmt.Errorf("load chunked file %s: %w", name, err)
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
				OSS:              e.OSS,
			}
		}
	}

	if err := r.SaveIndex(newIndex); err != nil {
		return fmt.Errorf("save index: %w", err)
	}

	// Update refs/stash to the stash's first parent (the HEAD it was based on)
	// or the second parent (previous stash) if available
	var newStash core.Hash
	if len(stash.Parents) >= 2 {
		newStash = stash.Parents[1]
	} else if len(stash.Parents) == 1 {
		newStash = stash.Parents[0]
	}

	if newStash.IsZero() {
		os.Remove(filepath.Join(r.RefsDir(), "stash"))
	} else {
		r.WriteRef("refs/stash", newStash.String())
	}

	return nil
}

// StashList returns a list of stash descriptions.
func (r *Repository) StashList() ([]string, error) {
	stashStr, err := r.ReadRef("refs/stash")
	if err != nil {
		return nil, nil
	}

	stashHash, err := core.HashFromHex(stashStr)
	if err != nil {
		return nil, err
	}

	var stashes []string
	for i := 0; !stashHash.IsZero(); i++ {
		stash, err := r.LoadCommit(stashHash)
		if err != nil {
			break
		}
		stashes = append(stashes, fmt.Sprintf("stash@{%d}: %s", i, stash.Message))
		// Follow parent chain to previous stashes
		if len(stash.Parents) >= 2 && !stash.Parents[1].IsZero() {
			stashHash = stash.Parents[1]
		} else if len(stash.Parents) >= 1 && !stash.Parents[0].IsZero() {
			// Check if first parent is another stash (not HEAD)
			p := stash.Parents[0]
			pCommit, err := r.LoadCommit(p)
			if err == nil && pCommit.Message == "stash" {
				stashHash = p
			} else {
				break
			}
		} else {
			break
		}
	}

	return stashes, nil
}
