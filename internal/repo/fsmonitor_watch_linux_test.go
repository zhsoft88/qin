//go:build linux
// +build linux

package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// requireInotify skips rather than fails where inotify is unavailable — a
// restricted container, a sandbox with the syscall filtered — so the suite
// stays green there instead of reporting a defect that does not exist. The
// skip says why, so a silent loss of coverage in CI is visible.
func requireInotify(t *testing.T) {
	t.Helper()
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		t.Skipf("inotify is unavailable in this environment, so the backend cannot be exercised: %v", err)
	}
	syscall.Close(fd)
}

// inotifyRepo builds a temporary tree and a repository over it.
func inotifyRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	requireInotify(t)
	dir, err := ioutil.TempDir("", "lo-test-inotify-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	} else if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

func TestInotifyWatcherEndToEnd(t *testing.T) {
	_, dir := inotifyRepo(t)

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !w.EventDriven() || w.Name() != "inotify" {
		t.Fatalf("got %q (eventDriven=%v), want inotify and event-driven", w.Name(), w.EventDriven())
	}

	t.Run("create", func(t *testing.T) {
		if err := ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		got := collectWatchPaths(t, w, 3*time.Second, "a.txt")
		if len(got) != 1 || got[0] != "a.txt" {
			t.Fatalf("got %v, want [a.txt]", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("modify an existing file", func(t *testing.T) {
		// The path a kqueue-based design cannot serve: a content change that
		// is not a directory entry change.
		if err := ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("different"), 0644); err != nil {
			t.Fatal(err)
		}
		got := collectWatchPaths(t, w, 3*time.Second, "a.txt")
		if len(got) != 1 || got[0] != "a.txt" {
			t.Fatalf("got %v, want [a.txt]", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("create in a watched subdirectory", func(t *testing.T) {
		if err := ioutil.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		got := collectWatchPaths(t, w, 3*time.Second, "sub/b.txt")
		if len(got) != 1 || got[0] != "sub/b.txt" {
			t.Fatalf("got %v, want [sub/b.txt]", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("delete a file", func(t *testing.T) {
		if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
			t.Fatal(err)
		}
		got := collectWatchPaths(t, w, 3*time.Second, "a.txt")
		if len(got) != 1 || got[0] != "a.txt" {
			t.Fatalf("got %v, want [a.txt]", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("a new directory is watched and its contents announced", func(t *testing.T) {
		// The subtree is created and populated in one go. The watcher can
		// only add a watch on the directory after the kernel tells it the
		// directory exists, so files that arrived first were never seen as
		// events — they must be announced by the enumeration that registering
		// the watch performs, or they would be assumed unchanged forever.
		if err := os.MkdirAll(filepath.Join(dir, "newdir", "inner"), 0755); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"newdir/f.txt", "newdir/inner/g.txt"} {
			if err := ioutil.WriteFile(filepath.Join(dir, p), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		got := collectWatchPaths(t, w, 3*time.Second, "newdir/f.txt", "newdir/inner/g.txt")
		if !contains(got, "newdir/f.txt") || !contains(got, "newdir/inner/g.txt") {
			t.Fatalf("got %v, want it to include the new subtree's files", got)
		}
		drainEvents(w, settleWindow)

		// The subtree is now watched, so a later change there arrives as an
		// ordinary event.
		if err := ioutil.WriteFile(filepath.Join(dir, "newdir", "inner", "g.txt"), []byte("changed"), 0644); err != nil {
			t.Fatal(err)
		}
		got = collectWatchPaths(t, w, 3*time.Second, "newdir/inner/g.txt")
		if !contains(got, "newdir/inner/g.txt") {
			t.Fatalf("got %v, want newdir/inner/g.txt", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("the object store is never reported", func(t *testing.T) {
		r, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.saveFsmonitorState(&fsmonitorState{Backend: "inotify", Gen: 1}); err != nil {
			t.Fatal(err)
		}
		select {
		case batch := <-w.Events():
			t.Fatalf("the monitor's own state was reported: %v", pathsOf(batch))
		case <-time.After(150 * time.Millisecond):
		}
	})
}

// TestInotifyWatcherReportsMovedSubtreeAsLost pins the one case where the
// events do not say everything that happened. A directory moved away is
// reported only as the directory's name on its parent, never file by file, so
// the index paths underneath it would never be looked at again — and their
// disappearance would go unremarked. Ending the epoch turns that into a full
// scan instead.
func TestInotifyWatcherReportsMovedSubtreeAsLost(t *testing.T) {
	_, dir := inotifyRepo(t)
	if err := ioutil.WriteFile(filepath.Join(dir, "sub", "a.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	drainEvents(w, settleWindow)

	if err := os.Rename(filepath.Join(dir, "sub"), filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	if !waitLost(t, w, 3*time.Second) {
		t.Fatal("moving a subtree away must end the coverage epoch, not be reported as an ordinary change")
	}
}

// TestInotifyWatcherAllOrNothing checks that a registration that cannot
// complete fails outright. A partly-watched tree is worse than an unwatched
// one, because the gap is silent.
func TestInotifyWatcherAllOrNothing(t *testing.T) {
	_, dir := inotifyRepo(t)

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// A path that does not exist cannot be watched, so the whole registration
	// must fail rather than leave the rest of the tree covered.
	if err := w.Watch(filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("expected watching a missing root to fail")
	}
}

// TestInotifyWatcherCloseReleasesDescriptor checks the descriptor is closed,
// which is how the watches are released — a leak here would accumulate one per
// coverage epoch, and the kernel's per-user instance limit is low enough that
// it would be reached.
func TestInotifyWatcherCloseReleasesDescriptor(t *testing.T) {
	_, dir := inotifyRepo(t)

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	iw := w.(*inotifyWatcher)
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}
	fd := iw.fd
	if fd < 0 {
		t.Fatal("expected an open descriptor while watching")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if !waitLost(t, w, 2*time.Second) {
		t.Fatal("Close did not end the event stream")
	}
	// The reader closes the descriptor as it exits, which the closed channel
	// above already guarantees has happened.
	if _, err := syscall.Read(fd, make([]byte, 1)); err == nil {
		t.Fatal("expected the descriptor to be closed")
	}
}

// TestInotifyWatcherStartsWatchingNewSubtrees is the regression guard for the
// ordering rule inside addTree: watch before reading, never after. If the
// enumeration came first, a file created between the read and the watch would
// never be reported at all.
func TestInotifyWatcherStartsWatchingNewSubtrees(t *testing.T) {
	_, dir := inotifyRepo(t)

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	drainEvents(w, settleWindow)

	// Create a directory and, without waiting, write into it. Whichever order
	// the watcher observes these, the file must end up reported: either as an
	// event after the watch was added, or by the enumeration that adding it
	// performs.
	if err := os.Mkdir(filepath.Join(dir, "fresh"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, "fresh", "h.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got := collectWatchPaths(t, w, 3*time.Second, "fresh/h.txt")
	if !contains(got, "fresh/h.txt") {
		t.Fatalf("got %v, want fresh/h.txt to be reported", got)
	}
}

// TestDaemonAnswersOverRealInotify is the one place the control watch is built
// the way the daemon really builds it: a second inotify instance rooted at
// .qin/fsmonitor/ctl, a directory that lives inside the tree the *other*
// watcher covers and that would be filtered out of it.
//
// The scripted tests stand in for this path, so what they leave untested is
// exactly what is checked here — that a watch rooted at the control directory
// delivers the arrival of a request file, and that the relative path it reports
// is the bare file name rather than something with an empty parent glued to its
// front.
func TestDaemonAnswersOverRealInotify(t *testing.T) {
	requireInotify(t)
	r, _ := daemonRepo(t)
	tree := newScriptedWatcher()
	d, stop := newTestDaemon(r, tree)
	// The tree watcher stays scripted so this test is about the control watch
	// alone; the real one is what the daemon uses for both.
	d.newControlWatcher = newEventWatcher
	startDaemon(t, d, stop)

	var info *fsmonitorDaemonInfo
	waitFor(t, "the daemon's record", func() bool {
		info = r.loadDaemonInfo()
		return info != nil && info.Events
	})

	leaveRequest(t, r, "over-inotify")
	waitFor(t, "the echo", func() bool { return journalHasEcho(r, info.Gen, "over-inotify") })
	if got := syncRequestFiles(t, r); len(got) != 0 {
		t.Fatalf("the answered request was left behind: %v", got)
	}
}
