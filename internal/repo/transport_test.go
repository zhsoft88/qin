package repo

import (
	"io/ioutil"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
)

// ---- HTTP transport tests ----

func startRepoServer(t *testing.T, repoPath string) *httptest.Server {
	t.Helper()
	r, err := Open(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(r)
}

func TestHTTPFetch(t *testing.T) {
	sourceDir, err := ioutil.TempDir("", "lo-http-source-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sourceDir)

	localDir, err := ioutil.TempDir("", "lo-http-local-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localDir)

	// Set up source repo with commits
	source, err := Init(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(sourceDir, "f.txt"), []byte("hello"), 0644)
	source.AddFile(filepath.Join(sourceDir, "f.txt"))
	ioutil.WriteFile(filepath.Join(sourceDir, "g.txt"), []byte("world"), 0644)
	source.AddFile(filepath.Join(sourceDir, "g.txt"))
	hashSrc, err := source.WriteCommit("Test", "first commit")
	if err != nil {
		t.Fatal(err)
	}

	// Start HTTP server
	server := startRepoServer(t, sourceDir)
	defer server.Close()

	// Local repo fetches via HTTP
	local, err := Init(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.SaveRemote("origin", server.URL); err != nil {
		t.Fatal(err)
	}
	if err := local.Fetch("origin"); err != nil {
		t.Fatal(err)
	}

	// Verify remote-tracking ref
	hashStr, err := local.ReadRef("refs/remotes/origin/main")
	if err != nil {
		t.Fatalf("expected remote-tracking ref: %v", err)
	}
	if hashStr != hashSrc.String() {
		t.Fatalf("expected tracking ref %s, got %s", hashSrc.Short(), hashStr[:8])
	}

	// Verify commit object was copied
	if !local.HasObject(hashSrc) {
		t.Fatal("commit object not found in local after HTTP fetch")
	}

	// Verify tree and blob objects were also copied
	commit, err := source.LoadCommit(hashSrc)
	if err != nil {
		t.Fatal(err)
	}
	if !local.HasObject(commit.Tree) {
		t.Fatal("tree object not found in local after HTTP fetch")
	}
}

func TestHTTPPush(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "lo-http-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "lo-http-local-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localDir)

	// Init bare remote (no commits)
	_, err = Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}

	// Start HTTP server serving the remote repo
	server := startRepoServer(t, remoteDir)
	defer server.Close()

	// Local repo with commits
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

	if err := local.SaveRemote("origin", server.URL); err != nil {
		t.Fatal(err)
	}

	// Push via HTTP
	if err := local.Push("origin"); err != nil {
		t.Fatal(err)
	}

	// Open remote directly and verify
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
		t.Fatal("commit object not found in remote after HTTP push")
	}
}

func TestHTTPClone(t *testing.T) {
	sourceDir, err := ioutil.TempDir("", "lo-http-source-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sourceDir)

	cloneDir, err := ioutil.TempDir("", "lo-http-clone-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cloneDir)

	// Set up source repo
	source, err := Init(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(sourceDir, "f.txt"), []byte("hello"), 0644)
	source.AddFile(filepath.Join(sourceDir, "f.txt"))
	_, err = source.WriteCommit("Test", "initial")
	if err != nil {
		t.Fatal(err)
	}

	// Start HTTP server
	server := startRepoServer(t, sourceDir)
	defer server.Close()

	// Clone via HTTP
	r, err := Clone(server.URL, cloneDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("expected non-nil repo from Clone")
	}

	// Verify origin remote points to HTTP URL
	url, err := r.LoadRemote("origin")
	if err != nil {
		t.Fatal(err)
	}
	if url != server.URL {
		t.Fatalf("expected origin pointing to HTTP URL, got %s", url)
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

	// Verify HEAD has a commit
	headStr, err := r.ResolveHEAD()
	if err != nil || headStr == "" {
		t.Fatal("expected HEAD to have a commit after clone")
	}
}

func TestHTTPPushIncremental(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "lo-http-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "lo-http-local-*")
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

	// Start HTTP server
	server := startRepoServer(t, remoteDir)
	defer server.Close()

	// Local clones via HTTP fetch + creates local branch
	local, err := Init(localDir)
	if err != nil {
		t.Fatal(err)
	}
	local.SaveRemote("origin", server.URL)
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

	// Push via HTTP
	if err := local.Push("origin"); err != nil {
		t.Fatal(err)
	}

	// Open remote directly and verify
	remote, err = Open(remoteDir)
	if err != nil {
		t.Fatal(err)
	}

	if !remote.HasObject(hLocal) {
		t.Fatal("new commit not found in remote after incremental push")
	}
	if !remote.HasObject(hBase) {
		t.Fatal("base commit should still be in remote")
	}
	hashStr, _ := remote.ReadRef("refs/heads/main")
	if hashStr != hLocal.String() {
		t.Fatal("remote main should point to pushed commit")
	}
}

func TestHTTPLazyCloneAndLfsPull(t *testing.T) {
	sourceDir, err := ioutil.TempDir("", "lo-http-source-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sourceDir)

	cloneDir, err := ioutil.TempDir("", "lo-http-clone-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cloneDir)

	// Source repo with small file + large file
	source, err := Init(sourceDir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(sourceDir, "readme.txt"), []byte("hello world"), 0644)
	source.AddFile(filepath.Join(sourceDir, "readme.txt"))

	source.Config.Core.ChunkMinSize = 128
	source.Config.Core.ChunkThreshold = 512
	source.Config.Core.ChunkMaxSize = 1024

	largeData := make([]byte, 5000)
	for i := range largeData {
		largeData[i] = byte(i % 251)
	}
	chunkHash, err := source.StoreChunkedFile(largeData)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := source.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	idx.Entries["large.bin"] = IndexEntry{
		Hash:        chunkHash,
		ContentHash: core.HashFromBytes(largeData),
		Size:        int64(len(largeData)),
		Mode:        0644,
	}
	if err := source.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(sourceDir, "large.bin"), largeData, 0644)

	_, err = source.WriteCommit("Test", "initial")
	if err != nil {
		t.Fatal(err)
	}

	// Start HTTP server
	server := startRepoServer(t, sourceDir)
	defer server.Close()

	// Lazy clone via HTTP
	r, err := Clone(server.URL, cloneDir, true)
	if err != nil {
		t.Fatal(err)
	}

	// Verify small file is real, large file is placeholder
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

	// LfsPull via HTTP
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
}

func TestHTTPRefList(t *testing.T) {
	sourceDir, err := ioutil.TempDir("", "lo-http-refs-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sourceDir)

	source, err := Init(sourceDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a commit on main
	ioutil.WriteFile(filepath.Join(sourceDir, "f.txt"), []byte("data"), 0644)
	source.AddFile(filepath.Join(sourceDir, "f.txt"))
	hMain, err := source.WriteCommit("Test", "main commit")
	if err != nil {
		t.Fatal(err)
	}

	// Create another branch
	source.CreateBranch("feature")
	source.SwitchBranch("feature")
	ioutil.WriteFile(filepath.Join(sourceDir, "g.txt"), []byte("feature data"), 0644)
	source.AddFile(filepath.Join(sourceDir, "g.txt"))
	hFeature, err := source.WriteCommit("Test", "feature commit")
	if err != nil {
		t.Fatal(err)
	}

	// Start HTTP server
	server := startRepoServer(t, sourceDir)
	defer server.Close()

	// List refs via HTTP
	refs, err := httpListRefs(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	if refs["refs/heads/main"] != hMain.String() {
		t.Fatalf("expected main ref %s, got %s", hMain.Short(), refs["refs/heads/main"][:8])
	}
	if refs["refs/heads/feature"] != hFeature.String() {
		t.Fatalf("expected feature ref %s, got %s", hFeature.Short(), refs["refs/heads/feature"][:8])
	}
}

// ---- SSH transport tests ----

func TestSSHParseURL(t *testing.T) {
	tests := []struct {
		input    string
		host     string
		path     string
		wantErr  bool
	}{
		{"ssh://git@github.com/user/repo", "git@github.com", "/user/repo", false},
		{"ssh://git@github.com:22/user/repo", "git@github.com:22", "/user/repo", false},
		{"git@github.com:user/repo.git", "git@github.com", "user/repo.git", false},
		{"user@host.xz:path/to/repo", "user@host.xz", "path/to/repo", false},
		{"git@github.com/user/repo", "", "", true},
		{"/absolute/path", "", "", true},
		{"ssh://user@host/", "", "", true},
	}

	for _, tt := range tests {
		host, path, err := sshParseURL(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("sshParseURL(%q): expected error, got host=%q path=%q", tt.input, host, path)
			}
			continue
		}
		if err != nil {
			t.Errorf("sshParseURL(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if host != tt.host {
			t.Errorf("sshParseURL(%q): expected host %q, got %q", tt.input, tt.host, host)
		}
		if path != tt.path {
			t.Errorf("sshParseURL(%q): expected path %q, got %q", tt.input, tt.path, path)
		}
	}
}

func TestSSHURLDetection(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-ssh-detect-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Test SSH URL detection via SaveRemote + LoadRemote (routing test)
	sshURL := "git@github.com:user/repo.git"
	if err := r.SaveRemote("sshremote", sshURL); err != nil {
		t.Fatal(err)
	}

	loaded, err := r.LoadRemote("sshremote")
	if err != nil {
		t.Fatal(err)
	}
	if loaded != sshURL {
		t.Fatalf("expected %s, got %s", sshURL, loaded)
	}

	// Test ssh:// URL
	sshURL2 := "ssh://git@github.com/user/repo"
	if err := r.SaveRemote("sshremote2", sshURL2); err != nil {
		t.Fatal(err)
	}

	// Add a commit so Push has branches to send
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("data"), 0644)
	r.AddFile(filepath.Join(dir, "f.txt"))
	if _, err := r.WriteCommit("Test", "commit"); err != nil {
		t.Fatal(err)
	}

	// Push to non-existent SSH remote should fail (SSH connection error)
	err = r.Push("sshremote")
	if err == nil {
		t.Fatal("expected error pushing to SSH remote with no server")
	}
}

func TestRemoteBranchesHTTP(t *testing.T) {
	sourceDir, err := ioutil.TempDir("", "lo-http-branches-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sourceDir)

	source, err := Init(sourceDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create commit on main
	ioutil.WriteFile(filepath.Join(sourceDir, "f.txt"), []byte("data"), 0644)
	source.AddFile(filepath.Join(sourceDir, "f.txt"))
	_, err = source.WriteCommit("Test", "main commit")
	if err != nil {
		t.Fatal(err)
	}

	// Create feature branch
	source.CreateBranch("feature")
	source.SwitchBranch("feature")
	ioutil.WriteFile(filepath.Join(sourceDir, "g.txt"), []byte("feat"), 0644)
	source.AddFile(filepath.Join(sourceDir, "g.txt"))
	_, err = source.WriteCommit("Test", "feature commit")
	if err != nil {
		t.Fatal(err)
	}

	// Start HTTP server
	server := startRepoServer(t, sourceDir)
	defer server.Close()

	// Test remoteBranches via HTTP
	branches, err := source.remoteBranches(server.URL, "origin")
	if err != nil {
		t.Fatal(err)
	}

	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d: %v", len(branches), branches)
	}
	if branches[0] != "feature" || branches[1] != "main" {
		t.Fatalf("expected [feature, main], got %v", branches)
	}
}
