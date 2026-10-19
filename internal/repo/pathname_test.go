package repo

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestHostPathError covers the guard that asks whether this host can create a
// stored name. The predicate takes goos as an argument precisely so the Windows
// branch can be tested on Linux; a real Windows checkout still has to be
// exercised on Windows.
func TestHostPathError(t *testing.T) {
	// Every one of these is a legal filename on the platforms that accept it,
	// including the backslash and the drive-relative spelling.
	legal := []string{
		"a.txt",
		"src/main.go",
		`src/system-systemd\x2dcryptsetup.slice`,
		"C:foo",
		"a..b",
		"a/b/c",
	}
	for _, goos := range []string{"linux", "darwin"} {
		for _, name := range legal {
			if err := hostPathError(name, goos); err != nil {
				t.Errorf("%s: %q refused: %v", goos, name, err)
			}
		}
	}

	// Under Windows' syntax these escape the repository, so they are fatal
	// rather than skippable — on Linux they are ordinary names.
	escapes := []string{
		`..\..\victim.txt`,
		`..\victim.txt`,
		`a\..\..\victim.txt`,
		"a/../../victim.txt",
	}
	for _, name := range escapes {
		if err := hostPathError(name, "windows"); !errors.Is(err, errOutsideWorkTree) {
			t.Errorf("%q: got %v, want errOutsideWorkTree", name, err)
		}
	}

	// Names Windows refuses to create, for reasons other than escaping. The
	// write path joins them onto the root, so a volume or rooted spelling does
	// not escape — it simply cannot be created.
	unrepresentable := []string{
		`a\b`,
		`C:\foo`,
		`\foo`,
		"C:foo",
		"a:b.txt",
		"a*b.txt",
		"a?b.txt",
		`a"b.txt`,
		"a<b.txt",
		"a>b.txt",
		"a|b.txt",
		"CON",
		"con.txt",
		"NUL",
		"aux.log",
		"COM1",
		"lpt9.txt",
		"trailing.",
		"trailing ",
		"a/b.",
		"a/b ",
		"ctrl\x01char",
	}
	for _, name := range unrepresentable {
		if err := hostPathError(name, "windows"); !errors.Is(err, errUnrepresentable) {
			t.Errorf("%q: got %v, want errUnrepresentable", name, err)
		}
	}
}

// TestBackslashNameRoundTrip is the point of the rule: a name the filesystem
// can hold survives add → commit → checkout byte-for-byte. Anything less makes
// a real Chromium checkout unversionable.
func TestBackslashNameRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a backslash name cannot exist on Windows; hostPathError covers the refusal")
	}

	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	name := `src/build/linux/debian_bullseye_i386-sysroot/lib/systemd/system/system-systemd\x2dcryptsetup.slice`
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	const payload = "[Unit]\n"
	if err := ioutil.WriteFile(full, []byte(payload), 0644); err != nil {
		t.Skipf("this filesystem cannot hold a name with a backslash: %v", err)
	}
	if err := r.AddFile(full); err != nil {
		t.Fatalf("add refused a legal filename: %v", err)
	}
	commit, err := r.WriteCommit("Test", "unit")
	if err != nil {
		t.Fatal(err)
	}

	// Wipe the working copy and restore it from the commit.
	if err := os.RemoveAll(filepath.Join(dir, "src")); err != nil {
		t.Fatal(err)
	}
	if err := r.Checkout(commit); err != nil {
		t.Fatalf("checkout failed: %v", err)
	}
	got, err := ioutil.ReadFile(full)
	if err != nil {
		t.Fatalf("file did not come back under its stored name: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("content = %q, want %q", got, payload)
	}
}

