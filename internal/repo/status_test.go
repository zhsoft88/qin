package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestStatusClean(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Add and commit a file
	ioutil.WriteFile(filepath.Join(dir, "f.txt"), []byte("content"), 0644)
	repo.AddFile(filepath.Join(dir, "f.txt"))
	repo.WriteCommit("Test", "init")

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	if s.Branch != "main" {
		t.Fatalf("expected main, got %s", s.Branch)
	}
	if len(s.Staged) != 0 {
		t.Fatalf("expected 0 staged (committed files filtered), got %d", len(s.Staged))
	}
	if len(s.Modified) != 0 {
		t.Fatalf("expected 0 modified, got %d", len(s.Modified))
	}
	if len(s.Untracked) != 0 {
		t.Fatalf("expected 0 untracked, got %d", len(s.Untracked))
	}
	if len(s.Deleted) != 0 {
		t.Fatalf("expected 0 deleted, got %d", len(s.Deleted))
	}
}

func TestStatusUntracked(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked"), 0644)
	repo.AddFile(filepath.Join(dir, "tracked.txt"))

	ioutil.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("untracked"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Untracked) != 1 || s.Untracked[0] != "untracked.txt" {
		t.Fatalf("expected [untracked.txt], got %v", s.Untracked)
	}
}

func TestStatusModified(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	fpath := filepath.Join(dir, "f.txt")
	ioutil.WriteFile(fpath, []byte("original"), 0644)
	repo.AddFile(fpath)
	repo.WriteCommit("Test", "init")

	// Modify the file without re-staging
	ioutil.WriteFile(fpath, []byte("modified content"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Modified) != 1 || s.Modified[0] != "f.txt" {
		t.Fatalf("expected [f.txt] modified, got %v", s.Modified)
	}
}

func TestStatusDeleted(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	fpath := filepath.Join(dir, "f.txt")
	ioutil.WriteFile(fpath, []byte("content"), 0644)
	repo.AddFile(fpath)
	repo.WriteCommit("Test", "init")

	// Delete the file
	os.Remove(fpath)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Deleted) != 1 || s.Deleted[0] != "f.txt" {
		t.Fatalf("expected [f.txt] deleted, got %v", s.Deleted)
	}
}

// TestStatusStatFastPath verifies the mtime/size fast path: an entry with a
// recorded mtime is reported clean without hashing, and a same-size rewrite
// (with a bumped mtime) is still detected as modified.
func TestStatusStatFastPath(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	fpath := filepath.Join(dir, "f.txt")
	ioutil.WriteFile(fpath, []byte("aaaa"), 0644)
	if err := repo.AddFile(fpath); err != nil {
		t.Fatal(err)
	}

	// Entry must record the file mtime for the fast path to engage.
	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := idx.Entries["f.txt"]
	if !ok || entry.Mtime == 0 {
		t.Fatal("expected index entry with non-zero Mtime")
	}

	// Unmodified → clean
	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 0 {
		t.Fatalf("expected 0 modified, got %v", s.Modified)
	}

	// Same-size rewrite with an explicit mtime bump → stat mismatch → modified
	ioutil.WriteFile(fpath, []byte("bbbb"), 0644)
	bumped := time.Unix(0, entry.Mtime+int64(time.Second))
	if err := os.Chtimes(fpath, bumped, bumped); err != nil {
		t.Fatal(err)
	}
	s, err = repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 1 || s.Modified[0] != "f.txt" {
		t.Fatalf("expected [f.txt] modified, got %v", s.Modified)
	}
}

// TestStatusFastPathAfterCheckout verifies restoreCommit records mtimes so a
// status right after checkout/rebase/merge doesn't re-hash everything.
func TestStatusFastPathAfterCheckout(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	fpath := filepath.Join(dir, "f.txt")
	ioutil.WriteFile(fpath, []byte("content"), 0644)
	repo.AddFile(fpath)
	h, err := repo.WriteCommit("Test", "init")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.restoreCommit(h); err != nil {
		t.Fatal(err)
	}

	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if entry, ok := idx.Entries["f.txt"]; !ok || entry.Mtime == 0 {
		t.Fatal("expected entry with Mtime after restoreCommit")
	}

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 0 || len(s.Untracked) != 0 || len(s.Deleted) != 0 {
		t.Fatalf("expected clean status, got modified=%v untracked=%v deleted=%v", s.Modified, s.Untracked, s.Deleted)
	}
}

