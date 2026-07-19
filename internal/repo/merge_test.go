package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
)

func TestFindMergeBaseLinear(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// A <- B <- C
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("a"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	hA, err := repo.WriteCommit("Test", "A")
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("b"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	hB, err := repo.WriteCommit("Test", "B")
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("c"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	hC, err := repo.WriteCommit("Test", "C")
	if err != nil {
		t.Fatal(err)
	}

	base, err := repo.FindMergeBase(hC, hA)
	if err != nil {
		t.Fatal(err)
	}
	if base != hA {
		t.Fatalf("expected base A, got %s", base.Short())
	}

	base, err = repo.FindMergeBase(hC, hB)
	if err != nil {
		t.Fatal(err)
	}
	if base != hB {
		t.Fatalf("expected base B, got %s", base.Short())
	}
}

func TestFindMergeBaseBranch(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// base A, then branch into B and C
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("base"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	hBase, err := repo.WriteCommit("Test", "base")
	if err != nil {
		t.Fatal(err)
	}

	repo.CreateBranch("feature")

	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("main"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	hMain, err := repo.WriteCommit("Test", "main")
	if err != nil {
		t.Fatal(err)
	}

	repo.SwitchBranch("feature")
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("feature"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	hFeat, err := repo.WriteCommit("Test", "feature")
	if err != nil {
		t.Fatal(err)
	}

	base, err := repo.FindMergeBase(hMain, hFeat)
	if err != nil {
		t.Fatal(err)
	}
	if base != hBase {
		t.Fatalf("expected base hash, got %s", base.Short())
	}
}

func TestFastForwardMerge(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// base on main
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("base"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "base")

	// create feature branch — both point to base
	repo.CreateBranch("feature")

	// commit on feature (not main)
	repo.SwitchBranch("feature")
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("feature change 1"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "feat1")

	ioutil.WriteFile(filepath.Join(dir, "g.txt"), []byte("new file"), 0644)
	repo.AddFile(filepath.Join(dir, "g.txt"))
	repo.WriteCommit("Test", "feat2")

	// Merge feature into main (fast-forward)
	repo.SwitchBranch("main")
	result, err := repo.Merge("feature")
	if err != nil {
		t.Fatal(err)
	}
	if !result.FastForward {
		t.Fatal("expected fast-forward merge")
	}
	if !result.Merged {
		t.Fatal("expected merged to be true")
	}

	// Verify files from feature branch exist
	if _, err := os.Stat(filepath.Join(dir, "g.txt")); os.IsNotExist(err) {
		t.Fatal("expected g.txt from feature branch after merge")
	}

	// Verify HEAD now matches feature branch tip
	headStr, _ := repo.ResolveHEAD()
	featStr, _ := repo.ReadRef("refs/heads/feature")
	if headStr != featStr {
		t.Fatal("expected HEAD to match feature branch tip after fast-forward")
	}
}

func TestAlreadyUpToDate(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("a"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "A")

	_, err = repo.Merge("main")
	if err == nil || err.Error() != "already up to date" {
		t.Fatalf("expected 'already up to date', got %v", err)
	}
}

func TestMergeNonExistentBranch(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("a"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "A")

	_, err = repo.Merge("nonexistent")
	if err == nil || !strings.Contains(err.Error(), "branch not found") {
		t.Fatalf("expected 'branch not found', got %v", err)
	}
}

func TestThreeWayMergeNoConflict(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// base
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("base"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "base")

	repo.CreateBranch("feature")

	// change on main
	ioutil.WriteFile(filepath.Join(dir, "main.txt"), []byte("main-only"), 0644)
	repo.AddFile(filepath.Join(dir, "main.txt"))
	repo.WriteCommit("Test", "main work")

	// change on feature
	repo.SwitchBranch("feature")
	ioutil.WriteFile(filepath.Join(dir, "feat.txt"), []byte("feature-only"), 0644)
	repo.AddFile(filepath.Join(dir, "feat.txt"))
	repo.WriteCommit("Test", "feature work")

	// Merge feature into main — no conflict (different files)
	repo.SwitchBranch("main")
	result, err := repo.Merge("feature")
	if err != nil {
		t.Fatal(err)
	}
	if result.FastForward {
		t.Fatal("expected non-fast-forward merge")
	}
	if !result.Merged {
		t.Fatal("expected merge commit to be created")
	}
	if len(result.Conflicts) > 0 {
		t.Fatalf("expected no conflicts, got %v", result.Conflicts)
	}

	// Verify both files exist
	if _, err := os.Stat(filepath.Join(dir, "feat.txt")); os.IsNotExist(err) {
		t.Fatal("expected feat.txt from feature branch after merge")
	}
	if _, err := os.Stat(filepath.Join(dir, "main.txt")); os.IsNotExist(err) {
		t.Fatal("expected main.txt after merge")
	}

	// Verify merge commit has two parents
	headStr, _ := repo.ResolveHEAD()
	head, _ := core.HashFromHex(headStr)
	commit, err := repo.LoadCommit(head)
	if err != nil {
		t.Fatal(err)
	}
	if len(commit.Parents) != 2 {
		t.Fatalf("expected 2 parents for merge commit, got %d", len(commit.Parents))
	}
}

func TestThreeWayMergeConflict(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// base with f.txt
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("base content"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "base")

	repo.CreateBranch("feature")

	// both sides change f.txt differently
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("main change"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "main work")

	repo.SwitchBranch("feature")
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("feature change"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "feature work")

	// Merge — should have conflict
	repo.SwitchBranch("main")
	result, err := repo.Merge("feature")
	if err == nil {
		t.Fatal("expected merge conflict error")
	}
	if result == nil || len(result.Conflicts) == 0 {
		t.Fatal("expected conflicts in result")
	}

	// Verify conflict files exist
	var foundConflict bool
	for _, name := range result.Conflicts {
		if name == "f.txt" {
			foundConflict = true
		}
	}
	if !foundConflict {
		t.Fatalf("expected f.txt in conflicts, got %v", result.Conflicts)
	}

	// Verify our, their, base versions were written
	if _, err := os.Stat(filepath.Join(dir, "f.txt")); os.IsNotExist(err) {
		t.Fatal("expected f.txt (ours version) to exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "f.txt.theirs")); os.IsNotExist(err) {
		t.Fatal("expected f.txt.theirs conflict file")
	}
	if _, err := os.Stat(filepath.Join(dir, "f.txt.base")); os.IsNotExist(err) {
		t.Fatal("expected f.txt.base conflict file")
	}
}

func TestMergeSameFileUnchanged(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// base
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("shared"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "base")

	repo.CreateBranch("feature")

	// Change f.txt same way on both branches
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("same change"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "main change")

	repo.SwitchBranch("feature")
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("same change"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "feature change")

	// Merge — should have no conflict (same change)
	repo.SwitchBranch("main")
	result, err := repo.Merge("feature")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) > 0 {
		t.Fatalf("expected no conflicts for same change, got %v", result.Conflicts)
	}
}

func TestMergeNoCommits(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = repo.Merge("main")
	if err == nil {
		t.Fatal("expected error for no commits")
	}
}
