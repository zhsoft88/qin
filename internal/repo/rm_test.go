package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func rmWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := ioutil.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func rmCommit(t *testing.T, r *Repository) {
	t.Helper()
	if _, err := r.WriteCommit("test", "init"); err != nil {
		t.Fatal(err)
	}
}

func rmNames(t *testing.T, r *Repository) map[string]bool {
	t.Helper()
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(idx.Entries))
	for key := range idx.Entries {
		name, _ := parseKey(key)
		names[name] = true
	}
	return names
}

// rmChdir changes into dir for the duration of the test. It must be called
// after newTempRepo so its cleanup (LIFO) restores the CWD before the
// temporary directory is removed.
func rmChdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

func TestRemovePathsCleanFile(t *testing.T) {
	r, dir := newTempRepo(t)
	f := filepath.Join(dir, "a.txt")
	rmWrite(t, f, "one")
	if err := r.AddFile(f); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)

	results, err := r.RemovePaths([]string{f}, RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "a.txt" || !results[0].WorkTreeRemoved {
		t.Fatalf("unexpected results: %+v", results)
	}
	if _, err := os.Lstat(f); !os.IsNotExist(err) {
		t.Fatalf("worktree file survived: %v", err)
	}
	if rmNames(t, r)["a.txt"] {
		t.Fatal("index entry survived")
	}
}

// Removing a path drops every OS variant, not just the one this host sees.
func TestRemovePathsRemovesAllVariants(t *testing.T) {
	r, dir := newTempRepo(t)
	f := filepath.Join(dir, "a.txt")
	rmWrite(t, f, "one")
	if err := r.AddFile(f); err != nil {
		t.Fatal(err)
	}

	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	variant := idx.Entries["a.txt"]
	variant.OSS = OSMac
	idx.Entries[entryKey("a.txt", OSMac)] = variant
	if err := r.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)

	if _, err := r.RemovePaths([]string{f}, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	for key := range rmNames(t, r) {
		if key == "a.txt" {
			t.Fatal("name still present after removing all variants")
		}
	}
	idx, err = r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	for key := range idx.Entries {
		if name, _ := parseKey(key); name == "a.txt" {
			t.Fatalf("variant survived: %q", key)
		}
	}
}

func TestRemovePathsDirectoryRequiresRecursive(t *testing.T) {
	r, dir := newTempRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "d", "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	rmWrite(t, filepath.Join(dir, "d", "a.txt"), "a")
	rmWrite(t, filepath.Join(dir, "d", "sub", "b.txt"), "b")
	if err := r.AddFiles([]string{filepath.Join(dir, "d", "a.txt"), filepath.Join(dir, "d", "sub", "b.txt")}); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)

	_, err := r.RemovePaths([]string{filepath.Join(dir, "d")}, RemoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "recursively without -r") {
		t.Fatalf("expected recursive refusal, got %v", err)
	}
	if !rmNames(t, r)["d/a.txt"] || !rmNames(t, r)["d/sub/b.txt"] {
		t.Fatal("refusal modified the index")
	}
	if _, err := os.Stat(filepath.Join(dir, "d", "a.txt")); err != nil {
		t.Fatalf("refusal removed a worktree file: %v", err)
	}
}

// A tracked empty directory is still a directory: it needs -r.
func TestRemovePathsEmptyDirEntryRequiresRecursive(t *testing.T) {
	r, dir := newTempRepo(t)
	empty := filepath.Join(dir, "d")
	if err := os.Mkdir(empty, 0755); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFile(empty); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)

	if _, err := r.RemovePaths([]string{empty}, RemoveOptions{}); err == nil ||
		!strings.Contains(err.Error(), "recursively without -r") {
		t.Fatalf("expected recursive refusal, got %v", err)
	}

	results, err := r.RemovePaths([]string{empty}, RemoveOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].WorkTreeRemoved {
		t.Fatalf("unexpected results: %+v", results)
	}
	if _, err := os.Lstat(empty); !os.IsNotExist(err) {
		t.Fatalf("empty directory survived: %v", err)
	}
}

