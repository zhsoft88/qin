//go:build darwin && cgo
// +build darwin,cgo

package repo

// The macOS backend's tests. They can only be run here — the FSEvents stream,
// the dispatch queue and the SDK's flag values are all things this file's
// siblings cannot stand in for — and they exist to check the three things the
// untagged code cannot: that the transcribed flag values are this system's, that
// a content change is reported as a content change, and that everything the
// stream holds is released.

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fseventsRepo builds a temporary tree and a repository over it.
//
// The temporary directory is used as it comes, unresolved, on purpose. On macOS
// it is under /var/folders, and /var is a symlink into /private — so every path
// here is one the kernel will report under a name the caller never wrote, which
// is precisely the trap the watcher has to absorb. A test that resolved the path
// itself would hide the bug it is meant to catch. The symlink subtest below
// makes that case deterministic rather than dependent on where TMPDIR points.
func fseventsRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	dir, err := ioutil.TempDir("", "lo-test-fsevents-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

// TestFseventsConstantsMatchCoreServices is the only place the values in
// fsmonitor_fsevents.go are checked against the SDK they were transcribed from.
// Everything else about the decoding is tested on every platform, and would pass
// just as well with a wrong constant.
func TestFseventsConstantsMatchCoreServices(t *testing.T) {
	if err := fseventsABI(); err != nil {
		t.Fatal(err)
	}
	w, err := newEventWatcher()
	if err != nil {
		t.Fatalf("no event backend on this machine: %v", err)
	}
	defer w.Close()
	if w.Name() != "fsevents" || !w.EventDriven() {
		t.Fatalf("got %q (eventDriven=%v), want fsevents and event-driven", w.Name(), w.EventDriven())
	}
}

func TestFseventsWatcherEndToEnd(t *testing.T) {
	_, dir := fseventsRepo(t)

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// The assertions below ask whether a path was reported, not whether it was
	// the only one reported: FSEvents names the directory a change sits in as
	// well as the path itself, and a directory is not something the index can
	// hold. Extra paths cost a map lookup; a missing one costs a change.

	t.Run("create", func(t *testing.T) {
		if err := ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if got := collectWatchPaths(t, w, 5*time.Second, "a.txt"); !contains(got, "a.txt") {
			t.Fatalf("got %v, want a.txt", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("modify an existing file", func(t *testing.T) {
		// The path that made FSEvents the choice over kqueue: a content change
		// that is not a directory entry change. kqueue needs a descriptor per
		// watched file to see this, and a working tree has more files than
		// descriptors.
		if err := ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("different"), 0644); err != nil {
			t.Fatal(err)
		}
		if got := collectWatchPaths(t, w, 5*time.Second, "a.txt"); !contains(got, "a.txt") {
			t.Fatalf("got %v, want a.txt", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("create in a watched subdirectory", func(t *testing.T) {
		if err := ioutil.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if got := collectWatchPaths(t, w, 5*time.Second, "sub/b.txt"); !contains(got, "sub/b.txt") {
			t.Fatalf("got %v, want sub/b.txt", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("delete a file", func(t *testing.T) {
		if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
			t.Fatal(err)
		}
		if got := collectWatchPaths(t, w, 5*time.Second, "a.txt"); !contains(got, "a.txt") {
			t.Fatalf("got %v, want a.txt", got)
		}
		drainEvents(w, settleWindow)
	})

	t.Run("a new directory's contents are announced", func(t *testing.T) {
		// Created and populated in one go. Whether the stream names each file
		// or collapses the whole thing into the directory is not something the
		// watcher can influence, so the contents are enumerated either way —
		// and a file that arrived without an event of its own is then reported
		// rather than assumed unchanged.
		if err := os.MkdirAll(filepath.Join(dir, "newdir", "inner"), 0755); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"newdir/f.txt", "newdir/inner/g.txt"} {
			if err := ioutil.WriteFile(filepath.Join(dir, p), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		got := collectWatchPaths(t, w, 5*time.Second, "newdir/f.txt", "newdir/inner/g.txt")
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
		if err := r.saveFsmonitorState(&fsmonitorState{Backend: "fsevents", Gen: 1}); err != nil {
			t.Fatal(err)
		}
		select {
		case batch := <-w.Events():
			t.Fatalf("the monitor's own state was reported: %v", pathsOf(batch))
		case <-time.After(settleWindow):
		}
	})
}

// TestFseventsWatcherReportsRemovedDirectoryAsLost pins the one case the events
// cannot express. A directory that goes away is named by the stream, not each
// file inside it, so the index paths underneath would never be looked at again
// and their disappearance would go unremarked. Ending the epoch turns that into
// a full scan instead.
//
// Whether FSEvents also names the files inside is the thing that cannot be
// settled from here — it is documented as best-effort and coalescing, so it may
// vary with how the directory was removed. The rule does not depend on the
// answer, which is the point of making it this way.
func TestFseventsWatcherReportsRemovedDirectoryAsLost(t *testing.T) {
	_, dir := fseventsRepo(t)
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
	if !waitLost(t, w, 5*time.Second) {
		t.Fatal("removing a directory must end the coverage epoch, not be reported as an ordinary change")
	}
}

// TestFseventsWatcherFollowsSymlinksToTheRepository is the regression guard for
// the resolution in Watch.
//
// FSEvents reports the paths it resolved, not the ones it was given, and /tmp is
// a symlink into /private on every Mac. A stream created from the path as
// written therefore reports every event under a prefix that filepath.Rel cannot
// strip, which makes each one look like it came from outside the repository —
// and the change stream silently reports nothing at all, which is the one
// failure this whole design exists to make impossible. Watching through a
// symlink makes that case deterministic, whatever TMPDIR happens to be.
func TestFseventsWatcherFollowsSymlinksToTheRepository(t *testing.T) {
	real, err := ioutil.TempDir("", "lo-test-fsevents-real-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(real) })
	if _, err := Init(real); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(real), filepath.Base(real)+"-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}
	t.Cleanup(func() { os.Remove(link) })

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	// Watched through the link; written through the resolved path. Only a
	// watcher that resolves its own root can match the two up.
	if err := w.Watch(link); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := ioutil.WriteFile(filepath.Join(real, "through.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got := collectWatchPaths(t, w, 5*time.Second, "through.txt")
	if !contains(got, "through.txt") {
		t.Fatalf("got %v, want through.txt — a path the kernel resolved under another name was dropped", got)
	}
}

// TestFseventsWatcherAllOrNothing checks that a root that cannot be watched
// fails outright rather than leaving a stream that reports nothing. A monitor
// that reports nothing is indistinguishable from a tree that never changes.
func TestFseventsWatcherAllOrNothing(t *testing.T) {
	_, dir := fseventsRepo(t)

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := w.Watch(filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("expected watching a missing root to fail")
	}
	if err := w.Watch(filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("expected a second attempt to fail too")
	}
}

// TestFseventsWatcherCloseReleasesTheStream checks that Close stops the stream,
// releases it and the queue it runs on, and ends the event stream — and that it
// is safe to call twice. The daemon closes a watcher on every path out of an
// epoch, including the ones where the epoch never started.
func TestFseventsWatcherCloseReleasesTheStream(t *testing.T) {
	_, dir := fseventsRepo(t)

	w, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	fw := w.(*fseventsWatcher)
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}
	if fw.stream == nil || fw.queue == nil {
		t.Fatal("expected a stream and a queue while watching")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if fw.stream != nil || fw.queue != nil {
		t.Fatal("expected the stream and the queue to be released")
	}
	if !waitLost(t, w, 2*time.Second) {
		t.Fatal("Close did not end the event stream")
	}
	// A watcher that was never started must close just as quietly: the daemon
	// constructs one before it knows whether the epoch will begin.
	other, err := newEventWatcher()
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("Close without Watch: %v", err)
	}
}
