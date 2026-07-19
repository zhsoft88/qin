package repo

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhsoft88/lo/internal/core"
)

// SymlinkMode is the file mode for symbolic links.
const SymlinkMode = uint32(os.ModeSymlink | 0777)

// IsSymlinkMode returns true if the given mode represents a symbolic link.
func IsSymlinkMode(mode uint32) bool {
	return mode&uint32(os.ModeSymlink) != 0
}

// writeFileFromEntry writes file data to disk, creating a symlink or regular
// file depending on the entry's mode.
func writeFileFromEntry(path string, data []byte, mode uint32) error {
	if IsSymlinkMode(mode) {
		return os.Symlink(string(data), path)
	}
	return ioutil.WriteFile(path, data, os.FileMode(mode))
}

// IndexEntry represents a staged file.
type IndexEntry struct {
	Hash        core.Hash `json:"hash"`          // object hash (for loading from store)
	ContentHash core.Hash `json:"content_hash"`  // raw file content hash (for change detection)
	Size        int64     `json:"size"`
	Mode        uint32    `json:"mode"`
	Lazy        bool      `json:"lazy,omitempty"` // true if chunks not yet fetched (lfs placeholder)
	OSS []uint8 `json:"oss,omitempty"`     // list of OS IDs the entry applies to; empty = all OSes
}

// Index is the staging area, mapping repo-relative paths to entries.
type Index struct {
	Entries map[string]IndexEntry `json:"entries"`
}

const indexFileName = "index"

func (r *Repository) indexPath() string {
	return filepath.Join(r.LoDir(), indexFileName)
}

// LoadIndex reads the index from disk, returning an empty index if none exists.
func (r *Repository) LoadIndex() (*Index, error) {
	data, err := ioutil.ReadFile(r.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Index{Entries: make(map[string]IndexEntry)}, nil
		}
		return nil, fmt.Errorf("read index: %w", err)
	}

	var idx Index
	if err := core.DeserializeJSON(data, &idx); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}
	if idx.Entries == nil {
		idx.Entries = make(map[string]IndexEntry)
	}
	return &idx, nil
}

// SaveIndex writes the index to disk.
func (r *Repository) SaveIndex(idx *Index) error {
	data, err := core.SerializeJSON(idx)
	if err != nil {
		return fmt.Errorf("serialize index: %w", err)
	}
	if err := ioutil.WriteFile(r.indexPath(), data, 0644); err != nil {
		return fmt.Errorf("write index: %w", err)
	}
	return nil
}

// AddFile reads a file from disk, stores it as an object, and adds it to the index
// as a default (all-OS) entry.
func (r *Repository) AddFile(filePath string) error {
	return r.addFileInternal(filePath, 0, nil)
}

// AddFileOS reads a file from disk, stores it as an object, and adds it to the
// index as an OS-specific entry. The os parameter must be a known OS identifier.
func (r *Repository) AddFileOS(filePath, osTag string) error {
	id := OSID(osTag)
	if osTag != "" && id == 0 {
		return fmt.Errorf("unknown OS: %s", osTag)
	}
	return r.addFileInternal(filePath, id, []uint8{id})
}

// AddFileOSMatch adds a file with an OS expression. The expression is resolved
// to a list of OS IDs stored in the entry's OSS field. Single-OS expressions use
// that OS ID as the key discriminator; complex expressions use key 0.
func (r *Repository) AddFileOSMatch(filePath, expr string) error {
	if expr == "" || expr == "*" {
		return r.addFileInternal(filePath, 0, nil)
	}
	include, exclude, err := ParseOSExpr(expr)
	if err != nil {
		return fmt.Errorf("invalid OS expression: %w", err)
	}
	var oss []uint8
	var osID uint8
	if len(include) > 0 && len(exclude) == 0 {
		// Simple include list
		for id := range include {
			oss = append(oss, id)
		}
		if len(oss) == 1 {
			osID = oss[0]
		}
	} else if len(exclude) > 0 {
		// Exclude list — oss = all known OSes except excluded
		for _, name := range KnownOSes {
			id := OSID(name)
			if !exclude[id] {
				oss = append(oss, id)
			}
		}
	}
	return r.addFileInternal(filePath, osID, oss)
}