func TestRemovePathsRecursiveKeepsUntracked(t *testing.T) {
	r, dir := newTempRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "d", "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	rmWrite(t, filepath.Join(dir, "d", "a.txt"), "a")
	rmWrite(t, filepath.Join(dir, "d", "sub", "b.txt"), "b")
	if err := r.AddFiles([]string{filepath.Join(dir, "d", "a.txt"), filepath.Join(dir, "d", "sub", "b.txt")}); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)

	rmWrite(t, filepath.Join(dir, "d", "untracked.txt"), "keep")

	results, err := r.RemovePaths([]string{filepath.Join(dir, "d")}, RemoveOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{results[0].Name, results[1].Name}
	if names[0] != "d/a.txt" || names[1] != "d/sub/b.txt" {
		t.Fatalf("expected sorted results, got %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "d", "untracked.txt")); err != nil {
		t.Fatalf("untracked file was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "d")); err != nil {
		t.Fatalf("directory holding untracked content was removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "d", "sub")); !os.IsNotExist(err) {
		t.Fatalf("emptied subdirectory was not pruned: %v", err)
	}
	if rmNames(t, r)["d/a.txt"] || rmNames(t, r)["d/sub/b.txt"] {
		t.Fatal("index entries survived")
	}
}

func TestRemovePathsRefusesLocalModification(t *testing.T) {
	r, dir := newTempRepo(t)
	f := filepath.Join(dir, "a.txt")
	rmWrite(t, f, "one")
	if err := r.AddFile(f); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)
	rmWrite(t, f, "changed")

	_, err := r.RemovePaths([]string{f}, RemoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "local modifications") ||
		!strings.Contains(err.Error(), "--cached") || !strings.Contains(err.Error(), "-f") {
		t.Fatalf("expected local-modification refusal, got %v", err)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("refusal removed the file: %v", err)
	}
	if !rmNames(t, r)["a.txt"] {
		t.Fatal("refusal modified the index")
	}

	if _, err := r.RemovePaths([]string{f}, RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(f); !os.IsNotExist(err) {
		t.Fatalf("forced removal left the file: %v", err)
	}
}

func TestRemovePathsRefusesStagedChange(t *testing.T) {
	r, dir := newTempRepo(t)
	f := filepath.Join(dir, "a.txt")
	rmWrite(t, f, "one")
	if err := r.AddFile(f); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)
	rmWrite(t, f, "staged")
	if err := r.AddFile(f); err != nil {
		t.Fatal(err)
	}

	_, err := r.RemovePaths([]string{f}, RemoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "changes staged in the index") {
		t.Fatalf("expected staged-change refusal, got %v", err)
	}
	if !rmNames(t, r)["a.txt"] {
		t.Fatal("refusal modified the index")
	}

	if _, err := r.RemovePaths([]string{f}, RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
}

// With no commit every index entry is a staged addition, so a plain rm
// refuses; --cached is the way to unstage it.
func TestRemovePathsUnbornBranch(t *testing.T) {
	r, dir := newTempRepo(t)
	f := filepath.Join(dir, "a.txt")
	rmWrite(t, f, "one")
	if err := r.AddFile(f); err != nil {
		t.Fatal(err)
	}

	if _, err := r.RemovePaths([]string{f}, RemoveOptions{}); err == nil ||
		!strings.Contains(err.Error(), "changes staged in the index") {
		t.Fatalf("expected staged refusal, got %v", err)
	}

	if _, err := r.RemovePaths([]string{f}, RemoveOptions{Cached: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("--cached removed the file: %v", err)
	}
	if rmNames(t, r)["a.txt"] {
		t.Fatal("--cached left the index entry")
	}
}

func TestRemovePathsCachedKeepsModifiedFile(t *testing.T) {
	r, dir := newTempRepo(t)
	f := filepath.Join(dir, "a.txt")
	rmWrite(t, f, "one")
	if err := r.AddFile(f); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)
	rmWrite(t, f, "changed")

	results, err := r.RemovePaths([]string{f}, RemoveOptions{Cached: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].WorkTreeRemoved {
		t.Fatalf("unexpected results: %+v", results)
	}
	got, err := ioutil.ReadFile(f)
	if err != nil || string(got) != "changed" {
		t.Fatalf("worktree changed: %q, %v", got, err)
	}
	if rmNames(t, r)["a.txt"] {
		t.Fatal("index entry survived --cached")
	}
}

func TestRemovePathsMissingWorkTreeFile(t *testing.T) {
	r, dir := newTempRepo(t)
	f := filepath.Join(dir, "a.txt")
	rmWrite(t, f, "one")
	if err := r.AddFile(f); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}

	results, err := r.RemovePaths([]string{f}, RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].WorkTreeRemoved {
		t.Fatalf("unexpected results: %+v", results)
	}
	if rmNames(t, r)["a.txt"] {
		t.Fatal("index entry survived")
	}
}

// Regression: the old -r implementation RemoveAll'd the repo root when the
// pathspec resolved to it. The root must lose tracked files only.
func TestRemovePathsRootKeepsMetadataAndUntracked(t *testing.T) {
	r, dir := newTempRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "b"), 0755); err != nil {
		t.Fatal(err)
	}
	rmWrite(t, filepath.Join(dir, "a.txt"), "a")
	rmWrite(t, filepath.Join(dir, "b", "c.txt"), "c")
	if err := r.AddFiles([]string{filepath.Join(dir, "a.txt"), filepath.Join(dir, "b", "c.txt")}); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)
	rmWrite(t, filepath.Join(dir, "untracked.txt"), "u")

	results, err := r.RemovePaths([]string{dir}, RemoveOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 removals, got %+v", results)
	}
	if _, err := os.Stat(filepath.Join(dir, "untracked.txt")); err != nil {
		t.Fatalf("untracked file was removed: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, QinDir)); err != nil || !fi.IsDir() {
		t.Fatalf("repository metadata was removed: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("repository root was removed: %v", err)
	}
}

func TestRemovePathsOutsideRepo(t *testing.T) {
	r, dir := newTempRepo(t)
	outside := filepath.Clean(filepath.Join(dir, "..", "outside.txt"))
	rmWrite(t, outside, "keep")
	if _, err := r.RemovePaths([]string{outside}, RemoveOptions{Force: true}); err == nil ||
		!strings.Contains(err.Error(), "path outside repository") {
		t.Fatalf("expected outside-repo refusal, got %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was removed: %v", err)
	}
}

func TestRemovePathsAllOrNothing(t *testing.T) {
	r, dir := newTempRepo(t)
	f := filepath.Join(dir, "a.txt")
	rmWrite(t, f, "a")
	if err := r.AddFile(f); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)

	_, err := r.RemovePaths([]string{f, filepath.Join(dir, "missing.txt")}, RemoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "did not match any tracked files") {
		t.Fatalf("expected pathspec error, got %v", err)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("valid pathspec was applied despite the error: %v", err)
	}
	if !rmNames(t, r)["a.txt"] {
		t.Fatal("index changed despite the error")
	}
}

func TestRemovePathsEmptyDirWithUntrackedContent(t *testing.T) {
	r, dir := newTempRepo(t)
	d := filepath.Join(dir, "d")
	if err := os.Mkdir(d, 0755); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFile(d); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)
	rmWrite(t, filepath.Join(d, "untracked.txt"), "keep")

	results, err := r.RemovePaths([]string{d}, RemoveOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].WorkTreeRemoved {
		t.Fatalf("unexpected results: %+v", results)
	}
	if _, err := os.Stat(filepath.Join(d, "untracked.txt")); err != nil {
		t.Fatalf("untracked content was removed: %v", err)
	}
	if rmNames(t, r)["d"] {
		t.Fatal("index entry survived")
	}
}

