package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAndSwitchBranch(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// First commit on main
	f1 := filepath.Join(dir, "a.txt")
	ioutil.WriteFile(f1, []byte("main content"), 0644)
	repo.AddFile(f1)
	mainHash, err := repo.WriteCommit("Test", "main")
	if err != nil {
		t.Fatal(err)
	}

	// Create a new branch
	if err := repo.CreateBranch("feature"); err != nil {
		t.Fatal(err)
	}

	// Switch to feature branch
	if err := repo.SwitchBranch("feature"); err != nil {
		t.Fatal(err)
	}

	// Verify HEAD points to the new branch
	branch := repo.CurrentBranch()
	if branch != "feature" {
		t.Fatalf("expected 'feature', got '%s'", branch)
	}

	// HEAD should resolve to the same commit
	resolved, err := repo.ResolveHEAD()
	if err != nil {
		t.Fatal(err)
	}
	if resolved != mainHash.String() {
		t.Fatalf("expected %s, got %s", mainHash, resolved)
	}

	// Switch back to main
	if err := repo.SwitchBranch("main"); err != nil {
		t.Fatal(err)
	}
	if repo.CurrentBranch() != "main" {
		t.Fatalf("expected 'main', got '%s'", repo.CurrentBranch())
	}
}

func TestSwitchBranchRestoresFiles(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Commit on main
	ioutil.WriteFile(filepath.Join(dir, "main.txt"), []byte("main"), 0644)
	repo.AddFile(filepath.Join(dir, "main.txt"))
	repo.WriteCommit("Test", "main commit")

	// Create feature branch and add a different file
	repo.CreateBranch("feature")
	repo.SwitchBranch("feature")

	ioutil.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0644)
	repo.AddFile(filepath.Join(dir, "feature.txt"))
	repo.WriteCommit("Test", "feature commit")

	// Switch back to main
	if err := repo.SwitchBranch("main"); err != nil {
		t.Fatal(err)
	}

	// main.txt should exist, feature.txt should be gone
	if _, err := os.Stat(filepath.Join(dir, "main.txt")); os.IsNotExist(err) {
		t.Fatal("expected main.txt to exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "feature.txt")); !os.IsNotExist(err) {
		t.Fatal("expected feature.txt to not exist on main")
	}

	// Switch to feature again
	if err := repo.SwitchBranch("feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "feature.txt")); os.IsNotExist(err) {
		t.Fatal("expected feature.txt to exist on feature branch")
	}
}

func TestSwitchNonExistentBranch(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.SwitchBranch("nonexistent"); err == nil {
		t.Fatal("expected error for nonexistent branch")
	}
}

func TestCreateBranchNoCommits(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.CreateBranch("feature"); err == nil {
		t.Fatal("expected error when no commits exist")
	}
}

func TestListBranches(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// No branches before first commit
	_, current, err := repo.ListBranches()
	if err != nil {
		t.Fatal(err)
	}
	if current != "" {
		t.Fatalf("expected no current branch, got '%s'", current)
	}

	// First commit creates main
	ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)
	repo.AddFile(filepath.Join(dir, "a.txt"))
	repo.WriteCommit("Test", "first")

	branches, current, err := repo.ListBranches()
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || branches[0] != "main" {
		t.Fatalf("expected [main], got %v", branches)
	}
	if current != "main" {
		t.Fatalf("expected 'main', got '%s'", current)
	}

	// Create another branch
	repo.CreateBranch("dev")
	branches, _, _ = repo.ListBranches()
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(branches))
	}
}

// TestSwitchBranchEndsProgressLine pins the shape of the final output: the
// restore rewrites an in-place stderr line, and a caller prints its result
// right after it returns ("fast-forward pull" after a pull). Leaving the line
// unterminated made that message land on top of the progress text.
func TestSwitchBranchEndsProgressLine(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "a.txt")
	if err := ioutil.WriteFile(f, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFile(f); err != nil {
		t.Fatal(err)
	}
	if _, err := r.WriteCommit("Test", "commit"); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateBranch("feature"); err != nil {
		t.Fatal(err)
	}

	old := os.Stderr
	capture, err := ioutil.TempFile("", "qin-stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = capture
	defer func() {
		os.Stderr = old
		capture.Close()
		os.Remove(capture.Name())
	}()
	lastProgressLen = 0

	if err := r.SwitchBranch("feature"); err != nil {
		t.Fatal(err)
	}
	if lastProgressLen != 0 {
		t.Errorf("progress state left open: lastProgressLen = %d", lastProgressLen)
	}
	if err := capture.Sync(); err != nil {
		t.Fatal(err)
	}
	data, err := ioutil.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("progress line not terminated: %q", data)
	}
}
