//go:build windows
// +build windows

package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// windowsRepo builds a temporary tree and a repository over it.
//
// Nothing here skips the way requireInotify does. The inotify skip exists
// because a container may refuse the syscall outright, whereas on Windows the
// call is always present — so a failure to open a directory here is a defect in
// this backend, and the whole point of running these tests on Windows is to
// find out. Skipping would hide exactly the failure they were written for.
func windowsRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	dir, err := ioutil.TempDir("", "lo-test-watch-*")
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

// TestWindowsWatcherEndToEnd is the acceptance test for this backend. It is the
// only place the syscall pipeline is exercised at all — the record decoding is
// tested by fsmonitor_fni_test.go on any platform, deliberately, so that what is
// left here is the part that genuinely needs Windows: CreateFile's sharing
// flags, ReadDirectoryChangesW's mask and buffer, and CancelIoEx on the way out.
func TestWindowsWatcherEndToEnd(t *testing.T) {
	_, dir := windowsRepo(t)

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !w.EventDriven() || w.Name() != "windows" {
		t.Fatalf("got %q (eventDriven=%v), want windows and event-driven", w.Name(), w.EventDriven())
	}

	// The first batch enumerates the tree, whatever happened before the reader
	// reached the kernel. Absorbing it here keeps the subtests below measuring
	// only their own writes.
	drainEvents(w, settleWindow)

	t.Run("create", func(t *testing.T) {
		if err := ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		got := collectWatchPaths(t, w, 3*time.Second, "a.txt")
		if !contains(got, "a.txt") {
			t.Fatalf("got %v, want a.txt", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("modify an existing file", func(t *testing.T) {
		// This is what the mask is for. Without FILE_NOTIFY_CHANGE_SIZE or
		// FILE_NOTIFY_CHANGE_LAST_WRITE an edit that leaves the directory entry
		// alone is reported as nothing at all, and the change is invisible until
		// something else touches the directory.
		if err := ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("different"), 0644); err != nil {
			t.Fatal(err)
		}
		got := collectWatchPaths(t, w, 3*time.Second, "a.txt")
		if !contains(got, "a.txt") {
			t.Fatalf("got %v, want a.txt", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("create in a watched subdirectory", func(t *testing.T) {
		// bWatchSubtree covers this, so nothing has to be registered per
		// directory — but the name still has to come back relative to the root.
		if err := ioutil.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		got := collectWatchPaths(t, w, 3*time.Second, "sub/b.txt")
		if !contains(got, "sub/b.txt") {
			t.Fatalf("got %v, want sub/b.txt", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("delete a file", func(t *testing.T) {
		if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
			t.Fatal(err)
		}
		got := collectWatchPaths(t, w, 3*time.Second, "a.txt")
		if !contains(got, "a.txt") {
			t.Fatalf("got %v, want a.txt", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("renaming a file reports both names", func(t *testing.T) {
		// A rename arrives as two records. The old name has to be reported as
		// gone or the index keeps believing in a path that no longer exists.
		if err := ioutil.WriteFile(filepath.Join(dir, "before.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		drainEvents(w, settleWindow)
		if err := os.Rename(filepath.Join(dir, "before.txt"), filepath.Join(dir, "after.txt")); err != nil {
			t.Fatal(err)
		}
		got := collectWatchPaths(t, w, 3*time.Second, "before.txt", "after.txt")
		if !contains(got, "before.txt") || !contains(got, "after.txt") {
			t.Fatalf("got %v, want both names of the rename", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("a new directory's contents are announced", func(t *testing.T) {
		// The subtree is created and populated in one go. The records name the
		// directory and never the files inside it, so those have to be
		// enumerated — otherwise they would be assumed unchanged forever.
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
			t.Fatalf("got %v, want the new subtree's files", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("the object store is never reported", func(t *testing.T) {
		r, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.saveFsmonitorState(&fsmonitorState{Backend: "windows", Gen: 1}); err != nil {
			t.Fatal(err)
		}
		select {
		case batch := <-w.Events():
			t.Fatalf("the monitor's own state was reported: %v", pathsOf(batch))
		case <-time.After(settleWindow):
		}
	})

	t.Run("the watched directory can be renamed and removed while watching", func(t *testing.T) {
		// Without FILE_SHARE_DELETE on the CreateFile that opened it, the
		// handle would lock the directory for as long as the monitor ran, and
		// the monitor would break exactly the operations it exists to observe.
		// This is checked on a subdirectory rather than the root so that the
		// rest of the tree stays usable.
		//
		// Renaming it ends the coverage epoch, as it should — a directory moving
		// away takes its contents' names with it — so this subtest deliberately
		// asserts only on the filesystem operations, and is placed last.
		sub := filepath.Join(dir, "movable")
		if err := os.Mkdir(sub, 0755); err != nil {
			t.Fatal(err)
		}
		drainEvents(w, settleWindow)
		if err := os.Rename(sub, filepath.Join(dir, "moved")); err != nil {
			t.Fatalf("the watch is holding the directory against a rename: %v", err)
		}
		if err := os.Remove(filepath.Join(dir, "moved")); err != nil {
			t.Fatalf("the watch is holding the directory against a removal: %v", err)
		}
	})
}

// TestWindowsWatcherKnownDirectoryRemovalEndsEpoch pins this backend's answer
// to the question the records do not answer.
//
// A removed path cannot be asked whether it was a directory, so the backend
// keeps its own inventory of them; a directory that was in it and is now gone
// ends the epoch. That is deliberately more conservative than the Linux
// backend, which can tell a delete from a move in the event mask: on Windows
// both arrive as "this name is gone", and a subtree moved away is reported as
// its own name alone, so the paths it held would never be looked at again. A
// wasted full scan is the right price for that.
func TestWindowsWatcherKnownDirectoryRemovalEndsEpoch(t *testing.T) {
	_, dir := windowsRepo(t)
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

	if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	if !waitLost(t, w, 3*time.Second) {
		t.Fatal("removing a watched directory must end the coverage epoch, not be reported as an ordinary change")
	}
}

// TestWindowsWatcherReportsMovedSubtreeAsLost is the case the inventory exists
// for: the subtree's own name is all that is reported, so the paths inside it
// would go unchecked and their disappearance unseen.
func TestWindowsWatcherReportsMovedSubtreeAsLost(t *testing.T) {
	_, dir := windowsRepo(t)
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
		t.Fatal("moving a subtree away must end the coverage epoch")
	}
}

// TestWindowsWatcherAllOrNothing checks that a registration that cannot
// complete fails outright. A partly-covered tree is worse than an uncovered
// one, because the gap is silent.
func TestWindowsWatcherAllOrNothing(t *testing.T) {
	_, dir := windowsRepo(t)

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := w.Watch(filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("expected watching a missing root to fail")
	}
}

// TestWindowsWatcherCloseUnblocksRead checks that Close is idempotent and that
// it ends the event stream while the reader is blocked in the kernel — which is
// what CancelIoEx is there for. Closing the handle instead would free the
// number for reuse underneath an outstanding read.
func TestWindowsWatcherCloseUnblocksRead(t *testing.T) {
	_, dir := windowsRepo(t)

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if !waitLost(t, w, 3*time.Second) {
		t.Fatal("Close did not end the event stream; the read is still blocked")
	}
}

// TestWindowsWatcherFirstBatchCoversTheGap is the regression guard for the one
// ordering rule that is specific to this platform.
//
// The system allocates the change buffer when the first ReadDirectoryChangesW
// call is made, not when the handle is opened, so a write between the daemon
// publishing itself and the reader reaching the kernel would never be recorded.
// The first batch therefore enumerates the tree rather than trusting the
// buffer. Without that, this test's file would exist on disk and in no batch.
func TestWindowsWatcherFirstBatchCoversTheGap(t *testing.T) {
	_, dir := windowsRepo(t)
	// Written before the watcher exists at all, so it can only be reported by
	// an enumeration and never by an event.
	if err := ioutil.WriteFile(filepath.Join(dir, "already-here.txt"), []byte("x"), 0644); err != nil {
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

	// Something has to change for the first read to return at all, so the
	// enumeration rides along with an ordinary event.
	if err := ioutil.WriteFile(filepath.Join(dir, "trigger.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got := collectWatchPaths(t, w, 3*time.Second, "trigger.txt", "already-here.txt")
	if !contains(got, "already-here.txt") {
		t.Fatalf("got %v, want the pre-existing file to be enumerated by the first batch", got)
	}
}
