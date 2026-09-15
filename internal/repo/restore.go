package repo

import (
	"fmt"
	"path/filepath"

	"github.com/zhsoft88/qin/internal/core"
)

// RestoreFile restores a file from the index to the working tree,
// discarding any unstaged changes.
func (r *Repository) RestoreFile(filePath string) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	relPath, err := filepath.Rel(r.Path, absPath)
	if err != nil {
		return fmt.Errorf("path outside repository: %w", err)
	}
	if isOutsideRepo(relPath) {
		return fmt.Errorf("path outside repository: %s", filePath)
	}

	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	relFormatted := filepath.ToSlash(relPath)

	cOS := currentOS()
	visible := visibleEntries(idx.Entries, cOS)
	entry, ok := visible[relFormatted]
	if !ok {
		return fmt.Errorf("file not tracked: %s", filePath)
	}

	objType, _, err := r.LoadObject(entry.Hash)
	if err != nil {
		return fmt.Errorf("load object: %w", err)
	}

	var fileData []byte
	if objType == core.ObjectChunkManifest {
		fileData, err = r.LoadChunkedFile(entry.Hash)
		if err != nil {
			return fmt.Errorf("load chunked file: %w", err)
		}
	} else {
		_, blobData, err := r.LoadObject(entry.Hash)
		if err != nil {
			return fmt.Errorf("load blob: %w", err)
		}
		fileData = blobData
	}

	_, err = r.writeWorkTreeFile(relFormatted, fileData, entry.Mode)
	return err
}

// RestoreStaged restores the index entry for a file to match HEAD (unstages).
func (r *Repository) RestoreStaged(filePath string) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	relPath, err := filepath.Rel(r.Path, absPath)
	if err != nil {
		return fmt.Errorf("path outside repository: %w", err)
	}
	if isOutsideRepo(relPath) {
		return fmt.Errorf("path outside repository: %s", filePath)
	}

	relFormatted := filepath.ToSlash(relPath)

	// Resolve HEAD
	headHashStr, err := r.ResolveHEAD()
	if err != nil || headHashStr == "" {
		return fmt.Errorf("no commits")
	}
	headHash, err := core.HashFromHex(headHashStr)
	if err != nil {
		return err
	}

	// Load HEAD commit tree
	commit, err := r.LoadCommit(headHash)
	if err != nil {
		return err
	}
	tree, err := r.LoadTree(commit.Tree)
	if err != nil {
		return err
	}

	// Find the file entry in HEAD tree (visible for current OS)
	cOS := currentOS()
	var headEntry *TreeEntry
	for _, e := range tree.Entries {
		if e.Name == relFormatted && osMatch(e.OSS, cOS) {
			headEntry = &e
			if osExactMatch(e.OSS, cOS) {
				break
			}
		}
	}
	if headEntry == nil {
		return fmt.Errorf("file not found in HEAD: %s", filePath)
	}

	// Update index
	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	var contentHash core.Hash
	_, bd, _ := r.LoadObject(headEntry.Hash)
	if bd != nil {
		contentHash = core.HashFromBytes(bd)
	}

	key := entryKey(relFormatted, headEntry.OSS)
	idx.Entries[key] = IndexEntry{
		Hash:        headEntry.Hash,
		ContentHash: contentHash,
		Size:        headEntry.Size,
		Mode:        headEntry.Mode,
		OSS:         headEntry.OSS,
	}

	return r.SaveIndex(idx)
}
