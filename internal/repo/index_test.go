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
