package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
)

func TestAddAndListFiles(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a test file
	testFile := filepath.Join(dir, "hello.txt")
	if err := ioutil.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := repo.AddFile(testFile); err != nil {
		t.Fatal(err)
	}

	files, err := repo.ListFiles()
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	entry, ok := files["hello.txt"]
	if !ok {
		t.Fatal("expected hello.txt in index")
	}

	if entry.Size != 11 {
		t.Fatalf("expected size 11, got %d", entry.Size)
	}

	if entry.Hash.IsZero() {
		t.Fatal("expected non-zero hash")
	}
}

func TestAddMultipleFiles(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"a.txt":     "content a",
		"b.txt":     "content b",
		"sub/c.txt": "content c",
	}
	for path, content := range files {
		fullPath := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := ioutil.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if err := repo.AddFile(fullPath); err != nil {
			t.Fatal(err)
		}
	}

	staged, err := repo.ListFiles()
	if err != nil {
		t.Fatal(err)
	}

	if len(staged) != 3 {
		t.Fatalf("expected 3 files, got %d", len(staged))
	}

	for path := range files {
		if _, ok := staged[filepath.ToSlash(path)]; !ok {
			t.Fatalf("expected %s in index", path)
		}
	}
}

func TestRemoveFile(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(dir, "remove.txt")
	if err := ioutil.WriteFile(testFile, []byte("to be removed"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := repo.AddFile(testFile); err != nil {
		t.Fatal(err)
	}

	if err := repo.RemoveFile(testFile); err != nil {
		t.Fatal(err)
	}

	files, err := repo.ListFiles()
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 0 {
		t.Fatalf("expected 0 files after removal, got %d", len(files))
	}
}

func TestAddDirectoryRejected(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := repo.AddFile(subdir); err != nil {
		t.Fatal(err)
	}
	// Non-empty directory should still be rejected
	subfile := filepath.Join(subdir, "f.txt")
	ioutil.WriteFile(subfile, []byte("content"), 0644)
	if err := repo.AddFile(subdir); err == nil {
		t.Fatal("expected error when adding non-empty directory")
	}
}

func TestIndexPersists(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(dir, "persist.txt")
	if err := ioutil.WriteFile(testFile, []byte("persist test"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := repo.AddFile(testFile); err != nil {
		t.Fatal(err)
	}

	// Re-open repo and check index persists
	repo2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	files, err := repo2.ListFiles()
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file after reopen, got %d", len(files))
	}
}

func TestAddFileOutsideRepo(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	outsideFile := filepath.Join(os.TempDir(), "outside.txt")
	if err := ioutil.WriteFile(outsideFile, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outsideFile)

	if err := repo.AddFile(outsideFile); err == nil {
		t.Fatal("expected error when adding file outside repo")
	}
}

// TestAddFileOutsideRepoRelativeForm covers the form that first slipped
// through: filepath.Rel does not fail on a "../" path, it returns one, so the
// guard has to inspect the result rather than just the error.
func TestAddFileOutsideRepoRelativeForm(t *testing.T) {
	base, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	repoDir := filepath.Join(base, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	repo, err := Init(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	sibling := filepath.Join(base, "outside.txt")
	if err := ioutil.WriteFile(sibling, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}

	// Paths are built from repoDir rather than as bare "../" strings: these
	// functions resolve relative inputs against the process working
	// directory, not the repository, so a bare "../outside.txt" would be a
	// different path entirely and would not exercise this code.
	for _, p := range []string{
		filepath.Join(repoDir, "..", "outside.txt"),
		filepath.Join(repoDir, "sub", "..", "..", "outside.txt"),
	} {
		if err := repo.AddFile(p); err == nil {
			t.Fatalf("expected error when adding %q", p)
		}
	}

	// The index must be untouched by the rejected adds.
	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 0 {
		t.Fatalf("expected an empty index, got %d entries", len(idx.Entries))
	}
}

// TestRestoreFileOutsideRepo plants an index entry whose path escapes the
// repository — something `add` now refuses to create, but which could still
// arrive from a hand-edited index — and asserts that restore refuses to act
// on it rather than writing through the "../".
func TestRestoreFileOutsideRepo(t *testing.T) {
	base, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	repoDir := filepath.Join(base, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	repo, err := Init(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	victim := filepath.Join(base, "victim.txt")
	const original = "original contents"
	if err := ioutil.WriteFile(victim, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// The object must really exist, or restore would fail at the load and
	// never reach the write this test is about.
	payload := []byte("overwritten")
	hash, err := repo.StoreObject(core.ObjectBlob, payload)
	if err != nil {
		t.Fatal(err)
	}

	// The key is the repo-relative form restore will derive; the argument is
	// absolute so the lookup does not depend on the process working
	// directory. Rel(repoDir, victim) is exactly this key.
	target := victim
	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	idx.Entries[entryKey(filepath.Join("..", "victim.txt"), 0)] = IndexEntry{
		Hash:        hash,
		ContentHash: core.HashFromBytes(payload),
		Size:        int64(len(payload)),
		Mode:        0644,
	}
	if err := repo.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}

	if err := repo.RestoreFile(target); err == nil {
		t.Fatal("expected restore to reject a path outside the repo")
	}

	got, err := ioutil.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("file outside the repo was modified: got %q", got)
	}
}

func TestAddPlaceholderRejected(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a manifest hash to simulate a large file entry
	chunkHash, err := r.StoreChunkedFile([]byte("some large content"))
	if err != nil {
		t.Fatal(err)
	}

	// Directly set up index with a lazy entry
	idx := &Index{Entries: map[string]IndexEntry{
		"large.bin": {
			Hash: chunkHash,
			Size: 12345,
			Mode: 0644,
			Lazy: true,
		},
	}}
	if err := r.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}

	// Write placeholder file to working tree
	if err := ioutil.WriteFile(filepath.Join(dir, "large.bin"), []byte("lo-lfs"), 0644); err != nil {
		t.Fatal(err)
	}

	// Try to add it — should be rejected
	err = r.AddFile(filepath.Join(dir, "large.bin"))
	if err == nil {
		t.Fatal("expected error when adding placeholder file")
	}
}

func TestIndexBinaryRoundTrip(t *testing.T) {
	idx := &Index{Entries: map[string]IndexEntry{
		"hello.txt": {
			Hash:        core.HashFromBytes([]byte("hello")),
			ContentHash: core.HashFromBytes([]byte("hello")),
			Size:        5,
			Mode:        0644,
			Mtime:       1234567890123456789,
		},
		"f.txt\x00\x04": { // OS-variant composite key (linux = 4)
			Hash:        core.HashFromBytes([]byte("linux")),
			ContentHash: core.HashFromBytes([]byte("linux")),
			Size:        5,
			Mode:        0755,
			Lazy:        true,
			Mtime:       999,
			OSS:         OSLinux,
		},
		"dir/": {
			Mode:  DirMode,
			Mtime: 42,
			OSS:   OSWin | OSMac,
		},
	}}

	data, err := encodeIndex(idx)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:6]) != indexMagic {
		t.Fatal("expected QINIDX magic")
	}

	got, err := decodeIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got.Entries))
	}
	if e := got.Entries["hello.txt"]; e.Size != 5 || e.Mode != 0644 || e.Mtime != 1234567890123456789 ||
		e.Hash != idx.Entries["hello.txt"].Hash || e.ContentHash != idx.Entries["hello.txt"].ContentHash {
		t.Errorf("hello.txt round-trip mismatch: %+v", e)
	}
	if e := got.Entries["f.txt\x00\x04"]; !e.Lazy || e.OSS != OSLinux || e.Mtime != 999 || e.Mode != 0755 {
		t.Errorf("f.txt variant round-trip mismatch: %+v", e)
	}
	if e := got.Entries["dir/"]; e.Mode != DirMode || e.OSS != OSWin|OSMac || e.Mtime != 42 {
		t.Errorf("dir entry round-trip mismatch: %+v", e)
	}
}

