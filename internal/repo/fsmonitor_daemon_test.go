package repo

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The daemon's correctness is almost entirely a matter of ordering: the journal
// before READY, READY before the record, the heartbeat before the record it
// belongs to. None of that needs a real event API to check, so every test here
// drives the loop with a scripted watcher — which also means the loop can be
// checked on a machine that cannot run the backend at all.
type scriptedWatcher struct {
	name        string
	eventDriven bool

	events    chan []watchEvent
	done      chan struct{}
	closeOnce sync.Once

	// onWatch runs inside Watch, before this watcher claims to be ready. It is
	// what makes ordering assertions possible: a callback here observes the
	// filesystem at exactly the moment coverage is being established.
	onWatch func(root string)
}

func newScriptedWatcher() *scriptedWatcher {
	return &scriptedWatcher{
		name:        "scripted",
		eventDriven: true,
		events:      make(chan []watchEvent, 16),
		done:        make(chan struct{}),
	}
}

func (w *scriptedWatcher) Watch(root string) error {
	if w.onWatch != nil {
		w.onWatch(root)
	}
	return nil
}
func (w *scriptedWatcher) Events() <-chan []watchEvent { return w.events }
func (w *scriptedWatcher) Name() string                { return w.name }
func (w *scriptedWatcher) EventDriven() bool           { return w.eventDriven }

// Close implements the interface's "Close also closes the events channel"
// rule, which is what tells the daemon coverage is over.
func (w *scriptedWatcher) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
		close(w.events)
	})
	return nil
}

// lose ends the coverage epoch the way a real backend does: by closing the
// stream. It leaves the stop channel alone, so the daemon reads a loss rather
// than a shutdown.
func (w *scriptedWatcher) lose() {
	w.closeOnce.Do(func() { close(w.events) })
}

func (w *scriptedWatcher) emit(paths ...string) {
	batch := make([]watchEvent, 0, len(paths))
	for _, p := range paths {
		batch = append(batch, watchEvent{Path: p, Kind: watchChanged})
	}
	select {
	case w.events <- batch:
	case <-w.done:
	}
}

// failingWatcher exists but cannot cover the tree.
type failingWatcher struct{ scriptedWatcher }

func (w *failingWatcher) Watch(string) error { return os.ErrPermission }

func newFailingWatcher() *failingWatcher {
	return &failingWatcher{scriptedWatcher{
		name:        "failing",
		eventDriven: true,
		events:      make(chan []watchEvent),
		done:        make(chan struct{}),
	}}
}

// daemonRepo builds a repository whose config on disk asks for a monitor.
//
// On disk matters here in a way it did not before the daemon existed: the
// daemon re-reads the config rather than trusting its own copy, so an
// in-memory-only setting would look to it like somebody had switched the
// feature off.
func daemonRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	dir, err := ioutil.TempDir("", "lo-test-daemon-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.Config.Core.Fsmonitor = FsmonitorOn
	if err := SaveConfig(dir, r.Config); err != nil {
		t.Fatal(err)
	}
	return r, dir
}

// testStop is the stop channel plus the right to close it.
//
// The daemon only ever reads the channel — as the real one only ever reads the
// one the CLI closes — so the tests own the sending end. The Once is there
// because a test may stop the daemon itself before the cleanup does.
type testStop struct {
	ch   chan struct{}
	once sync.Once
}

func newTestStop() *testStop { return &testStop{ch: make(chan struct{})} }

func (s *testStop) stop() { s.once.Do(func() { close(s.ch) }) }

// controlFactory mints the control watcher for each epoch and remembers the
// current one.
//
// It has to mint a new one every time: an epoch ends by closing its watchers,
// and a factory that handed back the same instance would give the rebuild a
// watcher whose stream is already closed — which reads as coverage lost, and
// would keep reading that way.
type controlFactory struct {
	mu  sync.Mutex
	cur *scriptedWatcher
	// made counts the watchers handed out, so a test can assert that none were.
	made int
	fail bool
}

func (f *controlFactory) new() (watcher, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.made++
	if f.fail {
		return newFailingWatcher(), nil
	}
	w := newScriptedWatcher()
	w.name = "scripted-ctl"
	f.cur = w
	return w, nil
}

