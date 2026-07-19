package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

func TestSaveLoadListRemoveRemote(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Save
	if err := r.SaveRemote("origin", "/tmp/upstream"); err != nil {
		t.Fatal(err)
	}

	// Load
	url, err := r.LoadRemote("origin")
	if err != nil {
		t.Fatal(err)
	}
	if url != "/tmp/upstream" {
		t.Fatalf("expected /tmp/upstream, got %s", url)
	}

	// List
	remotes, err := r.ListRemotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(remotes) != 1 || remotes[0].Name != "origin" {
		t.Fatalf("expected 1 remote (origin), got %d", len(remotes))
	}

	// Remove
	if err := r.RemoveRemote("origin"); err != nil {
		t.Fatal(err)
	}
	remotes, _ = r.ListRemotes()
	if len(remotes) != 0 {
		t.Fatal("expected no remotes after remove")
	}
}

func TestFetchFirstTime(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "lo-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "lo-local-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localDir)

	// Set up remote with a commit
	remote, err := Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(remoteDir, "f.txt"), []byte("remote content"), 0644)
	remote.AddFile(filepath.Join(remoteDir, "f.txt"))
	hashRemote, err := remote.WriteCommit("Test", "remote commit")
	if err != nil {
		t.Fatal(err)
	}

	// Set up local repo
	local, err := Init(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.SaveRemote("origin", remoteDir); err != nil {
		t.Fatal(err)
	}

	// Fetch
	if err := local.Fetch("origin"); err != nil {
		t.Fatal(err)
	}

	// Verify remote-tracking ref exists
	trackingRef := "refs/remotes/origin/main"
	hashStr, err := local.ReadRef(trackingRef)
	if err != nil {
		t.Fatalf("expected remote-tracking ref: %v", err)
	}
	if hashStr != hashRemote.String() {
		t.Fatalf("expected tracking ref %s, got %s", hashRemote.Short(), hashStr[:8])
	}

	// Verify commit object was copied
	if !local.HasObject(hashRemote) {
		t.Fatal("commit object not found in local after fetch")
	}
}

func TestFetchUpToDate(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "lo-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "lo-local-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localDir)

	// Set up remote
	remote, err := Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(remoteDir, "f.txt"), []byte("data"), 0644)
	remote.AddFile(filepath.Join(remoteDir, "f.txt"))
	remote.WriteCommit("Test", "commit")

	// Set up local and fetch first time
	local, err := Init(localDir)
	if err != nil {
		t.Fatal(err)
	}
	local.SaveRemote("origin", remoteDir)
	if err := local.Fetch("origin"); err != nil {
		t.Fatal(err)
	}

	// Fetch again — should be a no-op
	if err := local.Fetch("origin"); err != nil {
		t.Fatal(err)
	}
}

func TestPushFirstTime(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "lo-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "lo-local-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localDir)

	// Init bare remote (no commits)
	_, err = Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}

	// Init local with commits
	local, err := Init(localDir)
	if err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(localDir, "f.txt"), []byte("local data"), 0644)
	local.AddFile(filepath.Join(localDir, "f.txt"))
	hashLocal, err := local.WriteCommit("Test", "local commit")
	if err != nil {
		t.Fatal(err)
	}

	if err := local.SaveRemote("origin", remoteDir); err != nil {
		t.Fatal(err)
	}

	// Push
	if err := local.Push("origin"); err != nil {
		t.Fatal(err)
	}

	// Open remote and verify ref + objects
	remote, err := Open(remoteDir)
	if err != nil {
		t.Fatal(err)
	}

	hashStr, err := remote.ReadRef("refs/heads/main")
	if err != nil {
		t.Fatalf("remote should have main ref after push: %v", err)
	}
	if hashStr != hashLocal.String() {
		t.Fatalf("remote HEAD mismatch")
	}
	if !remote.HasObject(hashLocal) {
		t.Fatal("commit object not found in remote after push")
	}
}

