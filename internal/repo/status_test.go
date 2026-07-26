package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func TestStatusClean(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Add and commit a file
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("content"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "init")

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	if s.Branch != "main" {
		t.Fatalf("expected main, got %s", s.Branch)
	}
	if len(s.Staged) != 0 {
		t.Fatalf("expected 0 staged (committed files filtered), got %d", len(s.Staged))
	}
	if len(s.Modified) != 0 {
		t.Fatalf("expected 0 modified, got %d", len(s.Modified))
	}
	if len(s.Untracked) != 0 {
		t.Fatalf("expected 0 untracked, got %d", len(s.Untracked))
	}
	if len(s.Deleted) != 0 {
		t.Fatalf("expected 0 deleted, got %d", len(s.Deleted))
	}
}

func TestStatusUntracked(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked"), 0644)
	repo.AddFile(filepath.Join(dir, "tracked.txt"))

	ioutil.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("untracked"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Untracked) != 1 || s.Untracked[0] != "untracked.txt" {
		t.Fatalf("expected [untracked.txt], got %v", s.Untracked)
	}
}

func TestStatusModified(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	fpath := filepath.Join(dir, "f.txt")
	ioutil.WriteFile(fpath, []byte("original"), 0644)
	repo.AddFile(fpath)
	repo.WriteCommit("Test", "init")

	// Modify the file without re-staging
	ioutil.WriteFile(fpath, []byte("modified content"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Modified) != 1 || s.Modified[0] != "f.txt" {
		t.Fatalf("expected [f.txt] modified, got %v", s.Modified)
	}
}

func TestStatusDeleted(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	fpath := filepath.Join(dir, "f.txt")
	ioutil.WriteFile(fpath, []byte("content"), 0644)
	repo.AddFile(fpath)
	repo.WriteCommit("Test", "init")

	// Delete the file
	os.Remove(fpath)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Deleted) != 1 || s.Deleted[0] != "f.txt" {
		t.Fatalf("expected [f.txt] deleted, got %v", s.Deleted)
	}
}

func TestStatusSkipsLoDir(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a file inside .qin (should be ignored)
	ioutil.WriteFile(filepath.Join(dir, LoDir, "test-file"), []byte("should be ignored"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Untracked) != 0 {
		t.Fatalf("expected 0 untracked (lo dir skipped), got %v", s.Untracked)
	}
}