func (f *controlFactory) current() *scriptedWatcher {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur
}

func (f *controlFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.made
}

// newTestDaemon builds a daemon whose ticks are fast enough to test with.
func newTestDaemon(r *Repository, w watcher) (*daemon, *testStop) {
	d, stop, _ := newTestDaemonCtl(r, w)
	return d, stop
}

// newTestDaemonCtl also returns the control-watcher factory, for the tests that
// drive sync requests.
func newTestDaemonCtl(r *Repository, w watcher) (*daemon, *testStop, *controlFactory) {
	stop := newTestStop()
	ctl := &controlFactory{}
	d := &daemon{
		r:           r,
		stop:        stop.ch,
		fastTick:    time.Millisecond,
		slowTick:    5 * time.Millisecond,
		rebuildWait: time.Millisecond,
		logf:        func(string, ...interface{}) {},
	}
	if w != nil {
		d.newWatcher = func() (watcher, error) { return w, nil }
	}
	d.newControlWatcher = ctl.new
	return d, stop, ctl
}

// startDaemon runs the epoch loop in this process.
func startDaemon(t *testing.T, d *daemon, stop *testStop) chan error {
	t.Helper()
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		done <- d.run()
		close(finished)
	}()
	t.Cleanup(func() {
		stop.stop()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("the daemon did not stop")
		}
	})
	return done
}

// waitFor polls until cond holds, or fails the test.
//
// The daemon's work is asynchronous by construction, so every assertion about
// it is an assertion about eventual state. Polling for that state is honest
// about what is being claimed; sleeping a fixed amount and then asserting
// would be a guess about the scheduler.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// journalPaths reads a generation's journal and returns the paths recorded
// after coverage was established. A journal that cannot yet be read yields
// nothing, which is what every poller below is waiting to change.
func journalPaths(t *testing.T, r *Repository, gen uint64) []string {
	t.Helper()
	b, err := ioutil.ReadFile(r.journalPath(gen))
	if err != nil {
		return nil
	}
	v, err := openJournal(b)
	if err != nil {
		return nil
	}
	paths, err := v.changesSince(v.ReadyEnd, v.Size)
	if err != nil {
		return nil
	}
	return paths
}

func hasPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// ---- the happy path ----

func TestDaemonPublishesAndRecords(t *testing.T) {
	r, _ := daemonRepo(t)
	w := newScriptedWatcher()
	d, stop := newTestDaemon(r, w)
	startDaemon(t, d, stop)

	var rec *fsmonitorDaemonInfo
	waitFor(t, "the daemon's record", func() bool {
		rec = r.loadDaemonInfo()
		return rec != nil
	})
	if rec.PID != os.Getpid() {
		t.Errorf("pid = %d, want ours (%d)", rec.PID, os.Getpid())
	}
	if rec.Backend != "scripted" || !rec.Events {
		t.Errorf("record = %+v, want the scripted event backend", rec)
	}
	if rec.Gen == 0 {
		t.Error("generation 0 is never issued")
	}
	if !r.belongsToThisRepo(rec) {
		t.Errorf("watch = %q, want this repository (%q)", rec.Watch, r.Path)
	}

	// The journal must have a READY record. Without one a client can never
	// use this generation at all, so a daemon that published a record without
	// it would be unreachable while looking healthy.
	b, err := ioutil.ReadFile(r.journalPath(rec.Gen))
	if err != nil {
		t.Fatal(err)
	}
	v, err := openJournal(b)
	if err != nil {
		t.Fatalf("the published journal is unusable: %v", err)
	}
	if v.Backend != "scripted" {
		t.Errorf("journal READY names %q, want scripted", v.Backend)
	}

	// The lock is the spawner's, and the daemon clears it once it has
	// published: holding it longer would block the next spawn for the whole
	// staleness window after a daemon that is in fact running fine.
	if _, err := os.Stat(r.spawnLockPath()); !os.IsNotExist(err) {
		t.Error("the spawn lock outlived the daemon's publication")
	}
	if _, err := os.Stat(r.heartbeatPath()); err != nil {
		t.Errorf("no heartbeat: %v", err)
	}

	w.emit("a.txt", "sub/b.txt", "a.txt")
	waitFor(t, "the change to reach the journal", func() bool {
		return len(journalPaths(t, r, rec.Gen)) > 0
	})
	got := journalPaths(t, r, rec.Gen)
	if len(got) != 2 || got[0] != "a.txt" || got[1] != "sub/b.txt" {
		t.Fatalf("journal paths = %v, want [a.txt sub/b.txt] — deduplicated, in first-seen order", got)
	}

	// The daemon's own writes go to .qin/fsmonitor, and must never be
	// reported: a monitor that fed itself would keep the tree permanently
	// dirty. The client rejects such a path as corruption, so it is not
	// cosmetic even if it never got that far.
	for _, p := range got {
		if strings.HasPrefix(p, LoDir+"/") {
			t.Fatalf("the daemon journalled its own state: %q", p)
		}
	}
}