func TestPushIncremental(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "lo-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "lo-local-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localDir)

	// Remote with one commit
	remote, err := Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(remoteDir, "f.txt"), []byte("base"), 0644)
	remote.AddFile(filepath.Join(remoteDir, "f.txt"))
	hBase, err := remote.WriteCommit("Test", "base")
	if err != nil {
		t.Fatal(err)
	}

	// Local clones via fetch + creates local branch
	local, err := Init(localDir)
	if err != nil {
		t.Fatal(err)
	}
	local.SaveRemote("origin", remoteDir)
	if err := local.Fetch("origin"); err != nil {
		t.Fatal(err)
	}
	local.WriteRef("refs/heads/main", hBase.String())
	local.SetHEAD("ref: refs/heads/main")
	local.restoreCommit(hBase)

	// Add new commit on local
	ioutil.WriteFile(filepath.Join(localDir, "g.txt"), []byte("new"), 0644)
	local.AddFile(filepath.Join(localDir, "g.txt"))
	hLocal, err := local.WriteCommit("Test", "new commit")
	if err != nil {
		t.Fatal(err)
	}

	// Push
	if err := local.Push("origin"); err != nil {
		t.Fatal(err)
	}

	// Verify remote has both commits
	if !remote.HasObject(hLocal) {
		t.Fatal("new commit not found in remote after push")
	}
	if !remote.HasObject(hBase) {
		t.Fatal("base commit should still be in remote")
	}

	// Remote branch should point to new commit
	hashStr, _ := remote.ReadRef("refs/heads/main")
	if hashStr != hLocal.String() {
		t.Fatal("remote main should point to pushed commit")
	}
}

func TestPullFastForward(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "lo-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "lo-local-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localDir)

	// Remote: base A, then commit B on main
	remote, err := Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(remoteDir, "f.txt"), []byte("a"), 0644)
	remote.AddFile(filepath.Join(remoteDir, "f.txt"))
	_, err = remote.WriteCommit("Test", "A")
	if err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(remoteDir, "f.txt"), []byte("b"), 0644)
	remote.AddFile(filepath.Join(remoteDir, "f.txt"))
	hB, err := remote.WriteCommit("Test", "B")
	if err != nil {
		t.Fatal(err)
	}

	// Local: fetch, then create local main at A (behind remote)
	local, err := Init(localDir)
	if err != nil {
		t.Fatal(err)
	}
	local.SaveRemote("origin", remoteDir)

	// Find hashA by loading remote
	commitA, err := remote.LoadCommit(hB)
	if err != nil {
		t.Fatal(err)
	}
	hA := commitA.Parents[0]

	local.WriteRef("refs/heads/main", hA.String())
	local.SetHEAD("ref: refs/heads/main")
	local.restoreCommit(hA)

	// Pull — should fast-forward to B
	result, err := local.Pull("origin")
	if err != nil {
		t.Fatal(err)
	}
	if !result.FastForward {
		t.Fatal("expected fast-forward pull")
	}

	// Verify HEAD now at B
	headStr, _ := local.ResolveHEAD()
	if headStr != hB.String() {
		t.Fatal("HEAD should point to B after fast-forward pull")
	}
}

func TestPullThreeWay(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "lo-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "lo-local-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localDir)

	// Remote: commit A (base)
	remote, err := Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(remoteDir, "f.txt"), []byte("base"), 0644)
	remote.AddFile(filepath.Join(remoteDir, "f.txt"))
	hA, err := remote.WriteCommit("Test", "A")
	if err != nil {
		t.Fatal(err)
	}

	// Local: init + fetch + create local main at A
	local, err := Init(localDir)
	if err != nil {
		t.Fatal(err)
	}
	local.SaveRemote("origin", remoteDir)

	// Manually fetch (local has no commits yet)
	remoteRepo, _ := Open(remoteDir)
	objects, err := remoteRepo.collectObjects(local, hA, false)
	if err != nil {
		t.Fatal(err)
	}
	for h := range objects {
		copyObject(remoteRepo, local, h)
	}
	local.WriteRef("refs/remotes/origin/main", hA.String())
	local.WriteRef("refs/heads/main", hA.String())
	local.SetHEAD("ref: refs/heads/main")
	local.restoreCommit(hA)

	// Local adds commit B (different file, no conflict)
	ioutil.WriteFile(filepath.Join(localDir, "local.txt"), []byte("local"), 0644)
	local.AddFile(filepath.Join(localDir, "local.txt"))
	hB, err := local.WriteCommit("Test", "B")
	if err != nil {
		t.Fatal(err)
	}

	// Remote adds commit C (different file, no conflict)
	ioutil.WriteFile(filepath.Join(remoteDir, "remote.txt"), []byte("remote"), 0644)
	remote.AddFile(filepath.Join(remoteDir, "remote.txt"))
	hC, err := remote.WriteCommit("Test", "C")
	if err != nil {
		t.Fatal(err)
	}

	// Pull — should three-way merge (no conflict since different files)
	result, err := local.Pull("origin")
	if err != nil {
		t.Fatal(err)
	}
	if result.FastForward {
		t.Fatal("expected non-fast-forward (three-way) merge")
	}
	if !result.Merged {
		t.Fatal("expected merge commit")
	}
	if len(result.Conflicts) > 0 {
		t.Fatalf("unexpected conflicts: %v", result.Conflicts)
	}

	// Verify merge commit has two parents
	headStr, _ := local.ResolveHEAD()
	head, _ := core.HashFromHex(headStr)
	commit, err := local.LoadCommit(head)
	if err != nil {
		t.Fatal(err)
	}
	if len(commit.Parents) != 2 {
		t.Fatalf("expected 2 parents for merge commit, got %d", len(commit.Parents))
	}

	// Verify commit C from remote was fetched
	if !local.HasObject(hC) {
		t.Fatal("remote commit C should exist locally after pull")
	}
	_ = hB
}

