package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
)

func TestStageTrackedChangesStagesModifiedAndDeleted(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	ioutil.WriteFile(a, []byte("one"), 0644)
	ioutil.WriteFile(b, []byte("two"), 0644)
	if err := repo.AddFiles([]string{a, b}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.WriteCommit("Test", "init"); err != nil {
		t.Fatal(err)
	}

	// One tracked modification, one tracked deletion, one untracked file. Only
	// the first two belong to commit -a.
	ioutil.WriteFile(a, []byte("one changed"), 0644)
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(dir, "untracked.txt")
	ioutil.WriteFile(untracked, []byte("new"), 0644)

	modified, deleted, err := repo.StageTrackedChanges()
	if err != nil {
		t.Fatal(err)
	}
	if modified != 1 || deleted != 1 {
		t.Fatalf("expected 1 modified and 1 deleted, got %d and %d", modified, deleted)
	}

	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := idx.Entries["a.txt"]
	if !ok {
		t.Fatal("a.txt missing from index")
	}
	if entry.ContentHash != core.HashFromBytes([]byte("one changed")) {
		t.Fatal("a.txt was not restaged with its new content")
	}
	if _, ok := idx.Entries["b.txt"]; ok {
		t.Fatal("deleted b.txt still has an index entry")
	}
	if _, ok := idx.Entries["untracked.txt"]; ok {
		t.Fatal("commit -a must not stage untracked files")
	}

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 0 || len(s.Deleted) != 0 {
		t.Fatalf("expected a clean tracked tree, got modified %v deleted %v", s.Modified, s.Deleted)
	}
	if len(s.Untracked) != 1 || s.Untracked[0] != "untracked.txt" {
		t.Fatalf("expected [untracked.txt], got %v", s.Untracked)
	}
}

// Ignore rules only classify untracked files. A file that was tracked before a
// pattern came to match it must still be stageable; otherwise .qinignore could
// block commit -a with no way to reconcile the index.
func TestStageTrackedChangesIgnoresIgnoreRulesForTrackedPaths(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(dir, "app.log")
	ioutil.WriteFile(logPath, []byte("first"), 0644)
	if err := repo.AddFile(logPath); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.WriteCommit("Test", "init"); err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, ".qinignore"), []byte("*.log\n"), 0644)
	ioutil.WriteFile(logPath, []byte("second"), 0644)

	modified, deleted, err := repo.StageTrackedChanges()
	if err != nil {
		t.Fatal(err)
	}
	if modified != 1 || deleted != 0 {
		t.Fatalf("expected 1 modified and 0 deleted, got %d and %d", modified, deleted)
	}

	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if entry := idx.Entries["app.log"]; entry.ContentHash != core.HashFromBytes([]byte("second")) {
		t.Fatal("ignored tracked file was not restaged")
	}

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 0 {
		t.Fatalf("expected 0 modified after staging, got %v", s.Modified)
	}
}

// A deleted path loses every variant, not just the one this host sees: the
// file is gone, and leaving another platform's variant behind would keep the
// path in the next tree. This is what `rm` does, and commit -a follows it.
func TestStageTrackedChangesRemovesEveryVariantOfDeletedPath(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	fpath := filepath.Join(dir, "gone.txt")
	ioutil.WriteFile(fpath, []byte("content"), 0644)
	if err := repo.AddFile(fpath); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.WriteCommit("Test", "init"); err != nil {
		t.Fatal(err)
	}

	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	def := idx.Entries["gone.txt"]
	variant := def
	variant.OSS = OSMac
	idx.Entries[entryKey("gone.txt", OSMac)] = variant
	if err := repo.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(fpath); err != nil {
		t.Fatal(err)
	}
	modified, deleted, err := repo.StageTrackedChanges()
	if err != nil {
		t.Fatal(err)
	}
	if modified != 0 || deleted != 1 {
		t.Fatalf("expected 0 modified and 1 deleted, got %d and %d", modified, deleted)
	}

	idx, err = repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.Entries["gone.txt"]; ok {
		t.Fatal("default entry of deleted path survived")
	}
	if _, ok := idx.Entries[entryKey("gone.txt", OSMac)]; ok {
		t.Fatal("macOS variant of deleted path survived")
	}
}
