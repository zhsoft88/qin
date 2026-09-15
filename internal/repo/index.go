package repo

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/zhsoft88/qin/internal/core"
)

// SymlinkMode is the file mode for symbolic links.
const SymlinkMode = uint32(os.ModeSymlink | 0777)

// IsSymlinkMode returns true if the given mode represents a symbolic link.
func IsSymlinkMode(mode uint32) bool {
	return mode&uint32(os.ModeSymlink) != 0
}

// isOutsideRepo reports whether a path produced by filepath.Rel escapes the
// repository root.
//
// filepath.Rel only fails when the two paths cannot be related at all (a
// relative/absolute mix, or different volumes on Windows); for a sibling or
// ancestor it succeeds and returns a "../..." path. So callers must inspect
// the result, not merely the error — a bare `if err != nil` check accepts
// "../elsewhere/file" and lets the path be joined onto the repo root. This
// takes the OS-native form Rel produces, before any ToSlash conversion.
func isOutsideRepo(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// safeRepoPath reports whether a path taken from untrusted input — a patch
// file, a stored tree — may be joined onto the repository root. Such a path
// must stay inside the repository: filepath.Join resolves ".." silently, so
// an unchecked "../../x" writes outside the working tree.
//
// The check is deliberately platform-independent rather than delegating to
// filepath, which interprets only the host OS's separator. Paths are stored
// in canonical slash form, so a backslash is never valid in one, and on
// Windows filepath.Join would treat "..\\..\\x" as an escape that Clean — on
// Linux — sees as a single harmless filename component. Rejecting both forms
// keeps a given tree meaning the same thing on every OS.
func safeRepoPath(p string) bool {
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return false
	}
	if strings.ContainsRune(p, '\\') {
		return false
	}
	if len(p) >= 2 && p[1] == ':' { // Windows drive-relative, e.g. "C:foo"
		return false
	}
	// path.Clean is the slash-aware counterpart to filepath.Clean, so this
	// resolves "./..", "a/../../x" and friends identically everywhere. Only a
	// leading ".." component escapes; a name like "a..b" is untouched.
	clean := path.Clean(p)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

// writeFileFromEntry writes file data to disk, creating a symlink or regular
// file depending on the entry's mode.
func writeFileFromEntry(path string, data []byte, mode uint32) error {
	if IsSymlinkMode(mode) {
		return os.Symlink(string(data), path)
	}
	return ioutil.WriteFile(path, data, os.FileMode(mode))
}

// errOutsideWorkTree marks a path rejected because operating on it would land
// outside the repository. Callers that tolerate an unwritable file — entries
// a platform cannot represent — must not tolerate this one.
var errOutsideWorkTree = errors.New("path outside repository")

// checkParentsNotSymlinks rejects a target whose existing parent directories
// below root include a symlink. Path syntax alone cannot rule this out: a
// tree may legitimately carry a symlink entry, so a later entry named
// "link/child" is a well-formed repo-relative path that nonetheless resolves
// through the link — writing the file wherever it points.
//
// Only the components below root are examined. Prefixes above it are the
// user's own environment (a home directory or /tmp that is itself a symlink)
// and are not this code's business.
func checkParentsNotSymlinks(root, rel string) error {
	dir := path.Dir(path.Clean(rel))
	if dir == "." {
		return nil
	}
	cur := root
	for _, part := range strings.Split(dir, "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			// Not created yet: MkdirAll will make it a real directory.
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: refusing to write through symlink: %s", errOutsideWorkTree, rel)
		}
	}
	return nil
}

// writeWorkTreeFile writes content to a repo-relative path in the working
// tree, creating parent directories as needed.
//
// Every write of tree content goes through here. Safe paths alone are not
// enough: the path must also not traverse a symlink out of the repository,
// which is why the check is repeated at write time rather than trusted to
// validation of the tree it came from.
func (r *Repository) writeWorkTreeFile(rel string, data []byte, mode uint32) (string, error) {
	if !safeRepoPath(rel) {
		return "", fmt.Errorf("%w: %s", errOutsideWorkTree, rel)
	}
	if err := checkParentsNotSymlinks(r.Path, rel); err != nil {
		return "", err
	}
	fullPath := filepath.Join(r.Path, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return "", fmt.Errorf("create directory for %s: %w", rel, err)
	}
	// The parent check cannot see the final component. A symlink there would
	// be followed by the write, putting the content wherever it points — so
	// drop it first and let the entry replace it outright, as git does.
	if fi, err := os.Lstat(fullPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(fullPath); err != nil {
			// Refusing rather than writing anyway: the symlink is still there
			// and the write would follow it.
			return "", fmt.Errorf("%w: cannot replace symlink at %s: %v",
				errOutsideWorkTree, rel, err)
		}
	}
	if err := writeFileFromEntry(fullPath, data, mode); err != nil {
		return "", fmt.Errorf("write %s: %w", rel, err)
	}
	return fullPath, nil
}

