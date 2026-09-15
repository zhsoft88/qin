package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
)

func TestBuildTreeFromIndex(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	files := []string{"a.txt", "b.txt", "c.txt"}
	for _, name := range files {
		fullPath := filepath.Join(dir, name)
		if err := ioutil.WriteFile(fullPath, []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
		if err := repo.AddFile(fullPath); err != nil {
			t.Fatal(err)
		}
	}

	tree, err := repo.BuildTree()
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(tree.Entries))
	}

	// Verify sorted order
	for i := 1; i < len(tree.Entries); i++ {
		if tree.Entries[i].Name <= tree.Entries[i-1].Name {
			t.Fatal("entries not sorted")
		}
	}
}

func TestWriteAndLoadTree(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(dir, "data.txt")
	if err := ioutil.WriteFile(testFile, []byte("tree test"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddFile(testFile); err != nil {
		t.Fatal(err)
	}

	treeHash, err := repo.WriteTree()
	if err != nil {
		t.Fatal(err)
	}

	if treeHash.IsZero() {
		t.Fatal("expected non-zero tree hash")
	}

	loaded, err := repo.LoadTree(treeHash)
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(loaded.Entries))
	}
	if loaded.Entries[0].Name != "data.txt" {
		t.Fatalf("expected data.txt, got %s", loaded.Entries[0].Name)
	}
}

func TestBuildTreeEmptyIndex(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repo.BuildTree(); err == nil {
		t.Fatal("expected error for empty index")
	}
}