// TestStatusUntrackedDirPruning verifies that an untracked directory with no
// tracked content is reported as a single entry without descending.
func TestStatusUntrackedDirPruning(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked"), 0644)
	repo.AddFile(filepath.Join(dir, "tracked.txt"))

	// Deep untracked subtree
	if err := os.MkdirAll(filepath.Join(dir, "build", "gen"), 0755); err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(dir, "build", "gen", "x.txt"), []byte("x"), 0644)
	ioutil.WriteFile(filepath.Join(dir, "build", "y.txt"), []byte("y"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Untracked) != 1 || s.Untracked[0] != "build/" {
		t.Fatalf("expected [build/], got %v", s.Untracked)
	}
}

// TestStatusIgnoredDirPruned verifies an ignored directory is skipped entirely.
func TestStatusIgnoredDirPruned(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked"), 0644)
	repo.AddFile(filepath.Join(dir, "tracked.txt"))

	if err := os.MkdirAll(filepath.Join(dir, "out"), 0755); err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(dir, "out", "a.txt"), []byte("a"), 0644)
	ioutil.WriteFile(filepath.Join(dir, ".qinignore"), []byte("out/\n"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Untracked) != 0 {
		t.Fatalf("expected 0 untracked (out/ ignored), got %v", s.Untracked)
	}
}

// TestStatusIgnoredDirWithNegate verifies re-include rules force the walk to
// descend into ignored directories so re-included children are still listed.
func TestStatusIgnoredDirWithNegate(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked"), 0644)
	repo.AddFile(filepath.Join(dir, "tracked.txt"))

	if err := os.MkdirAll(filepath.Join(dir, "out"), 0755); err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(dir, "out", "keep.txt"), []byte("keep"), 0644)
	ioutil.WriteFile(filepath.Join(dir, "out", "drop.txt"), []byte("drop"), 0644)
	ioutil.WriteFile(filepath.Join(dir, ".qinignore"), []byte("out/\n!out/keep.txt\n"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Untracked) != 1 || s.Untracked[0] != "out/keep.txt" {
		t.Fatalf("expected [out/keep.txt], got %v", s.Untracked)
	}
}

// TestStatusUntrackedCache verifies the per-directory untracked cache:
// results are correct on cache hits, new untracked files are found after a
// directory mtime change, and ignore-rule changes invalidate the cache.
func TestStatusUntrackedCache(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Tracked content in a subdir forces the walk to descend into it
	ioutil.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked"), 0644)
	repo.AddFile(filepath.Join(dir, "tracked.txt"))
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(dir, "src", "tracked.txt"), []byte("t2"), 0644)
	repo.AddFile(filepath.Join(dir, "src", "tracked.txt"))
	ioutil.WriteFile(filepath.Join(dir, "u.txt"), []byte("u"), 0644)
	ioutil.WriteFile(filepath.Join(dir, "src", "u.txt"), []byte("u"), 0644)
	if err := os.MkdirAll(filepath.Join(dir, "d"), 0755); err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(filepath.Join(dir, "d", "x.txt"), []byte("x"), 0644)

	// First run: full scan, cache written
	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"d/", "src/u.txt", "u.txt"}
	if len(s.Untracked) != len(want) || s.Untracked[0] != want[0] || s.Untracked[1] != want[1] || s.Untracked[2] != want[2] {
		t.Fatalf("expected %v, got %v", want, s.Untracked)
	}
	if _, err := os.Stat(repo.untrackedCachePath()); err != nil {
		t.Fatal("expected untracked cache file to be written")
	}

	// Second run: cache hit, same results
	s, err = repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Untracked) != 3 {
		t.Fatalf("expected 3 untracked on cache hit, got %v", s.Untracked)
	}

	// New untracked file in the root bumps the root dir mtime → found
	time.Sleep(20 * time.Millisecond)
	ioutil.WriteFile(filepath.Join(dir, "u2.txt"), []byte("u2"), 0644)
	s, err = repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range s.Untracked {
		if p == "u2.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected u2.txt after dir mtime change, got %v", s.Untracked)
	}

	// New untracked file in a SUBDIR changes only that dir's mtime — the
	// root cache entry stays valid but the walk must still descend into
	// the cached child dir and validate it at its own level.
	time.Sleep(20 * time.Millisecond)
	ioutil.WriteFile(filepath.Join(dir, "src", "u2.txt"), []byte("u2"), 0644)
	s, err = repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, p := range s.Untracked {
		if p == "src/u2.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected src/u2.txt (subdir changed), got %v", s.Untracked)
	}
	// The root entry must record the descended child dirs
	cache := repo.loadUntrackedCache()
	rootEnt, ok := cache.Dirs[""]
	if !ok {
		t.Fatal("expected root cache entry")
	}
	if len(rootEnt.Dirs) == 0 {
		t.Fatal("expected root entry to record descended child dirs")
	}

	// Ignore-rule change invalidates the cache: u.txt now ignored
	ioutil.WriteFile(filepath.Join(dir, ".qinignore"), []byte("u.txt\n"), 0644)
	s, err = repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Untracked {
		if p == "u.txt" {
			t.Fatalf("u.txt should be ignored after .qinignore change, got %v", s.Untracked)
		}
	}
}

