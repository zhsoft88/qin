package repo

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
)

func TestIsSubmoduleMode(t *testing.T) {
	if !IsSubmoduleMode(SubmoduleMode) {
		t.Fatal("expected SubmoduleMode to match")
	}
	if IsSubmoduleMode(0644) {
		t.Fatal("expected 0644 to NOT be submodule mode")
	}
	if IsSubmoduleMode(0755) {
		t.Fatal("expected 0755 to NOT be submodule mode")
	}
}

func TestLoadLoModulesMissing(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-modules-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	mods, err := LoadLoModules(r)
	if err != nil {
		t.Fatal(err)
	}
	if mods == nil {
		t.Fatal("expected non-nil LoModules")
	}
	if len(mods.Submodules) != 0 {
		t.Fatalf("expected empty submodules, got %d", len(mods.Submodules))
	}
}

func TestSaveAndLoadLoModules(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-modules-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	mods := &LoModules{
		Submodules: map[string]SubmoduleDef{
			"lib/foo": {URL: "https://github.com/user/repo"},
		},
	}

	if err := SaveLoModules(r, mods); err != nil {
		t.Fatal(err)
	}

	// Verify file exists and is valid JSON
	data, err := ioutil.ReadFile(filepath.Join(dir, ".lomodules"))
	if err != nil {
		t.Fatal(err)
	}

	var parsed LoModules
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}

	if len(parsed.Submodules) != 1 {
		t.Fatalf("expected 1 submodule, got %d", len(parsed.Submodules))
	}
	def, ok := parsed.Submodules["lib/foo"]
	if !ok {
		t.Fatal("expected lib/foo in submodules")
	}
	if def.URL != "https://github.com/user/repo" {
		t.Fatalf("expected URL https://github.com/user/repo, got %s", def.URL)
	}

	// Reload via LoadLoModules
	loaded, err := LoadLoModules(r)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Submodules["lib/foo"].URL != "https://github.com/user/repo" {
		t.Fatal("reloaded modules mismatch")
	}
}

func TestAddSubmoduleIndexEntry(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-submodule-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a mini repo to use as submodule
	subDir, err := ioutil.TempDir("", "lo-sub-repo-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(subDir)

	sub, err := Init(subDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(subDir, "file.txt"), []byte("submodule content"), 0644); err != nil {
		t.Fatal(err)
	}
	sub.AddFile(filepath.Join(subDir, "file.txt"))
	subHash, err := sub.WriteCommit("Test", "submodule commit")
	if err != nil {
		t.Fatal(err)
	}

	// Add submodule via index-level method (skipping the clone step)
	if err := r.AddSubmodule("lib/mysub", subHash.String()); err != nil {
		t.Fatal(err)
	}

	// Verify index entry
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}

	entry, ok := idx.Entries["lib/mysub"]
	if !ok {
		t.Fatal("expected lib/mysub in index")
	}
	if entry.Mode != SubmoduleMode {
		t.Fatalf("expected SubmoduleMode %o, got %o", SubmoduleMode, entry.Mode)
	}
	if entry.Hash != subHash {
		t.Fatalf("expected hash %s, got %s", subHash.Short(), entry.Hash.Short())
	}
	if entry.Size != 0 {
		t.Fatalf("expected size 0 for submodule, got %d", entry.Size)
	}
}

func TestBuildTreeWithSubmodule(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-tree-sub-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Add a regular file
	if err := ioutil.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFile(filepath.Join(dir, "readme.txt")); err != nil {
		t.Fatal(err)
	}

	// Add a submodule entry
	subHash := core.HashFromBytes([]byte("fake-commit-hash-for-testing"))
	if err := r.AddSubmodule("lib/sub", subHash.String()); err != nil {
		t.Fatal(err)
	}

	// Build tree
	tree, err := r.BuildTree()
	if err != nil {
		t.Fatal(err)
	}

	// Check both entries exist with correct modes
	var foundFile, foundSub bool
	for _, e := range tree.Entries {
		if e.Name == "readme.txt" {
			foundFile = true
			if IsSubmoduleMode(e.Mode) {
				t.Fatal("readme.txt should not be submodule mode")
			}
		}
		if e.Name == "lib/sub" {
			foundSub = true
			if !IsSubmoduleMode(e.Mode) {
				t.Fatal("lib/sub should have SubmoduleMode")
			}
			if e.Hash != subHash {
				t.Fatal("lib/sub hash mismatch")
			}
		}
	}
	if !foundFile {
		t.Fatal("expected readme.txt in tree")
	}
	if !foundSub {
		t.Fatal("expected lib/sub in tree")
	}

	// Write tree and verify it stores correctly
	treeHash, err := r.WriteTree()
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := r.LoadTree(treeHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(loaded.Entries))
	}
}

