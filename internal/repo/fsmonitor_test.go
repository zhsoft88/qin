package repo

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ---- test helpers ----

// fakeBackend installs a monitor seam that reports the given paths as changed,
// and otherwise behaves like the real backend: it carries the persisted dirty
// set forward, so a deviation reported once is re-reported on later runs until
// the index catches up. This is the contract the journal backend implements in
// full; here it lets the status integration be tested without a daemon.
func fakeBackend(t *testing.T, changed ...string) {
	t.Helper()
	prev := fsmonitorBackend
	fsmonitorBackend = func(r *Repository) (*fsmonitorChanges, error) {
		if !r.FsmonitorEnabled() {
			return nil, nil
		}
		st := r.loadFsmonitorState()
		if st == nil {
			st = &fsmonitorState{Backend: "test", Gen: 1}
		}
		must := make(map[string]bool, len(st.Dirty)+len(changed))
		for p := range st.Dirty {
			must[p] = true
		}
		for _, p := range changed {
			must[p] = true
		}
		return &fsmonitorChanges{MustCheck: must, state: st}, nil
	}
	t.Cleanup(func() { fsmonitorBackend = prev })
}

// noBackend restores the real seam for the duration of a test, so the run sees
// whatever a repository with no daemon running actually sees.
func noBackend(t *testing.T) {
	t.Helper()
	prev := fsmonitorBackend
	fsmonitorBackend = func(r *Repository) (*fsmonitorChanges, error) {
		return nil, nil
	}
	t.Cleanup(func() { fsmonitorBackend = prev })
}