// A submodule is a non-empty directory owned by another repository: remove the
// entry, never the checkout.
func TestRemovePathsSubmoduleLeavesWorktree(t *testing.T) {
	r, dir := newTempRepo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	rmWrite(t, filepath.Join(sub, "file.txt"), "submodule content")

	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	idx.Entries["sub"] = IndexEntry{Mode: SubmoduleMode}
	if err := r.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}

	results, err := r.RemovePaths([]string{sub}, RemoveOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].WorkTreeRemoved {
		t.Fatalf("unexpected results: %+v", results)
	}
	if _, err := os.Stat(filepath.Join(sub, "file.txt")); err != nil {
		t.Fatalf("submodule worktree was removed: %v", err)
	}
	if rmNames(t, r)["sub"] {
		t.Fatal("index entry survived")
	}
}

func TestRemovePathsSymlinkRemovesLinkOnly(t *testing.T) {
	r, dir := newTempRepo(t)
	target := filepath.Join(dir, "target.txt")
	rmWrite(t, target, "target")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := r.AddFile(link); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)

	if _, err := r.RemovePaths([]string{link}, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("link survived: %v", err)
	}
	if got, err := ioutil.ReadFile(target); err != nil || string(got) != "target" {
		t.Fatalf("link target was damaged: %q, %v", got, err)
	}
}

