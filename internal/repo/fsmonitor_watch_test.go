package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// ---- relWatchPath ----

func TestRelWatchPath(t *testing.T) {
	root := filepath.FromSlash("/repo")
	cases := []struct {
		abs  string
		want string
		ok   bool
	}{
		{filepath.FromSlash("/repo/a.txt"), "a.txt", true},
		{filepath.FromSlash("/repo/src/main.go"), "src/main.go", true},
		{filepath.FromSlash("/repo/a/b/c.txt"), "a/b/c.txt", true},
		{filepath.FromSlash("/repo"), "", false},               // the root itself
		{filepath.FromSlash("/repo/.qin/index"), "", false},    // the monitor's own state
		{filepath.FromSlash("/repo/.qin"), "", false},          //
		{filepath.FromSlash("/repos/a.txt"), "", false},        // sibling with a shared prefix
		{filepath.FromSlash("/other/a.txt"), "", false},        // outside
		{filepath.FromSlash("/repo/../escape.txt"), "", false}, // escapes
		{filepath.FromSlash("/repo/a/../../escape.txt"), "", false},
	}
	for _, tc := range cases {
		got, ok := relWatchPath(root, tc.abs)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("relWatchPath(%q) = (%q, %v), want (%q, %v)", tc.abs, got, ok, tc.want, tc.ok)
		}
	}
}

// ---- watchBatch ----