// TestIndexJSONMigration verifies a legacy JSON index is loaded and rewritten
// in the binary format.
func TestIndexJSONMigration(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	legacy := &Index{Entries: map[string]IndexEntry{
		"legacy.txt": {
			Hash:        core.HashFromBytes([]byte("legacy")),
			ContentHash: core.HashFromBytes([]byte("legacy")),
			Size:        6,
			Mode:        0644,
			Mtime:       777,
		},
	}}
	data, err := core.SerializeJSON(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(repo.indexPath(), data, 0644); err != nil {
		t.Fatal(err)
	}

	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := idx.Entries["legacy.txt"]; !ok || e.Size != 6 || e.Mtime != 777 {
		t.Fatalf("legacy entry not preserved: %+v", e)
	}

	// The file must now be binary
	onDisk, err := ioutil.ReadFile(repo.indexPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk[:6]) != indexMagic {
		t.Fatal("expected index migrated to binary format")
	}
}

func TestIndexCorrupt(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Truncated binary index
	if err := ioutil.WriteFile(repo.indexPath(), []byte("QINIDX\x01\x00\xff\xff\xff\xff"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadIndex(); err == nil {
		t.Fatal("expected error for truncated index")
	}

	// Garbage
	if err := ioutil.WriteFile(repo.indexPath(), []byte("not an index at all"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadIndex(); err == nil {
		t.Fatal("expected error for garbage index")
	}

	// Missing file → empty index, no error
	if err := os.Remove(repo.indexPath()); err != nil {
		t.Fatal(err)
	}
	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 0 {
		t.Fatal("expected empty index")
	}
}

func TestAddNonPlaceholderWithSameContent(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// A file that happens to have content "lo-lfs" but no lazy index entry
	// should be addable normally
	if err := ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("lo-lfs"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := r.AddFile(filepath.Join(dir, "f.txt")); err != nil {
		t.Fatalf("should be able to add non-placeholder with same content: %v", err)
	}
}

// TestAddFileToIndexChanged verifies the git-style skip: re-adding an
// unchanged file reports no change, while new/changed content or a new OS
// variant still does.
func TestAddFileToIndexChanged(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}

	// First add of a new file → changed
	fpath := filepath.Join(dir, "f.txt")
	ioutil.WriteFile(fpath, []byte("content"), 0644)
	changed, err := repo.AddFileToIndex(fpath, 0, idx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected first add to report a change")
	}

	// Same content again → skipped
	changed, err = repo.AddFileToIndex(fpath, 0, idx)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected unchanged re-add to be skipped")
	}

	// Modified content → changed
	ioutil.WriteFile(fpath, []byte("changed content"), 0644)
	changed, err = repo.AddFileToIndex(fpath, 0, idx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected modified file to report a change")
	}

	// Same path with a different OS mask is a new variant → changed
	changed, err = repo.AddFileToIndex(fpath, OSWin, idx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected new OS variant to report a change")
	}

	// Empty directory: first add changed, repeat skipped
	subdir := filepath.Join(dir, "emptydir")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	changed, err = repo.AddFileToIndex(subdir, 0, idx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected first dir add to report a change")
	}
	changed, err = repo.AddFileToIndex(subdir, 0, idx)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected unchanged dir re-add to be skipped")
	}
}

// newTempRepo returns an empty initialized repository inside a temporary
// directory, removed when the test ends. The repository is the "repo"
// subdirectory, so a test can place sibling paths next to it without touching
// shared locations like /tmp itself.
func newTempRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	root, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })

	dir := filepath.Join(root, "repo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

// The three helpers below are the single path every write, directory creation
// and removal of tree content goes through. Each must refuse a target that
// resolves through a symlink out of the repository, and must still allow
// ordinary nested paths — a guard that rejects everything is not a fix.

func TestWriteWorkTreeFileRefusesSymlinkedParent(t *testing.T) {
	r, repoDir := newTempRepo(t)

	outside := filepath.Join(repoDir, "..", "outside")
	outside = filepath.Clean(outside)
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "pwned.txt")
	const original = "ORIGINAL"
	if err := ioutil.WriteFile(victim, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repoDir, "link")); err != nil {
		t.Fatal(err)
	}

	_, err := r.writeWorkTreeFile("link/pwned.txt", []byte("PWNED"), 0644)
	if err == nil {
		t.Fatal("expected a write through a symlinked parent to be rejected")
	}
	got, err := ioutil.ReadFile(victim)
	if err != nil {
		t.Fatalf("file outside the repo disappeared: %v", err)
	}
	if string(got) != original {
		t.Fatalf("escaped: outside file is now %q, want %q", got, original)
	}
}