// newMonitorRepo builds a repo with two committed files and the monitor
// enabled, returning the repository and its directory.
func newMonitorRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	dir, err := ioutil.TempDir("", "lo-test-mon-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.Config.Core.Fsmonitor = FsmonitorOn

	for _, name := range []string{"a.txt", "b.txt"} {
		if err := ioutil.WriteFile(filepath.Join(dir, name), []byte("original-"+name), 0644); err != nil {
			t.Fatal(err)
		}
		if err := r.AddFile(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	r.WriteCommit("Test", "init")
	return r, dir
}

// ---- state file ----

func TestFsmonitorStateRoundTrip(t *testing.T) {
	r, _ := newMonitorRepo(t)

	st := &fsmonitorState{
		Backend: "inotify",
		Watch:   r.Path,
		Gen:     0x1234abcd,
		Offset:  4096,
		Dirty:   map[string]bool{"a.txt": true},
	}
	if err := r.saveFsmonitorState(st); err != nil {
		t.Fatal(err)
	}

	got := r.loadFsmonitorState()
	if got == nil {
		t.Fatal("expected the state to load back")
	}
	if got.Version != fsmonitorVersion {
		t.Fatalf("expected version %d, got %d", fsmonitorVersion, got.Version)
	}
	if got.Gen != 0x1234abcd || got.Offset != 4096 || got.Backend != "inotify" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if !got.Dirty["a.txt"] || len(got.Dirty) != 1 {
		t.Fatalf("expected the dirty set to survive, got %v", got.Dirty)
	}

	// The save is atomic, so no temp file may be left behind.
	if _, err := os.Stat(r.fsmonitorPath() + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}

	// A corrupt sidecar must degrade to "no cursor", not to an error.
	if err := ioutil.WriteFile(r.fsmonitorPath(), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := r.loadFsmonitorState(); got != nil {
		t.Fatalf("expected nil for a corrupt sidecar, got %+v", got)
	}
}

// TestFsmonitorStateVersionRejected pins the guard that makes the watchman-era
// sidecar harmless. Version 1 described a watch root and a watchman clock;
// reading it as a cursor would trust a position that means nothing now.
func TestFsmonitorStateVersionRejected(t *testing.T) {
	r, _ := newMonitorRepo(t)

	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"backend":"watchman","watch":"/w","clock":"c:9"}`
	if err := ioutil.WriteFile(r.fsmonitorPath(), []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	if got := r.loadFsmonitorState(); got != nil {
		t.Fatalf("expected a version-1 sidecar to be ignored, got %+v", got)
	}
}

func TestFsmonitorCursor(t *testing.T) {
	r, _ := newMonitorRepo(t)

	if _, _, _, ok := r.FsmonitorCursor(); ok {
		t.Fatal("expected no cursor before one is recorded")
	}
	if err := r.saveFsmonitorState(&fsmonitorState{
		Backend: "inotify", Gen: 7, Offset: 128,
	}); err != nil {
		t.Fatal(err)
	}
	gen, offset, backend, ok := r.FsmonitorCursor()
	if !ok || gen != 7 || offset != 128 || backend != "inotify" {
		t.Fatalf("got (%d, %d, %q, %v), want (7, 128, inotify, true)", gen, offset, backend, ok)
	}
}

// ---- config ----

func TestFsmonitorConfigKey(t *testing.T) {
	cfg := DefaultConfig()

	if v, err := ConfigGet(cfg, "core.fsmonitor"); err != nil || v != FsmonitorOff {
		t.Fatalf("expected default %q, got %q (%v)", FsmonitorOff, v, err)
	}
	if _, ok := ConfigKeys()["core.fsmonitor"]; !ok {
		t.Fatal("core.fsmonitor missing from ConfigKeys")
	}
	for _, v := range []string{"false", "true", "TRUE", ""} {
		if !ValidFsmonitorValue(v) {
			t.Fatalf("expected %q to be a valid core.fsmonitor value", v)
		}
	}
	// "watchman" was a valid value while the monitor shelled out to watchman.
	// The backend is built in now, so the value must no longer be accepted.
	for _, v := range []string{"yes", "watchman", "WATCHMAN"} {
		if ValidFsmonitorValue(v) {
			t.Fatalf("expected %q to be rejected", v)
		}
	}

	if err := ConfigSet(cfg, "core.fsmonitor", "true"); err != nil {
		t.Fatal(err)
	}
	if v, _ := ConfigGet(cfg, "core.fsmonitor"); v != FsmonitorOn {
		t.Fatalf("expected %q, got %q", FsmonitorOn, v)
	}
	if err := ConfigSet(cfg, "core.fsmonitor", "watchman"); err == nil {
		t.Fatal("expected an error for the retired watchman value")
	}
	if err := ConfigSet(cfg, "core.fsmonitor", "bogus"); err == nil {
		t.Fatal("expected an error for an invalid value")
	}
	if err := ConfigUnset(cfg, "core.fsmonitor"); err != nil {
		t.Fatal(err)
	}
	if v, _ := ConfigGet(cfg, "core.fsmonitor"); v != FsmonitorOff {
		t.Fatalf("expected %q after unset, got %q", FsmonitorOff, v)
	}
	if cfg.Core.Fsmonitor != FsmonitorOff {
		t.Fatalf("expected the struct field to be %q after unset, got %q", FsmonitorOff, cfg.Core.Fsmonitor)
	}
}

// ---- status integration ----

// TestStatusFsmonitorNoBackendMatchesOff is the primary regression guard.
// Asking for a monitor that is not running must change nothing: same findings,
// and no monitor state written.
func TestStatusFsmonitorNoBackendMatchesOff(t *testing.T) {
	noBackend(t)
	r, dir := newMonitorRepo(t)

	if err := ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, "c.txt"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}

	on, err := r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	r.Config.Core.Fsmonitor = FsmonitorOff
	off, err := r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"Modified", "Deleted", "Untracked"} {
		var a, b []string
		switch f {
		case "Modified":
			a, b = on.Modified, off.Modified
		case "Deleted":
			a, b = on.Deleted, off.Deleted
		case "Untracked":
			a, b = on.Untracked, off.Untracked
		}
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Fatalf("%s differs between monitor-on and monitor-off: %v vs %v", f, a, b)
		}
	}
	if len(on.Modified) != 1 || on.Modified[0] != "a.txt" {
		t.Fatalf("expected [a.txt] modified, got %v", on.Modified)
	}
	if len(on.Deleted) != 1 || on.Deleted[0] != "b.txt" {
		t.Fatalf("expected [b.txt] deleted, got %v", on.Deleted)
	}
	if len(on.Untracked) != 1 || on.Untracked[0] != "c.txt" {
		t.Fatalf("expected [c.txt] untracked, got %v", on.Untracked)
	}

	// No monitor ran, so no cursor may be written.
	r.Config.Core.Fsmonitor = FsmonitorOn
	if _, err := os.Stat(r.fsmonitorPath()); !os.IsNotExist(err) {
		t.Fatalf("cursor written without a monitor: %v", err)
	}
}

// TestStatusFsmonitorPersistsDeviation pins the property that shapes the whole
// design: the monitor reports a change only once, but status reports state, so
// a deviation must survive runs in which nothing new is reported.
func TestStatusFsmonitorPersistsDeviation(t *testing.T) {
	fakeBackend(t)
	r, dir := newMonitorRepo(t)

	// A size-changing edit, reported by the monitor exactly once.
	fpath := filepath.Join(dir, "a.txt")
	if err := ioutil.WriteFile(fpath, []byte("a much longer replacement"), 0644); err != nil {
		t.Fatal(err)
	}
	fakeBackend(t, "a.txt")

	s, err := r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 1 || s.Modified[0] != "a.txt" {
		t.Fatalf("run 1: expected [a.txt] modified, got %v", s.Modified)
	}

	// The next run reports nothing new. The deviation is still there.
	fakeBackend(t)
	s, err = r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 1 || s.Modified[0] != "a.txt" {
		t.Fatalf("run 2: the deviation was lost, expected [a.txt] modified, got %v", s.Modified)
	}

	// Staging the file makes the index match again, clearing the deviation.
	if err := r.AddFile(fpath); err != nil {
		t.Fatal(err)
	}
	fakeBackend(t)
	s, err = r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 0 {
		t.Fatalf("run 3: expected the deviation to clear after add, got %v", s.Modified)
	}
}

// TestStatusFsmonitorTrustsUnreportedPaths documents the contract this feature
// is built on: for a path the monitor does not report, the monitor is
// authoritative and the stat is skipped. A change the monitor missed is
// therefore not detected — that is the trade being made, and the reason the
// monitor is dropped whenever it cannot be sure.
func TestStatusFsmonitorTrustsUnreportedPaths(t *testing.T) {
	r, dir := newMonitorRepo(t)

	// The size changes, so without the monitor this is always detected.
	fpath := filepath.Join(dir, "a.txt")
	if err := ioutil.WriteFile(fpath, []byte("a much longer replacement"), 0644); err != nil {
		t.Fatal(err)
	}

	fakeBackend(t)
	s, err := r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 0 {
		t.Fatalf("expected the unreported path to be skipped, got %v", s.Modified)
	}

	// The same on-disk state, now reported: detected.
	fakeBackend(t, "a.txt")
	s, err = r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 1 || s.Modified[0] != "a.txt" {
		t.Fatalf("expected [a.txt] modified once reported, got %v", s.Modified)
	}
}

// TestStatusFsmonitorDeletion checks that a deletion reported once keeps being
// reported, the same way a modification does.
func TestStatusFsmonitorDeletion(t *testing.T) {
	fakeBackend(t)
	r, dir := newMonitorRepo(t)

	if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	fakeBackend(t, "b.txt")

	s, err := r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Deleted) != 1 || s.Deleted[0] != "b.txt" {
		t.Fatalf("run 1: expected [b.txt] deleted, got %v", s.Deleted)
	}

	fakeBackend(t)
	s, err = r.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Deleted) != 1 || s.Deleted[0] != "b.txt" {
		t.Fatalf("run 2: the deletion was lost, expected [b.txt] deleted, got %v", s.Deleted)
	}
}

// TestStatusFsmonitorDirtySetBounded checks that an over-large dirty set is
// dropped rather than allowed to grow the sidecar without limit.
func TestStatusFsmonitorDirtySetBounded(t *testing.T) {
	r, _ := newMonitorRepo(t)

	if err := r.saveFsmonitorState(&fsmonitorState{Backend: "test", Gen: 1}); err != nil {
		t.Fatal(err)
	}
	st := r.loadFsmonitorState()
	if st == nil {
		t.Fatal("expected a state file")
	}
	c := &fsmonitorChanges{state: st}
	oversized := make([]string, fsmonitorMaxDirty+1)
	for i := range oversized {
		oversized[i] = fmt.Sprintf("f%d.txt", i)
	}
	c.save(r, oversized, nil)

	if got := r.loadFsmonitorState(); got == nil || len(got.Dirty) != 0 {
		t.Fatalf("expected the oversized dirty set to be dropped, got %+v", got)
	}
}

// TestFsmonitorDaemonStartWithoutBackend checks the CLI-facing verb reports the
// absence of a monitor rather than failing silently on a platform that has no
// event API — a situation this machine cannot otherwise be put in, so the
// availability check is stubbed.
func TestFsmonitorDaemonStartWithoutBackend(t *testing.T) {
	r, _ := newMonitorRepo(t)

	restore := fsmonitorEventBackendAvailable
	fsmonitorEventBackendAvailable = func() bool { return false }
	defer func() { fsmonitorEventBackendAvailable = restore }()

	spawned := 0
	restoreSpawn := fsmonitorSpawn
	fsmonitorSpawn = func(*exec.Cmd) error { spawned++; return nil }
	defer func() { fsmonitorSpawn = restoreSpawn }()

	if err := r.FsmonitorDaemonStart(); err != ErrNoMonitor {
		t.Fatalf("expected ErrNoMonitor, got %v", err)
	}
	if spawned != 0 {
		t.Fatal("a platform with no event backend must not spawn a daemon that can only poll")
	}

	// A stop on a repository with no monitor is a no-op, and must still drop
	// the cursor: a cursor naming a generation nothing is writing is exactly
	// the thing that must not survive.
	if err := r.saveFsmonitorState(&fsmonitorState{Backend: "test", Gen: 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.FsmonitorDaemonStop(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := r.FsmonitorCursor(); ok {
		t.Fatal("expected stop to drop the cursor")
	}
}