func TestRemovePathsRefusesSymlinkedParent(t *testing.T) {
	r, dir := newTempRepo(t)
	outside := filepath.Clean(filepath.Join(dir, "..", "outside"))
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "victim.txt")
	rmWrite(t, victim, "keep")
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	idx.Entries["link/victim.txt"] = IndexEntry{Mode: 0644}
	if err := r.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}

	_, err = r.RemovePaths([]string{filepath.Join(dir, "link", "victim.txt")}, RemoveOptions{Force: true})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlinked-parent refusal, got %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("file outside the repo was removed: %v", err)
	}
	if !rmNames(t, r)["link/victim.txt"] {
		t.Fatal("index changed despite the refusal")
	}
}

func TestRemovePathsFileReplacedByDirectory(t *testing.T) {
	r, dir := newTempRepo(t)
	f := filepath.Join(dir, "a.txt")
	rmWrite(t, f, "one")
	if err := r.AddFile(f); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)

	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f, 0755); err != nil {
		t.Fatal(err)
	}
	rmWrite(t, filepath.Join(f, "inner.txt"), "inner")

	if _, err := r.RemovePaths([]string{f}, RemoveOptions{}); err == nil ||
		!strings.Contains(err.Error(), "local modifications") {
		t.Fatalf("expected type-change refusal, got %v", err)
	}

	// Forced removal must not recurse: os.Remove fails on the non-empty
	// directory and the index is left alone.
	if _, err := r.RemovePaths([]string{f}, RemoveOptions{Force: true}); err == nil {
		t.Fatal("expected a removal error for a non-empty directory")
	}
	if !rmNames(t, r)["a.txt"] {
		t.Fatal("index changed despite the removal error")
	}
	if _, err := os.Stat(filepath.Join(f, "inner.txt")); err != nil {
		t.Fatalf("directory content was removed: %v", err)
	}
}

func TestRemovePathsResolvesRelativeToCWD(t *testing.T) {
	r, dir := newTempRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	rmWrite(t, filepath.Join(dir, "sub", "f.txt"), "f")
	rmWrite(t, filepath.Join(dir, "g.txt"), "g")
	if err := r.AddFiles([]string{filepath.Join(dir, "sub", "f.txt"), filepath.Join(dir, "g.txt")}); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)

	rmChdir(t, filepath.Join(dir, "sub"))
	if _, err := r.RemovePaths([]string{"f.txt"}, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RemovePaths([]string{"../g.txt"}, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if names := rmNames(t, r); names["sub/f.txt"] || names["g.txt"] {
		t.Fatalf("index entries survived: %v", names)
	}
}

// A variant invisible on this host has no worktree file of ours. A file at
// that name belongs to the user (status calls it untracked) and must stay.
func TestRemovePathsInvisibleVariantKeepsWorktree(t *testing.T) {
	r, dir := newTempRepo(t)
	f := filepath.Join(dir, "f.txt")
	rmWrite(t, f, "local content")
	tag := "linux"
	if runtime.GOOS == "linux" {
		tag = "win"
	}
	if err := r.AddFileOS(f, tag); err != nil {
		t.Fatal(err)
	}

	results, err := r.RemovePaths([]string{f}, RemoveOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].WorkTreeRemoved {
		t.Fatalf("unexpected results: %+v", results)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("invisible variant's worktree file was removed: %v", err)
	}
	if rmNames(t, r)["f.txt"] {
		t.Fatal("index entry survived")
	}
}

func TestRemovePathsDedupesAndSorts(t *testing.T) {
	r, dir := newTempRepo(t)
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	rmWrite(t, a, "a")
	rmWrite(t, b, "b")
	if err := r.AddFiles([]string{a, b}); err != nil {
		t.Fatal(err)
	}
	rmCommit(t, r)

	results, err := r.RemovePaths([]string{b, a, a, dir}, RemoveOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Name != "a.txt" || results[1].Name != "b.txt" {
		t.Fatalf("expected sorted unique results, got %+v", results)
	}
}
