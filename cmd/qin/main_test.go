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
	dir, err := ioutil.TempDir("", "qin-add-*")
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

// indexAfterAdd is runAdd's path when no OS expression was given: same walker,
// index left unsaved.
func indexAfterAdd(t *testing.T, r *repo.Repository, path string) *repo.Index {
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
	if err := addFileOrDir(r, filepath.Join(r.Path, path), nil, &added, idx, ignorer); err != nil {
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

// TestAddDoesNotEnterRepoMetadata is the regression test for `add .` indexing
// the repository itself: HEAD, config, the object store, the empty ref
// directories. The walk had no idea .qin was anything other than a directory
// to descend into, and what it found there was its own output — the objects it
// had just stored for the files it was reading. status skips the same
// directory, so the result was never reported as staged *or* untracked; it
// existed only in the index, which is the one place nobody looks.
func TestAddDoesNotEnterRepoMetadata(t *testing.T) {
	r, dir := newAddRepo(t)
	writeAt(t, dir, "a.txt", "hello")

	if got := indexedPaths(indexAfterAdd(t, r, ".")); !reflect.DeepEqual(got, []string{"a.txt"}) {
		t.Fatalf("add . indexed %v, want just a.txt", got)
	}

	// Naming the directory — or a file inside it — must not get around that.
	for _, p := range []string{".qin", ".qin/config"} {
		if got := indexedPaths(indexAfterAdd(t, r, p)); len(got) != 0 {
			t.Fatalf("add %s indexed %v, want nothing", p, got)
		}
	}
}

// TestAddOSExprDoesNotEnterRepoMetadata covers the other walker, which needs
// the same rule: runAdd picks between the two by whether an OS expression was
// given.
func TestAddOSExprDoesNotEnterRepoMetadata(t *testing.T) {
	r, dir := newAddRepo(t)
	writeAt(t, dir, "a.txt", "hello")

	idx := indexAfterAddOSExpr(t, r, ".")
	if got := indexedPaths(idx); !reflect.DeepEqual(got, []string{"a.txt"}) {
		t.Fatalf("add --os . indexed %v, want just a.txt", got)
	}
	if e := entryAt(t, idx, "a.txt"); e.OSS != repo.CurrentOSID() {
		t.Fatalf("a.txt OS mask = %d, want the --os mask %d", e.OSS, repo.CurrentOSID())
	}
}

// TestAddOSExprTagsEmptyDir covers the one entry whose mask the walk cannot
// read off a file: an empty directory. That branch hardcoded mask 0, so
// `add --os` tagged an empty directory as the default variant — visible on
// every platform — while the files next to it carried the requested mask.
func TestAddOSExprTagsEmptyDir(t *testing.T) {
	r, dir := newAddRepo(t)
	writeAt(t, dir, "pkg/thing.txt", "payload")
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "empty"), 0755); err != nil {
		t.Fatal(err)
	}

	idx := indexAfterAddOSExpr(t, r, "pkg")

	want := repo.CurrentOSID()
	if e := entryAt(t, idx, "pkg/empty"); e.OSS != want {
		t.Fatalf("empty dir OS mask = %d, want the --os mask %d", e.OSS, want)
	}
	if e := entryAt(t, idx, "pkg/thing.txt"); e.OSS != want {
		t.Fatalf("file OS mask = %d, want the --os mask %d", e.OSS, want)
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

// TestAddStoresBackslashName drives the real walk over a name that is legal on
// this platform. The filesystem is the source of truth: anything it holds has
// to be storable. Refusing such a name made a Linux checkout of chromium
// impossible to add, and the answer belongs at the platform that cannot create
// it, not in a rule applied everywhere.
func TestAddStoresBackslashName(t *testing.T) {
	r, dir := newAddRepo(t)

	// systemd escapes unit names with a literal backslash, so a Linux checkout
	// of chromium really contains this file.
	name := `system-systemd\x2dcryptsetup.slice`
	if err := ioutil.WriteFile(filepath.Join(dir, name), []byte("unit"), 0644); err != nil {
		t.Skipf("this filesystem cannot hold a name with a backslash: %v", err)
	}
	writeAt(t, dir, "ok.slice", "fine")

	idx := indexAfterAdd(t, r, ".")

	want := []string{"ok.slice", name}
	if got := indexedPaths(idx); !reflect.DeepEqual(got, want) {
		t.Fatalf("indexed %v, want %v", got, want)
	}
}

// TestAddSkipsIgnoredName: an ignored path is a skip, not a failure. The walks
// subtract ErrIgnored with errors.Is, so `qin add .` in a tree carrying a
// .qinignore exits 0; counting skips as failures would make every such
// repository unaddable.
func TestAddSkipsIgnoredName(t *testing.T) {
	r, dir := newAddRepo(t)

	bad := `system-systemd\x2dcryptsetup.slice`
	if err := ioutil.WriteFile(filepath.Join(dir, bad), []byte("unit"), 0644); err != nil {
		t.Skipf("this filesystem cannot hold a name with a backslash: %v", err)
	}
	writeAt(t, dir, "ok.slice", "fine")
	writeAt(t, dir, ".qinignore", "system-systemd*\n")

	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	ignorer, err := r.LoadIgnoreMatcher()
	if err != nil {
		t.Fatal(err)
	}

	added := 0
	if err := addFileOrDir(r, dir, nil, &added, idx, ignorer); err != nil {
		t.Fatalf("an ignored file is a skip, not a failure: %v", err)
	}
	if got := indexedPaths(idx); !reflect.DeepEqual(got, []string{".qinignore", "ok.slice"}) {
		t.Fatalf("indexed = %v, want [.qinignore ok.slice]", got)
	}
}