// makeWorkTreeDir creates a repo-relative directory in the working tree
// (submodule and empty-directory entries), with the same checks as
// writeWorkTreeFile — a directory created through a symlinked parent lands
// outside the repository just as a file does.
func (r *Repository) makeWorkTreeDir(rel string) (string, error) {
	if !safeRepoPath(rel) {
		return "", fmt.Errorf("%w: %s", errOutsideWorkTree, rel)
	}
	if err := checkParentsNotSymlinks(r.Path, rel); err != nil {
		return "", err
	}
	fullPath := filepath.Join(r.Path, filepath.FromSlash(rel))
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		return "", fmt.Errorf("create directory %s: %w", rel, err)
	}
	return fullPath, nil
}

// removeWorkTreeFile removes a repo-relative path from the working tree,
// applying the same symlink check as writeWorkTreeFile so a tree cannot
// delete content outside the repository either.
func (r *Repository) removeWorkTreeFile(rel string) error {
	if !safeRepoPath(rel) {
		return fmt.Errorf("%w: %s", errOutsideWorkTree, rel)
	}
	if err := checkParentsNotSymlinks(r.Path, rel); err != nil {
		return err
	}
	return os.Remove(filepath.Join(r.Path, filepath.FromSlash(rel)))
}

// IndexEntry represents a staged file.
type IndexEntry struct {
	Hash        core.Hash `json:"hash"`         // object hash (for loading from store)
	ContentHash core.Hash `json:"content_hash"` // raw file content hash (for change detection)
	Size        int64     `json:"size"`
	Mode        uint32    `json:"mode"`
	Lazy        bool      `json:"lazy,omitempty"`  // true if chunks not yet fetched (lfs placeholder)
	Mtime       int64     `json:"mtime,omitempty"` // file mtime (UnixNano) at add time; 0 = unknown → status always re-hashes
	OSS         uint8     `json:"oss,omitempty"`   // OS bitmask: 1=win, 2=mac, 4=linux; 0 = all OSes
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
// A legacy JSON index is migrated to the binary format on load.
func (r *Repository) LoadIndex() (*Index, error) {
	data, err := ioutil.ReadFile(r.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Index{Entries: make(map[string]IndexEntry)}, nil
		}
		return nil, fmt.Errorf("read index: %w", err)
	}

	if len(data) >= len(indexMagic) && string(data[:len(indexMagic)]) == indexMagic {
		idx, err := decodeIndex(data)
		if err != nil {
			return nil, fmt.Errorf("parse index: %w", err)
		}
		return idx, nil
	}

	// Legacy JSON index — migrate to binary in place
	var idx Index
	if err := core.DeserializeJSON(data, &idx); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}
	if idx.Entries == nil {
		idx.Entries = make(map[string]IndexEntry)
	}
	if err := r.SaveIndex(&idx); err != nil {
		return nil, fmt.Errorf("migrate index: %w", err)
	}
	return &idx, nil
}

// SaveIndex writes the index to disk in the binary format.
func (r *Repository) SaveIndex(idx *Index) error {
	data, err := encodeIndex(idx)
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
	return r.addFileInternal(filePath, 0)
}

// AddFileOS reads a file from disk, stores it as an object, and adds it to the
// index as an OS-specific entry. The os parameter must be a known OS identifier.
func (r *Repository) AddFileOS(filePath, osTag string) error {
	id := OSID(osTag)
	if osTag != "" && id == 0 {
		return fmt.Errorf("unknown OS: %s", osTag)
	}
	return r.addFileInternal(filePath, id)
}

// AddFileOSMatch adds a file with an OS expression. The expression is resolved
// to an OS bitmask stored in the entry's OSS field; the mask is also used as
// the key discriminator.
func (r *Repository) AddFileOSMatch(filePath, expr string) error {
	if expr == "" || expr == "*" {
		return r.addFileInternal(filePath, 0)
	}
	include, exclude, err := ParseOSExpr(expr)
	if err != nil {
		return fmt.Errorf("invalid OS expression: %w", err)
	}
	return r.addFileInternal(filePath, MaskFromOSExpr(include, exclude))
}

// addFileInternal is the shared implementation for AddFile, AddFileOS, and AddFileOSMatch.
// It loads the index, processes one file, and saves — use AddFiles/AddFilesOSMatch
// for batch operations that avoid per-file index save.
func (r *Repository) addFileInternal(filePath string, oss uint8) error {
	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}
	changed, err := r.AddFileToIndex(filePath, oss, idx)
	if err != nil {
		return err
	}
	if !changed {
		return nil // nothing to save
	}
	return r.SaveIndex(idx)
}