func TestRestoreCommitWithSubmodule(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-restore-sub-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Add regular file
	if err := ioutil.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFile(filepath.Join(dir, "readme.txt")); err != nil {
		t.Fatal(err)
	}

	// Add submodule entry
	subHash := core.HashFromBytes([]byte("fake-commit-hash-for-testing"))
	if err := r.AddSubmodule("lib/sub", subHash.String()); err != nil {
		t.Fatal(err)
	}

	// Commit
	commitHash, err := r.WriteCommit("Test", "initial commit with submodule")
	if err != nil {
		t.Fatal(err)
	}

	// Verify the submodule is listed in files
	files, err := r.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := files["lib/sub"]
	if !ok {
		t.Fatal("expected lib/sub in files after commit")
	}
	if entry.Mode != SubmoduleMode {
		t.Fatalf("expected SubmoduleMode, got %o", entry.Mode)
	}
	if entry.Hash != subHash {
		t.Fatal("submodule hash mismatch after commit")
	}

	// Verify the submodule directory was created
	subDir := filepath.Join(dir, "lib", "sub")
	if _, err := os.Stat(subDir); !os.IsNotExist(err) {
		t.Fatal("expected submodule dir to NOT be created (commit doesn't call restoreCommit)")
	}

	// Restore the commit explicitly
	if err := r.restoreCommit(commitHash); err != nil {
		t.Fatal(err)
	}

	// Now the submodule directory should exist
	if _, err := os.Stat(subDir); os.IsNotExist(err) {
		t.Fatal("expected submodule dir to exist after restoreCommit")
	}

	// Verify it's a directory, not a file
	fi, err := os.Stat(subDir)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() {
		t.Fatal("expected submodule path to be a directory")
	}
}

func TestCollectTreeRecSkipsSubmodule(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-collect-sub-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create another repo as the "remote" to test collectTreeRec
	remoteDir, err := ioutil.TempDir("", "lo-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	remote, err := Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}

	// Add a regular file to remote
	if err := ioutil.WriteFile(filepath.Join(remoteDir, "readme.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := remote.AddFile(filepath.Join(remoteDir, "readme.txt")); err != nil {
		t.Fatal(err)
	}

	// Add submodule-like entry (we reuse a random hash, but the key is the mode)
	subHash := core.HashFromBytes([]byte("submodule-hash-test"))
	if err := remote.AddSubmodule("lib/sub", subHash.String()); err != nil {
		t.Fatal(err)
	}

	// Commit
	commitHash, err := remote.WriteCommit("Test", "commit with submodule")
	if err != nil {
		t.Fatal(err)
	}

	// Now collect objects from remote into r (simulating fetch)
	set, err := remote.collectObjects(r, commitHash, false)
	if err != nil {
		t.Fatal(err)
	}

	// The submodule hash should NOT be in the set (skipped by collectTreeRec)
	if set[subHash] {
		t.Fatal("submodule hash should NOT be collected - it lives in submodule repo")
	}

	// The regular file's content hash should be in the set
	commit, err := remote.LoadCommit(commitHash)
	if err != nil {
		t.Fatal(err)
	}
	if !set[commitHash] {
		t.Fatal("commit should be in collected set")
	}
	if !set[commit.Tree] {
		t.Fatal("tree should be in collected set")
	}
}

func TestSubmoduleCollectTreeRecSkipsEntry(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-collect-sub2-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Store real blob and tree objects
	fileHash, err := r.StoreObject(core.ObjectBlob, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}

	subHash := core.HashFromBytes([]byte("submodule-hash"))

	tree := &Tree{
		Entries: []TreeEntry{
			{Name: "readme.txt", Hash: fileHash, Size: 5, Mode: 0644},
			{Name: "lib/sub", Hash: subHash, Size: 0, Mode: SubmoduleMode},
		},
	}
	treeContent, err := core.SerializeJSON(tree)
	if err != nil {
		t.Fatal(err)
	}
	treeHash, err := r.StoreObject(core.ObjectTree, treeContent)
	if err != nil {
		t.Fatal(err)
	}

	set := make(map[core.Hash]bool)
	boundary := &Repository{}

	if err := r.collectTreeRec(boundary, set, treeHash, false); err != nil {
		t.Fatal(err)
	}

	if set[subHash] {
		t.Fatal("submodule hash should have been skipped by collectTreeRec")
	}

	if !set[fileHash] {
		t.Fatal("regular file hash should be in collected set")
	}
}