func TestClone(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "lo-source-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	cloneDir, err := ioutil.TempDir("", "lo-clone-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cloneDir)

	// Set up source repo
	source, err := Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(remoteDir, "f.txt"), []byte("hello"), 0644)
	source.AddFile(filepath.Join(remoteDir, "f.txt"))
	_, err = source.WriteCommit("Test", "initial")
	if err != nil {
		t.Fatal(err)
	}

	// Clone
	r, err := Clone(remoteDir, cloneDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("expected non-nil repo from Clone")
	}

	// Verify origin remote
	url, err := r.LoadRemote("origin")
	if err != nil {
		t.Fatal(err)
	}
	if url != remoteDir {
		t.Fatalf("expected origin pointing to remote, got %s", url)
	}

	// Verify on main branch
	branch := r.CurrentBranch()
	if branch != "main" {
		t.Fatalf("expected main branch, got %s", branch)
	}

	// Verify f.txt exists in working tree
	if _, err := os.Stat(filepath.Join(cloneDir, "f.txt")); os.IsNotExist(err) {
		t.Fatal("expected f.txt in cloned working tree")
	}

	// Verify we have a commit
	headStr, err := r.ResolveHEAD()
	if err != nil || headStr == "" {
		t.Fatal("expected HEAD to have a commit after clone")
	}
}

func TestCollectTreeChunks(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "lo-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	boundaryDir, err := ioutil.TempDir("", "lo-boundary-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(boundaryDir)

	r, err := Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}

	r.Config.Core.ChunkMinSize = 128
	r.Config.Core.ChunkThreshold = 512
	r.Config.Core.ChunkMaxSize = 1024

	// Store data as chunked (StoreChunkedFile is called directly)
	data := make([]byte, 3000) // enough to trigger CDC chunking
	for i := range data {
		data[i] = byte(i % 256)
	}
	chunkHash, err := r.StoreChunkedFile(data)
	if err != nil {
		t.Fatal(err)
	}

	// Create tree with chunked entry
	tree := &Tree{Entries: []TreeEntry{
		{Name: "large.bin", Hash: chunkHash, Size: int64(len(data)), Mode: 0644},
	}}
	treeContent, _ := core.SerializeJSON(tree)
	treeHash, err := r.StoreObject(core.ObjectTree, treeContent)
	if err != nil {
		t.Fatal(err)
	}

	// Create commit
	commit := Commit{
		Tree:    treeHash,
		Parents: nil,
		Author:  "Test",
		Message: "chunk test",
		Time:    time.Now(),
	}
	commitContent, _ := core.SerializeJSON(commit)
	commitHash, err := r.StoreObject(core.ObjectCommit, commitContent)
	if err != nil {
		t.Fatal(err)
	}

	// Empty boundary
	boundary, err := Init(boundaryDir)
	if err != nil {
		t.Fatal(err)
	}

	// Collect objects
	objects, err := r.collectObjects(boundary, commitHash, false)
	if err != nil {
		t.Fatal(err)
	}

	// Verify commit and tree collected
	if !objects[commitHash] {
		t.Fatal("commit hash not collected")
	}
	if !objects[treeHash] {
		t.Fatal("tree hash not collected")
	}
	if !objects[chunkHash] {
		t.Fatal("chunk manifest hash not collected")
	}

	// Verify all chunk blobs collected
	manifest, err := r.LoadChunkManifest(chunkHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for _, c := range manifest.Chunks {
		if !objects[c.Hash] {
			t.Fatalf("chunk blob %s not collected", c.Hash.Short())
		}
	}
}

func TestPushPullNonExistentRemote(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Push to non-existent remote
	err = r.Push("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent remote")
	}

	// Pull from non-existent remote
	_, err = r.Pull("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent remote")
	}
}

