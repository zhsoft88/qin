package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
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

// newRepoWithParent creates the repository in parent/repo and returns the
// parent too, so a test can put a list file outside the repository.
func newRepoWithParent(t *testing.T) (*repo.Repository, string, string) {
	t.Helper()
	parent := t.TempDir()
	dir := filepath.Join(parent, "repo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	r, err := repo.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r, dir, parent
}

// chdir changes the process working directory for the rest of the test. The
// cmd/qin tests do not run in parallel, so a process-wide chdir is safe here;
// update this comment if that ever changes. Cleanups are LIFO, so this restore
// runs before t.TempDir's removal — which matters on Windows, where the tree
// cannot be removed while it is the current directory.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
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

// masksAt lists the OS masks stored for path, sorted. A path with both a
// default and an OS variant has two entries under different keys.
func masksAt(idx *repo.Index, path string) []uint8 {
	var out []uint8
	for key := range idx.Entries {
		if p, m := repo.ParseKey(key); p == path {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
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

// TestAddOSExprSplitsDefaultVariant: a default entry means "every OS", so an
// --os add on the same path removes it and gives every other known OS an
// explicit variant holding the content the default held. The tagged OS gets
// the content on disk; nobody loses what they saw before.
func TestAddOSExprSplitsDefaultVariant(t *testing.T) {
	r, dir := newAddRepo(t)
	host := repo.CurrentOSID()
	if host == 0 {
		t.Skip("unknown host OS")
	}

	writeAt(t, dir, "pkg/thing.txt", "old")
	idx := indexAfterAdd(t, r, "pkg")
	if got := masksAt(idx, "pkg/thing.txt"); !reflect.DeepEqual(got, []uint8{0}) {
		t.Fatalf("plain add masks = %v, want the default", got)
	}
	if err := r.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}

	writeAt(t, dir, "pkg/thing.txt", "new")
	idx = indexAfterAddOSExpr(t, r, "pkg")
	want := []uint8{repo.OSWin, repo.OSMac, repo.OSLinux}
	if got := masksAt(idx, "pkg/thing.txt"); !reflect.DeepEqual(got, want) {
		t.Fatalf("--os add masks = %v, want %v", got, want)
	}
	oldHash := core.HashFromBytes([]byte("old"))
	newHash := core.HashFromBytes([]byte("new"))
	for _, mask := range want {
		e, ok := idx.Entries[repo.EntryKey("pkg/thing.txt", mask)]
		if !ok {
			t.Errorf("missing variant for mask %d", mask)
			continue
		}
		wantHash := oldHash
		if mask == host {
			wantHash = newHash
		}
		if e.ContentHash != wantHash {
			t.Errorf("variant %d holds the wrong content", mask)
		}
	}
}

// TestPlainAddKeepsOSVariant is the other half: after a path is tagged, a plain
// add updates the variant this host sees. Writing a default instead would be
// shadowed here (the add would look like a no-op) and visible elsewhere.
func TestPlainAddKeepsOSVariant(t *testing.T) {
	r, dir := newAddRepo(t)
	host := repo.CurrentOSID()
	if host == 0 {
		t.Skip("unknown host OS")
	}

	writeAt(t, dir, "pkg/thing.txt", "one")
	idx := indexAfterAddOSExpr(t, r, "pkg")
	if err := r.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}

	writeAt(t, dir, "pkg/thing.txt", "two")
	idx = indexAfterAdd(t, r, "pkg")
	if got := masksAt(idx, "pkg/thing.txt"); !reflect.DeepEqual(got, []uint8{host}) {
		t.Fatalf("plain add masks = %v, want only the host's %d", got, host)
	}
	e := entryAt(t, idx, "pkg/thing.txt")
	if e.ContentHash != core.HashFromBytes([]byte("two")) {
		t.Error("plain add did not update the visible variant")
	}
}

// TestExpandAtFiles pins the list-file contract shared by positional add
// arguments and --exclude: the list path resolves against baseDir unless it is
// absolute, and its lines are returned verbatim so the walkers treat them like
// literal arguments.
func TestExpandAtFiles(t *testing.T) {
	base := t.TempDir()
	listDir := t.TempDir()

	absList := filepath.Join(listDir, "files.txt")
	content := "a.txt\n\n   \n# comment\n  sub/b.txt  \n@literal\n"
	if err := ioutil.WriteFile(absList, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := expandAtFiles([]string{"plain", "@" + absList}, base, base, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"plain", "a.txt", "sub/b.txt", "@literal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expanded %v, want %v", got, want)
	}

	// Relative list paths, including directory components, resolve against
	// baseDir. The entries themselves are untouched.
	if err := os.MkdirAll(filepath.Join(base, "lists"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(base, "lists", "rel.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = expandAtFiles([]string{"@lists/rel.txt"}, base, base, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("relative @file expanded to %v, want [x]", got)
	}

	// A missing list is a hard error with no literal fallback.
	if _, err := expandAtFiles([]string{"@nope.txt"}, base, base, true); err == nil {
		t.Fatal("a missing @file must be an error")
	} else if !strings.Contains(err.Error(), "read @file") {
		t.Fatalf("error = %v, want it to name the @file", err)
	}
}

// TestRunAddPositionalAtFileExternal drives the real command: the list file
// lives outside the repository, and its entries resolve from the process CWD.
func TestRunAddPositionalAtFileExternal(t *testing.T) {
	r, repoDir, parent := newRepoWithParent(t)
	writeAt(t, repoDir, "a.txt", "a")
	writeAt(t, repoDir, "sub/b.txt", "b")
	writeAt(t, repoDir, "c.txt", "c")

	listDir := filepath.Join(parent, "lists")
	if err := os.MkdirAll(listDir, 0755); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(listDir, "files.txt")
	if err := ioutil.WriteFile(list, []byte("a.txt\nsub/b.txt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repoDir)

	// Relative @file with directory components, outside the repo.
	if err := runAdd([]string{"@" + filepath.Join("..", "lists", "files.txt")}); err != nil {
		t.Fatal(err)
	}
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got := indexedPaths(idx); !reflect.DeepEqual(got, []string{"a.txt", "sub/b.txt"}) {
		t.Fatalf("indexed %v, want the two listed files", got)
	}

	// Absolute @file takes the filepath.IsAbs branch.
	absList := filepath.Join(listDir, "abs.txt")
	if err := ioutil.WriteFile(absList, []byte("c.txt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runAdd([]string{"@" + absList}); err != nil {
		t.Fatal(err)
	}
	idx, err = r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got := indexedPaths(idx); !reflect.DeepEqual(got, []string{"a.txt", "c.txt", "sub/b.txt"}) {
		t.Fatalf("indexed %v, want the third file from the absolute list", got)
	}
}

// TestRunAddExcludeAtFileExternal covers the other caller of expandAtFiles:
// --exclude @file may also point outside the repository.
func TestRunAddExcludeAtFileExternal(t *testing.T) {
	r, repoDir, parent := newRepoWithParent(t)
	writeAt(t, repoDir, "keep.txt", "keep")
	writeAt(t, repoDir, "debug.log", "log")

	list := filepath.Join(parent, "excludes.txt")
	if err := ioutil.WriteFile(list, []byte("*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repoDir)

	if err := runAdd([]string{"--exclude", "@" + list, "."}); err != nil {
		t.Fatal(err)
	}
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got := indexedPaths(idx); !reflect.DeepEqual(got, []string{"keep.txt"}) {
		t.Fatalf("indexed %v, want only keep.txt", got)
	}
}

// TestRunAddAtFileEscapingEntryRejected: a list entry is resolved like any
// other add argument, so one that lands outside the repository is refused by
// addFileToIndex's existing containment check.
func TestRunAddAtFileEscapingEntryRejected(t *testing.T) {
	r, repoDir, parent := newRepoWithParent(t)
	writeAt(t, repoDir, "a.txt", "a")
	writeAt(t, parent, "outside.txt", "outside")

	listDir := filepath.Join(parent, "lists")
	if err := os.MkdirAll(listDir, 0755); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(listDir, "files.txt")
	if err := ioutil.WriteFile(list, []byte("a.txt\n../outside.txt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repoDir)

	if err := runAdd([]string{"@" + filepath.Join("..", "lists", "files.txt")}); err == nil {
		t.Fatal("an entry outside the repository must fail the add")
	}
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got := indexedPaths(idx); !reflect.DeepEqual(got, []string{"a.txt"}) {
		t.Fatalf("indexed %v, want only a.txt", got)
	}

	// Pin the reason: the existing predicate, not a second rule in the
	// expander, is what refuses it.
	ignorer, err := r.LoadIgnoreMatcher()
	if err != nil {
		t.Fatal(err)
	}
	added := 0
	err = addFileOrDir(r, "../outside.txt", nil, &added, idx, ignorer)
	if err == nil || !strings.Contains(err.Error(), "path outside repository") {
		t.Fatalf("addFileOrDir error = %v, want path outside repository", err)
	}
}

// TestRunAddGlobsFromList drives the real command with wildcard lines: the
// expansion feeds the existing walkers, so what lands in the index is what an
// equivalent literal argument would have staged.
func TestRunAddGlobsFromList(t *testing.T) {
	r, repoDir, parent := newRepoWithParent(t)
	for _, rel := range []string{"src/a.go", "src/sub/b.go", "top.go", ".hidden.go"} {
		writeAt(t, repoDir, rel, rel)
	}
	list := filepath.Join(parent, "globs.txt")
	if err := ioutil.WriteFile(list, []byte("src/*.go\n"), 0644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repoDir)
	at := "@" + filepath.Join("..", "globs.txt")

	if err := runAdd([]string{at}); err != nil {
		t.Fatal(err)
	}
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got := indexedPaths(idx); !reflect.DeepEqual(got, []string{"src/a.go"}) {
		t.Fatalf("indexed %v, want only src/a.go", got)
	}

	// "**" is recursive and still refuses dot names.
	if err := ioutil.WriteFile(list, []byte("**/*.go\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runAdd([]string{at}); err != nil {
		t.Fatal(err)
	}
	idx, err = r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"src/a.go", "src/sub/b.go", "top.go"}
	if got := indexedPaths(idx); !reflect.DeepEqual(got, want) {
		t.Fatalf("indexed %v, want %v", got, want)
	}
}

// TestRunAddGlobNoMatchFails: an unmatched pattern is kept literally, so the
// add walker's no-such-file error reaches the exit status instead of the
// pattern vanishing.
func TestRunAddGlobNoMatchFails(t *testing.T) {
	r, repoDir := newAddRepo(t)
	writeAt(t, repoDir, "a.txt", "a")
	list := filepath.Join(t.TempDir(), "bad.txt")
	if err := ioutil.WriteFile(list, []byte("nomatch/*.go\n"), 0644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repoDir)

	if err := runAdd([]string{"@" + list}); err == nil {
		t.Fatal("an unmatched pattern must fail the add")
	}
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got := indexedPaths(idx); len(got) != 0 {
		t.Fatalf("indexed %v, want nothing", got)
	}
}

// TestRunAddGlobDoesNotFollowSymlink: the shell would descend through a
// symlinked directory; qin's walkers never do, and expansion must not quietly
// reintroduce what they refuse.
func TestRunAddGlobDoesNotFollowSymlink(t *testing.T) {
	r, repoDir, parent := newRepoWithParent(t)
	writeAt(t, repoDir, "real.txt", "real")
	writeAt(t, parent, "outside/x.txt", "x")
	if err := os.Symlink(filepath.Join(parent, "outside"), filepath.Join(repoDir, "link")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	list := filepath.Join(parent, "globs.txt")
	if err := ioutil.WriteFile(list, []byte("**/*.txt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repoDir)

	if err := runAdd([]string{"@" + filepath.Join("..", "globs.txt")}); err != nil {
		t.Fatal(err)
	}
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got := indexedPaths(idx); !reflect.DeepEqual(got, []string{"real.txt"}) {
		t.Fatalf("indexed %v, want only real.txt (link must not be followed)", got)
	}
}

// ---- ls filters ----

// captureStdout runs fn with os.Stdout replaced by a pipe and returns what it
// printed. The package's tests are not parallel, so swapping the process-wide
// os.Stdout is safe here.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wr
	defer func() {
		os.Stdout = old
		rd.Close()
	}()

	done := make(chan string, 1)
	go func() {
		b, _ := ioutil.ReadAll(rd)
		done <- string(b)
	}()

	runErr := fn()
	wr.Close()
	return <-done, runErr
}

// lsOutputPaths extracts the repo path from each `qin ls` line, whose format
// is: "<short hash>  <size>  [<os>]  <path>[  [N chunks]]".
func lsOutputPaths(out string) []string {
	var paths []string
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		i := strings.Index(line, "]  ")
		if i < 0 {
			continue
		}
		p := line[i+3:]
		if j := strings.Index(p, "  ["); j >= 0 {
			p = p[:j]
		}
		paths = append(paths, p)
	}
	return paths
}

func TestResolveLsFilter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	cwd := root

	cases := []struct {
		name    string
		arg     string
		ok      bool
		pattern string
		glob    bool
	}{
		{"file", "a.txt", true, "a.txt", false},
		{"dir", "src", true, "src", false},
		{"root", ".", true, "", false},
		{"dotdot cleanup", filepath.Join("src", "..", "a.txt"), true, "a.txt", false},
		{"glob after cleanup", "./src/../*.go", true, "*.go", true},
		{"glob", "src/*.go", true, "src/*.go", true},
		{"globstar", "src/**", true, "src/**", true},
		{"absolute glob", filepath.Join(root, "src", "*.go"), true, "src/*.go", true},
		{"outside", filepath.Join("..", "out.txt"), false, "", false},
	}
	for _, tc := range cases {
		f, ok := resolveLsFilter(tc.arg, cwd, root)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if f.pattern != tc.pattern || f.glob != tc.glob {
			t.Errorf("%s: filter = {%q, glob=%v}, want {%q, glob=%v}",
				tc.name, f.pattern, f.glob, tc.pattern, tc.glob)
		}
	}

	if runtime.GOOS == "windows" {
		return // backslash is a separator there, not an escape
	}
	escaped := []struct {
		arg     string
		pattern string
		glob    bool
	}{
		{`a\*b`, "a*b", false},
		{`docs\[old\]`, "docs[old]", false},
		{`a\*b/*.go`, `a\*b/*.go`, true},
	}
	for _, tc := range escaped {
		f, ok := resolveLsFilter(tc.arg, cwd, root)
		if !ok || f.pattern != tc.pattern || f.glob != tc.glob {
			t.Errorf("%q: filter = {%q, glob=%v}, ok=%v; want {%q, glob=%v}",
				tc.arg, f.pattern, f.glob, ok, tc.pattern, tc.glob)
		}
	}
}

func TestLsMatchesAny(t *testing.T) {
	lit := func(p string) lsFilter { return lsFilter{pattern: p} }
	glob := func(p string) lsFilter { return lsFilter{pattern: p, glob: true} }

	cases := []struct {
		name    string
		path    string
		filters []lsFilter
		want    bool
	}{
		{"literal dir itself", "src", []lsFilter{lit("src")}, true},
		{"literal child", "src/a.go", []lsFilter{lit("src")}, true},
		{"literal does not match sibling prefix", "srcx/a.go", []lsFilter{lit("src")}, false},
		{"literal is anchored", "x/src/a.go", []lsFilter{lit("src")}, false},
		{"root matches everything", "anything/x", []lsFilter{lit("")}, true},
		{"glob top level", "a.go", []lsFilter{glob("*.go")}, true},
		{"glob does not cross a slash", "src/a.go", []lsFilter{glob("*.go")}, false},
		{"globstar matches the dir itself", "src", []lsFilter{glob("src/**")}, true},
		{"globstar matches deep", "src/deep/x.go", []lsFilter{glob("src/**")}, true},
		{"single star stays one level", "src/deep/x.go", []lsFilter{glob("src/*.go")}, false},
		{"literal star name", "*.go", []lsFilter{lit("*.go")}, true},
		{"literal star is not a pattern", "a.go", []lsFilter{lit("*.go")}, false},
		{"union", "b.txt", []lsFilter{lit("a.txt"), glob("*.txt")}, true},
		{"no filters", "a.go", nil, false},
	}
	for _, tc := range cases {
		if got := lsMatchesAny(tc.path, tc.filters); got != tc.want {
			t.Errorf("%s: lsMatchesAny(%q) = %v, want %v", tc.name, tc.path, got, tc.want)
		}
	}
}

// stageLsRepo creates the tree shared by the runLs tests and returns its root.
func stageLsRepo(t *testing.T) string {
	t.Helper()
	_, dir := newAddRepo(t)
	for _, f := range []string{"a.go", "b.txt", "docs/readme.md", "src/main.go", "src/deep/x.go"} {
		writeAt(t, dir, f, f)
	}
	chdir(t, dir)
	if err := runAdd([]string{"a.go", "b.txt", "src", "docs"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunLsFilters(t *testing.T) {
	dir := stageLsRepo(t)

	run := func(args ...string) []string {
		t.Helper()
		out, err := captureStdout(t, func() error { return runLs(args) })
		if err != nil {
			t.Fatalf("runLs(%v): %v", args, err)
		}
		return lsOutputPaths(out)
	}

	all := []string{"a.go", "b.txt", "docs/readme.md", "src/deep/x.go", "src/main.go"}
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"no args", nil, all},
		{"exact file", []string{"a.go"}, []string{"a.go"}},
		{"directory prefix", []string{"src"}, []string{"src/deep/x.go", "src/main.go"}},
		{"nested directory", []string{"src/deep"}, []string{"src/deep/x.go"}},
		{"glob one level", []string{"*.go"}, []string{"a.go"}},
		{"globstar any depth", []string{"**/*.go"}, []string{"a.go", "src/deep/x.go", "src/main.go"}},
		{"subtree", []string{"src/**"}, []string{"src/deep/x.go", "src/main.go"}},
		{"absolute path", []string{filepath.Join(dir, "src", "main.go")}, []string{"src/main.go"}},
		{"root", []string{"."}, all},
		{"two args", []string{"a.go", "src/deep"}, []string{"a.go", "src/deep/x.go"}},
		{"no match", []string{"nomatch"}, nil},
		{"outside the repo", []string{filepath.Join("..", "outside")}, nil},
	}
	for _, tc := range cases {
		got := run(tc.args...)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: runLs(%v) = %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

func TestRunLsCWDRelative(t *testing.T) {
	dir := stageLsRepo(t)
	chdir(t, filepath.Join(dir, "src"))

	run := func(args ...string) []string {
		t.Helper()
		out, err := captureStdout(t, func() error { return runLs(args) })
		if err != nil {
			t.Fatalf("runLs(%v): %v", args, err)
		}
		return lsOutputPaths(out)
	}

	if got, want := run("*.go"), []string{"src/main.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("from src, *.go = %v, want %v", got, want)
	}
	if got, want := run("deep"), []string{"src/deep/x.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("from src, deep = %v, want %v", got, want)
	}
	writeAt(t, dir, "top.go", "top")
	if err := runAdd([]string{"../top.go"}); err != nil {
		t.Fatal(err)
	}
	if got, want := run("../top.go"), []string{"top.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("from src, ../top.go = %v, want %v", got, want)
	}
}

func TestRunLsEmptyIndex(t *testing.T) {
	_, dir := newAddRepo(t)
	chdir(t, dir)

	out, err := captureStdout(t, func() error { return runLs(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "nothing staged\n" {
		t.Errorf("no args on an empty index printed %q, want %q", out, "nothing staged\n")
	}

	out, err = captureStdout(t, func() error { return runLs([]string{"a.go"}) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("args on an empty index printed %q, want nothing", out)
	}
}

func TestRunLsShowsAllOSVariants(t *testing.T) {
	r, dir := newAddRepo(t)
	writeAt(t, dir, "pkg/tool.txt", "tool")
	chdir(t, dir)

	full := filepath.Join(dir, "pkg", "tool.txt")
	if err := r.AddFileOS(full, "win"); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFileOS(full, "linux"); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error { return runLs([]string{"pkg/tool.txt"}) })
	if err != nil {
		t.Fatal(err)
	}
	if got, want := lsOutputPaths(out), []string{"pkg/tool.txt", "pkg/tool.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("literal: paths = %v, want both variants %v", got, want)
	}
	for _, tag := range []string{"[win]", "[linux]"} {
		if !strings.Contains(out, tag) {
			t.Errorf("literal: output lacks %s:\n%s", tag, out)
		}
	}
	if strings.Contains(out, "[mac]") {
		t.Errorf("literal: output has an unstaged mac variant:\n%s", out)
	}

	out, err = captureStdout(t, func() error { return runLs([]string{"pkg/*.txt"}) })
	if err != nil {
		t.Fatal(err)
	}
	if got, want := lsOutputPaths(out), []string{"pkg/tool.txt", "pkg/tool.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("glob: paths = %v, want both variants %v", got, want)
	}
}

func TestRunLsEscapedLiteralWildcard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a wildcard is not a legal Windows filename, and \\ is a separator there")
	}
	_, dir := newAddRepo(t)
	writeAt(t, dir, "star*name.txt", "star")
	writeAt(t, dir, "starXname.txt", "x")
	chdir(t, dir)

	if err := runAdd([]string{"star*name.txt", "starXname.txt"}); err != nil {
		t.Fatal(err)
	}

	run := func(arg string) []string {
		t.Helper()
		out, err := captureStdout(t, func() error { return runLs([]string{arg}) })
		if err != nil {
			t.Fatalf("runLs(%q): %v", arg, err)
		}
		return lsOutputPaths(out)
	}

	if got, want := run(`star\*name.txt`), []string{"star*name.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("escaped: %v, want the literal file %v", got, want)
	}
	if got, want := run("star*name.txt"), []string{"star*name.txt", "starXname.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unescaped: %v, want both files %v", got, want)
	}
}

// git's -am is the usual spelling of "commit everything tracked"; Go's flag
// package would reject it, so it is rewritten before parsing.
func TestExpandCommitAll(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{[]string{"-am", "msg"}, []string{"-a", "-m", "msg"}},
		{[]string{"-am=msg"}, []string{"-a", "-m=msg"}},
		{[]string{"-ammsg"}, []string{"-a", "-m", "msg"}},
		{[]string{"-a", "-m", "msg"}, []string{"-a", "-m", "msg"}},
		{[]string{"-m", "msg", "-a"}, []string{"-m", "msg", "-a"}},
		{[]string{"--all", "-m", "msg"}, []string{"--all", "-m", "msg"}},
	}
	for _, c := range cases {
		if got := expandCommitAll(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("expandCommitAll(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