// TestStatusUntrackedCacheIndexInvalidation verifies that adding a cached
// untracked file to the index invalidates the cache (index mtime changed),
// so the file is no longer reported as untracked.
func TestStatusUntrackedCacheIndexInvalidation(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	ioutil.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked"), 0644)
	repo.AddFile(filepath.Join(dir, "tracked.txt"))
	ioutil.WriteFile(filepath.Join(dir, "u.txt"), []byte("u"), 0644)

	if _, err := repo.WorkTreeStatus(); err != nil {
		t.Fatal(err)
	}
	cache := repo.loadUntrackedCache()
	if len(cache.Dirs[""].Untracked) == 0 {
		t.Fatal("expected u.txt in cached untracked list")
	}

	// Stage u.txt — index changes, cache becomes stale
	repo.AddFile(filepath.Join(dir, "u.txt"))
	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Untracked {
		if p == "u.txt" {
			t.Fatalf("u.txt should not be untracked after being staged, got %v", s.Untracked)
		}
	}
}

// TestStatusTrackedEmptyDir verifies a tracked empty directory is not
// reported as untracked.
func TestStatusTrackedEmptyDir(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "e"), 0755); err != nil {
		t.Fatal(err)
	}
	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddFileToIndex(filepath.Join(dir, "e"), 0, idx); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Untracked {
		if p == "e/" {
			t.Fatalf("tracked empty dir should not be untracked, got %v", s.Untracked)
		}
	}
}