// AddFileToIndex processes a single file and adds it to a pre-loaded index.
// oss is the OS bitmask (0 = all OSes) and is used as the key discriminator.
// Returns whether the index entry was actually written: an existing entry
// with the same object hash and mode is left untouched (git-style skip).
// Does NOT save the index — caller must call SaveIndex.
func (r *Repository) AddFileToIndex(filePath string, oss uint8, idx *Index) (bool, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return false, fmt.Errorf("resolve path: %w", err)
	}

	relPath, err := filepath.Rel(r.Path, absPath)
	if err != nil {
		return false, fmt.Errorf("path outside repository: %w", err)
	}
	if isOutsideRepo(relPath) {
		return false, fmt.Errorf("path outside repository: %s", filePath)
	}

	fi, err := os.Lstat(absPath)
	if err != nil {
		return false, fmt.Errorf("lstat file: %w", err)
	}

	isSymlink := fi.Mode()&os.ModeSymlink != 0

	if !isSymlink && fi.IsDir() {
		// Allow empty directories
		empty, _ := isDirEmpty(absPath)
		if !empty {
			return false, fmt.Errorf("cannot add non-empty directory: %s", filePath)
		}
		key := entryKey(filepath.ToSlash(relPath), oss)
		if e, ok := idx.Entries[key]; ok && e.Mode == DirMode {
			return false, nil // unchanged — already in the index
		}
		idx.Entries[key] = IndexEntry{
			Mode:  DirMode,
			Mtime: fi.ModTime().UnixNano(),
			OSS:   oss,
		}
		return true, nil
	}

	ignorer, err := r.LoadIgnoreMatcher()
	if err != nil {
		return false, err
	}
	relFormatted := filepath.ToSlash(relPath)
	if ignorer.Match(relFormatted, false) {
		return false, fmt.Errorf("matches .qinignore")
	}

	var data []byte
	if isSymlink {
		target, err := os.Readlink(absPath)
		if err != nil {
			return false, fmt.Errorf("read symlink: %w", err)
		}
		data = []byte(target)
	} else {
		data, err = ioutil.ReadFile(absPath)
		if err != nil {
			return false, fmt.Errorf("read file: %w", err)
		}

		// Reject LFS placeholder files — user must lfs-pull first
		if string(data) == "lo-lfs" && r.hasAnyLazyEntry(filepath.ToSlash(relPath)) {
			return false, fmt.Errorf("cannot add placeholder file '%s': use 'lfs-pull' to fetch real content first", filePath)
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
		return false, fmt.Errorf("store file: %w", err)
	}

	mode := uint32(fi.Mode())

	key := entryKey(filepath.ToSlash(relPath), oss)
	if e, ok := idx.Entries[key]; ok && e.Hash == h && e.Mode == mode {
		return false, nil // unchanged — already in the index
	}
	idx.Entries[key] = IndexEntry{
		Hash:        h,
		ContentHash: contentHash,
		Size:        fi.Size(),
		Mode:        mode,
		Mtime:       fi.ModTime().UnixNano(),
		OSS:         oss,
	}
	return true, nil
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
	var mask uint8
	if expr != "" && expr != "*" {
		include, exclude, err := ParseOSExpr(expr)
		if err != nil {
			return fmt.Errorf("invalid OS expression: %w", err)
		}
		mask = MaskFromOSExpr(include, exclude)
	}

	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	changedAny := false
	for _, f := range files {
		changed, err := r.AddFileToIndex(f, mask, idx)
		if err != nil {
			return fmt.Errorf("add %s: %w", f, err)
		}
		changedAny = changedAny || changed
	}

	if !changedAny {
		return nil // nothing to save
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
	if isOutsideRepo(relPath) {
		return fmt.Errorf("path outside repository: %s", filePath)
	}

	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	relFormatted := filepath.ToSlash(relPath)

	// Remove all entries for this path (default and OS-specific variants)
	found := false
	for key := range idx.Entries {
		if path, _ := parseKey(key); path == relFormatted {
			delete(idx.Entries, key)
			found = true
		}
	}
	if !found {
		return fmt.Errorf("file not tracked: %s", filePath)
	}
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
	if isOutsideRepo(relPath) {
		return fmt.Errorf("path outside repository: %s", filePath)
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
	// Index keys are later committed into trees and joined onto the working
	// tree root, so a path that escapes must not get in here either.
	if !safeRepoPath(path) {
		return fmt.Errorf("submodule path outside repository: %s", path)
	}

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
