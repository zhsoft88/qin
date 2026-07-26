package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
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
		"a.txt": "content a",
		"b.txt": "content b",
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