// addFileInternal is the shared implementation for AddFile, AddFileOS, and AddFileOSMatch.
// It loads the index, processes one file, and saves — use AddFiles/AddFilesOSMatch
// for batch operations that avoid per-file index save.
func (r *Repository) addFileInternal(filePath string, osID uint8, oss []uint8) error {
	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}
	if err := r.AddFileToIndex(filePath, osID, oss, idx); err != nil {
		return err
	}
	return r.SaveIndex(idx)
}

// addFileToIndex processes a single file and adds it to a pre-loaded index.
// Does NOT save the index — caller must call SaveIndex.
func (r *Repository) AddFileToIndex(filePath string, osID uint8, oss []uint8, idx *Index) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	relPath, err := filepath.Rel(r.Path, absPath)
	if err != nil {
		return fmt.Errorf("path outside repository: %w", err)
	}
	if relPath == "" || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) || relPath == ".." {
		return fmt.Errorf("path outside repository")
	}

	fi, err := os.Lstat(absPath)
	if err != nil {
		return fmt.Errorf("lstat file: %w", err)
	}

	isSymlink := fi.Mode()&os.ModeSymlink != 0

	// Quick skip: file already in index with same size
	skipKey := entryKey(filepath.ToSlash(relPath), osID)
	if !isSymlink && !fi.IsDir() {
		if existing, ok := idx.Entries[skipKey]; ok && existing.Size == fi.Size() && existing.Mode != DirMode {
			return nil
		}
	}

	if !isSymlink && fi.IsDir() {
		// Allow empty directories
		empty, _ := isDirEmpty(absPath)
		if !empty {
			return fmt.Errorf("cannot add non-empty directory: %s", filePath)
		}
		key := entryKey(filepath.ToSlash(relPath), osID)
		idx.Entries[key] = IndexEntry{
			Mode: DirMode,
			OSS:  oss,
		}
		return nil
	}

	ignorer, err := r.LoadIgnoreMatcher()
	if err != nil {
		return err
	}
	relFormatted := filepath.ToSlash(relPath)
	if ignorer.Match(relFormatted, false) {
		return fmt.Errorf("matches .loignore")
	}

	var data []byte
	if isSymlink {
		target, err := os.Readlink(absPath)
		if err != nil {
			return fmt.Errorf("read symlink: %w", err)
		}
		data = []byte(target)
	} else {
		data, err = ioutil.ReadFile(absPath)
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}

		// Reject LFS placeholder files — user must lfs-pull first
		if string(data) == "lo-lfs" && r.hasAnyLazyEntry(filepath.ToSlash(relPath)) {
			return fmt.Errorf("cannot add placeholder file '%s': use 'lfs-pull' to fetch real content first", filePath)
		}
	}

	contentHash := core.HashFromBytes(data)

	var h core.Hash
	if isSymlink {
		h, err = r.StoreObject(core.ObjectBlob, data)
	} else {
		h, err = r.StoreChunkedFile(data)
	}
	if err != nil {
		return fmt.Errorf("store file: %w", err)
	}

	mode := uint32(fi.Mode())

	key := entryKey(filepath.ToSlash(relPath), osID)
	idx.Entries[key] = IndexEntry{
		Hash:        h,
		ContentHash: contentHash,
		Size:        fi.Size(),
		Mode:        mode,
		OSS:         oss,
	}
	return nil
}

// isDirEmpty checks whether a directory has no entries.
func isDirEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = f.Readdir(1)
	if err != nil {
		return true, nil // empty
	}
	return false, nil // has entries
}

// AddFiles adds multiple files in a batch — loads index once, saves once.
func (r *Repository) AddFiles(files []string) error {
	return r.AddFilesOSMatch(files, "")
}

