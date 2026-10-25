package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

func TestSaveLoadListRemoveRemote(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Save. The path must already be absolute: SaveRemote canonicalizes local
	// paths, and "/tmp/upstream" is not absolute on Windows.
	upstream := filepath.Join(dir, "upstream")
	if err := r.SaveRemote("origin", upstream); err != nil {
		t.Fatal(err)
	}

	// Load
	url, err := r.LoadRemote("origin")
	if err != nil {
		t.Fatal(err)
	}
	if url != upstream {
		t.Fatalf("expected %s, got %s", upstream, url)
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

func TestSetRemoteURL(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldURL := filepath.Join(dir, "old")
	newURL := filepath.Join(dir, "new")
	if err := r.SaveRemote("origin", oldURL); err != nil {
		t.Fatal(err)
	}

	if err := r.SetRemoteURL("origin", newURL); err != nil {
		t.Fatal(err)
	}
	url, err := r.LoadRemote("origin")
	if err != nil {
		t.Fatal(err)
	}
	if url != newURL {
		t.Fatalf("expected %s, got %s", newURL, url)
	}
	remotes, _ := r.ListRemotes()
	if len(remotes) != 1 {
		t.Fatalf("expected 1 remote, got %d", len(remotes))
	}

	// A typo must not create a new remote.
	err = r.SetRemoteURL("orign", "/tmp/typo")
	if err == nil || !strings.Contains(err.Error(), "remote not found") {
		t.Fatalf("expected remote not found, got %v", err)
	}
	remotes, _ = r.ListRemotes()
	if len(remotes) != 1 {
		t.Fatalf("expected still 1 remote, got %d", len(remotes))
	}
}

// TestRemoteTransportPredicate pins the one rule that decides whether a stored
// URL is a filesystem path or a network transport. A drive path has a colon but
// no '@', so it stays local; a path containing both is still taken for scp
// form, as it always was.
func TestRemoteTransportPredicate(t *testing.T) {
	cases := []struct {
		raw       string
		http, ssh bool
	}{
		{"http://example.com/repo", true, false},
		{"https://example.com/repo", true, false},
		{"ssh://git@host/repo", false, true},
		{"git@host:repo", false, true},
		{`C:\src\repo`, false, false},
		{"/tmp/repo", false, false},
		{`repo\maskio.qin`, false, false},
		{"foo:bar", false, false},
	}
	for _, tc := range cases {
		if got := isHTTPURL(tc.raw); got != tc.http {
			t.Errorf("isHTTPURL(%q) = %v, want %v", tc.raw, got, tc.http)
		}
		if got := isSSHURL(tc.raw); got != tc.ssh {
			t.Errorf("isSSHURL(%q) = %v, want %v", tc.raw, got, tc.ssh)
		}
		if got := isRemoteTransport(tc.raw); got != (tc.http || tc.ssh) {
			t.Errorf("isRemoteTransport(%q) = %v", tc.raw, got)
		}
	}
}

func TestSaveRemoteCanonicalizesLocalPaths(t *testing.T) {
	work, err := ioutil.TempDir("", "qin-canon-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)

	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Defers run in reverse order, so the CWD is restored before work is
	// removed: Windows cannot delete a directory that is the process CWD.
	defer os.Chdir(old)
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}

	r, err := Init(filepath.Join(work, "repo"))
	if err != nil {
		t.Fatal(err)
	}

	rel := filepath.Join("..", "remote")
	want, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SaveRemote("origin", rel); err != nil {
		t.Fatal(err)
	}
	got, err := r.LoadRemote("origin")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("relative remote stored as %s, want %s", got, want)
	}

	abs := filepath.Join(work, "abs-remote")
	if err := r.SaveRemote("abs", abs); err != nil {
		t.Fatal(err)
	}
	got, err = r.LoadRemote("abs")
	if err != nil {
		t.Fatal(err)
	}
	if got != abs {
		t.Fatalf("absolute remote stored as %s, want %s", got, abs)
	}
}

func TestSaveRemoteLeavesTransportURLsAlone(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{
		"http://example.com/repo",
		"https://example.com/repo",
		"ssh://git@host/repo",
		"git@host:repo",
	} {
		if err := r.SaveRemote("origin", u); err != nil {
			t.Fatal(err)
		}
		got, err := r.LoadRemote("origin")
		if err != nil {
			t.Fatal(err)
		}
		if got != u {
			t.Fatalf("transport URL stored as %q, want %q", got, u)
		}
	}
}

func TestSaveRemoteRejectsEmptyURL(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"", "   "} {
		if err := r.SaveRemote("origin", u); err == nil {
			t.Fatalf("SaveRemote accepted %q", u)
		}
	}
	if _, err := r.LoadRemote("origin"); err == nil {
		t.Fatal("an empty URL created a remote")
	}
}