// journalFiles lists the generation files present, for assertions that have to
// name one from inside a callback.
func journalFiles(r *Repository) []string {
	entries, err := ioutil.ReadDir(r.fsmonitorDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), fsmonitorJournalPrefix) {
			out = append(out, filepath.Join(r.fsmonitorDir(), e.Name()))
		}
	}
	return out
}

// TestDaemonEstablishesCoverageBeforeReady is the ordering rule the whole
// client protocol rests on. If READY were written before the watches were
// registered, a client with a cursor below it would skip the window in which
// changes were not being recorded — a silently wrong answer rather than a
// slow one.
func TestDaemonEstablishesCoverageBeforeReady(t *testing.T) {
	r, _ := daemonRepo(t)
	w := newScriptedWatcher()

	var journalsAtWatch int
	var readyAtWatch bool
	w.onWatch = func(string) {
		files := journalFiles(r)
		journalsAtWatch = len(files)
		for _, f := range files {
			b, err := ioutil.ReadFile(f)
			if err != nil {
				continue
			}
			if _, err := openJournal(b); err == nil {
				readyAtWatch = true
			}
		}
	}

	d, stop := newTestDaemon(r, w)
	startDaemon(t, d, stop)
	waitFor(t, "the daemon's record", func() bool { return r.loadDaemonInfo() != nil })

	if journalsAtWatch != 1 {
		t.Errorf("saw %d journals when Watch ran, want exactly the one this epoch created", journalsAtWatch)
	}
	if readyAtWatch {
		t.Error("READY was already written when Watch ran, so a client would trust a window that was never watched")
	}
}

// ---- ending an epoch ----

func TestDaemonStopsOnTheStopFile(t *testing.T) {
	r, _ := daemonRepo(t)
	d, stop := newTestDaemon(r, newScriptedWatcher())
	done := startDaemon(t, d, stop)
	waitFor(t, "the daemon's record", func() bool { return r.loadDaemonInfo() != nil })

	if err := r.requestStop(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stop returned %v, want a clean exit", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the stop file did not stop the daemon")
	}
}

// TestDaemonStopsWhenTheConfigIsSwitchedOff checks the daemon re-reads the
// config rather than trusting the copy it started with.
func TestDaemonStopsWhenTheConfigIsSwitchedOff(t *testing.T) {
	r, dir := daemonRepo(t)
	d, stop := newTestDaemon(r, newScriptedWatcher())
	done := startDaemon(t, d, stop)
	waitFor(t, "the daemon's record", func() bool { return r.loadDaemonInfo() != nil })

	cfg := r.Config
	cfg.Core.Fsmonitor = FsmonitorOff
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("config-off exit returned %v, want a clean exit", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("switching core.fsmonitor off did not stop the daemon")
	}
}

// TestDaemonStepsAsideForItsSuccessor covers the duplicate-daemon case: a
// second daemon published its own record, so this one must stop rather than
// keep writing a journal no client is pointed at.
func TestDaemonStepsAsideForItsSuccessor(t *testing.T) {
	r, _ := daemonRepo(t)
	d, stop := newTestDaemon(r, newScriptedWatcher())
	done := startDaemon(t, d, stop)
	waitFor(t, "the daemon's record", func() bool { return r.loadDaemonInfo() != nil })

	// The pid is deliberately left as ours, which is the harder case: the
	// generation alone has to be enough to see that this is not our epoch.
	successor := &fsmonitorDaemonInfo{
		PID:     os.Getpid(),
		Gen:     d.prevGen + 1000,
		Backend: "successor",
		Events:  true,
		Watch:   r.Path,
	}
	if err := r.saveDaemonInfo(successor); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stepping aside returned %v, want a clean exit", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a successor's record did not make this daemon step aside")
	}
}

