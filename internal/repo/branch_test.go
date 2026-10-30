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
	maxProgressLen = 0

	if err := r.SwitchBranch("feature"); err != nil {
		t.Fatal(err)
	}
	if maxProgressLen != 0 {
		t.Errorf("progress state left open: maxProgressLen = %d", maxProgressLen)
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

// A restore that deletes a tracked file must also remove the directories it
// empties. qin tracks empty directories, so a skeleton left behind reads as
// untracked content: `status` lists the very paths the restored commit
// deleted, and a later `add .` stages them back.
func TestRestorePrunesEmptiedDirectories(t *testing.T) {
	r, dir := newTempRepo(t)

	gone := filepath.Join(dir, "gone", "deep")
	kept := filepath.Join(dir, "kept")
	for _, d := range []string{gone, kept} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{
		filepath.Join(gone, "file.txt"),
		filepath.Join(kept, "file.txt"),
		filepath.Join(dir, "top.txt"),
	}
	for _, f := range files {
		rmWrite(t, f, filepath.Base(f))
		if err := r.AddFile(f); err != nil {
			t.Fatal(err)
		}
	}
	hA, err := r.WriteCommit("test", "A")
	if err != nil {
		t.Fatal(err)
	}

	// B deletes the file and, with it, the whole gone/ chain.
	if _, err := r.RemovePaths([]string{files[0]}, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	hB, err := r.WriteCommit("test", "B")
	if err != nil {
		t.Fatal(err)
	}

	// A's worktree is what a pull starts from.
	if err := r.restoreCommit(hA); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatalf("restoring A did not recreate the file: %v", err)
	}

	if err := r.restoreCommit(hB); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatal("expected the deleted file to be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "gone")); !os.IsNotExist(err) {
		t.Fatal("expected the emptied directory chain to be removed, not left as untracked content")
	}
	if _, err := os.Stat(files[1]); err != nil {
		t.Fatalf("restore removed content the target tree still has: %v", err)
	}
}

// Pruning is an emptiness test, not a directory wipe: untracked content keeps
// its directory alive, exactly as `rm -r` does.
func TestRestoreKeepsDirectoryWithUntrackedContent(t *testing.T) {
	r, dir := newTempRepo(t)
	gone := filepath.Join(dir, "gone")
	if err := os.MkdirAll(gone, 0755); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(gone, "file.txt")
	rmWrite(t, tracked, "tracked")
	top := filepath.Join(dir, "top.txt")
	rmWrite(t, top, "top")
	for _, f := range []string{tracked, top} {
		if err := r.AddFile(f); err != nil {
			t.Fatal(err)
		}
	}
	hA, err := r.WriteCommit("test", "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RemovePaths([]string{tracked}, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	hB, err := r.WriteCommit("test", "B")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.restoreCommit(hA); err != nil {
		t.Fatal(err)
	}

	untracked := filepath.Join(gone, "local.txt")
	rmWrite(t, untracked, "mine")

	if err := r.restoreCommit(hB); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(untracked); err != nil {
		t.Fatalf("restore deleted untracked content: %v", err)
	}
	if _, err := os.Stat(tracked); !os.IsNotExist(err) {
		t.Fatal("expected the tracked file to be gone")
	}
}

// The directory the process runs in is never pruned: the rest of the command
// resolves relative paths against it, and the user's shell is sitting in it.
func TestRestoreKeepsCurrentDirectory(t *testing.T) {
	r, dir := newTempRepo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "f.txt")
	rmWrite(t, file, "f")
	top := filepath.Join(dir, "top.txt")
	rmWrite(t, top, "top")
	for _, f := range []string{file, top} {
		if err := r.AddFile(f); err != nil {
			t.Fatal(err)
		}
	}
	hA, err := r.WriteCommit("test", "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RemovePaths([]string{file}, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	hB, err := r.WriteCommit("test", "B")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.restoreCommit(hA); err != nil {
		t.Fatal(err)
	}

	rmChdir(t, sub)
	if err := r.restoreCommit(hB); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sub); err != nil {
		t.Fatalf("restore removed the process working directory: %v", err)
	}
}
