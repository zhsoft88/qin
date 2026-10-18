package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
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