// ---- losing coverage ----

func TestDaemonRebuildsAfterCoverageIsLost(t *testing.T) {
	r, _ := daemonRepo(t)
	first := newScriptedWatcher()
	second := newScriptedWatcher()

	var mu sync.Mutex
	built := 0
	d, stop := newTestDaemon(r, nil)
	d.newWatcher = func() (watcher, error) {
		mu.Lock()
		defer mu.Unlock()
		built++
		if built == 1 {
			return first, nil
		}
		return second, nil
	}
	startDaemon(t, d, stop)

	waitFor(t, "the first generation", func() bool { return r.loadDaemonInfo() != nil })
	gen1 := r.loadDaemonInfo().Gen

	first.lose()
	waitFor(t, "a rebuild under a new generation", func() bool {
		info := r.loadDaemonInfo()
		return info != nil && info.Gen != gen1
	})

	// A new generation is a complete answer on its own — the client refuses
	// the old cursor — so the remaining question is only whether the daemon
	// kept working after the rebuild.
	gen2 := r.loadDaemonInfo().Gen
	second.emit("later.txt")
	waitFor(t, "changes to be recorded after the rebuild", func() bool {
		return hasPath(journalPaths(t, r, gen2), "later.txt")
	})

	// The generation it gave up on stays on disk, and must be inert: nothing
	// names it any more, so no client can read it.
	if info := r.loadDaemonInfo(); info.Gen != gen2 {
		t.Fatalf("the record names generation %d, want %d", info.Gen, gen2)
	}
}

// TestDaemonGivesUpOnAFlappingBackend is the spawn-storm valve: a backend that
// dies immediately every time must not leave a live heartbeat behind for
// clients to handshake with.
func TestDaemonGivesUpOnAFlappingBackend(t *testing.T) {
	r, _ := daemonRepo(t)
	d, _ := newTestDaemon(r, nil)
	d.newWatcher = func() (watcher, error) {
		w := newScriptedWatcher()
		w.lose() // coverage lost before a single change is recorded
		return w, nil
	}
	done := make(chan error, 1)
	go func() { done <- d.run() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the daemon to give up rather than spin")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon is spinning on a backend that never settles")
	}
	f := r.loadLastFailure()
	if f == nil {
		t.Fatal("giving up must leave a failure record, or the next status spawns another doomed child")
	}
	if r.spawnSuppressed() == nil {
		t.Error("the recorded failure must suppress the next spawn")
	}
}

// TestDaemonRecordsAStartupFailure checks the platform-has-no-backend case: no
// coverage was ever established, so there is nothing to rebuild, and the
// reason is worth recording so the next status does not pay for the same
// discovery.
func TestDaemonRecordsAStartupFailure(t *testing.T) {
	r, _ := daemonRepo(t)
	d, _ := newTestDaemon(r, nil)
	d.newWatcher = func() (watcher, error) { return nil, errNoEventBackend }

	err := d.run()
	if err == nil {
		t.Fatal("expected an error when no event backend can be built")
	}
	if !strings.Contains(err.Error(), errNoEventBackend.Error()) {
		t.Errorf("error %q does not say why", err)
	}
	if r.loadLastFailure() == nil {
		t.Fatal("expected a recorded startup failure")
	}
	if r.loadDaemonInfo() != nil {
		t.Error("nothing may be published for an epoch that never started")
	}
	if r.spawnSuppressed() == nil {
		t.Error("a fresh failure must suppress the next spawn")
	}
}