// TestRestoreSkipsUnrepresentableName drives the whole skip path by judging the
// host as Windows: the entry is kept in the index, the restorable file is
// written, and the checkout reports what it left behind. Silent skipping would
// be a checkout that exits 0 with part of the tree missing.
func TestRestoreSkipsUnrepresentableName(t *testing.T) {
	old := hostGOOS
	hostGOOS = func() string { return "windows" }
	defer func() { hostGOOS = old }()

	base, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	repoDir := filepath.Join(base, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	r, err := Init(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	name := `a\b.txt`
	hostileCommit(t, r, []hostileEntry{
		{name: name, mode: 0644, content: "PWNED"},
		{name: "ok.txt", mode: 0644, content: "fine"},
	})

	err = r.SwitchBranch("main")
	if err == nil {
		t.Fatal("checkout did not report the path it could not create")
	}
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("error does not name %q: %v", name, err)
	}

	// The restorable entry is on disk; the skipped one is not.
	if got, err := ioutil.ReadFile(filepath.Join(repoDir, "ok.txt")); err != nil || string(got) != "fine" {
		t.Fatalf("ok.txt = %q, %v; want the restored content", got, err)
	}
	if _, err := os.Lstat(filepath.Join(repoDir, name)); !os.IsNotExist(err) {
		t.Fatalf("the unrepresentable entry was written anyway: %v", err)
	}

	// It still belongs to the index, so the next commit keeps it.
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.Entries[entryKey(name, 0)]; !ok {
		t.Fatalf("skipped path missing from the index: %v", idx.Entries)
	}
}

// TestRestoreReportsEverySkippedName pins the report shape: every name, one per
// line, with no truncated "... and N more" tail. It deliberately exceeds the
// old ten-name summary limit, and includes a name holding a real newline so the
// line-oriented format has to stay unambiguous.
func TestRestoreReportsEverySkippedName(t *testing.T) {
	old := hostGOOS
	hostGOOS = func() string { return "windows" }
	defer func() { hostGOOS = old }()

	base, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	repoDir := filepath.Join(base, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	r, err := Init(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	entries := []hostileEntry{{name: "ok.txt", mode: 0644, content: "fine"}}
	var want []string
	for i := 0; i < 15; i++ {
		name := fmt.Sprintf("bad%02d\\name.txt", i)
		want = append(want, name)
		entries = append(entries, hostileEntry{name: name, mode: 0644, content: "x"})
	}
	newlineName := "newline\nname.txt"
	want = append(want, newlineName)
	entries = append(entries, hostileEntry{name: newlineName, mode: 0644, content: "x"})
	hostileCommit(t, r, entries)

	err = r.SwitchBranch("main")
	if err == nil {
		t.Fatal("checkout did not report the names it could not create")
	}
	msg := err.Error()

	sort.Strings(want)
	lines := strings.Split(msg, "\n")
	if len(lines) != 1+len(want) {
		t.Fatalf("report has %d lines, want %d (one per name):\n%s", len(lines), 1+len(want), msg)
	}
	if header := "could not restore these paths on windows:"; lines[0] != header {
		t.Errorf("header = %q, want %q", lines[0], header)
	}
	for i, name := range want {
		display := "  " + name
		if nameNeedsQuoting(name) {
			display = "  " + strconv.Quote(name)
		}
		if lines[i+1] != display {
			t.Errorf("line %d = %q, want %q", i+2, lines[i+1], display)
		}
	}
	if !strings.Contains(msg, strconv.Quote(newlineName)) {
		t.Errorf("newline name is not quoted: %v", err)
	}
	if strings.Contains(msg, "... and ") || strings.Contains(msg, "more") {
		t.Errorf("report still summarizes the names: %v", err)
	}

	// The restorable entry is on disk; every skipped entry stays in the index
	// so the next commit does not quietly drop it.
	if got, err := ioutil.ReadFile(filepath.Join(repoDir, "ok.txt")); err != nil || string(got) != "fine" {
		t.Fatalf("ok.txt = %q, %v; want the restored content", got, err)
	}
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range want {
		if _, ok := idx.Entries[entryKey(name, 0)]; !ok {
			t.Errorf("skipped path %q missing from the index", name)
		}
	}
}

// TestOSMaskHidesUnrepresentableName pins the intended cross-platform idiom:
// content only one platform can hold is stored with an --os mask, and the
// variant filter keeps it out of every other platform's view. Windows then does
// not have to skip the entry at checkout, because it never selects it.
func TestOSMaskHidesUnrepresentableName(t *testing.T) {
	r, repoDir := newTempRepo(t)
	idx, err := r.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}

	name := `system-systemd\x2dcryptsetup.slice`
	if err := ioutil.WriteFile(filepath.Join(repoDir, name), []byte("unit"), 0644); err != nil {
		t.Skipf("this filesystem cannot hold a name with a backslash: %v", err)
	}
	if _, err := r.AddFileToIndex(filepath.Join(repoDir, name), OSLinux, idx); err != nil {
		t.Fatal(err)
	}

	if _, ok := visibleEntries(idx.Entries, OSLinux)[name]; !ok {
		t.Error("the linux variant is not visible on linux")
	}
	if _, ok := visibleEntries(idx.Entries, OSWin)[name]; ok {
		t.Error("the linux variant is visible on windows")
	}
}
