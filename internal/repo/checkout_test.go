package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestCheckout(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create and commit a file
	testFile := filepath.Join(dir, "hello.txt")
	if err := ioutil.WriteFile(testFile, []byte("checkout test"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddFile(testFile); err != nil {
		t.Fatal(err)
	}
	commitHash, err := repo.WriteCommit("Test", "first")
	if err != nil {
		t.Fatal(err)
	}

	// Remove the file and clear index
	os.Remove(testFile)
	repo.RemoveFile("hello.txt")

	// Verify file is gone
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Fatal("file should have been deleted")
	}

	// Checkout the commit
	if err := repo.Checkout(commitHash); err != nil {
		t.Fatal(err)
	}

	// Verify file is restored
	data, err := ioutil.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "checkout test" {
		t.Fatalf("expected 'checkout test', got '%s'", data)
	}

	// Verify index is updated
	files, err := repo.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["hello.txt"]; !ok {
		t.Fatal("expected hello.txt in index after checkout")
	}
}

func TestCheckoutChunkedFile(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a file that will be chunked
	data := make([]byte, 10000)
	for i := range data {
		data[i] = byte(i)
	}
	testFile := filepath.Join(dir, "large.bin")
	if err := ioutil.WriteFile(testFile, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddFile(testFile); err != nil {
		t.Fatal(err)
	}
	commitHash, err := repo.WriteCommit("Test", "chunked file")
	if err != nil {
		t.Fatal(err)
	}

	// Remove file and checkout
	os.Remove(testFile)
	if err := repo.Checkout(commitHash); err != nil {
		t.Fatal(err)
	}

	restored, err := ioutil.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != len(data) {
		t.Fatalf("size mismatch: %d vs %d", len(restored), len(data))
	}
	for i := range data {
		if restored[i] != data[i] {
			t.Fatalf("byte %d mismatch: %d vs %d", i, restored[i], data[i])
		}
	}
}

func TestResolveRefFullHash(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(dir, "a.txt")
	ioutil.WriteFile(testFile, []byte("a"), 0644)
	repo.AddFile(testFile)
	h, err := repo.WriteCommit("Test", "msg")
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := repo.ResolveRef(h.String())
	if err != nil {
		t.Fatal(err)
	}
	if resolved != h {
		t.Fatalf("expected %s, got %s", h, resolved)
	}
}

func TestResolveRefHEAD(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(dir, "a.txt")
	ioutil.WriteFile(testFile, []byte("a"), 0644)
	repo.AddFile(testFile)
	h, err := repo.WriteCommit("Test", "msg")
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := repo.ResolveRef("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != h {
		t.Fatalf("expected %s, got %s", h, resolved)
	}
}

func TestResolveRefBranch(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(dir, "a.txt")
	ioutil.WriteFile(testFile, []byte("a"), 0644)
	repo.AddFile(testFile)
	h, err := repo.WriteCommit("Test", "msg")
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := repo.ResolveRef("main")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != h {
		t.Fatalf("expected %s, got %s", h, resolved)
	}
}

func TestResolveRefShortHash(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(dir, "a.txt")
	ioutil.WriteFile(testFile, []byte("a"), 0644)
	repo.AddFile(testFile)
	h, err := repo.WriteCommit("Test", "msg")
	if err != nil {
		t.Fatal(err)
	}

	short := h.Short() // first 16 hex chars
	resolved, err := repo.ResolveRef(short)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != h {
		t.Fatalf("expected %s, got %s", h, resolved)
	}
}

// TestCheckoutRestoresSymlink is the round trip for the shape add now records:
// a link comes back as a link, and the directory it points at is not duplicated
// alongside it.
//
// The second half is the point of the test. A walk that followed the link would
// have committed the target's contents under the link's path, and this restore
// would then produce two copies of a tree that exists once — the duplication
// would survive checkout, and nothing downstream could tell it apart from real
// content.
func TestCheckoutRestoresSymlink(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	written := filepath.Join(dir, "pkg", "real", "thing.txt")
	if err := os.MkdirAll(filepath.Dir(written), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(written, []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "pkg", "_current")
	if err := os.Symlink("real", link); err != nil {
		t.Fatal(err)
	}

	if err := r.AddFile(written); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFile(link); err != nil {
		t.Fatal(err)
	}
	commitHash, err := r.WriteCommit("Test", "links")
	if err != nil {
		t.Fatal(err)
	}

	// Explicitly opt in to real links: the platform default is false on
	// Windows, where this test's os.Symlink setup already requires the
	// privilege that makes a real link possible.
	on := true
	r.Config.Core.Symlinks = &on

	// Wipe the worktree, so everything that comes back comes from the commit.
	if err := os.RemoveAll(filepath.Join(dir, "pkg")); err != nil {
		t.Fatal(err)
	}
	if err := r.Checkout(commitHash); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("_current was not restored: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("_current restored as mode %v, want a symlink", fi.Mode())
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != "real" {
		t.Fatalf("link target = %q, want %q", target, "real")
	}
	// Reading through the link reaches the real file. That is only meaningful
	// together with the Lstat above: it rules out a copy holding the same bytes
	// in a directory that happens to be named _current.
	data, err := ioutil.ReadFile(filepath.Join(link, "thing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Fatalf("thing.txt through the link = %q, want %q", data, "payload")
	}

	// Both paths, once each: no entry under the link's path.
	files, err := r.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for name := range files {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"pkg/_current", "pkg/real/thing.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("index after checkout = %v, want %v", got, want)
	}
}

// TestCheckoutWritesDegradedSymlinkAsFile is the git core.symlinks=false
// shape: the link entry materializes as a regular file holding the target
// text, keeps its symlink mode in the index, and status compares that text so
// the checkout is clean. Editing the file rewrites the target, and add keeps
// the recorded type instead of turning it into a regular file.
func TestCheckoutWritesDegradedSymlinkAsFile(t *testing.T) {
	r, repoDir := newTempRepo(t)
	off := false
	r.Config.Core.Symlinks = &off

	const target = "../real"
	hostileCommit(t, r, []hostileEntry{
		{name: "a", mode: SymlinkMode, content: target},
		{name: "real", mode: 0644, content: "payload"},
	})
	if err := r.SwitchBranch("main"); err != nil {
		t.Fatal(err)
	}

	full := filepath.Join(repoDir, "a")
	fi, err := os.Lstat(full)
	if err != nil {
		t.Fatalf("a was not materialized: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("a was created as a real symlink, want a plain file")
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("a mode = %v, want a regular file", fi.Mode())
	}
	data, err := ioutil.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != target {
		t.Fatalf("a contains %q, want the link target %q", data, target)
	}

	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := idx.Entries[entryKey("a", 0)]
	if !ok || !IsSymlinkMode(entry.Mode) {
		t.Fatalf("index lost the symlink type: %+v", entry)
	}

	st, err := r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Modified) != 0 {
		t.Fatalf("degraded checkout is not clean: modified = %v", st.Modified)
	}

	// Editing the plain file edits the recorded target.
	if err := ioutil.WriteFile(full, []byte("../other"), 0644); err != nil {
		t.Fatal(err)
	}
	st, err = r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range st.Modified {
		if p == "a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("changed target not reported: modified = %v", st.Modified)
	}

	if err := r.AddFile(full); err != nil {
		t.Fatal(err)
	}
	idx, err = r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	entry = idx.Entries[entryKey("a", 0)]
	if !IsSymlinkMode(entry.Mode) {
		t.Fatalf("add turned the link into a regular file: mode = %v", entry.Mode)
	}
	content, err := r.LoadFileContent(entry.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "../other" {
		t.Fatalf("stored target = %q, want %q", content, "../other")
	}
}

// TestCheckoutDegradedSymlinkByPlatformDefault pins the Windows default
// without needing a Windows host: an unset core.symlinks key must degrade.
func TestCheckoutDegradedSymlinkByPlatformDefault(t *testing.T) {
	old := hostGOOS
	hostGOOS = func() string { return "windows" }
	defer func() { hostGOOS = old }()

	r, repoDir := newTempRepo(t)
	if r.Config.Core.Symlinks != nil {
		t.Fatalf("fresh config should leave core.symlinks unset, got %v", *r.Config.Core.Symlinks)
	}
	hostileCommit(t, r, []hostileEntry{{name: "a", mode: SymlinkMode, content: "real"}})
	if err := r.SwitchBranch("main"); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(filepath.Join(repoDir, "a"))
	if err != nil {
		t.Fatalf("a was not materialized: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		t.Fatalf("a mode = %v, want a regular file by the windows default", fi.Mode())
	}
}

func TestResolveRefNotFound(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ResolveRef("nonexistent"); err == nil {
		t.Fatal("expected error for nonexistent ref")
	}
}
