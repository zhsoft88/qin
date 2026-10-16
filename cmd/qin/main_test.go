package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/zhsoft88/qin/internal/repo"
)

// The add walkers live in this package, and until this file they had no test at
// all — which is how a walk that followed symlinks survived. These drive the
// walk the way runAdd does.

func newAddRepo(t *testing.T) (*repo.Repository, string) {
	t.Helper()
	dir, err := ioutil.TempDir("", "lo-add-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	r, err := repo.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

func writeAt(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func linkAt(t *testing.T, dir, rel, target string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, full); err != nil {
		t.Fatal(err)
	}
}

// indexAfterAddOSExpr is runAdd's `--os` path: same walker, same expression,
// index left unsaved.
func indexAfterAddOSExpr(t *testing.T, r *repo.Repository, path string) *repo.Index {
	t.Helper()
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	ignorer, err := r.LoadIgnoreMatcher()
	if err != nil {
		t.Fatal(err)
	}
	added := 0
	expr := repo.OSName(repo.CurrentOSID())
	if err := addFileOrDirExpr(r, filepath.Join(r.Path, path), expr, nil, &added, idx, ignorer); err != nil {
		t.Fatal(err)
	}
	return idx
}

func indexedPaths(idx *repo.Index) []string {
	var out []string
	for key := range idx.Entries {
		p, _ := repo.ParseKey(key)
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func entryAt(t *testing.T, idx *repo.Index, path string) repo.IndexEntry {
	t.Helper()
	for key, e := range idx.Entries {
		if p, _ := repo.ParseKey(key); p == path {
			return e
		}
	}
	t.Fatalf("%s is not in the index; indexed: %v", path, indexedPaths(idx))
	return repo.IndexEntry{}
}

// assertSymlinkEntry checks that an entry stores the link itself: a symlink
// mode and a blob whose content is the target string.
func assertSymlinkEntry(t *testing.T, r *repo.Repository, e repo.IndexEntry, wantTarget string) {
	t.Helper()
	if !repo.IsSymlinkMode(e.Mode) {
		t.Fatalf("mode = %#x, want a symlink mode", e.Mode)
	}
	content, err := r.LoadFileContent(e.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != wantTarget {
		t.Fatalf("stored content = %q, want the link target %q", content, wantTarget)
	}
}

// TestAddDoesNotFollowSymlinkToDir is the regression test for the shape cipd
// writes: .cipd/pkgs/<n>/_current is a symlink to a sibling directory.
//
// The walk decided directory-vs-file with os.Stat, which resolves the link, so
// the target's contents were recorded under the link's path and the link itself
// was never recorded. status then called it untracked forever: its directory
// entries come from Readdir and so carry Lstat semantics — a symlink is not a
// directory there — and nothing else looks at the paths behind it.
func TestAddDoesNotFollowSymlinkToDir(t *testing.T) {
	r, dir := newAddRepo(t)
	writeAt(t, dir, "pkg/real/thing.txt", "payload")
	writeAt(t, dir, "pkg/real/sub/deep.txt", "deep")
	linkAt(t, dir, "pkg/_current", "real")

	idx := indexAfterAddOSExpr(t, r, "pkg")

	e := entryAt(t, idx, "pkg/_current")
	assertSymlinkEntry(t, r, e, "real")
	if e.OSS != repo.CurrentOSID() {
		t.Fatalf("link OS mask = %d, want the --os mask %d", e.OSS, repo.CurrentOSID())
	}

	// One assertion covers three claims: the link is indexed, nothing was
	// indexed *through* it, and the directory it points at is still walked.
	want := []string{"pkg/_current", "pkg/real/sub/deep.txt", "pkg/real/thing.txt"}
	if got := indexedPaths(idx); !reflect.DeepEqual(got, want) {
		t.Fatalf("indexed %v, want %v", got, want)
	}
}

// TestAddStoresSymlinkToFile covers the case that already worked — the walk
// took the file branch, because Stat resolves a link to a file to a non-dir —
// so it is here to keep it working.
func TestAddStoresSymlinkToFile(t *testing.T) {
	r, dir := newAddRepo(t)
	writeAt(t, dir, "target.txt", "alphabet")
	linkAt(t, dir, "pkg/link", "../target.txt")

	idx := indexAfterAddOSExpr(t, r, "pkg/link")

	assertSymlinkEntry(t, r, entryAt(t, idx, "pkg/link"), "../target.txt")
}

// TestAddStoresDanglingSymlink: a link whose target does not exist is still a
// link, and Stat would have failed on it outright.
func TestAddStoresDanglingSymlink(t *testing.T) {
	r, dir := newAddRepo(t)
	writeAt(t, dir, "pkg/.keep", "")
	linkAt(t, dir, "pkg/gone", "nowhere")

	idx := indexAfterAddOSExpr(t, r, "pkg/gone")

	assertSymlinkEntry(t, r, entryAt(t, idx, "pkg/gone"), "nowhere")
}

// TestAddWithoutOSExprDoesNotFollowSymlinks covers the other walker: runAdd
// picks between the two by whether an OS expression was given, and both had the
// same os.Stat.
func TestAddWithoutOSExprDoesNotFollowSymlinks(t *testing.T) {
	r, dir := newAddRepo(t)
	writeAt(t, dir, "pkg/real/thing.txt", "payload")
	linkAt(t, dir, "pkg/_current", "real")

	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	ignorer, err := r.LoadIgnoreMatcher()
	if err != nil {
		t.Fatal(err)
	}
	added := 0
	if err := addFileOrDir(r, filepath.Join(dir, "pkg"), nil, &added, idx, ignorer); err != nil {
		t.Fatal(err)
	}

	e := entryAt(t, idx, "pkg/_current")
	assertSymlinkEntry(t, r, e, "real")
	if e.OSS != 0 {
		t.Fatalf("OS mask = %d, want 0 without --os", e.OSS)
	}
	want := []string{"pkg/_current", "pkg/real/thing.txt"}
	if got := indexedPaths(idx); !reflect.DeepEqual(got, want) {
		t.Fatalf("indexed %v, want %v", got, want)
	}
}
