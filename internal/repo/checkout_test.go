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
