package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestStatusStatFastPath verifies the mtime/size fast path: an entry with a
// recorded mtime is reported clean without hashing, and a same-size rewrite
// (with a bumped mtime) is still detected as modified.
func TestStatusStatFastPath(t *testing.T) {
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
	ioutil.WriteFile(fpath, []byte("aaaa"), 0644)
	if err := repo.AddFile(fpath); err != nil {
		t.Fatal(err)
	}

	// Entry must record the file mtime for the fast path to engage.
	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := idx.Entries["f.txt"]
	if !ok || entry.Mtime == 0 {
		t.Fatal("expected index entry with non-zero Mtime")
	}

	// Unmodified → clean
	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 0 {
		t.Fatalf("expected 0 modified, got %v", s.Modified)
	}

	// Same-size rewrite with an explicit mtime bump → stat mismatch → modified
	ioutil.WriteFile(fpath, []byte("bbbb"), 0644)
	bumped := time.Unix(0, entry.Mtime+int64(time.Second))
	if err := os.Chtimes(fpath, bumped, bumped); err != nil {
		t.Fatal(err)
	}
	s, err = repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 1 || s.Modified[0] != "f.txt" {
		t.Fatalf("expected [f.txt] modified, got %v", s.Modified)
	}
}

// TestStatusFastPathAfterCheckout verifies restoreCommit records mtimes so a
// status right after checkout/rebase/merge doesn't re-hash everything.
func TestStatusFastPathAfterCheckout(t *testing.T) {
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
	h, err := repo.WriteCommit("Test", "init")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.restoreCommit(h); err != nil {
		t.Fatal(err)
	}

	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if entry, ok := idx.Entries["f.txt"]; !ok || entry.Mtime == 0 {
		t.Fatal("expected entry with Mtime after restoreCommit")
	}

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 0 || len(s.Untracked) != 0 || len(s.Deleted) != 0 {
		t.Fatalf("expected clean status, got modified=%v untracked=%v deleted=%v", s.Modified, s.Untracked, s.Deleted)
	}
}

// TestStatusUntrackedDirPruning verifies that an untracked directory with no
// tracked content is reported as a single entry without descending.
func TestStatusUntrackedDirPruning(t *testing.T) {
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

	// Deep untracked subtree
	if err := os.MkdirAll(filepath.Join(dir, "build", "gen"), 0755); err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(dir, "build", "gen", "x.txt"), []byte("x"), 0644)
	ioutil.WriteFile(filepath.Join(dir, "build", "y.txt"), []byte("y"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Untracked) != 1 || s.Untracked[0] != "build/" {
		t.Fatalf("expected [build/], got %v", s.Untracked)
	}
}

// TestStatusIgnoredDirPruned verifies an ignored directory is skipped entirely.
func TestStatusIgnoredDirPruned(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Join(dir, "out"), 0755); err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(dir, "out", "a.txt"), []byte("a"), 0644)
	ioutil.WriteFile(filepath.Join(dir, ".qinignore"), []byte("out/\n"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Untracked) != 0 {
		t.Fatalf("expected 0 untracked (out/ ignored), got %v", s.Untracked)
	}
}

// TestStatusIgnoredDirWithNegate verifies re-include rules force the walk to
// descend into ignored directories so re-included children are still listed.
func TestStatusIgnoredDirWithNegate(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Join(dir, "out"), 0755); err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(dir, "out", "keep.txt"), []byte("keep"), 0644)
	ioutil.WriteFile(filepath.Join(dir, "out", "drop.txt"), []byte("drop"), 0644)
	ioutil.WriteFile(filepath.Join(dir, ".qinignore"), []byte("out/\n!out/keep.txt\n"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Untracked) != 1 || s.Untracked[0] != "out/keep.txt" {
		t.Fatalf("expected [out/keep.txt], got %v", s.Untracked)
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