func TestWriteCommit(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(dir, "file.txt")
	if err := ioutil.WriteFile(testFile, []byte("commit test"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddFile(testFile); err != nil {
		t.Fatal(err)
	}

	commitHash, err := repo.WriteCommit("Test Author <test@test>", "initial commit")
	if err != nil {
		t.Fatal(err)
	}

	if commitHash.IsZero() {
		t.Fatal("expected non-zero commit hash")
	}

	loaded, err := repo.LoadCommit(commitHash)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Message != "initial commit" {
		t.Fatalf("expected 'initial commit', got '%s'", loaded.Message)
	}
	if loaded.Author != "Test Author <test@test>" {
		t.Fatalf("expected 'Test Author <test@test>', got '%s'", loaded.Author)
	}
	if loaded.Tree.IsZero() {
		t.Fatal("expected non-zero tree hash in commit")
	}
	if len(loaded.Parents) != 0 {
		t.Fatalf("expected 0 parents for first commit, got %d", len(loaded.Parents))
	}

	// Verify HEAD was updated
	resolved, err := repo.ResolveHEAD()
	if err != nil {
		t.Fatal(err)
	}
	if resolved != commitHash.String() {
		t.Fatalf("HEAD points to %s, expected %s", resolved, commitHash.String())
	}
}

func TestWriteMultipleCommits(t *testing.T) {
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
	f1 := filepath.Join(dir, "a.txt")
	if err := ioutil.WriteFile(f1, []byte("file a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddFile(f1); err != nil {
		t.Fatal(err)
	}
	h1, err := repo.WriteCommit("Author", "first")
	if err != nil {
		t.Fatal(err)
	}

	// Verify parents
	c1, _ := repo.LoadCommit(h1)
	if len(c1.Parents) != 0 {
		t.Fatalf("first commit: expected 0 parents, got %d", len(c1.Parents))
	}

	// Second commit
	f2 := filepath.Join(dir, "b.txt")
	if err := ioutil.WriteFile(f2, []byte("file b"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddFile(f2); err != nil {
		t.Fatal(err)
	}
	h2, err := repo.WriteCommit("Author", "second")
	if err != nil {
		t.Fatal(err)
	}

	c2, err := repo.LoadCommit(h2)
	if err != nil {
		t.Fatal(err)
	}
	if len(c2.Parents) != 1 {
		t.Fatalf("second commit: expected 1 parent, got %d", len(c2.Parents))
	}
	if c2.Parents[0] != h1 {
		t.Fatalf("second commit parent should be first commit hash")
	}
}

func TestWriteCommitNothingStaged(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repo.WriteCommit("Author", "empty"); err == nil {
		t.Fatal("expected error when nothing staged")
	}
}

// TestHostileTreeCannotEscapeRepo pins the guard in LoadTree. A tree arrives
// from a remote as an ordinary object, and every consumer joins its entry
// names onto the working tree root, so a name with "../" is remote input that
// would otherwise be written outside the repository.
func TestHostileTreeCannotEscapeRepo(t *testing.T) {
	for _, name := range []string{
		"../victim.txt",
		"../../victim.txt",
		"a/../../victim.txt",
		`..\..\victim.txt`,
		"/tmp/victim.txt",
		"C:victim.txt",
		"..",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			base, err := ioutil.TempDir("", "lo-test-*")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(base)

			repoDir := filepath.Join(base, "repo")
			if err := os.MkdirAll(repoDir, 0755); err != nil {
				t.Fatal(err)
			}
			r, err := Init(repoDir)
			if err != nil {
				t.Fatal(err)
			}

			victim := filepath.Join(base, "victim.txt")
			const original = "ORIGINAL"
			if err := ioutil.WriteFile(victim, []byte(original), 0644); err != nil {
				t.Fatal(err)
			}

			// Hand-craft the tree a hostile remote could serve.
			payload := []byte("PWNED")
			blob, err := r.StoreObject(core.ObjectBlob, payload)
			if err != nil {
				t.Fatal(err)
			}
			tree := &Tree{Entries: []TreeEntry{
				{Name: name, Hash: blob, Size: int64(len(payload)), Mode: 0644},
			}}
			treeContent, err := core.SerializeJSON(tree)
			if err != nil {
				t.Fatal(err)
			}
			treeHash, err := r.StoreObject(core.ObjectTree, treeContent)
			if err != nil {
				t.Fatal(err)
			}
			commit := &Commit{Tree: treeHash, Author: "attacker", Message: "hostile"}
			commitContent, err := core.SerializeJSON(commit)
			if err != nil {
				t.Fatal(err)
			}
			commitHash, err := r.StoreObject(core.ObjectCommit, commitContent)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.WriteRef("refs/heads/main", commitHash.String()); err != nil {
				t.Fatal(err)
			}
			if err := r.SetHEAD("ref: refs/heads/main"); err != nil {
				t.Fatal(err)
			}

			// Checkout must refuse rather than write.
			if err := r.SwitchBranch("main"); err == nil {
				t.Fatal("expected checkout to reject a tree path outside the repo")
			}
			got, err := ioutil.ReadFile(victim)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != original {
				t.Fatalf("file outside the repo was overwritten: got %q", got)
			}
		})
	}
}

// TestTreePathThroughSymlinkedParentCannotEscapeRepo is the regression test
// for a tree entry that is a well-formed repository-relative path but resolves
// through a symlink to a directory outside the repository.
//
// Path validation alone cannot catch this: "link/pwned.txt" contains no "..",
// so safeRepoPath accepts it, and the tree is loaded and walked normally. Only
// the check at write time — where the existing working tree is visible — can
// see that "link" is a symlink pointing elsewhere.
func TestTreePathThroughSymlinkedParentCannotEscapeRepo(t *testing.T) {
	base, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	repoDir := filepath.Join(base, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	r, err := Init(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "pwned.txt")
	const original = "ORIGINAL"
	if err := ioutil.WriteFile(victim, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// The symlink is already in the working tree — it was checked out earlier,
	// or created by the user. The tree below only has to name a path through
	// it, which is all a hostile remote can do.
	if err := os.Symlink(outside, filepath.Join(repoDir, "link")); err != nil {
		t.Fatal(err)
	}

	hostileCommit(t, r, []hostileEntry{
		{name: "link/pwned.txt", mode: 0644, content: "PWNED"},
	})

	if err := r.SwitchBranch("main"); err == nil {
		t.Fatal("expected checkout to reject a write through a symlinked parent")
	}
	got, err := ioutil.ReadFile(victim)
	if err != nil {
		t.Fatalf("file outside the repo disappeared: %v", err)
	}
	if string(got) != original {
		t.Fatalf("escaped: outside file is now %q, want %q", got, original)
	}
}

// TestSymlinkAndPathThroughItCannotEscapeRepo covers the same escape with the
// symlink supplied by the tree itself rather than the working tree.
//
// Map iteration order is unspecified, so the checkout may either create "link"
// first — after which "link/pwned.txt" is rejected and the checkout fails — or
// see "link/pwned.txt" first and make "link" an ordinary directory, after which
// the symlink entry cannot be written and is skipped. Both outcomes are
// acceptable; what must never happen is the write landing outside the repo.
func TestSymlinkAndPathThroughItCannotEscapeRepo(t *testing.T) {
	base, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	repoDir := filepath.Join(base, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	r, err := Init(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "pwned.txt")
	const original = "ORIGINAL"
	if err := ioutil.WriteFile(victim, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	hostileCommit(t, r, []hostileEntry{
		{name: "link", mode: SymlinkMode, content: outside},
		{name: "link/pwned.txt", mode: 0644, content: "PWNED"},
	})

	// The error is allowed to be nil here — see the comment above.
	r.SwitchBranch("main")

	got, err := ioutil.ReadFile(victim)
	if err != nil {
		t.Fatalf("file outside the repo disappeared: %v", err)
	}
	if string(got) != original {
		t.Fatalf("escaped: outside file is now %q, want %q", got, original)
	}
}

// hostileEntry is a tree entry named by its content, for hostileCommit.
type hostileEntry struct {
	name    string
	mode    uint32
	content string
}

// hostileCommit stores each entry's content as a blob, builds a tree from
// them, and points refs/heads/main at a commit for it — the shape a hostile
// remote would serve.
func hostileCommit(t *testing.T, r *Repository, entries []hostileEntry) {
	t.Helper()
	tree := &Tree{Entries: make([]TreeEntry, 0, len(entries))}
	for _, e := range entries {
		blob, err := r.StoreObject(core.ObjectBlob, []byte(e.content))
		if err != nil {
			t.Fatal(err)
		}
		tree.Entries = append(tree.Entries, TreeEntry{
			Name: e.name,
			Hash: blob,
			Size: int64(len(e.content)),
			Mode: e.mode,
		})
	}

	treeContent, err := core.SerializeJSON(tree)
	if err != nil {
		t.Fatal(err)
	}
	treeHash, err := r.StoreObject(core.ObjectTree, treeContent)
	if err != nil {
		t.Fatal(err)
	}
	commit := &Commit{Tree: treeHash, Author: "attacker", Message: "hostile"}
	commitContent, err := core.SerializeJSON(commit)
	if err != nil {
		t.Fatal(err)
	}
	commitHash, err := r.StoreObject(core.ObjectCommit, commitContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.WriteRef("refs/heads/main", commitHash.String()); err != nil {
		t.Fatal(err)
	}
	if err := r.SetHEAD("ref: refs/heads/main"); err != nil {
		t.Fatal(err)
	}
}

// TestTreeEntryReplacingSymlinkCannotEscapeRepo is the end-to-end form of the
// final-component case: the tree names a regular file at a path where the
// working tree already holds a symlink.
//
// The entry's name is an ordinary one-component path, so nothing about it
// looks hostile — the escape depends entirely on what is already on disk.
func TestTreeEntryReplacingSymlinkCannotEscapeRepo(t *testing.T) {
	base, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	repoDir := filepath.Join(base, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	r, err := Init(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "victim.txt")
	const original = "ORIGINAL"
	if err := ioutil.WriteFile(victim, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(repoDir, "link")); err != nil {
		t.Fatal(err)
	}

	hostileCommit(t, r, []hostileEntry{
		{name: "link", mode: 0644, content: "PWNED"},
	})

	if err := r.SwitchBranch("main"); err != nil {
		t.Fatalf("switch failed: %v", err)
	}

	got, err := ioutil.ReadFile(victim)
	if err != nil {
		t.Fatalf("file outside the repo disappeared: %v", err)
	}
	if string(got) != original {
		t.Fatalf("escaped: outside file is now %q, want %q", got, original)
	}
	content, err := ioutil.ReadFile(filepath.Join(repoDir, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "PWNED" {
		t.Fatalf("entry not written: got %q, want %q", content, "PWNED")
	}
}