// AddFilesOSMatch adds multiple files with an OS expression in a batch.
func (r *Repository) AddFilesOSMatch(files []string, expr string) error {
	include, exclude, err := ParseOSExpr(expr)
	if err != nil {
		return fmt.Errorf("invalid OS expression: %w", err)
	}

	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	for _, f := range files {
		var oss []uint8
		var osID uint8
		if expr != "" && expr != "*" {
			include, exclude, _ = ParseOSExpr(expr)
			if len(include) > 0 && len(exclude) == 0 {
				for id := range include {
					oss = append(oss, id)
				}
				if len(oss) == 1 {
					osID = oss[0]
				}
			} else if len(exclude) > 0 {
				for _, name := range KnownOSes {
					id := OSID(name)
					if !exclude[id] {
						oss = append(oss, id)
					}
				}
			}
		}
		if err := r.AddFileToIndex(f, osID, oss, idx); err != nil {
			return fmt.Errorf("add %s: %w", f, err)
		}
	}

	return r.SaveIndex(idx)
}

// hasAnyLazyEntry checks whether any OS variant of the given path has a lazy
// (LFS placeholder) entry in the index.
func (r *Repository) hasAnyLazyEntry(relPath string) bool {
	idx, err := r.LoadIndex()
	if err != nil {
		return false
	}
	for key, entry := range idx.Entries {
		if path, _ := parseKey(key); path == relPath && entry.Lazy {
			return true
		}
	}
	return false
}

// RemoveFile removes the visible variant of a file from the index for the
// current OS. This is the user-facing remove: it only removes what's visible
// in the working tree.
func (r *Repository) RemoveFile(filePath string) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	relPath, err := filepath.Rel(r.Path, absPath)
	if err != nil {
		return fmt.Errorf("path outside repository: %w", err)
	}

	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	relFormatted := filepath.ToSlash(relPath)

	// Find which variant is visible on the current OS and remove only that one
	cOS := currentOS()
	var keyToDelete string
	for key, entry := range idx.Entries {
		if path, os := parseKey(key); path == relFormatted && osMatch(entry.OSS, cOS) {
			if keyToDelete == "" || os != 0 {
				// Prefer OS-specific match over default (same logic as visibleEntries)
				keyToDelete = key
				if os != 0 {
					break // OS-specific match is the definitive visible entry
				}
			}
		}
	}
	if keyToDelete == "" {
		return fmt.Errorf("file not tracked: %s", filePath)
	}

	delete(idx.Entries, keyToDelete)
	return r.SaveIndex(idx)
}

// RemoveFileOS removes a specific OS-tagged variant of a file from the index.
// When os is empty, all variants (default + all OS-specific) are removed.
func (r *Repository) RemoveFileOS(filePath, osTag string) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	relPath, err := filepath.Rel(r.Path, absPath)
	if err != nil {
		return fmt.Errorf("path outside repository: %w", err)
	}

	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	relFormatted := filepath.ToSlash(relPath)
	id := OSID(osTag)
	if id == 0 && osTag != "" {
		return fmt.Errorf("unknown OS: %s", osTag)
	}
	if osTag == "" {
		// Remove all variants
		for key := range idx.Entries {
			if path, _ := parseKey(key); path == relFormatted {
				delete(idx.Entries, key)
			}
		}
	} else {
		delete(idx.Entries, entryKey(relFormatted, id))
	}

	return r.SaveIndex(idx)
}

// AddSubmodule records a submodule entry in the index with SubmoduleMode.
// The hashHex is the pinned commit hash of the submodule repository.
func (r *Repository) AddSubmodule(path, hashHex string) error {
	h, err := core.HashFromHex(hashHex)
	if err != nil {
		return fmt.Errorf("invalid hash: %w", err)
	}

	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	key := entryKey(path, 0)
	idx.Entries[key] = IndexEntry{
		Hash: h,
		Mode: SubmoduleMode,
	}

	return r.SaveIndex(idx)
}

// ListFiles returns the staged file entries keyed by repo-relative path.
func (r *Repository) ListFiles() (map[string]IndexEntry, error) {
	idx, err := r.LoadIndex()
	if err != nil {
		return nil, err
	}
	return idx.Entries, nil
}