func TestStatusSkipsLoDir(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a file inside .qin (should be ignored)
	ioutil.WriteFile(filepath.Join(dir, LoDir, "test-file"), []byte("should be ignored"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Untracked) != 0 {
		t.Fatalf("expected 0 untracked (lo dir skipped), got %v", s.Untracked)
	}
}

// renderProgressLine simulates a terminal's carriage-return overwrite
// behavior for a single progress line.
func renderProgressLine(s string) string {
	var out []byte
	for _, seg := range strings.Split(s, "\r") {
		if len(seg) > len(out) {
			out = append(out, make([]byte, len(seg)-len(out))...)
		}
		copy(out, seg)
	}
	return string(out)
}

func TestPrintProgressErasesTail(t *testing.T) {
	old := os.Stderr
	f, err := ioutil.TempFile("", "lo-progress-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	os.Stderr = f
	defer func() { os.Stderr = old }()

	lastProgressLen = 0 // reset shared state

	// A shorter follow-up must erase the previous line's tail
	printProgress("scanned: 500 dirs")
	printProgress("scanned: 1 dirs")
	endProgressLine()

	// A longer follow-up must be written in full
	printProgress("scanned: 1 dirs")
	printProgress("scanned: 500 dirs")
	endProgressLine()

	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	data, err := ioutil.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(string(data), "\n")
	visible := renderProgressLine(lines[0])
	if strings.Contains(visible, "500") {
		t.Fatalf("residue of previous longer line remains: %q", visible)
	}
	if !strings.HasPrefix(visible, "scanned: 1 dirs") {
		t.Fatalf("current message missing: %q", visible)
	}

	visible = renderProgressLine(lines[1])
	if !strings.HasPrefix(visible, "scanned: 500 dirs") {
		t.Fatalf("longer message corrupted: %q", visible)
	}
}

// TestStatusOtherOSVariantUntracked verifies that a path tracked only by
// another OS's variant (e.g. a win-only file on linux) is reported as
// untracked when created locally, instead of being swallowed as "tracked"
// and reported clean.
func TestStatusOtherOSVariantUntracked(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Win-tagged variant, like Windows "add --os multi/my.txt" + push
	subdir := filepath.Join(dir, "multi")
	os.MkdirAll(subdir, 0755)
	fpath := filepath.Join(subdir, "my.txt")
	ioutil.WriteFile(fpath, []byte("win content"), 0644)
	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddFileToIndex(fpath, OSWin, idx); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}

	// A linux clone checks out nothing for win-only paths — remove it
	os.RemoveAll(subdir)

	linuxView := map[uint8]bool{OSLinux: true}
	// The untracked cache is not OS-filter-aware; drop it between views so
	// each filtered status scans fresh.
	cachePath := filepath.Join(dir, LoDir, "untracked-cache.json")

	// Fresh clone on linux: nothing on disk → clean
	os.Remove(cachePath)
	s, err := repo.WorkTreeStatusFiltered(linuxView, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Untracked) != 0 || len(s.Modified) != 0 || len(s.Deleted) != 0 {
		t.Fatalf("expected clean, got untracked=%v modified=%v deleted=%v", s.Untracked, s.Modified, s.Deleted)
	}

	// User creates the file locally → must surface as untracked
	os.MkdirAll(subdir, 0755)
	ioutil.WriteFile(fpath, []byte("win content"), 0644)
	os.Remove(cachePath)
	s, err = repo.WorkTreeStatusFiltered(linuxView, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Untracked) != 1 || s.Untracked[0] != "multi/my.txt" {
		t.Fatalf("expected untracked [multi/my.txt], got %v", s.Untracked)
	}
	if len(s.Modified) != 0 || len(s.Deleted) != 0 {
		t.Fatalf("expected no modified/deleted, got modified=%v deleted=%v", s.Modified, s.Deleted)
	}

	// The win view still sees it as tracked and clean
	os.Remove(cachePath)
	s, err = repo.WorkTreeStatusFiltered(map[uint8]bool{OSWin: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Untracked) != 0 || len(s.Modified) != 0 || len(s.Deleted) != 0 {
		t.Fatalf("expected clean on win view, got untracked=%v modified=%v deleted=%v", s.Untracked, s.Modified, s.Deleted)
	}
}

// TestStatusStagedOSVariants verifies that committed OS variants are not
// reported as staged: the HEAD tree comparison must match the same variant
// (composite key), not just the path name.
func TestStatusStagedOSVariants(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	os.MkdirAll(filepath.Join(dir, "multi"), 0755)
	fpath := filepath.Join(dir, "multi", "my.txt")

	// Two variants of the same path, both committed
	ioutil.WriteFile(fpath, []byte("win content"), 0644)
	idx, err := repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddFileToIndex(fpath, OSWin, idx); err != nil {
		t.Fatal(err)
	}
	ioutil.WriteFile(fpath, []byte("mac content!"), 0644)
	if _, err := repo.AddFileToIndex(fpath, OSMac, idx); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.WriteCommit("Test", "init"); err != nil {
		t.Fatal(err)
	}

	// Neither variant may show as staged — each must match its own tree entry
	for _, osTag := range []uint8{OSWin, OSMac} {
		s, err := repo.WorkTreeStatusFiltered(map[uint8]bool{osTag: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Staged) != 0 {
			t.Fatalf("expected no staged on OS %d, got %v", osTag, s.Staged)
		}
	}

	// A third variant added after the commit → staged
	ioutil.WriteFile(fpath, []byte("linux content"), 0644)
	idx, err = repo.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddFileToIndex(fpath, OSLinux, idx); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}

	s, err := repo.WorkTreeStatusFiltered(map[uint8]bool{OSLinux: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Staged) != 1 {
		t.Fatalf("expected 1 staged, got %v", s.Staged)
	}
}

// ---- staged deletions ----

// TestStatusStagedDeletion covers the blind spot: a path HEAD has that is in
// neither the index nor the working tree. Before StagedDeleted existed, every
// list below was empty and status said the tree was clean.
func TestStatusStagedDeletion(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFile(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.WriteCommit("Test", "a"); err != nil {
		t.Fatal(err)
	}

	// What `qin rm` does: drop the index entry, then delete the file.
	if err := r.RemoveFile(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}

	s, err := r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.StagedDeleted) != 1 || s.StagedDeleted[0] != "a.txt" {
		t.Fatalf("StagedDeleted = %v, want [a.txt]", s.StagedDeleted)
	}
	// This is the defect itself, written down: nothing else can see the path,
	// which is why the status read as clean.
	if len(s.Staged) != 0 {
		t.Fatalf("Staged = %v, want empty", s.Staged)
	}
	if len(s.Modified) != 0 {
		t.Fatalf("Modified = %v, want empty", s.Modified)
	}
	if len(s.Deleted) != 0 {
		t.Fatalf("Deleted = %v, want empty", s.Deleted)
	}
	if len(s.Untracked) != 0 {
		t.Fatalf("Untracked = %v, want empty", s.Untracked)
	}
}

// TestStatusStagedDeletionCached is the `rm --cached` shape: the file stays on
// disk, so the same path is simultaneously a staged deletion and untracked —
// the same pair git reports.
func TestStatusStagedDeletionCached(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFile(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.WriteCommit("Test", "a"); err != nil {
		t.Fatal(err)
	}

	if err := r.RemoveFile(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}

	s, err := r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.StagedDeleted) != 1 || s.StagedDeleted[0] != "a.txt" {
		t.Fatalf("StagedDeleted = %v, want [a.txt]", s.StagedDeleted)
	}
	if len(s.Untracked) != 1 || s.Untracked[0] != "a.txt" {
		t.Fatalf("Untracked = %v, want [a.txt]", s.Untracked)
	}
	if len(s.Deleted) != 0 {
		t.Fatalf("Deleted = %v, want empty: the file is still on disk", s.Deleted)
	}
}

// TestStatusStagedDeletionMatchesDiffIndex pins status and diff --cached to the
// same answer. They enumerate different structures — diff compares two trees,
// status compares the HEAD tree against the index — so an equality test is the
// only thing that keeps them from drifting apart again.
func TestStatusStagedDeletionMatchesDiffIndex(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"keep.txt", "gone1.txt", "gone2.txt"} {
		if err := ioutil.WriteFile(filepath.Join(dir, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
		if err := r.AddFile(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.WriteCommit("Test", "base"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gone1.txt", "gone2.txt"} {
		if err := r.RemoveFile(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	// A staged add is the mirror image and must not be mistaken for one.
	if err := ioutil.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFile(filepath.Join(dir, "new.txt")); err != nil {
		t.Fatal(err)
	}

	s, err := r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	diff, err := r.DiffIndex()
	if err != nil {
		t.Fatal(err)
	}
	var fromDiff []string
	for _, f := range diff.Files {
		if f.Type == DiffDeleted {
			fromDiff = append(fromDiff, f.Name)
		}
	}
	sort.Strings(fromDiff)

	if !reflect.DeepEqual(s.StagedDeleted, fromDiff) {
		t.Fatalf("status says %v, diff --cached says %v", s.StagedDeleted, fromDiff)
	}
	if len(fromDiff) != 2 {
		t.Fatalf("fixture is wrong: diff --cached reports %v", fromDiff)
	}
}

// TestStatusStagedDeletionOSFiltered: a path is tracked if the index tracks it,
// whatever variant each side holds. HEAD's default variant against an
// index entry for the current OS only is the same path, not a deletion — a
// composite-key comparison would call it one.
func TestStatusStagedDeletionOSFiltered(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commit := func() {
		t.Helper()
		if err := ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := r.AddFile(filepath.Join(dir, "a.txt")); err != nil {
			t.Fatal(err)
		}
		if _, err := r.WriteCommit("Test", "a"); err != nil {
			t.Fatal(err)
		}
	}
	commit()

	names := OSNames(currentOS())
	if len(names) == 0 {
		t.Skip("no OS name for this platform")
	}
	// HEAD's entry is the default variant; the index now holds the same path
	// as a variant for this OS. Same path, same content, different key.
	if err := r.RemoveFile(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFileOS(filepath.Join(dir, "a.txt"), names[0]); err != nil {
		t.Fatal(err)
	}
	s, err := r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.StagedDeleted) != 0 {
		t.Fatalf("StagedDeleted = %v, want empty: the index still tracks a.txt", s.StagedDeleted)
	}
}

// TestStatusStagedDeletionInBareTarget is the consequence worth writing down:
// a target's HEAD points at a commit while its index is empty, so every path
// in that tree is a staged deletion. That is the truth — those blobs are in
// neither the index nor a working tree, and diff --cached has always said so.
func TestStatusStagedDeletionInBareTarget(t *testing.T) {
	target, _ := newTargetDir(t, true)
	commitIntoBare(t, target, "a.txt", "a")

	s, err := target.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.StagedDeleted, []string{"a.txt"}) {
		t.Fatalf("StagedDeleted = %v, want [a.txt]", s.StagedDeleted)
	}
	// The same answer as the command that always had it.
	diff, err := target.DiffIndex()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range diff.Files {
		if f.Type == DiffDeleted && f.Name == "a.txt" {
			return
		}
	}
	t.Fatalf("diff --cached does not report a.txt as deleted: %+v", diff.Files)
}
