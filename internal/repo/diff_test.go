package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
)

func TestDiffCommits(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// First commit
	ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("file a"), 0644)
	ioutil.WriteFile(filepath.Join(dir, "b.txt"), []byte("file b"), 0644)
	repo.AddFile(filepath.Join(dir, "a.txt"))
	repo.AddFile(filepath.Join(dir, "b.txt"))
	h1, err := repo.WriteCommit("Test", "first")
	if err != nil {
		t.Fatal(err)
	}

	// Modify a.txt, add c.txt, remove b.txt
	ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("modified a"), 0644)
	ioutil.WriteFile(filepath.Join(dir, "c.txt"), []byte("file c"), 0644)
	repo.AddFile(filepath.Join(dir, "a.txt"))
	repo.AddFile(filepath.Join(dir, "c.txt"))
	if err := repo.RemoveFile(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	h2, err := repo.WriteCommit("Test", "second")
	if err != nil {
		t.Fatal(err)
	}

	diff, err := repo.DiffCommits(h1, h2)
	if err != nil {
		t.Fatal(err)
	}

	if len(diff.Files) != 3 {
		t.Fatalf("expected 3 files changed, got %d", len(diff.Files))
	}

	changes := make(map[string]DiffType)
	for _, f := range diff.Files {
		changes[f.Name] = f.Type
	}

	if changes["a.txt"] != DiffModified {
		t.Fatal("expected a.txt to be modified")
	}
	if changes["b.txt"] != DiffDeleted {
		t.Fatal("expected b.txt to be deleted")
	}
	if changes["c.txt"] != DiffAdded {
		t.Fatal("expected c.txt to be added")
	}
}

func TestDiffIndexVsHEAD(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Commit
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("original"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "first")

	// Stage a modification
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("modified"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))

	diff, err := repo.DiffIndex()
	if err != nil {
		t.Fatal(err)
	}

	if len(diff.Files) != 1 {
		t.Fatalf("expected 1 changed file, got %d", len(diff.Files))
	}
	if diff.Files[0].Type != DiffModified {
		t.Fatalf("expected modified, got %s", diff.Files[0].Type)
	}
}

func TestDiffWorking(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Commit a file
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("original"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "first")

	// Modify without staging
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("modified on disk"), 0644)

	diff, err := repo.DiffWorking()
	if err != nil {
		t.Fatal(err)
	}

	if len(diff.Files) != 1 {
		t.Fatalf("expected 1 changed file, got %d", len(diff.Files))
	}
	if diff.Files[0].Type != DiffModified {
		t.Fatalf("expected modified, got %s", diff.Files[0].Type)
	}
}

func TestDiffNoChanges(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("content"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	h, err := repo.WriteCommit("Test", "first")
	if err != nil {
		t.Fatal(err)
	}

	diff, err := repo.DiffCommits(h, h)
	if err != nil {
		t.Fatal(err)
	}

	if len(diff.Files) != 0 {
		t.Fatalf("expected 0 changes for same commit, got %d", len(diff.Files))
	}
}

func TestDiffRender(t *testing.T) {
	hA := core.HashFromBytes([]byte("a"))
	hB := core.HashFromBytes([]byte("b"))
	diff := &Diff{
		Files: []DiffFile{
			{Name: "new.txt", NewSize: 100, Type: DiffAdded},
			{Name: "old.txt", OldSize: 200, Type: DiffDeleted},
			{Name: "mod.txt", OldSize: 50, NewSize: 75, OldHash: hA, NewHash: hB, Type: DiffModified},
		},
	}

	output := diff.Render()
	if !strings.Contains(output, "+") || !strings.Contains(output, "-") || !strings.Contains(output, "~") {
		t.Fatal("render should contain +, -, ~ markers")
	}
}

func TestDiffContent(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// First commit
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("line1\nline2\nline3\n"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "first")

	// Modify: change line2, add line4
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("line1\nmodified\nline3\nline4\n"), 0644)

	diff, err := repo.DiffWorking()
	if err != nil {
		t.Fatal(err)
	}

	if len(diff.Files) != 1 {
		t.Fatalf("expected 1 changed file, got %d", len(diff.Files))
	}

	output := diff.Render()
	f := diff.Files[0]
	if len(f.OldContent) == 0 || len(f.NewContent) == 0 {
		t.Fatal("expected old/new content to be populated")
	}
	if !strings.Contains(output, "- line2") {
		t.Fatal("expected '- line2' in content diff output")
	}
	if !strings.Contains(output, "+ modified") {
		t.Fatal("expected '+ modified' in content diff output")
	}
	if !strings.Contains(output, "  line1") {
		t.Fatal("expected '  line1' (unchanged line) in content diff output")
	}
	if !strings.Contains(output, "  line3") {
		t.Fatal("expected '  line3' (unchanged line) in content diff output")
	}
	if !strings.Contains(output, "+ line4") {
		t.Fatal("expected '+ line4' (new line) in content diff output")
	}
}