func TestObjectTypeAllTypes(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Blob
	blobHash, err := r.StoreObject(core.ObjectBlob, []byte("blob data"))
	if err != nil {
		t.Fatal(err)
	}

	// Tree
	tree := &Tree{Entries: []TreeEntry{
		{Name: "a.txt", Hash: blobHash, Size: 5, Mode: 0644},
	}}
	treeContent, _ := core.SerializeJSON(tree)
	treeHash, err := r.StoreObject(core.ObjectTree, treeContent)
	if err != nil {
		t.Fatal(err)
	}

	// Commit
	commit := Commit{
		Tree:    treeHash,
		Author:  "Test",
		Message: "test",
		Time:    time.Now(),
	}
	commitContent, _ := core.SerializeJSON(commit)
	commitHash, err := r.StoreObject(core.ObjectCommit, commitContent)
	if err != nil {
		t.Fatal(err)
	}

	r.Config.Core.ChunkMinSize = 2
	r.Config.Core.ChunkThreshold = 4
	r.Config.Core.ChunkMaxSize = 8
	// Chunk manifest
	chunkHash, err := r.StoreChunkedFile([]byte("chunked data"))
	if err != nil {
		t.Fatal(err)
	}

	// Verify ObjectType returns correct types
	typ, err := r.ObjectType(blobHash)
	if err != nil || typ != core.ObjectBlob {
		t.Fatalf("expected blob, got %v (err=%v)", typ, err)
	}

	typ, err = r.ObjectType(treeHash)
	if err != nil || typ != core.ObjectTree {
		t.Fatalf("expected tree, got %v", typ)
	}

	typ, err = r.ObjectType(commitHash)
	if err != nil || typ != core.ObjectCommit {
		t.Fatalf("expected commit, got %v", typ)
	}

	typ, err = r.ObjectType(chunkHash)
	if err != nil || typ != core.ObjectChunkManifest {
		t.Fatalf("expected chunk_manifest, got %v", typ)
	}

	// Chunk blob should have ObjectBlob type
	manifest, err := r.LoadChunkManifest(chunkHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chunks) > 0 {
		typ, err = r.ObjectType(manifest.Chunks[0].Hash)
		if err != nil || typ != core.ObjectBlob {
			t.Fatalf("expected chunk blob to be blob type, got %v", typ)
		}
	}
}

func TestLazyCloneAndLfsPull(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "lo-source-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	cloneDir, err := ioutil.TempDir("", "lo-clone-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cloneDir)

	remote, err := Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(remoteDir, "readme.txt"), []byte("hello world"), 0644)
	remote.AddFile(filepath.Join(remoteDir, "readme.txt"))

	remote.Config.Core.ChunkMinSize = 128
		remote.Config.Core.ChunkThreshold = 512
		remote.Config.Core.ChunkMaxSize = 1024

		largeData := make([]byte, 5000)
	for i := range largeData {
		largeData[i] = byte(i % 251)
	}
	chunkHash, err := remote.StoreChunkedFile(largeData)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := remote.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	idx.Entries["large.bin"] = IndexEntry{
		Hash:        chunkHash,
		ContentHash: core.HashFromBytes(largeData),
		Size:        int64(len(largeData)),
		Mode:        0644,
	}
	if err := remote.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(remoteDir, "large.bin"), largeData, 0644)

	hRemote, err := remote.WriteCommit("Test", "initial")
	if err != nil {
		t.Fatal(err)
	}
	_ = hRemote

	r, err := Clone(remoteDir, cloneDir, true)
	if err != nil {
		t.Fatal(err)
	}

	data, err := ioutil.ReadFile(filepath.Join(cloneDir, "readme.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Fatalf("expected real content for small file, got %q", data)
	}

	data, err = ioutil.ReadFile(filepath.Join(cloneDir, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "lo-lfs" {
		t.Fatalf("expected placeholder for large file, got %q", data)
	}

	if r.hasAllChunks(chunkHash) {
		t.Fatal("expected chunks to NOT be present after lazy clone")
	}

	statusFiles, err := r.LfsStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(statusFiles) != 1 {
		t.Fatalf("expected 1 large file, got %d", len(statusFiles))
	}
	if statusFiles[0].Path != "large.bin" {
		t.Fatalf("expected large.bin, got %s", statusFiles[0].Path)
	}
	if statusFiles[0].OnDisk {
		t.Fatal("expected large.bin to be not-on-disk (placeholder)")
	}

	if err := r.LfsPull("origin", "large.bin"); err != nil {
		t.Fatal(err)
	}

	if !r.hasAllChunks(chunkHash) {
		t.Fatal("expected chunks to be present after lfs-pull")
	}

	data, err = ioutil.ReadFile(filepath.Join(cloneDir, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != len(largeData) {
		t.Fatalf("expected %d bytes after lfs-pull, got %d", len(largeData), len(data))
	}
	for i := range largeData {
		if data[i] != largeData[i] {
			t.Fatalf("content mismatch at byte %d", i)
		}
	}

	statusFiles, err = r.LfsStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !statusFiles[0].OnDisk {
		t.Fatal("expected large.bin to be available after lfs-pull")
	}
}

func TestLfsStatusNoLargeFiles(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	files, err := r.LfsStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("expected empty lfs status, got %d files", len(files))
	}
}