func TestOpenExactRootDoesNotWalkUp(t *testing.T) {
	_, dir := newTargetDir(t, true)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}

	// Control: Open is for a working directory and does walk up.
	if _, err := Open(sub); err != nil {
		t.Fatalf("Open should find the repository above %s: %v", sub, err)
	}

	_, err := openExactRoot(sub)
	if err == nil {
		t.Fatal("openExactRoot walked up to the repository")
	}
	if !strings.Contains(err.Error(), "not a repository") || !strings.Contains(err.Error(), sub) {
		t.Fatalf("error %q should name the path it checked", err)
	}
	if r, err := openExactRoot(dir); err != nil || r.Path != dir {
		t.Fatalf("openExactRoot at the root: path %v, err %v", r, err)
	}
}

// TestPushStaleRelativeRemoteFailsNamingResolvedPath reproduces the report that
// started this fix: a relative origin written before SaveRemote canonicalised
// paths used to resolve against the process CWD, walk up into the pusher's own
// checkout, and be reported as a non-bare target with main checked out.
func TestPushStaleRelativeRemoteFailsNamingResolvedPath(t *testing.T) {
	target, targetDir := newTargetDir(t, true)
	pusher := pusherWithCommit(t, targetDir)

	// Write the legacy relative value directly: SaveRemote would canonicalize it.
	stale := filepath.Join("missing-dir", "missing-target")
	remotesDir := filepath.Join(pusher.QinDir(), "remotes")
	if err := os.MkdirAll(remotesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(remotesDir, "origin"), []byte(stale+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := pusher.Push("origin", false)
	if err == nil {
		t.Fatal("push with a stale relative remote should fail")
	}
	resolved := filepath.Join(pusher.Path, stale)
	if !strings.Contains(err.Error(), "not a repository") || !strings.Contains(err.Error(), resolved) {
		t.Fatalf("error %q should name the resolved path %s", err, resolved)
	}
	if strings.Contains(err.Error(), "checked out") {
		t.Fatalf("push reported the checked-out refusal instead of the missing remote: %v", err)
	}
	if _, err := target.ReadRef("refs/heads/main"); err == nil {
		t.Fatal("a failed push wrote the target's main")
	}
}

// TestLegacyRelativeRemoteResolvesAgainstRepoRoot proves the base is r.Path,
// not the directory the command runs from: the package CWD is not the pusher,
// and the test also chdirs into a subdirectory.
func TestLegacyRelativeRemoteResolvesAgainstRepoRoot(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-legacy-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	pusher, err := Init(filepath.Join(dir, "pusher"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(pusher.Path, "f.txt")
	if err := ioutil.WriteFile(file, []byte("f"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := pusher.AddFile(file); err != nil {
		t.Fatal(err)
	}
	if _, err := pusher.WriteCommit("Test", "f"); err != nil {
		t.Fatal(err)
	}

	target, err := InitBare(filepath.Join(pusher.Path, "target"))
	if err != nil {
		t.Fatal(err)
	}
	remotesDir := filepath.Join(pusher.QinDir(), "remotes")
	if err := os.MkdirAll(remotesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(remotesDir, "origin"), []byte("target\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sub := filepath.Join(pusher.Path, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}

	if err := pusher.Push("origin", false); err != nil {
		t.Fatalf("push to a repo-root-relative legacy remote failed: %v", err)
	}
	tip, err := pusher.ReadRef("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	got, err := target.ReadRef("refs/heads/main")
	if err != nil {
		t.Fatalf("the target's main was not written: %v", err)
	}
	if got != tip {
		t.Fatalf("target main = %s, want %s", got, tip)
	}
}

// TestCloneSavesAbsoluteRemote pins that Clone addresses the same repository
// for the whole operation when its argument is a relative path.
func TestCloneSavesAbsoluteRemote(t *testing.T) {
	work, err := ioutil.TempDir("", "qin-cloneabs-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)

	source, err := Init(filepath.Join(work, "source"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(source.Path, "f.txt")
	if err := ioutil.WriteFile(file, []byte("f"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := source.AddFile(file); err != nil {
		t.Fatal(err)
	}
	if _, err := source.WriteCommit("Test", "f"); err != nil {
		t.Fatal(err)
	}

	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(work, "dest")
	cloned, err := Clone("source", dest, false)
	if err != nil {
		t.Fatalf("clone from a relative path failed: %v", err)
	}
	want := filepath.Join(work, "source")
	got, err := cloned.LoadRemote("origin")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("origin stored as %s, want %s", got, want)
	}
}

func TestFetchFirstTime(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "qin-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "qin-local-*")
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
	remoteDir, err := ioutil.TempDir("", "qin-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "qin-local-*")
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
	remoteDir, err := ioutil.TempDir("", "qin-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "qin-local-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localDir)

	// A push target: InitBare, or the push to its checked-out branch is refused.
	_, err = InitBare(remoteDir)
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
	if err := local.Push("origin", false); err != nil {
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
	remoteDir, err := ioutil.TempDir("", "qin-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "qin-local-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localDir)

	// A push target holding one commit. It has no working tree to write a file
	// into, so the baseline goes in object by object.
	remote, err := InitBare(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	hBase := commitIntoBare(t, remote, "f.txt", "base")

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
	if err := local.Push("origin", false); err != nil {
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
	remoteDir, err := ioutil.TempDir("", "qin-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "qin-local-*")
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
	remoteDir, err := ioutil.TempDir("", "qin-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "qin-local-*")
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
	remoteDir, err := ioutil.TempDir("", "qin-source-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	cloneDir, err := ioutil.TempDir("", "qin-clone-*")
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
	remoteDir, err := ioutil.TempDir("", "qin-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	boundaryDir, err := ioutil.TempDir("", "qin-boundary-*")
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
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Push to non-existent remote
	err = r.Push("nonexistent", false)
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
	dir, err := ioutil.TempDir("", "qin-test-*")
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
	remoteDir, err := ioutil.TempDir("", "qin-source-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)

	cloneDir, err := ioutil.TempDir("", "qin-clone-*")
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
	if string(data) != "qin-lfs" {
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
	dir, err := ioutil.TempDir("", "qin-test-*")
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

// ---- push safety ----

// commitIntoBare writes a one-file commit into a repository with no working
// tree. The ordinary route — write a file, add it, commit — needs somewhere to
// write the file, and a push target does not have one; the same tree still has
// to be reachable so that pushes into it are fast-forwards.
func commitIntoBare(t *testing.T, r *Repository, name, content string) core.Hash {
	t.Helper()
	// The layout is the same as for a checkout; only core.bare differs.
	if !r.Config.Core.Bare {
		t.Fatalf("commitIntoBare is for a bare repository, %s is not one", r.Path)
	}

	blob, err := r.StoreObject(core.ObjectBlob, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	treeHash, err := r.buildTreeFromEntries(map[string]TreeEntry{
		name: {Hash: blob, Size: int64(len(content)), Mode: 0644},
	})
	if err != nil {
		t.Fatal(err)
	}

	var parents []core.Hash
	if head, err := r.ResolveHEAD(); err == nil && head != "" {
		p, err := core.HashFromHex(head)
		if err != nil {
			t.Fatal(err)
		}
		parents = append(parents, p)
	}
	commit := Commit{
		Tree:    treeHash,
		Parents: parents,
		Author:  "Test",
		Message: name,
		Time:    time.Now(),
	}
	data, err := core.SerializeJSON(commit)
	if err != nil {
		t.Fatal(err)
	}
	h, err := r.StoreObject(core.ObjectCommit, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.WriteRef("refs/heads/main", h.String()); err != nil {
		t.Fatal(err)
	}
	return h
}

// rewindFixture is the ordinary non-fast-forward situation: alice pushed A,
// bob built B on top of A and pushed it back, and alice — who has not fetched
// since — has committed C on top of A. Pushing C would orphan B.
type rewindFixture struct {
	alice, bob, remote *Repository
	hA, hB, hC         core.Hash
}

// localOrigin is the origin callback for fixtures whose remote is a directory.
func localOrigin(remoteDir string) string { return remoteDir }

// newRewindFixture builds the fixture. origin maps the remote directory to the
// URL the two clients should use, so the same setup serves the local-path and
// the HTTP transports; it is called once the remote repository exists.
func newRewindFixture(t *testing.T, origin func(remoteDir string) string) *rewindFixture {
	t.Helper()
	root, err := ioutil.TempDir("", "qin-rewind-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })

	// The remote is the push target, so it is bare; the two clients are
	// checkouts, because that is what pushes into it.
	mkRepo := func(name string, bare bool) (*Repository, string) {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		newRepo := Init
		if bare {
			newRepo = InitBare
		}
		r, err := newRepo(dir)
		if err != nil {
			t.Fatal(err)
		}
		return r, dir
	}
	write := func(dir, name, content string) {
		t.Helper()
		if err := ioutil.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	remote, remoteDir := mkRepo("remote", true)
	url := origin(remoteDir)

	alice, aliceDir := mkRepo("alice", false)
	if err := alice.SaveRemote("origin", url); err != nil {
		t.Fatal(err)
	}
	write(aliceDir, "a.txt", "a")
	if err := alice.AddFile(filepath.Join(aliceDir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	hA, err := alice.WriteCommit("Test", "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := alice.Push("origin", false); err != nil {
		t.Fatal(err)
	}

	bobDir := filepath.Join(root, "bob")
	bob, err := Clone(url, bobDir, false)
	if err != nil {
		t.Fatal(err)
	}
	write(bobDir, "b.txt", "b")
	if err := bob.AddFile(filepath.Join(bobDir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	hB, err := bob.WriteCommit("Test", "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := bob.Push("origin", false); err != nil {
		t.Fatal(err)
	}

	write(aliceDir, "c.txt", "c")
	if err := alice.AddFile(filepath.Join(aliceDir, "c.txt")); err != nil {
		t.Fatal(err)
	}
	hC, err := alice.WriteCommit("Test", "c")
	if err != nil {
		t.Fatal(err)
	}

	return &rewindFixture{alice: alice, bob: bob, remote: remote, hA: hA, hB: hB, hC: hC}
}

func pushTipOf(t *testing.T, r *Repository) string {
	t.Helper()
	tip, err := r.ReadRef("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	return tip
}

func TestPushRewindRefused(t *testing.T) {
	f := newRewindFixture(t, localOrigin)

	err := f.alice.Push("origin", false)
	if err == nil {
		t.Fatal("expected a non-fast-forward push to be refused")
	}
	if !strings.Contains(err.Error(), "not a fast-forward") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Refused means refused: nothing was written and bob's commit is the tip.
	if got := pushTipOf(t, f.remote); got != f.hB.String() {
		t.Fatalf("target main = %s, want bob's %s", got, f.hB.Short())
	}
	if !f.remote.HasObject(f.hB) {
		t.Fatal("bob's commit disappeared from the target")
	}
}

// TestPushRewindAfterFetchRefused is the same refusal decided the other way:
// with bob's tip in alice's object store the verdict comes from the ancestry
// walk rather than from the commit being absent.
func TestPushRewindAfterFetchRefused(t *testing.T) {
	f := newRewindFixture(t, localOrigin)

	if err := f.alice.Fetch("origin"); err != nil {
		t.Fatal(err)
	}
	if !f.alice.HasObject(f.hB) {
		t.Fatal("fetch did not bring bob's commit")
	}

	if err := f.alice.Push("origin", false); err == nil {
		t.Fatal("expected the push to stay refused after a fetch")
	}
	if got := pushTipOf(t, f.remote); got != f.hB.String() {
		t.Fatalf("target main = %s, want bob's %s", got, f.hB.Short())
	}
}

func TestPushForceAllowsRewind(t *testing.T) {
	f := newRewindFixture(t, localOrigin)

	if err := f.alice.Push("origin", true); err != nil {
		t.Fatalf("--force should allow the overwrite: %v", err)
	}
	if got := pushTipOf(t, f.remote); got != f.hC.String() {
		t.Fatalf("target main = %s, want alice's %s", got, f.hC.Short())
	}
	// Bob's commit is orphaned, not deleted: it stays until gc prunes it.
	if !f.remote.HasObject(f.hB) {
		t.Fatal("orphaned commit should stay in the target until gc")
	}
}

func TestPushUpToDateIsNoOp(t *testing.T) {
	f := newRewindFixture(t, localOrigin)

	if err := f.alice.Push("origin", true); err != nil {
		t.Fatal(err)
	}
	// The second push has nothing to transfer and the ref already agrees.
	if err := f.alice.Push("origin", false); err != nil {
		t.Fatalf("a push of an unchanged branch must be a no-op: %v", err)
	}
	if got := pushTipOf(t, f.remote); got != f.hC.String() {
		t.Fatalf("target main = %s, want %s", got, f.hC.Short())
	}
}

// TestPushCompletesInterruptedRefUpdate covers the state an interrupted push
// leaves: every object has arrived, but the ref was never written. A push
// whose objects are all present must still finish that ref rather than report
// "everything up to date" and stop.
func TestPushCompletesInterruptedRefUpdate(t *testing.T) {
	root, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)

	remoteDir := filepath.Join(root, "remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatal(err)
	}
	remote, err := InitBare(remoteDir)
	if err != nil {
		t.Fatal(err)
	}

	aliceDir := filepath.Join(root, "alice")
	if err := os.MkdirAll(aliceDir, 0755); err != nil {
		t.Fatal(err)
	}
	alice, err := Init(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(aliceDir, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := alice.AddFile(filepath.Join(aliceDir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.WriteCommit("Test", "a"); err != nil {
		t.Fatal(err)
	}
	if err := alice.SaveRemote("origin", remoteDir); err != nil {
		t.Fatal(err)
	}
	if err := alice.Push("origin", false); err != nil {
		t.Fatal(err)
	}

	if err := ioutil.WriteFile(filepath.Join(aliceDir, "c.txt"), []byte("c"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := alice.AddFile(filepath.Join(aliceDir, "c.txt")); err != nil {
		t.Fatal(err)
	}
	hC, err := alice.WriteCommit("Test", "c")
	if err != nil {
		t.Fatal(err)
	}

	// Transfer the objects without touching the ref — the interrupted push.
	objects, err := alice.collectObjects(remote, hC, false)
	if err != nil {
		t.Fatal(err)
	}
	for h := range objects {
		if err := copyObject(alice, remote, h); err != nil {
			t.Fatal(err)
		}
	}
	if before := pushTipOf(t, remote); before == hC.String() {
		t.Fatal("fixture is wrong: the ref was already updated")
	}

	if err := alice.Push("origin", false); err != nil {
		t.Fatal(err)
	}
	if got := pushTipOf(t, remote); got != hC.String() {
		t.Fatalf("target main = %s, want %s after the retry", got, hC.Short())
	}
}

// ---- checked-out guard ----

// newTargetDir creates a directory holding a repository of the requested kind
// and returns it with its path.
func newTargetDir(t *testing.T, bare bool) (*Repository, string) {
	t.Helper()
	dir, err := ioutil.TempDir("", "qin-target-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	newRepo := Init
	if bare {
		newRepo = InitBare
	}
	r, err := newRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

// pusherWithCommit is the simplest client that can be aimed at a target: a
// checkout with one commit on main and origin already saved.
func pusherWithCommit(t *testing.T, targetURL string) *Repository {
	t.Helper()
	dir, err := ioutil.TempDir("", "qin-pusher-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("f"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFile(filepath.Join(dir, "f.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.WriteCommit("Test", "f"); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveRemote("origin", targetURL); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestCheckPushRefGuardMatrix is the rule itself, read off the four kinds of
// target. It is a unit test on purpose: the integration tests below each reach
// only one row of this table, and the rows that protect nothing are exactly the
// ones a bug would turn into silent overwrites.
func TestCheckPushRefGuardMatrix(t *testing.T) {
	r, _ := newTargetDir(t, false)
	old := strings.Repeat("ab", 32)
	newTip := strings.Repeat("cd", 32)

	// The fast-forward verdict is not what these cases are about, so every
	// target starts with no ref at all: only the checked-out rule can fire.
	cases := []struct {
		name string
		st   remoteState
		ref  string
		want string // "" means allowed
	}{
		{"bare target, checked-out name", remoteState{Bare: true, HeadBranch: "main"}, "refs/heads/main", ""},
		{"detached target", remoteState{HeadBranch: ""}, "refs/heads/main", ""},
		{"target on another branch", remoteState{HeadBranch: "other"}, "refs/heads/main", ""},
		{"non-bare target on that branch", remoteState{HeadBranch: "main"}, "refs/heads/main", "checked out"},
		{"non-bare target, tag", remoteState{HeadBranch: "main"}, "refs/tags/v1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.st
			st.Refs = map[string]string{"refs/heads/other": old}
			err := r.checkPushRef(st, tc.ref, newTip, false)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected the update to be allowed, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestPushToCheckedOutBranchRefused(t *testing.T) {
	target, targetDir := newTargetDir(t, false)
	pusher := pusherWithCommit(t, targetDir)

	err := pusher.Push("origin", false)
	if err == nil {
		t.Fatal("expected a push to a non-bare target's checked-out branch to be refused")
	}
	if !strings.Contains(err.Error(), "checked out") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := target.ReadRef("refs/heads/main"); err == nil {
		t.Fatal("the refused push still wrote the target's main")
	}
}

// TestPushForceDoesNotOverrideCheckedOut pins the decision that --force covers
// the fast-forward rule and nothing else: being checked out is a property of
// the target, not of the update, so there is no way for the pusher to consent
// to it.
func TestPushForceDoesNotOverrideCheckedOut(t *testing.T) {
	target, targetDir := newTargetDir(t, false)
	pusher := pusherWithCommit(t, targetDir)

	err := pusher.Push("origin", true)
	if err == nil {
		t.Fatal("--force must not override the checked-out refusal")
	}
	if !strings.Contains(err.Error(), "checked out") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := target.ReadRef("refs/heads/main"); err == nil {
		t.Fatal("the refused push still wrote the target's main")
	}
}

func TestPushToNonBareTargetErrorNamesInitBare(t *testing.T) {
	_, targetDir := newTargetDir(t, false)
	pusher := pusherWithCommit(t, targetDir)

	err := pusher.Push("origin", false)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	// The remedy is not guessable, so the message has to carry it.
	if !strings.Contains(err.Error(), "qin init --bare") {
		t.Fatalf("the refusal does not name the remedy: %v", err)
	}
}

// TestPushToOtherBranchOfNonBareTargetAllowed shows the guard is per ref, not
// per target: a checkout that is on some other branch is an ordinary target
// for the branches it is not on.
func TestPushToOtherBranchOfNonBareTargetAllowed(t *testing.T) {
	target, targetDir := newTargetDir(t, false)
	if err := target.SetHEAD("ref: refs/heads/other"); err != nil {
		t.Fatal(err)
	}
	pusher := pusherWithCommit(t, targetDir)

	if err := pusher.Push("origin", false); err != nil {
		t.Fatalf("pushing a branch the target does not have checked out: %v", err)
	}
	got, err := target.ReadRef("refs/heads/main")
	if err != nil {
		t.Fatalf("the push did not write main: %v", err)
	}
	tip, err := pusher.ReadRef("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if got != tip {
		t.Fatalf("target main = %s, want %s", got[:8], tip[:8])
	}
}

// TestPushToDetachedHeadTargetAllowed: a detached HEAD has no branch checked
// out, so it protects nothing. The target is a real checkout — cloned from,
// and carrying a commit — with its HEAD moved off the branch.
func TestPushToDetachedHeadTargetAllowed(t *testing.T) {
	target, targetDir := newTargetDir(t, false)
	if err := ioutil.WriteFile(filepath.Join(targetDir, "base.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := target.AddFile(filepath.Join(targetDir, "base.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := target.WriteCommit("Test", "base"); err != nil {
		t.Fatal(err)
	}
	head, err := target.ResolveHEAD()
	if err != nil {
		t.Fatal(err)
	}
	if err := target.SetHEAD(head); err != nil {
		t.Fatal(err)
	}

	// The pusher's commit is a child of the target's, so only the
	// checked-out rule could refuse it.
	pusherDir, err := ioutil.TempDir("", "qin-detached-pusher-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(pusherDir) })
	pusher, err := Clone(targetDir, pusherDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(pusherDir, "next.txt"), []byte("next"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := pusher.AddFile(filepath.Join(pusherDir, "next.txt")); err != nil {
		t.Fatal(err)
	}
	tip, err := pusher.WriteCommit("Test", "next")
	if err != nil {
		t.Fatal(err)
	}

	if err := pusher.Push("origin", false); err != nil {
		t.Fatalf("pushing to a target with a detached HEAD: %v", err)
	}
	got, err := target.ReadRef("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if got != tip.String() {
		t.Fatalf("target main = %s, want %s", got[:8], tip.Short())
	}
}

// TestPushToBareTargetAllowsCheckedOutName: a bare target protects no branch,
// the name it has checked out included. The other half — that a bare target
// still refuses a non-fast-forward — is TestPushRewindRefused, whose target is
// bare as well.
func TestPushToBareTargetAllowsCheckedOutName(t *testing.T) {
	target, targetDir := newTargetDir(t, true)
	pusher := pusherWithCommit(t, targetDir)

	if err := pusher.Push("origin", false); err != nil {
		t.Fatalf("a bare target must accept a push to main: %v", err)
	}
	tip, err := pusher.ReadRef("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := target.ReadRef("refs/heads/main"); err != nil || got != tip {
		t.Fatalf("target main = %q (%v), want %s", got, err, tip[:8])
	}
}

func TestInitBareConfig(t *testing.T) {
	_, bareDir := newTargetDir(t, true)
	bareCfg, err := LoadConfig(bareDir)
	if err != nil {
		t.Fatal(err)
	}
	if !bareCfg.Core.Bare {
		t.Fatal("InitBare did not set core.bare")
	}
	data, err := ioutil.ReadFile(filepath.Join(bareDir, ".qin", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"bare": true`) {
		t.Fatalf("core.bare is not written to the target's config: %s", data)
	}

	// A plain Init is a checkout, and its config must not have grown a field:
	// omitempty is what keeps existing repositories byte-identical.
	_, plainDir := newTargetDir(t, false)
	data, err = ioutil.ReadFile(filepath.Join(plainDir, ".qin", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "bare") {
		t.Fatalf("a non-bare config should not mention bare: %s", data)
	}
	plainCfg, err := LoadConfig(plainDir)
	if err != nil {
		t.Fatal(err)
	}
	if plainCfg.Core.Bare {
		t.Fatal("a plain Init must not be bare")
	}

	// core.bare is the escape hatch for a target that already exists, so it
	// has to be reachable through the config commands.
	if v, err := ConfigGet(plainCfg, "core.bare"); err != nil || v != "false" {
		t.Fatalf("core.bare = %q, %v, want false", v, err)
	}
	if err := ConfigSet(plainCfg, "core.bare", "true"); err != nil {
		t.Fatal(err)
	}
	if v, _ := ConfigGet(plainCfg, "core.bare"); v != "true" {
		t.Fatalf("core.bare = %q after set, want true", v)
	}
	if err := ConfigSet(plainCfg, "core.bare", "maybe"); err == nil {
		t.Fatal("expected an invalid bool to be rejected")
	}
	if err := ConfigUnset(plainCfg, "core.bare"); err != nil {
		t.Fatal(err)
	}
	if v, _ := ConfigGet(plainCfg, "core.bare"); v != "false" {
		t.Fatalf("core.bare = %q after unset, want false", v)
	}
	if _, ok := ConfigKeys()["core.bare"]; !ok {
		t.Fatal("core.bare missing from ConfigKeys")
	}
}

func TestBareFromConfigJSON(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"bare", `{"core":{"bare":true}}`, true},
		{"not bare", `{"core":{"bare":false}}`, false},
		{"key absent", `{"core":{"fsmonitor":"true"}}`, false},
		{"no core", `{}`, false},
		{"empty", ``, false},
		{"not json", `not json at all`, false},
		{"wrong type", `{"core":{"bare":"yes"}}`, false},
		{"truncated", `{"core":{"bare":tr`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bareFromConfigJSON([]byte(tc.data)); got != tc.want {
				t.Fatalf("bareFromConfigJSON(%q) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}

func TestHeadBranchFromHEAD(t *testing.T) {
	cases := []struct {
		head string
		want string
	}{
		{"ref: refs/heads/main\n", "main"},
		{"ref: refs/heads/feature/x", "feature/x"},
		{"ref:refs/heads/main", "main"},
		{"  ref: refs/heads/main  ", "main"},
		{"ref: refs/tags/v1", ""}, // a tag is not a checked-out branch
		{"ref: HEAD", ""},
		{"0123456789abcdef", ""}, // detached: a hash protects nothing
		{"", ""},
	}
	for _, tc := range cases {
		if got := headBranchFromHEAD(tc.head); got != tc.want {
			t.Errorf("headBranchFromHEAD(%q) = %q, want %q", tc.head, got, tc.want)
		}
	}
}

// TestPushRefusesMissingTree is the regression test for a push that trimmed
// itself down to what it could read and then said "pushed".
//
// Every walk that collects objects to move used to print "warning: tree ...
// missing, skipping" and return nil. Collection finishes before the first
// object is copied and before the ref is written, so what reached the target
// was a branch pointing at a commit whose tree it did not have — a repository
// that looks pushed and cannot be checked out. An object the walk cannot read
// has to fail the push instead.
func TestPushRefusesMissingTree(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "qin-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)
	localDir, err := ioutil.TempDir("", "qin-local-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localDir)

	if _, err := InitBare(remoteDir); err != nil {
		t.Fatal(err)
	}
	local, err := Init(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(localDir, "f.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := local.AddFile(filepath.Join(localDir, "f.txt")); err != nil {
		t.Fatal(err)
	}
	commitHash, err := local.WriteCommit("Test", "commit")
	if err != nil {
		t.Fatal(err)
	}
	if err := local.SaveRemote("origin", remoteDir); err != nil {
		t.Fatal(err)
	}

	// Damage the local store the way a name it cannot store damages it: the
	// commit is there, the tree it names is not.
	commit, err := local.LoadCommit(commitHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(local.objectPath(commit.Tree)); err != nil {
		t.Fatal(err)
	}

	err = local.Push("origin", false)
	if err == nil {
		t.Fatal("push reported success with the commit's tree missing")
	}
	if !strings.Contains(err.Error(), commit.Tree.Short()) {
		t.Errorf("error does not name the unreadable tree: %v", err)
	}

	// A push that cannot move everything must move nothing — the ref is what
	// would make the target look complete.
	remote, err := Open(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	if hashStr, err := remote.ReadRef("refs/heads/main"); err == nil {
		t.Errorf("failed push wrote the target's ref: %s", hashStr)
	}
	objects, err := remote.enumerateAllObjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Errorf("failed push left %d objects on the target", len(objects))
	}
}

// TestPullAbortsWhenChunkCannotBeFetched: a chunk the remote does not have
// must fail the pull before the merge. Merging anyway would move HEAD to a
// commit whose large file cannot be reconstructed.
func TestPullAbortsWhenChunkCannotBeFetched(t *testing.T) {
	remoteDir := t.TempDir()
	remote, err := Init(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	remote.Config.Core.ChunkMinSize = 128
	remote.Config.Core.ChunkThreshold = 512
	remote.Config.Core.ChunkMaxSize = 1024

	data := make([]byte, 3000)
	for i := range data {
		data[i] = byte(i % 256)
	}
	remoteFile := filepath.Join(remoteDir, "big.bin")
	if err := ioutil.WriteFile(remoteFile, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := remote.AddFile(remoteFile); err != nil {
		t.Fatal(err)
	}
	tip, err := remote.WriteCommit("Test", "chunked")
	if err != nil {
		t.Fatal(err)
	}

	commit, err := remote.LoadCommit(tip)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := remote.LoadTree(commit.Tree)
	if err != nil {
		t.Fatal(err)
	}
	var manifestHash core.Hash
	for _, entry := range tree.Entries {
		if entry.Name == "big.bin" {
			manifestHash = entry.Hash
		}
	}
	if manifestHash.IsZero() {
		t.Fatal("fixture tree has no big.bin")
	}
	manifest, err := remote.LoadChunkManifest(manifestHash)
	if err != nil {
		t.Fatalf("fixture should be chunked: %v", err)
	}
	if len(manifest.Chunks) == 0 {
		t.Fatal("fixture manifest has no chunks")
	}
	if err := os.Remove(remote.objectPath(manifest.Chunks[0].Hash)); err != nil {
		t.Fatal(err)
	}

	localDir := t.TempDir()
	local, err := Init(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.SaveRemote("origin", remoteDir); err != nil {
		t.Fatal(err)
	}
	if err := local.SetHEAD("ref: refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	// The pull-side scan only treats entries above its own chunk threshold as
	// large files, so match the remote's lowered setting.
	local.Config.Core.ChunkThreshold = 512

	_, err = local.Pull("origin")
	if err == nil {
		t.Fatal("pull should fail when a chunk cannot be fetched")
	}
	if !strings.Contains(err.Error(), "chunk") {
		t.Fatalf("error should name the chunk: %v", err)
	}
	if _, refErr := local.ReadRef("refs/heads/main"); refErr == nil {
		t.Fatal("pull advanced main despite the missing chunk")
	}
	if _, statErr := os.Lstat(filepath.Join(localDir, "big.bin")); statErr == nil {
		t.Fatal("pull checked out the worktree despite the missing chunk")
	}
}

// TestFailedCloneLeavesNothingBehind: a clone that fails rolls back what it
// created. A directory it made is removed whole; an existing directory keeps
// the user's files and loses only the .qin the clone added.
func TestFailedCloneLeavesNothingBehind(t *testing.T) {
	source := filepath.Join(t.TempDir(), "missing")

	newDir := filepath.Join(t.TempDir(), "clone")
	if _, err := Clone(source, newDir, false); err == nil {
		t.Fatal("clone from a missing remote should fail")
	}
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Fatalf("failed clone left %s behind", newDir)
	}

	existing := t.TempDir()
	userFile := filepath.Join(existing, "keep.txt")
	if err := ioutil.WriteFile(userFile, []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Clone(source, existing, false); err == nil {
		t.Fatal("clone into an existing directory from a missing remote should fail")
	}
	if _, err := ioutil.ReadFile(userFile); err != nil {
		t.Fatalf("failed clone removed the user's file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(existing, QinDir)); !os.IsNotExist(err) {
		t.Fatal("failed clone left its .qin behind")
	}

	// A directory that is already a repository is refused, and the refusal
	// must not delete the repository Clone did not create.
	repoDir := t.TempDir()
	if _, err := Init(repoDir); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repoDir, QinDir, "marker")
	if err := ioutil.WriteFile(marker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Clone(source, repoDir, false); err == nil {
		t.Fatal("clone into an existing repository should fail")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("failed clone removed a repository it did not create")
	}
}