func TestRemoveWorkTreeFileRefusesSymlinkedParent(t *testing.T) {
	r, repoDir := newTempRepo(t)

	outside := filepath.Clean(filepath.Join(repoDir, "..", "outside"))
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "victim.txt")
	if err := ioutil.WriteFile(victim, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repoDir, "link")); err != nil {
		t.Fatal(err)
	}

	if err := r.removeWorkTreeFile("link/victim.txt"); err == nil {
		t.Fatal("expected a removal through a symlinked parent to be rejected")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("file outside the repo was removed: %v", err)
	}
}

func TestMakeWorkTreeDirRefusesSymlinkedParent(t *testing.T) {
	r, repoDir := newTempRepo(t)

	outside := filepath.Clean(filepath.Join(repoDir, "..", "outside"))
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repoDir, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := r.makeWorkTreeDir("link/sub"); err == nil {
		t.Fatal("expected directory creation through a symlinked parent to be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "sub")); err == nil {
		t.Fatal("directory was created outside the repo")
	}
}

// TestWorkTreeHelpersAllowNestedPaths is the positive control: the symlink
// checks must not reject ordinary nested paths, and a path whose parent does
// not exist yet must still be created.
func TestWorkTreeHelpersAllowNestedPaths(t *testing.T) {
	r, repoDir := newTempRepo(t)

	full, err := r.writeWorkTreeFile("a/b/c.txt", []byte("hi"), 0644)
	if err != nil {
		t.Fatalf("nested write rejected: %v", err)
	}
	got, err := ioutil.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Fatalf("wrote %q, want %q", got, "hi")
	}

	dir, err := r.makeWorkTreeDir("a/b/d")
	if err != nil {
		t.Fatalf("nested dir rejected: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("nested dir not created: %v", err)
	}

	if err := r.removeWorkTreeFile("a/b/c.txt"); err != nil {
		t.Fatalf("nested removal rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "a", "b", "c.txt")); !os.IsNotExist(err) {
		t.Fatalf("file was not removed: %v", err)
	}
}

// TestWriteWorkTreeFileReplacesSymlinkAtTarget covers the final path
// component, which the parent check above cannot see.
//
// A tree entry named "link" with a regular-file mode must end up as a regular
// file. If the working tree already has a symlink there — a path an earlier
// commit made, or the user created — writing through it would put the content
// wherever the link points, outside the repository.
func TestWriteWorkTreeFileReplacesSymlinkAtTarget(t *testing.T) {
	r, repoDir := newTempRepo(t)

	outside := filepath.Clean(filepath.Join(repoDir, "..", "outside"))
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "victim.txt")
	const original = "ORIGINAL"
	if err := ioutil.WriteFile(victim, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(repoDir, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := r.writeWorkTreeFile("link", []byte("PWNED"), 0644); err != nil {
		t.Fatalf("write over a symlink failed: %v", err)
	}

	got, err := ioutil.ReadFile(victim)
	if err != nil {
		t.Fatalf("file outside the repo disappeared: %v", err)
	}
	if string(got) != original {
		t.Fatalf("escaped: outside file is now %q, want %q", got, original)
	}

	fi, err := os.Lstat(filepath.Join(repoDir, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("symlink at the target path was not replaced by the entry's file")
	}
	content, err := ioutil.ReadFile(filepath.Join(repoDir, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "PWNED" {
		t.Fatalf("wrote %q, want %q", content, "PWNED")
	}
}