// TestDaemonPublishesNothingWhenWatchFails pins the all-or-nothing rule: a
// watcher that exists but cannot cover the tree is not a slow monitor, it is a
// monitor whose silence would be read as "unchanged".
func TestDaemonPublishesNothingWhenWatchFails(t *testing.T) {
	r, _ := daemonRepo(t)
	d, _ := newTestDaemon(r, newFailingWatcher())
	if err := d.run(); err == nil {
		t.Fatal("expected Watch to fail the start")
	}
	if r.loadDaemonInfo() != nil {
		t.Error("a watcher that failed to cover the tree must not be published")
	}
	if r.loadLastFailure() == nil {
		t.Error("expected the failure to be recorded")
	}
	// The generation it created is still on disk, and that is correct: it has
	// a header and no READY, so a client that finds it scans everything.
}

// ---- rotation ----

func TestDaemonRotatesToANewGeneration(t *testing.T) {
	r, _ := daemonRepo(t)
	w := newScriptedWatcher()
	d, stop := newTestDaemon(r, w)
	d.rotateSize = int64(journalHeaderSize + 48) // a couple of records
	startDaemon(t, d, stop)

	var gen1 uint64
	waitFor(t, "a generation", func() bool {
		info := r.loadDaemonInfo()
		if info == nil {
			return false
		}
		gen1 = info.Gen
		return true
	})

	for i := 0; i < 40; i++ {
		w.emit(fmt.Sprintf("dir/file-%02d.txt", i))
	}
	waitFor(t, "a rotation", func() bool {
		info := r.loadDaemonInfo()
		return info != nil && info.Gen != gen1
	})
	gen2 := r.loadDaemonInfo().Gen

	// The new journal must be usable on its own. A client that reads the
	// record, opens the generation it names, and finds no READY would fall
	// back to a full scan for a rotation that was meant to be invisible.
	b, err := ioutil.ReadFile(r.journalPath(gen2))
	if err != nil {
		t.Fatalf("the rotated-to journal is missing: %v", err)
	}
	if _, err := openJournal(b); err != nil {
		t.Fatalf("the rotated-to journal is unusable: %v", err)
	}

	// A path emitted after the rotation lands in the new journal, not the old
	// one: the client that has just scanned everything reads it as the
	// over-approximation it is, which is the safe direction.
	w.emit("after.txt")
	waitFor(t, "post-rotation changes in the new journal", func() bool {
		return hasPath(journalPaths(t, r, gen2), "after.txt")
	})
}

// ---- generation seeding ----

