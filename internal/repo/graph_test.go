package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
)

func TestWalkGraphLinear(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)
	repo.AddFile(filepath.Join(dir, "a.txt"))
	repo.WriteCommit("Test", "first")

	ioutil.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0644)
	repo.AddFile(filepath.Join(dir, "b.txt"))
	repo.WriteCommit("Test", "second")

	commits, err := repo.WalkGraph(10)
	if err != nil {
		t.Fatal(err)
	}

	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}

	if commits[0].Commit.Message != "second" {
		t.Fatalf("expected newest first, got %s", commits[0].Commit.Message)
	}
}

func TestWalkGraphMerge(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// base commit
	ioutil.WriteFile(filepath.Join(dir, "base.txt"), []byte("base"), 0644)
	repo.AddFile(filepath.Join(dir, "base.txt"))
	_, err = repo.WriteCommit("Test", "base")
	if err != nil {
		t.Fatal(err)
	}

	// branch: feature
	repo.CreateBranch("feature")

	// commit on main
	ioutil.WriteFile(filepath.Join(dir, "main.txt"), []byte("main"), 0644)
	repo.AddFile(filepath.Join(dir, "main.txt"))
	mainHash, err := repo.WriteCommit("Test", "main work")
	if err != nil {
		t.Fatal(err)
	}

	// switch to feature, commit
	repo.SwitchBranch("feature")
	ioutil.WriteFile(filepath.Join(dir, "feat.txt"), []byte("feat"), 0644)
	repo.AddFile(filepath.Join(dir, "feat.txt"))
	featHash, err := repo.WriteCommit("Test", "feature work")
	if err != nil {
		t.Fatal(err)
	}

	// Create a merge commit manually: switch to main, write a merge commit with two parents
	repo.SwitchBranch("main")

	// Manually create merge commit with two parents
	mergeCommit := Commit{
		Tree:    mainHash, // reuse main's tree for simplicity
		Parents: []core.Hash{mainHash, featHash},
		Author:  "Test",
		Message: "merge feature",
	}

	content, err := core.SerializeJSON(mergeCommit)
	if err != nil {
		t.Fatal(err)
	}

	mergeHash, err := repo.StoreObject(core.ObjectCommit, content)
	if err != nil {
		t.Fatal(err)
	}

	// Update main ref to merge commit
	repo.WriteRef("refs/heads/main", mergeHash.String())
	repo.SetHEAD(mergeHash.String())

	// Now walk graph — should see 4 commits
	commits, err := repo.WalkGraph(10)
	if err != nil {
		t.Fatal(err)
	}

	if len(commits) != 4 {
		t.Fatalf("expected 4 commits, got %d", len(commits))
	}

	// Verify merge commit is first (newest)
	if commits[0].Commit.Message != "merge feature" {
		t.Fatalf("expected merge commit first, got %s", commits[0].Commit.Message)
	}

	// Verify merge commit has two parents
	if len(commits[0].Commit.Parents) != 2 {
		t.Fatalf("expected 2 parents for merge commit, got %d", len(commits[0].Commit.Parents))
	}

	// Verify all commits are present
	messages := make(map[string]bool)
	for _, c := range commits {
		messages[c.Commit.Message] = true
	}
	for _, m := range []string{"merge feature", "main work", "feature work", "base"} {
		if !messages[m] {
			t.Fatalf("missing commit: %s", m)
		}
	}

	// Verify no parents reference zero hash
	for _, c := range commits {
		for _, p := range c.Commit.Parents {
			if p.IsZero() {
				t.Fatalf("commit %s has zero parent", c.Hash)
			}
		}
	}
}

func TestWalkGraphNoCommits(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	commits, err := repo.WalkGraph(10)
	if err != nil {
		t.Fatal(err)
	}
	if commits != nil {
		t.Fatal("expected nil for no commits")
	}
}

func TestWalkGraphLimit(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		f := filepath.Join(dir, "f.txt")
		ioutil.WriteFile(f, []byte(string(rune('0'+i))), 0644)
		repo.AddFile(f)
		repo.WriteCommit("Test", "commit")
	}

	commits, err := repo.WalkGraph(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(commits))
	}
}

func TestRenderGraphLinear(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)
	repo.AddFile(filepath.Join(dir, "a.txt"))
	repo.WriteCommit("Test", "first commit")

	ioutil.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0644)
	repo.AddFile(filepath.Join(dir, "b.txt"))
	repo.WriteCommit("Test", "second commit")

	commits, err := repo.WalkGraph(10)
	if err != nil {
		t.Fatal(err)
	}

	lines := RenderGraph(commits)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	// Linear output should start with "*" for each line
	for i, line := range lines {
		if !strings.HasPrefix(line, "* ") {
			t.Fatalf("line %d: expected prefix '* ', got %q", i, line)
		}
	}

	// Newest first
	if !strings.Contains(lines[0], "second") {
		t.Fatalf("expected newest first: %s", lines[0])
	}
}

func TestRenderGraphWithMerge(t *testing.T) {
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
	baseHash, err := repo.WriteCommit("Test", "base")
	if err != nil {
		t.Fatal(err)
	}

	// branch feature
	repo.CreateBranch("feature")

	// main side: modify f.txt
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("main change"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	mainHash, err := repo.WriteCommit("Test", "main work")
	if err != nil {
		t.Fatal(err)
	}

	// feature side: modify f.txt differently
	repo.SwitchBranch("feature")
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("feature change"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	featHash, err := repo.WriteCommit("Test", "feature work")
	if err != nil {
		t.Fatal(err)
	}

	// Manually create merge commit on main
	repo.SwitchBranch("main")

	mergeCommit := Commit{
		Tree:    baseHash, // simplified: just reuse base tree
		Parents: []core.Hash{mainHash, featHash},
		Author:  "Test",
		Message: "merge",
	}
	content, err := core.SerializeJSON(mergeCommit)
	if err != nil {
		t.Fatal(err)
	}
	mergeHash, err := repo.StoreObject(core.ObjectCommit, content)
	if err != nil {
		t.Fatal(err)
	}
	repo.WriteRef("refs/heads/main", mergeHash.String())
	repo.SetHEAD(mergeHash.String())

	commits, err := repo.WalkGraph(10)
	if err != nil {
		t.Fatal(err)
	}

	lines := RenderGraph(commits)

	// Should have 4 lines for 4 commits
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(lines))
	}

	// First line (merge commit) should have graph prefix with just "*"
	if !strings.HasPrefix(lines[0], "* ") {
		t.Fatalf("expected merge commit prefix '*', got %q", lines[0])
	}

	// At least one line should have "|" showing branching
	hasPipe := false
	for _, line := range lines {
		if strings.Contains(line, "|") {
			hasPipe = true
			break
		}
	}
	if !hasPipe {
		t.Fatal("expected some lines with '|' for branch graph")
	}

	// Verify all commit messages are present
	found := make(map[string]bool)
	for _, line := range lines {
		for _, m := range []string{"merge", "main work", "feature work", "base"} {
			if strings.Contains(line, m) {
				found[m] = true
			}
		}
	}
	for _, m := range []string{"merge", "main work", "feature work", "base"} {
		if !found[m] {
			t.Fatalf("missing commit message in output: %s", m)
		}
	}
}
