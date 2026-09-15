package repo

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- the client's half ----

// publishDaemon leaves the repository in the state a client finds when it
// considers a handshake: a generation with a READY record and a published
// record naming it. No process is running, which is the point — the client's
// half of the handshake is about what it does with an answer, and a test that
// had to run a daemon to ask the question could not vary the answer.
func publishDaemon(t *testing.T, r *Repository, gen uint64, events bool) *fsmonitorDaemonInfo {
	t.Helper()
	jw, err := createJournalWriter(r, gen)
	if err != nil {
		t.Fatal(err)
	}
	if err := jw.writeRecord(recReady, []byte("scripted")); err != nil {
		t.Fatal(err)
	}
	if err := jw.close(); err != nil {
		t.Fatal(err)
	}
	info := &fsmonitorDaemonInfo{
		Version:   fsmonitorDaemonVersion,
		PID:       os.Getpid(),
		Gen:       gen,
		Backend:   "scripted",
		Events:    events,
		Watch:     r.Path,
		StartedAt: time.Now().UnixNano(),
	}
	if err := r.saveDaemonInfo(info); err != nil {
		t.Fatal(err)
	}
	return info
}

// syncRequestFiles lists what is waiting in the control directory.
func syncRequestFiles(t *testing.T, r *Repository) []string {
	t.Helper()
	entries, err := ioutil.ReadDir(r.controlDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), fsmonitorSyncPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

type journalFrame struct {
	kind byte
	body []byte
}

// echoWhenAsked plays the daemon's half of the handshake in a goroutine: it
// waits for the client's request, reads the nonce out of it, and appends the
// echo — after whatever frames the test wants to appear before it.
//
// It returns the journal's length once the echo is written, which is the
// position a correct client must report. Nothing else writes to the journal
// here, so that length *is* the echo's end.
func echoWhenAsked(r *Repository, gen uint64, before ...journalFrame) (int64, error) {
	var req string
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, err := ioutil.ReadDir(r.controlDir())
		if err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), fsmonitorSyncPrefix) {
					req = filepath.Join(r.controlDir(), e.Name())
				}
			}
		}
		if req != "" {
			break
		}
		if time.Now().After(deadline) {
			return 0, errors.New("no sync request appeared")
		}
		time.Sleep(time.Millisecond)
	}
	nonce, err := ioutil.ReadFile(req)
	if err != nil {
		return 0, fmt.Errorf("read request: %w", err)
	}

	var buf []byte
	for _, f := range before {
		if buf, err = appendJournalRecord(buf, f.kind, f.body); err != nil {
			return 0, err
		}
	}
	if buf, err = appendJournalRecord(buf, recSync, nonce); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(r.journalPath(gen), os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return 0, err
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	fi, err := os.Stat(r.journalPath(gen))
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func TestSyncFindsTheEcho(t *testing.T) {
	r, _ := daemonRepo(t)
	const gen = 0x5eed
	info := publishDaemon(t, r, gen, true)

	answered := make(chan int64, 1)
	go func() {
		end, err := echoWhenAsked(r, gen,
			journalFrame{recChange, []byte("a.txt")},
			journalFrame{recChange, []byte("sub/b.txt")})
		if err != nil {
			answered <- -1
			return
		}
		answered <- end
	}()

	res := r.syncWithDaemon(info)
	if res == nil {
		t.Fatal("expected the handshake to complete")
	}
	if res.Gen != gen || res.Backend != "scripted" {
		t.Errorf("handshake named (%d, %q), want (%d, scripted)", res.Gen, res.Backend, gen)
	}
	want := <-answered
	if want < 0 {
		t.Fatal("the answering goroutine failed; see its error path")
	}
	if res.SyncEnd != want {
		t.Fatalf("sync end = %d, want %d — the echo is the last thing in the file", res.SyncEnd, want)
	}
	if res.ReadyEnd <= 0 || res.ReadyEnd >= res.SyncEnd {
		t.Fatalf("ready end = %d, sync end = %d: expected ready < sync", res.ReadyEnd, res.SyncEnd)
	}

	// Everything the daemon had already seen is below the position it named:
	// that is the entire claim the client is about to rely on.
	paths := journalPaths(t, r, gen)
	if !hasPath(paths, "a.txt") || !hasPath(paths, "sub/b.txt") {
		t.Fatalf("journal paths = %v, want the changes before the echo", paths)
	}
	if got := syncRequestFiles(t, r); len(got) != 0 {
		t.Fatalf("the answered request was left behind: %v", got)
	}
}

func TestSyncTimesOutWithoutAnEcho(t *testing.T) {
	r, _ := daemonRepo(t)
	info := publishDaemon(t, r, 0x5eee, true)

	restore := fsmonitorSyncTimeout
	fsmonitorSyncTimeout = 40 * time.Millisecond
	defer func() { fsmonitorSyncTimeout = restore }()

	if res := r.syncWithDaemon(info); res != nil {
		t.Fatalf("expected no position without an echo, got %+v", res)
	}
	// The request must not be left for a daemon that is no longer there to
	// answer it, nor for the next client to read as its own.
	if got := syncRequestFiles(t, r); len(got) != 0 {
		t.Fatalf("the request was left behind: %v", got)
	}
}

// TestSyncRejectsARotatedGeneration pins the rule that makes a stored cursor
// safe: a position is only ever agreed for the generation the daemon is
// currently writing, and a rotation in the middle of the handshake means the
// answer describes a journal that has stopped growing.
func TestSyncRejectsARotatedGeneration(t *testing.T) {
	r, _ := daemonRepo(t)
	const gen = 0x5e01
	info := publishDaemon(t, r, gen, true)

	rotated := make(chan struct{})
	go func() {
		defer close(rotated)
		// Wait for the request, then move the record on: the client is
		// mid-poll, and its next look must tell it to give up.
		deadline := time.Now().Add(10 * time.Second)
		for len(syncRequestFiles(t, r)) == 0 {
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		r.saveDaemonInfo(&fsmonitorDaemonInfo{
			Version: fsmonitorDaemonVersion,
			PID:     os.Getpid(),
			Gen:     gen + 1,
			Backend: "scripted",
			Events:  true,
			Watch:   r.Path,
		})
	}()

	if res := r.syncWithDaemon(info); res != nil {
		t.Fatalf("expected no position after a rotation, got %+v", res)
	}
	<-rotated
}

// TestSyncIgnoresAnEarlierEcho checks the nonce actually does something. The
// journal outlives the requests written into it, so an echo from an earlier
// request — by this process or by one that held its pid — is sitting in the
// file, and a client that accepted the first SYNC it saw would take a position
// from a request that was never its own.
func TestSyncIgnoresAnEarlierEcho(t *testing.T) {
	r, _ := daemonRepo(t)
	const gen = 0x5e02
	info := publishDaemon(t, r, gen, true)

	answered := make(chan int64, 1)
	go func() {
		end, err := echoWhenAsked(r, gen, journalFrame{recSync, []byte("a request from someone else")})
		if err != nil {
			answered <- -1
			return
		}
		answered <- end
	}()

	res := r.syncWithDaemon(info)
	if res == nil {
		t.Fatal("expected the handshake to complete")
	}
	want := <-answered
	if want < 0 {
		t.Fatal("the answering goroutine failed")
	}
	if res.SyncEnd != want {
		t.Fatalf("sync end = %d, want %d: the client accepted an echo meant for another request",
			res.SyncEnd, want)
	}
}

func TestSyncRejectsAPollingDaemon(t *testing.T) {
	r, _ := daemonRepo(t)
	info := publishDaemon(t, r, 0x5e03, false)

	if res := r.syncWithDaemon(info); res != nil {
		t.Fatalf("expected no position from a polling daemon, got %+v", res)
	}
	// Not merely unanswered: a poller is not asked at all. Asking would mean
	// waking it to answer a question whose answer is not trustworthy.
	if got := syncRequestFiles(t, r); len(got) != 0 {
		t.Fatalf("a polling daemon was sent a request: %v", got)
	}
}

// TestSyncRejectsAJournalWithoutReady covers the daemon that is still starting
// up. Its journal exists, but the window it describes is the window before its
// watches were established — exactly the window a client must never be told it
// may skip.
func TestSyncRejectsAJournalWithoutReady(t *testing.T) {
	r, _ := daemonRepo(t)
	const gen = 0x5e04
	jw, err := createJournalWriter(r, gen)
	if err != nil {
		t.Fatal(err)
	}
	jw.close()
	info := &fsmonitorDaemonInfo{
		Version: fsmonitorDaemonVersion,
		PID:     os.Getpid(),
		Gen:     gen,
		Backend: "scripted",
		Events:  true,
		Watch:   r.Path,
	}
	if err := r.saveDaemonInfo(info); err != nil {
		t.Fatal(err)
	}

	if res := r.syncWithDaemon(info); res != nil {
		t.Fatalf("expected no position from a journal with no READY, got %+v", res)
	}
	if got := syncRequestFiles(t, r); len(got) != 0 {
		t.Fatalf("the request was left behind: %v", got)
	}
}

func TestSyncRejectsAnotherRepositorysRecord(t *testing.T) {
	r, _ := daemonRepo(t)
	info := publishDaemon(t, r, 0x5e05, true)
	info.Watch = filepath.Join(r.Path, "elsewhere")

	if res := r.syncWithDaemon(info); res != nil {
		t.Fatalf("expected no position for another repository's daemon, got %+v", res)
	}
	if res := r.syncWithDaemon(nil); res != nil {
		t.Fatalf("expected no position with no record, got %+v", res)
	}
}

// ---- the reply walk, on its own ----

// scanForSync is the one part of the handshake whose failure mode is silent: a
// frame misread as complete would put the client's position past a change it
// never saw, and everything after that position would be believed. So it is
// checked against bytes directly, where the frames can be built to order.
func TestScanForSyncStopsAtAPartialFrame(t *testing.T) {
	var b []byte
	var err error
	b, _ = appendJournalRecord(b, recChange, []byte("a.txt"))
	first := len(b)
	b, err = appendJournalRecord(b, recChange, []byte("b.txt"))
	if err != nil {
		t.Fatal(err)
	}

	// The first frame whole, the second cut in half — what a reader sees when
	// it arrives while the daemon is appending.
	cut := first + (len(b)-first)/2
	consumed, syncEnd, err := scanForSync(b[:cut], 0, []byte("nonce"))
	if err != nil {
		t.Fatalf("a half-written frame is not damage: %v", err)
	}
	if syncEnd != 0 {
		t.Fatalf("reported an echo at %d in a buffer that has none", syncEnd)
	}
	if consumed != first {
		t.Fatalf("consumed %d bytes, want %d — the partial frame must be left for the next read", consumed, first)
	}

	// The whole buffer, with the echo at the end.
	end, err := appendJournalRecord(nil, recSync, []byte("nonce"))
	if err != nil {
		t.Fatal(err)
	}
	full := append(append([]byte{}, b...), end...)
	consumed, syncEnd, err = scanForSync(full, 100, []byte("nonce"))
	if err != nil {
		t.Fatal(err)
	}
	if syncEnd != 100+int64(len(full)) {
		t.Fatalf("sync end = %d, want %d", syncEnd, 100+int64(len(full)))
	}
	if consumed != len(full) {
		t.Fatalf("consumed %d, want %d", consumed, len(full))
	}
}

func TestScanForSyncReportsACorruptFrame(t *testing.T) {
	var b []byte
	b, _ = appendJournalRecord(b, recChange, []byte("a.txt"))
	next, err := appendJournalRecord(nil, recChange, []byte("b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// A whole frame, one of whose payload bytes is wrong. Nothing about an
	// append produces this — a half-written frame is short, not wrong — so it
	// is damage, and a reader that stepped over it would be trusting every
	// offset after it.
	next[len(next)-1] ^= 0xff
	b = append(b, next...)

	_, _, err = scanForSync(b, 0, []byte("nonce"))
	if !errors.Is(err, errJournalCorrupt) {
		t.Fatalf("expected corruption to be reported, got %v", err)
	}
}

func TestScanForSyncIgnoresOtherRecords(t *testing.T) {
	var b []byte
	b, _ = appendJournalRecord(b, recChange, []byte("a.txt"))
	b, _ = appendJournalRecord(b, recSync, []byte("another request"))
	b, _ = appendJournalRecord(b, recReady, []byte("inotify"))
	_, syncEnd, err := scanForSync(b, 0, []byte("nonce"))
	if err != nil {
		t.Fatal(err)
	}
	if syncEnd != 0 {
		t.Fatalf("found an echo at %d in a buffer that has none", syncEnd)
	}
}

// ---- the daemon's half ----

// waitForControl returns the control watcher of the current epoch.
func waitForControl(t *testing.T, ctl *controlFactory) *scriptedWatcher {
	t.Helper()
	var w *scriptedWatcher
	waitFor(t, "the control watcher", func() bool {
		w = ctl.current()
		return w != nil
	})
	return w
}

// leaveRequest writes the file a client would leave. The client's half is
// covered above; these tests are about what the daemon does when it sees one.
func leaveRequest(t *testing.T, r *Repository, nonce string) {
	t.Helper()
	if err := os.MkdirAll(r.controlDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(r.syncRequestPath(), []byte(nonce), 0644); err != nil {
		t.Fatal(err)
	}
}

// journalHasEcho reports whether a generation's journal carries an echo of
// nonce — the daemon's side of the handshake, read back as the client would.
func journalHasEcho(r *Repository, gen uint64, nonce string) bool {
	b, err := ioutil.ReadFile(r.journalPath(gen))
	if err != nil {
		return false
	}
	v, err := openJournal(b)
	if err != nil {
		return false
	}
	for _, rec := range v.records {
		if rec.Kind == recSync && string(rec.Body) == nonce {
			return true
		}
	}
	return false
}

func TestDaemonAnswersASyncRequest(t *testing.T) {
	r, _ := daemonRepo(t)
	w := newScriptedWatcher()
	d, stop, ctl := newTestDaemonCtl(r, w)
	startDaemon(t, d, stop)

	var info *fsmonitorDaemonInfo
	waitFor(t, "the daemon's record", func() bool {
		info = r.loadDaemonInfo()
		return info != nil
	})
	leaveRequest(t, r, "0123456789abcdef")
	waitForControl(t, ctl).emit(fsmonitorSyncPrefix + strconv.Itoa(os.Getpid()))

	waitFor(t, "the echo", func() bool { return journalHasEcho(r, info.Gen, "0123456789abcdef") })
	if _, err := os.Stat(r.syncRequestPath()); !os.IsNotExist(err) {
		t.Fatal("the answered request was left behind")
	}
	// Two more wakes, because removing a file wakes the control watcher too,
	// and neither must produce a second echo or an error.
	waitForControl(t, ctl).emit("noise")
	if got := syncRequestFiles(t, r); len(got) != 0 {
		t.Fatalf("requests reappeared: %v", got)
	}
}

// TestDaemonFlushesBeforeItAnswers is the ordering the whole handshake exists
// for. The daemon's own ticker is pushed out of reach, so the only writer left
// is the code that answers a request — and if the change is in the journal at
// all, it is because answering flushed it.
func TestDaemonFlushesBeforeItAnswers(t *testing.T) {
	r, _ := daemonRepo(t)
	w := newScriptedWatcher()
	// Unbuffered, so a send completes only once the daemon has received it:
	// that is what makes "the change is pending" an observable state rather
	// than a guess about the scheduler.
	w.events = make(chan []watchEvent)
	d, stop, ctl := newTestDaemonCtl(r, w)
	d.fastTick = time.Hour
	d.newWatcher = func() (watcher, error) { return w, nil }
	startDaemon(t, d, stop)

	var info *fsmonitorDaemonInfo
	waitFor(t, "the daemon's record", func() bool {
		info = r.loadDaemonInfo()
		return info != nil
	})
	waitForControl(t, ctl)

	w.emit("pending.txt") // returns once the daemon holds it
	leaveRequest(t, r, "before-answering")
	ctl.current().emit("request")

	waitFor(t, "the echo", func() bool { return journalHasEcho(r, info.Gen, "before-answering") })

	// The echo's position has to cover the change, which means the change was
	// written first — not merely written at some point.
	b, err := ioutil.ReadFile(r.journalPath(info.Gen))
	if err != nil {
		t.Fatal(err)
	}
	v, err := openJournal(b)
	if err != nil {
		t.Fatal(err)
	}
	var echoEnd, changeEnd int64
	for _, rec := range v.records {
		if rec.Kind == recChange && string(rec.Body) == "pending.txt" {
			changeEnd = rec.End
		}
		if rec.Kind == recSync && string(rec.Body) == "before-answering" {
			echoEnd = rec.End
		}
	}
	if changeEnd == 0 {
		t.Fatal("the pending change was not flushed by the answer")
	}
	if changeEnd > echoEnd {
		t.Fatalf("the echo at %d precedes the change at %d: a client storing it would skip the change",
			echoEnd, changeEnd)
	}
	// And the client's own view agrees, which is the property it actually uses.
	paths, err := v.changesSince(v.ReadyEnd, echoEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPath(paths, "pending.txt") {
		t.Fatalf("changes up to the echo = %v, want the pending change", paths)
	}
}

func TestDaemonIgnoresAMalformedRequest(t *testing.T) {
	r, _ := daemonRepo(t)
	w := newScriptedWatcher()
	d, stop, ctl := newTestDaemonCtl(r, w)
	startDaemon(t, d, stop)

	var info *fsmonitorDaemonInfo
	waitFor(t, "the daemon's record", func() bool {
		info = r.loadDaemonInfo()
		return info != nil
	})
	request := waitForControl(t, ctl)
	// The daemon echoes what it finds into its own journal, so the contents
	// are treated as untrusted like everything else on disk.
	leaveRequest(t, r, strings.Repeat("x", syncMaxRequest+1))
	request.emit("oversized")
	waitFor(t, "the request to be discarded", func() bool {
		return len(syncRequestFiles(t, r)) == 0
	})

	leaveRequest(t, r, "")
	request.emit("empty")
	waitFor(t, "the empty request to be discarded", func() bool {
		return len(syncRequestFiles(t, r)) == 0
	})

	if journalHasEcho(r, info.Gen, "") || journalHasEcho(r, info.Gen, strings.Repeat("x", syncMaxRequest+1)) {
		t.Fatal("a malformed request was echoed back")
	}
}

// TestDaemonRebuildsWhenTheControlStreamEnds: the same reasoning as a lost tree
// stream. A control watch that stops delivering does not make the daemon
// wrong — it makes it unreachable, and no client can tell that apart from a
// daemon that is merely slow.
func TestDaemonRebuildsWhenTheControlStreamEnds(t *testing.T) {
	r, _ := daemonRepo(t)
	first := newScriptedWatcher()
	second := newScriptedWatcher()

	// A fresh tree watcher per epoch, as the real backend gives: an epoch ends
	// by closing its watchers, so handing the same one back would put the
	// rebuild into a stream that is already over.
	var mu sync.Mutex
	built := 0
	d, stop, ctl := newTestDaemonCtl(r, nil)
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

	var info *fsmonitorDaemonInfo
	waitFor(t, "the first generation", func() bool {
		info = r.loadDaemonInfo()
		return info != nil
	})
	gen1 := info.Gen
	waitForControl(t, ctl).lose()

	waitFor(t, "a rebuild under a new generation", func() bool {
		cur := r.loadDaemonInfo()
		return cur != nil && cur.Gen != gen1
	})
	// And the rebuilt epoch can answer, which is the only reason the rebuild
	// was worth doing. Taking the watcher once matters here: it is replaced by
	// the rebuild, and emitting into a replaced one would go nowhere.
	cur := r.loadDaemonInfo()
	rebuilt := waitForControl(t, ctl)
	leaveRequest(t, r, "after-the-rebuild")
	rebuilt.emit("request")
	waitFor(t, "the echo", func() bool { return journalHasEcho(r, cur.Gen, "after-the-rebuild") })
}

// TestDaemonRefusesAnEpochItCannotAnswer: a daemon that can watch the tree but
// not the requests is worse than no daemon at all, because every client would
// wait out the handshake's timeout and then scan everything anyway. It must
// give up rather than run like that.
func TestDaemonRefusesAnEpochItCannotAnswer(t *testing.T) {
	r, _ := daemonRepo(t)
	tree := newScriptedWatcher()
	d, stop, ctl := newTestDaemonCtl(r, tree)
	ctl.fail = true
	done := startDaemon(t, d, stop)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the epoch to fail")
		}
		if !errors.Is(err, errEpochNotStarted) {
			t.Fatalf("err = %v, want a failure to start", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon kept running without a control watch")
	}
	// The failure is recorded, which is what keeps a spawner from trying again
	// on every status for the next ten minutes.
	if r.loadLastFailure() == nil {
		t.Fatal("expected the failure to be recorded for the spawn backoff")
	}
	if r.loadDaemonInfo() != nil {
		t.Fatal("a daemon that never established coverage must not publish itself")
	}
}

// TestDaemonSkipsTheControlWatcherWhenPolling: no client will handshake with a
// polling daemon — its positions are not trustworthy — so there is nothing for
// a control watch to receive.
func TestDaemonSkipsTheControlWatcherWhenPolling(t *testing.T) {
	r, _ := daemonRepo(t)
	w := newScriptedWatcher()
	w.name = "polling"
	w.eventDriven = false
	d, stop, ctl := newTestDaemonCtl(r, w)
	startDaemon(t, d, stop)

	var info *fsmonitorDaemonInfo
	waitFor(t, "the daemon's record", func() bool {
		info = r.loadDaemonInfo()
		return info != nil
	})
	if info.Events {
		t.Fatal("a polling daemon must not claim to be event-driven")
	}
	if n := ctl.count(); n != 0 {
		t.Fatalf("a polling daemon created %d control watchers", n)
	}
	if _, err := os.Stat(r.controlDir()); !os.IsNotExist(err) {
		t.Fatal("a polling daemon created a control directory it cannot serve")
	}
}

// ---- the client's decision, end to end ----

// TestQueryAnswersIncrementallyAndAdvancesTheCursor is the whole client path
// with a real journal, a live heartbeat and a real handshake. Three things have
// to hold at once, and each of them has a way of being wrong that still looks
// like a working monitor: exactly the reported paths need a stat, the dirty set
// has to survive, and the position stored has to be this run's rather than the
// one it started from. The last one is invisible from the outside — a client
// that stores the offset it read from keeps answering correctly while reading
// the same window forever.
func TestQueryAnswersIncrementallyAndAdvancesTheCursor(t *testing.T) {
	r, _ := daemonRepo(t)
	const gen = 0x8001
	publishDaemon(t, r, gen, true)
	// Liveness is a heartbeat, and the handshake is the proof; this test
	// supplies the first so it can be asked for the second.
	if err := r.touchHeartbeat(); err != nil {
		t.Fatal(err)
	}
	raw, err := ioutil.ReadFile(r.journalPath(gen))
	if err != nil {
		t.Fatal(err)
	}
	v, err := openJournal(raw)
	if err != nil {
		t.Fatal(err)
	}

	// A cursor from an earlier run, with a path that run found deviating and
	// that has not been reconciled since.
	if err := r.saveFsmonitorState(&fsmonitorState{
		Backend: "inotify",
		Gen:     gen,
		Offset:  v.ReadyEnd,
		Dirty:   map[string]bool{"still-dirty.txt": true},
	}); err != nil {
		t.Fatal(err)
	}

	answered := make(chan int64, 1)
	go func() {
		end, err := echoWhenAsked(r, gen, journalFrame{recChange, []byte("changed.txt")})
		if err != nil {
			answered <- -1
			return
		}
		answered <- end
	}()

	c := r.fsmonitorQuery()
	if c == nil {
		t.Fatal("expected the query to answer")
	}
	if c.MustCheck == nil {
		t.Fatal("expected an incremental change set, got a full scan")
	}
	if !c.MustCheck["changed.txt"] {
		t.Fatalf("MustCheck = %v, want the path the daemon reported", c.MustCheck)
	}
	if !c.MustCheck["still-dirty.txt"] {
		t.Fatalf("MustCheck = %v, want the carried-forward dirty path", c.MustCheck)
	}
	if len(c.MustCheck) != 2 {
		t.Fatalf("MustCheck = %v, want exactly those two paths", c.MustCheck)
	}

	want := <-answered
	if want < 0 {
		t.Fatal("the answering goroutine failed")
	}
	// The path the run found deviant is what gets carried forward, and the
	// position is the one the handshake named.
	c.save(r, nil, []string{"changed.txt"})
	st := r.loadFsmonitorState()
	if st == nil {
		t.Fatal("expected the state to be stored")
	}
	if st.Gen != gen || st.Offset != want {
		t.Fatalf("stored cursor = (%d, %d), want (%d, %d)", st.Gen, st.Offset, gen, want)
	}
	if len(st.Dirty) != 1 || !st.Dirty["changed.txt"] {
		t.Fatalf("stored dirty set = %v, want just the deviation found", st.Dirty)
	}
}

// TestQueryStoresNothingWhenTheWindowIsUnreadable is the one row of the
// validation table where a scan may not be paid for with a cursor. A window
// that cannot be read may be hiding a completed change, so a position written
// past it would mean that change is never reported — by this run, which did not
// look, or by the next, which would start after it.
func TestQueryStoresNothingWhenTheWindowIsUnreadable(t *testing.T) {
	r, _ := daemonRepo(t)
	const gen = 0x8002
	publishDaemon(t, r, gen, true)
	if err := r.touchHeartbeat(); err != nil {
		t.Fatal(err)
	}
	raw, err := ioutil.ReadFile(r.journalPath(gen))
	if err != nil {
		t.Fatal(err)
	}
	v, err := openJournal(raw)
	if err != nil {
		t.Fatal(err)
	}

	cursor := &fsmonitorState{Backend: "inotify", Gen: gen, Offset: v.ReadyEnd}
	if err := r.saveFsmonitorState(cursor); err != nil {
		t.Fatal(err)
	}

	// A record the daemon wrote and that is now damaged, inside the window
	// this run would read. The handshake is unaffected — it starts at the end
	// of the file — so this reaches the window read rather than being caught
	// by a failed synchronization.
	frame, err := appendJournalRecord(nil, recChange, []byte("damaged.txt"))
	if err != nil {
		t.Fatal(err)
	}
	frame[len(frame)-1] ^= 0xff
	appendJournalBytes(t, r, gen, frame)

	answered := make(chan int64, 1)
	go func() {
		end, err := echoWhenAsked(r, gen)
		if err != nil {
			answered <- -1
			return
		}
		answered <- end
	}()

	if c := r.fsmonitorQuery(); c != nil {
		t.Fatalf("expected no change set from an unreadable window, got %+v", c)
	}
	if <-answered < 0 {
		t.Fatal("the answering goroutine failed")
	}
	if st := r.loadFsmonitorState(); st == nil || st.Offset != cursor.Offset {
		t.Fatalf("stored state = %+v, want the untouched cursor", st)
	}
}