func TestWatchBatchCollapsesByPath(t *testing.T) {
	var b watchBatch

	// A build writing one file repeatedly is a single re-stat.
	for i := 0; i < 50000; i++ {
		b.add("main.go", watchChanged)
	}
	b.add("a.txt", watchChanged)
	// Created then deleted: the set still names it once, and the stat that
	// follows is what settles it.
	b.add("tmp.txt", watchChanged)
	b.add("tmp.txt", watchRemoved)

	got := b.flush()
	want := []watchEvent{
		{Path: "main.go", Kind: watchChanged},
		{Path: "a.txt", Kind: watchChanged},
		{Path: "tmp.txt", Kind: watchRemoved},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Later knowledge wins, so a remove-then-recreate reports as changed.
	var b2 watchBatch
	b2.add("x.txt", watchRemoved)
	b2.add("x.txt", watchChanged)
	if got := b2.flush(); len(got) != 1 || got[0].Kind != watchChanged {
		t.Fatalf("got %+v, want a single changed event", got)
	}

	// A quiet interval sends nothing at all rather than an empty batch.
	var b3 watchBatch
	if got := b3.flush(); got != nil {
		t.Fatalf("expected nil for an empty batch, got %v", got)
	}
	// ...and flushing resets, so a path is not reported twice.
	var b4 watchBatch
	b4.add("a.txt", watchChanged)
	b4.flush()
	if got := b4.flush(); got != nil {
		t.Fatalf("expected nil after a flush, got %v", got)
	}
}

// ---- backend selection ----

func TestSelectWatcher(t *testing.T) {
	// An explicit interval forces polling, whatever the platform offers.
	w, err := selectWatcher(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.Name() != "polling" || w.EventDriven() {
		t.Fatalf("got %q (eventDriven=%v), want polling and not event-driven", w.Name(), w.EventDriven())
	}

	// With no interval the platform's event backend is preferred, and its
	// absence is reported rather than silently swapped for polling: the caller
	// decides what to do about it.
	w2, err := selectWatcher(0)
	if err == errNoEventBackend {
		if w2 != nil {
			t.Fatal("expected no watcher alongside errNoEventBackend")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if !w2.EventDriven() {
		t.Fatalf("%q claimed to be the event backend but is not event-driven", w2.Name())
	}
}

// ---- polling backend ----

// pollRepo builds a repository directory with a couple of files.
func pollRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	dir, err := ioutil.TempDir("", "lo-test-poll-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := ioutil.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return r, dir
}

func pathsOf(events []watchEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Path)
	}
	sort.Strings(out)
	return out
}

// collectPaths accumulates batches until every wanted path has been seen.
//
// The contract is the set of paths reported across a window, not the contents
// of any single batch. A file is written by truncating and refilling, so a
// scan that lands mid-write sees a different size and reports the path again
// when the write completes — correctly, since the file really was changing.
// Asserting on one batch would fail on that timing rather than on a defect.
func collectWatchPaths(t *testing.T, w watcher, d time.Duration, want ...string) []string {
	t.Helper()
	need := make(map[string]bool, len(want))
	for _, p := range want {
		need[p] = true
	}
	got := make(map[string]bool)
	deadline := time.After(d)
	for {
		missing := false
		for p := range need {
			if !got[p] {
				missing = true
				break
			}
		}
		if !missing {
			out := make([]string, 0, len(got))
			for p := range got {
				out = append(out, p)
			}
			sort.Strings(out)
			return out
		}
		select {
		case batch, ok := <-w.Events():
			if !ok {
				t.Fatalf("the watcher closed its channel; wanted %v, saw %v", want, sortedSet(got))
			}
			for _, e := range batch {
				got[e.Path] = true
			}
		case <-deadline:
			t.Fatalf("timed out; wanted %v, saw %v", want, sortedSet(got))
		}
	}
}

// settleWindow is how long the tree must stay untouched before a drain can be
// believed.
//
// It has to exceed how long the backend may take to read an event that is
// already queued — on the inotify backend the reader sleeps up to 50ms between
// reads, so it does. A shorter quiet window returns before the event it was
// meant to absorb has been read, and the next assertion inherits it, which
// shows up as a failure in whichever test happens to run next.
const settleWindow = 200 * time.Millisecond

// drainEvents absorbs whatever has arrived, so a later assertion measures only
// what it caused.
func drainEvents(w watcher, quiet time.Duration) {
	deadline := time.After(quiet)
	for {
		select {
		case _, ok := <-w.Events():
			if !ok {
				return
			}
		case <-deadline:
			return
		}
	}
}

// waitLost reports whether the watcher ends its coverage epoch, which it
// signals by closing the events channel.
func waitLost(t *testing.T, w watcher, d time.Duration) bool {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case _, ok := <-w.Events():
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func TestPollWatcherReportsChanges(t *testing.T) {
	_, dir := pollRepo(t)

	w := newPollWatcher(5 * time.Millisecond)
	// The baseline is taken by Watch and emits nothing.
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	select {
	case batch := <-w.Events():
		t.Fatalf("the baseline emitted %v; it should be the comparison point, not a change", batch)
	case <-time.After(40 * time.Millisecond):
	}

	if err := ioutil.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	// Nothing else was touched, so nothing else may be reported.
	if got := collectWatchPaths(t, w, 2*time.Second, "a.txt"); len(got) != 1 || got[0] != "a.txt" {
		t.Fatalf("got %v, want [a.txt]", got)
	}

	// A new file, and a deletion.
	if err := ioutil.WriteFile(filepath.Join(dir, "c.txt"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	// Containment rather than equality: a.txt may still be settling into its
	// new size, and that late report is a true statement about a file that did
	// change. Phase one is where exactness is asserted, because there nothing
	// else had been touched at all.
	got := collectWatchPaths(t, w, 2*time.Second, "b.txt", "c.txt")
	if !contains(got, "b.txt") || !contains(got, "c.txt") {
		t.Fatalf("got %v, want it to include [b.txt c.txt]", got)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestPollWatcherIgnoresObjectStore checks the monitor does not report its own
// state directory: those writes are the monitor's, and feeding them back would
// keep the tree permanently dirty.
func TestPollWatcherIgnoresObjectStore(t *testing.T) {
	r, dir := pollRepo(t)

	w := newPollWatcher(5 * time.Millisecond)
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := r.saveFsmonitorState(&fsmonitorState{Backend: "polling", Gen: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case batch := <-w.Events():
		t.Fatalf("the object store was reported as a change: %v", pathsOf(batch))
	case <-time.After(80 * time.Millisecond):
	}
}

// TestPollWatcherCloseIdempotentAndUnblocking pins the two properties the
// daemon relies on: Close can be called more than once, and it wakes a reader
// that is parked on the events channel.
func TestPollWatcherCloseIdempotentAndUnblocking(t *testing.T) {
	_, dir := pollRepo(t)

	w := newPollWatcher(time.Hour) // long interval, so nothing else will wake it
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range w.Events() {
		}
	}()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake the blocked reader")
	}
	// A closed channel stays closed and readable.
	if _, ok := <-w.Events(); ok {
		t.Fatal("expected the events channel to be closed")
	}
}

// TestPollWatcherDeepAndDottedNames covers names that trip up naive path
// handling: a file whose name merely starts the same as an ignored directory,
// and a subdirectory.
func TestPollWatcherDeepAndDottedNames(t *testing.T) {
	_, dir := pollRepo(t)

	if err := os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, "sub", "deep", "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// A sibling of the object store with a similar name must still be watched.
	if err := ioutil.WriteFile(filepath.Join(dir, ".qinignore"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	w := newPollWatcher(5 * time.Millisecond)
	if err := w.Watch(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := ioutil.WriteFile(filepath.Join(dir, "sub", "deep", "f.txt"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	got := collectWatchPaths(t, w, 2*time.Second, "sub/deep/f.txt")
	if len(got) != 1 || got[0] != "sub/deep/f.txt" {
		t.Fatalf("got %v, want [sub/deep/f.txt]", got)
	}
}