// TestMaxGenerationSeenIgnoresNamesThatAreNotGenerations checks the seed comes
// from real generations only: a file that merely shares the prefix must not
// become one, or a stray name could push a daemon's numbering to the ceiling.
func TestMaxGenerationSeenIgnoresNamesThatAreNotGenerations(t *testing.T) {
	r, _ := daemonRepo(t)
	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(r.fsmonitorDir(), "journal.notahex"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := r.maxGenerationSeen(); got != 0 {
		t.Fatalf("maxGenerationSeen = %d, want 0", got)
	}
	for _, g := range []uint64{7, 900} {
		if err := ioutil.WriteFile(r.journalPath(g), encodeJournalHeader(g), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.maxGenerationSeen(); got != 900 {
		t.Fatalf("maxGenerationSeen = %d, want 900", got)
	}
}

// TestDaemonStartsAboveEveryGenerationOnDisk is what the seeding is for: a
// restarted daemon must never reissue a generation a stored cursor still
// names. The clock alone would usually give that, and "usually" is exactly
// what a backwards clock step defeats.
func TestDaemonStartsAboveEveryGenerationOnDisk(t *testing.T) {
	r, _ := daemonRepo(t)
	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		t.Fatal(err)
	}
	// A generation in the future, as a wrong clock would have produced.
	future := uint64(time.Now().Add(time.Hour).UnixNano())
	if err := ioutil.WriteFile(r.journalPath(future), encodeJournalHeader(future), 0644); err != nil {
		t.Fatal(err)
	}

	d, stop := newTestDaemon(r, newScriptedWatcher())
	d.prevGen = r.maxGenerationSeen()
	startDaemon(t, d, stop)

	waitFor(t, "the daemon's record", func() bool { return r.loadDaemonInfo() != nil })
	if got := r.loadDaemonInfo().Gen; got <= future {
		t.Fatalf("generation %d is not above the one already on disk (%d)", got, future)
	}
}

// ---- the spawn lock ----

// TestSpawnLockAdmitsExactlyOne is the whole point of the lock: N clients
// racing on a cold repository must produce one daemon, not N.
func TestSpawnLockAdmitsExactlyOne(t *testing.T) {
	r, _ := daemonRepo(t)
	const racers = 16

	var mu sync.Mutex
	winners := 0
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := r.acquireSpawnLock()
			if err != nil {
				t.Errorf("acquireSpawnLock: %v", err)
				return
			}
			if ok {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners != 1 {
		t.Fatalf("%d processes won the spawn lock, want exactly 1", winners)
	}
}

// TestSpawnLockBreaksAStaleLock covers the spawner that died before the daemon
// could clear the lock: without this, one crashed spawn blocks every later
// attempt for the whole staleness window.
func TestSpawnLockBreaksAStaleLock(t *testing.T) {
	r, _ := daemonRepo(t)
	if ok, err := r.acquireSpawnLock(); err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	if ok, err := r.acquireSpawnLock(); err != nil || ok {
		t.Fatalf("a fresh lock was taken: ok=%v err=%v", ok, err)
	}
	old := time.Now().Add(-2 * spawnLockStale)
	if err := os.Chtimes(r.spawnLockPath(), old, old); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.acquireSpawnLock(); err != nil || !ok {
		t.Fatalf("a stale lock was not broken: ok=%v err=%v", ok, err)
	}
}

// ---- spawning ----

// TestSpawnDaemonHoldsTheLockUntilTheDaemonPublishes is the duplicate-daemon
// guard. The lock has to outlive the fork, because what follows is the daemon's
// startup — a full walk of the tree, which can take seconds — and a second
// client arriving in that window must not start a second daemon.
func TestSpawnDaemonHoldsTheLockUntilTheDaemonPublishes(t *testing.T) {
	r, _ := daemonRepo(t)

	var calls int
	var args []string
	restore := fsmonitorSpawn
	fsmonitorSpawn = func(cmd *exec.Cmd) error {
		calls++
		args = cmd.Args[1:]
		return nil
	}
	defer func() { fsmonitorSpawn = restore }()

	if err := r.FsmonitorDaemonStart(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("spawned %d times, want 1", calls)
	}
	want := []string{"fsmonitor--daemon", "run", "--repo", r.Path}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}
	if _, err := os.Stat(r.spawnLockPath()); err != nil {
		t.Error("the spawn lock was released before the daemon could publish")
	}

	// A second client arriving while the first daemon is still starting.
	if err := r.FsmonitorDaemonStart(); err != ErrDaemonBusy {
		t.Fatalf("second start = %v, want ErrDaemonBusy", err)
	}
	if calls != 1 {
		t.Fatalf("spawned %d times, want the second start to spawn nothing", calls)
	}
}

// TestSpawnDaemonReleasesTheLockWhenTheForkFails covers the path where no
// daemon will ever publish: without releasing the lock, the next attempt would
// be refused for the whole staleness window after a transient failure.
func TestSpawnDaemonReleasesTheLockWhenTheForkFails(t *testing.T) {
	r, _ := daemonRepo(t)
	restore := fsmonitorSpawn
	fsmonitorSpawn = func(*exec.Cmd) error { return errors.New("no fork for you") }
	defer func() { fsmonitorSpawn = restore }()

	if err := r.FsmonitorDaemonStart(); err == nil {
		t.Fatal("expected the failed spawn to be reported")
	}
	if _, err := os.Stat(r.spawnLockPath()); !os.IsNotExist(err) {
		t.Error("a failed spawn left the lock behind, blocking the next attempt for the staleness window")
	}
}

// ---- the record's scope and the spawn backoff ----

// TestDaemonRecordBelongsToItsRepository covers the copied-repository case: a
// record travels with .qin, and a daemon watching somewhere else must not be
// mistaken for one watching here.
func TestDaemonRecordBelongsToItsRepository(t *testing.T) {
	r, _ := daemonRepo(t)
	if r.belongsToThisRepo(&fsmonitorDaemonInfo{PID: 1, Gen: 5, Watch: filepath.Join(filepath.Dir(r.Path), "elsewhere")}) {
		t.Error("a record watching another directory was accepted")
	}
	if !r.belongsToThisRepo(&fsmonitorDaemonInfo{PID: 1, Gen: 5, Watch: r.Path}) {
		t.Error("a record watching this repository was rejected")
	}
	if r.belongsToThisRepo(nil) {
		t.Error("an absent record was accepted")
	}
}

// TestSpawnSuppressionExpires checks the backoff is a window and not a
// permanent refusal: a platform whose watch could not be registered once must
// be tried again later.
func TestSpawnSuppressionExpires(t *testing.T) {
	r, _ := daemonRepo(t)
	if r.spawnSuppressed() != nil {
		t.Fatal("nothing has failed yet")
	}
	r.recordLastFailure("test")
	if r.spawnSuppressed() == nil {
		t.Fatal("a fresh failure must suppress spawning")
	}

	// Rewriting the timestamp rather than sleeping for ten minutes.
	old := time.Now().Add(-2 * spawnFailureWindow).UnixNano()
	data := []byte(`{"reason":"test","at":` + strconv.FormatInt(old, 10) + `}`)
	if err := ioutil.WriteFile(r.lastFailurePath(), data, 0644); err != nil {
		t.Fatal(err)
	}
	if r.spawnSuppressed() != nil {
		t.Fatal("an expired failure must not suppress spawning")
	}
}

// TestStopAgainstALiveDaemonDoesNotKillAnythingThisProcessOwns is a guard on
// the test arrangement rather than on the code.
//
// FsmonitorDaemonStop escalates to terminating the recorded pid, which is
// exactly right for a daemon in another process and would take the test binary
// down if a test pointed it at an in-process one. The check below documents
// that and pins the invariant that matters: a stop whose daemon has already
// gone is a success, not a hunt for the pid.
func TestStopWithNoLiveDaemon(t *testing.T) {
	r, _ := daemonRepo(t)
	// A record that belongs to this repository, whose heartbeat has long
	// stopped: a daemon that was killed. The pid is this process's, which is
	// the case that would be dangerous to "escalate" on — the heartbeat is
	// what has to decide it, and it decides "gone".
	if err := r.saveDaemonInfo(&fsmonitorDaemonInfo{PID: os.Getpid(), Gen: 42, Watch: r.Path, Backend: "inotify"}); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(r.heartbeatPath(), nil, 0644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * heartbeatTTL)
	if err := os.Chtimes(r.heartbeatPath(), old, old); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(r.journalPath(42), encodeJournalHeader(42), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.saveFsmonitorState(&fsmonitorState{Gen: 42, Offset: 32}); err != nil {
		t.Fatal(err)
	}

	if err := r.FsmonitorDaemonStop(); err != nil {
		t.Fatalf("stop against a dead daemon: %v", err)
	}
	for _, p := range []string{r.daemonInfoPath(), r.heartbeatPath(), r.journalPath(42), r.fsmonitorPath(), r.stopPath()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived a stop", filepath.Base(p))
		}
	}
	// Idempotent: a second stop on an already-clean repository is a no-op.
	if err := r.FsmonitorDaemonStop(); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}

// TestStopRemovesTheMonitorFiles covers the cleanup itself, without a live
// daemon to escalate against: after a stop the next status must find no
// monitor at all rather than a record pointing at a journal that is gone.
func TestStopRemovesTheMonitorFiles(t *testing.T) {
	r, _ := daemonRepo(t)
	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		t.Fatal(err)
	}
	gen := uint64(7)
	for _, p := range []string{r.journalPath(gen), r.daemonInfoPath(), r.lastFailurePath()} {
		if err := ioutil.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.saveFsmonitorState(&fsmonitorState{Gen: gen, Offset: 32}); err != nil {
		t.Fatal(err)
	}
	// A sync request from a client that died before it could clean up.
	if err := ioutil.WriteFile(filepath.Join(r.fsmonitorDir(), fsmonitorSyncPrefix+"1234"), []byte("nonce"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := r.removeMonitorFiles(); err != nil {
		t.Fatal(err)
	}
	entries, err := ioutil.ReadDir(r.fsmonitorDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s survived the cleanup", e.Name())
	}
}
