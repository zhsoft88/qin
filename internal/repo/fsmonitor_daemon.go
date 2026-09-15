package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The daemon is the process that watches the working tree and writes what
// changed into the journal. Everything in this file is platform-independent —
// it talks to a watcher, never to a syscall — so the sequencing rules, which
// are where the correctness lives, can be tested with a scripted watcher on
// any machine.
//
// The daemon is one goroutine. It owns the journal file and the pending change
// set, reads from exactly one watcher, and is driven by two tickers and a stop
// channel. There is no lock anywhere below because there is nothing to lock:
// nothing else touches that state.
//
// On a wall-clock schedule it does three things: flushes the pending paths to
// the journal (fast tick), proves it is alive (slow tick), and checks whether
// it should still be running at all (slow tick). The fast tick is short
// because stop latency is bounded by it — the stop request arrives as a file,
// and a file can only be noticed by looking.
const (
	// fsmonitorDaemonVersion is the daemon record's format version. A record
	// written by a different build is treated as absent, so an upgrade cannot
	// be tripped up by a leftover file.
	fsmonitorDaemonVersion = 1

	fsmonitorDaemonName      = "daemon.json"
	fsmonitorHeartbeatName   = "heartbeat"
	fsmonitorStopName        = "stop"
	fsmonitorSpawnLockName   = "spawn.lock"
	fsmonitorLastFailureName = "last-failure"
	fsmonitorLogName         = "daemon.log"
	fsmonitorJournalPrefix   = "journal."
	fsmonitorSyncPrefix      = "sync."

	// flushInterval is how long a change may sit in memory before it reaches
	// the journal. It is not a correctness bound — the sync handshake is what
	// establishes "everything up to here is written" — only a limit on how
	// much a crash could discard, and a crash ends the epoch anyway.
	flushInterval = 250 * time.Millisecond

	// heartbeatInterval is how often liveness is proved, and heartbeatTTL how
	// stale that proof may be before a client stops believing it. Five missed
	// beats: long enough that a loaded machine does not read as dead, short
	// enough that a real death is noticed promptly.
	heartbeatInterval = 2 * time.Second
	heartbeatTTL      = 10 * time.Second

	// journalRotateSize is when the journal is replaced by a fresh generation.
	// A record is roughly 24 bytes per batched path, so this is on the order of
	// 300k batches — large enough to be rare, small enough that the compacted
	// set a client rebuilds after it stays bounded.
	journalRotateSize = 8 << 20

	// staleJournalAge is how old a journal must be before the daemon's startup
	// sweep may delete it. A day-old journal belongs to a daemon that is gone.
	staleJournalAge = time.Hour

	// rebuildBackoff separates rebuild attempts after coverage is lost, and
	// rebuildQuickLoss is how short an epoch must be to count as a failure to
	// settle rather than an ordinary loss.
	rebuildBackoff   = time.Second
	rebuildQuickLoss = 5 * time.Second

	// rebuildMaxQuick bounds consecutive failed rebuilds. A backend that dies
	// immediately every time is not going to settle, and a daemon that keeps a
	// fresh heartbeat through that would have clients handshaking against a
	// generation that changes under them. Giving up hands the problem back to
	// the spawn backoff, which is where it belongs.
	rebuildMaxQuick = 3

	// spawnLockStale is when another process's spawn lock may be broken. It
	// only has to exceed the time a spawn takes — a fork and an exec.
	spawnLockStale = 10 * time.Second

	// stopWait is how long a client waits for a daemon to notice the stop
	// file before killing it. It has to exceed the daemon's fast tick, which
	// is what looks for the file — a shorter wait would escalate every time.
	stopWait = 3 * time.Second

	// spawnFailureWindow is how long a recorded startup failure suppresses
	// further automatic spawns. Without it, a platform whose watch cannot be
	// registered at all would pay for a doomed child process on every single
	// status.
	spawnFailureWindow = 10 * time.Minute

	// daemonLogMaxSize bounds the daemon's log, which is only ever written to
	// when something is going wrong.
	daemonLogMaxSize = 1 << 20

	// deletedSuffix is what Linux appends to /proc/self/exe when the binary
	// was replaced while it was running.
	deletedSuffix = " (deleted)"
)

// ErrNoMonitor is reported by the fsmonitor--daemon verbs when no change
// monitor is available. It is not an error condition for status: with no
// monitor, every run simply does the full scan it always did.
var ErrNoMonitor = errors.New("no change-monitor backend is available")

// ErrDaemonBusy reports that another process is already starting the daemon.
// It is not a failure: the caller falls back to a full scan and tries again
// next time.
var ErrDaemonBusy = errors.New("another process is starting the change monitor")

// errDaemonStopped and errCoverageLost are the two ways an epoch ends besides
// a real error. They are sentinels because the loop treats them differently:
// one exits, the other rebuilds.
var (
	errDaemonStopped = errors.New("fsmonitor daemon stopped")
	errCoverageLost  = errors.New("fsmonitor coverage lost")

	// errEpochNotStarted marks a failure to establish an epoch at all, before
	// anything was published. It is the one failure worth recording as a
	// startup failure, since it is what an unusable platform looks like.
	errEpochNotStarted = errors.New("could not establish change coverage")
)

// ---- paths ----

func (r *Repository) daemonInfoPath() string {
	return filepath.Join(r.fsmonitorDir(), fsmonitorDaemonName)
}
func (r *Repository) heartbeatPath() string {
	return filepath.Join(r.fsmonitorDir(), fsmonitorHeartbeatName)
}
func (r *Repository) stopPath() string {
	return filepath.Join(r.fsmonitorDir(), fsmonitorStopName)
}
func (r *Repository) spawnLockPath() string {
	return filepath.Join(r.fsmonitorDir(), fsmonitorSpawnLockName)
}
func (r *Repository) lastFailurePath() string {
	return filepath.Join(r.fsmonitorDir(), fsmonitorLastFailureName)
}

// DaemonLogPath is where a spawned daemon's output goes. It is exported so the
// CLI can tell the user where to look when the daemon will not stay up.
func (r *Repository) DaemonLogPath() string {
	return filepath.Join(r.fsmonitorDir(), fsmonitorLogName)
}

func (r *Repository) journalPath(gen uint64) string {
	return filepath.Join(r.fsmonitorDir(), fsmonitorJournalPrefix+journalGenName(gen))
}

// ---- the daemon's published record ----

// fsmonitorDaemonInfo is what a running daemon publishes about itself. Its
// existence means "a daemon reached the point of covering the tree", never
// "a daemon is starting" — which is what lets concurrent clients skip waiting
// on a daemon that has nothing to offer yet.
type fsmonitorDaemonInfo struct {
	Version   int    `json:"version"`
	PID       int    `json:"pid"`
	Gen       uint64 `json:"gen"`
	Backend   string `json:"backend"`
	Events    bool   `json:"events"`
	Watch     string `json:"watch"`
	StartedAt int64  `json:"started_at"`
}

func (r *Repository) loadDaemonInfo() *fsmonitorDaemonInfo {
	data, err := ioutil.ReadFile(r.daemonInfoPath())
	if err != nil {
		return nil
	}
	var info fsmonitorDaemonInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil
	}
	if info.Version != fsmonitorDaemonVersion || info.Watch == "" {
		return nil
	}
	return &info
}

func (r *Repository) saveDaemonInfo(info *fsmonitorDaemonInfo) error {
	info.Version = fsmonitorDaemonVersion
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("fsmonitor: marshal daemon record: %w", err)
	}
	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		return fmt.Errorf("fsmonitor: create state dir: %w", err)
	}
	tmp := r.daemonInfoPath() + ".tmp"
	if err := ioutil.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("fsmonitor: write daemon record: %w", err)
	}
	// Atomic replace, because the record is what another client reads to
	// decide whether to handshake. A torn read would look like a corrupt
	// record and cost that client a full scan; a torn write could.
	return os.Rename(tmp, r.daemonInfoPath())
}

// belongsToThisRepo reports whether a record describes this repository.
//
// The record lives inside .qin, so it is normally this repository's by
// construction — but a copied or moved repository carries one along, and then
// the daemon it describes is watching somewhere else entirely. Acting on that
// record would mean handshaking with a journal this tree has nothing to do
// with, so the path is checked rather than assumed.
func (r *Repository) belongsToThisRepo(info *fsmonitorDaemonInfo) bool {
	if info == nil || info.Watch == "" {
		return false
	}
	return filepath.Clean(info.Watch) == filepath.Clean(r.Path)
}

// ---- liveness ----

// touchHeartbeat marks the daemon alive.
//
// Chtimes rather than a write: the contents are irrelevant and an mtime that
// only ever moves forward is exactly the signal wanted. The file is created
// first if it is missing, which is the only case Chtimes fails on.
func (r *Repository) touchHeartbeat() error {
	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		return err
	}
	p := r.heartbeatPath()
	now := time.Now()
	if err := os.Chtimes(p, now, now); err == nil {
		return nil
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	return f.Close()
}

// heartbeatAge reports how long ago the daemon last proved it was alive.
//
// ok is false when there is no heartbeat at all, which is not the same as a
// stale one: a daemon that never started is normal, one that stopped beating
// may still hold a generation.
func (r *Repository) heartbeatAge() (time.Duration, bool) {
	fi, err := os.Stat(r.heartbeatPath())
	if err != nil {
		return 0, false
	}
	return time.Since(fi.ModTime()), true
}

// liveDaemon returns the published record when a daemon is claiming to watch
// this repository and its heartbeat is recent enough to try talking to it.
//
// None of this is proof. A heartbeat is written by whoever holds the file, and
// a pid outlives its process. It decides only whether an attempt is worth
// making; the proof is the daemon's answer to a sync request.
func (r *Repository) liveDaemon() *fsmonitorDaemonInfo {
	info := r.loadDaemonInfo()
	if !r.belongsToThisRepo(info) {
		return nil
	}
	age, ok := r.heartbeatAge()
	if !ok || age > heartbeatTTL {
		return nil
	}
	return info
}

// ---- startup failures ----

// fsmonitorFailure is the daemon's own record of why it could not start. The
// client reads it only to decide whether spawning again is worth trying.
type fsmonitorFailure struct {
	Reason string `json:"reason"`
	At     int64  `json:"at"`
}

func (r *Repository) recordLastFailure(reason string) {
	data, err := json.Marshal(&fsmonitorFailure{Reason: reason, At: time.Now().UnixNano()})
	if err != nil {
		return
	}
	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		return
	}
	ioutil.WriteFile(r.lastFailurePath(), data, 0644)
}

func (r *Repository) loadLastFailure() *fsmonitorFailure {
	data, err := ioutil.ReadFile(r.lastFailurePath())
	if err != nil {
		return nil
	}
	var f fsmonitorFailure
	if err := json.Unmarshal(data, &f); err != nil {
		return nil
	}
	return &f
}

// spawnSuppressed reports whether a recent startup failure should keep the
// caller from spawning another daemon.
func (r *Repository) spawnSuppressed() *fsmonitorFailure {
	f := r.loadLastFailure()
	if f == nil || f.At == 0 {
		return nil
	}
	if time.Since(time.Unix(0, f.At)) > spawnFailureWindow {
		return nil
	}
	return f
}

// ---- the journal's write side ----

// journalWriter is the daemon's append side of one coverage epoch.
type journalWriter struct {
	f    *os.File
	gen  uint64
	path string
	size int64
}

// createJournalWriter creates a generation's journal and writes its header.
//
// O_EXCL is not decoration. Creating a journal that already exists would mean
// reusing a generation, and a generation that came back would make a stale
// cursor look current again — the one failure the whole scheme is built to
// prevent. The filesystem refusing it is a stronger guarantee than a check in
// this code would be.
func createJournalWriter(r *Repository, gen uint64) (*journalWriter, error) {
	if gen == 0 {
		return nil, errors.New("fsmonitor: refusing to create a journal for generation 0")
	}
	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		return nil, fmt.Errorf("fsmonitor: create state dir: %w", err)
	}
	path := r.journalPath(gen)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("fsmonitor: create journal for generation %s: %w", journalGenName(gen), err)
	}
	hdr := encodeJournalHeader(gen)
	if _, err := f.Write(hdr); err != nil {
		f.Close()
		return nil, fmt.Errorf("fsmonitor: write journal header: %w", err)
	}
	return &journalWriter{f: f, gen: gen, path: path, size: int64(len(hdr))}, nil
}

func (j *journalWriter) writeRecord(kind byte, body []byte) error {
	rec, err := appendJournalRecord(nil, kind, body)
	if err != nil {
		return err
	}
	return j.write(rec)
}

// writeChanges appends one record per path in a single write.
//
// One write rather than one per path: a process that dies mid-write leaves a
// partial frame at the tail, and while a CRC catches that either way, a single
// write makes the window one syscall wide instead of one per path.
//
// A path too long to frame is not skipped. Dropping it would be a change the
// client is never told about, which is the one thing the journal may not do;
// failing ends the epoch instead, and the rebuild starts from a full
// enumeration. The case is unreachable in practice — the limit is far past
// what any of the three platforms accepts as a name — so the honest handling
// is the safe one, not the clever one.
func (j *journalWriter) writeChanges(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	var buf []byte
	for _, p := range paths {
		var err error
		buf, err = appendJournalRecord(buf, recChange, []byte(p))
		if err != nil {
			return err
		}
	}
	return j.write(buf)
}

func (j *journalWriter) write(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if _, err := j.f.Write(b); err != nil {
		return fmt.Errorf("fsmonitor: append to journal: %w", err)
	}
	j.size += int64(len(b))
	return nil
}

func (j *journalWriter) close() error {
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}

// ---- generation seeding and cleanup ----

// maxGenerationSeen is the largest generation already on disk.
//
// Nanosecond timestamps make a fresh generation later than any previous one
// almost always — and "almost" is the whole problem: a clock that steps
// backwards, or a machine whose clock was wrong and was then corrected, would
// reissue a generation a stored cursor still names, and that cursor would be
// believed. Listing the directory at startup costs nothing and removes the
// dependence on the clock being right.
func (r *Repository) maxGenerationSeen() uint64 {
	var max uint64
	if info := r.loadDaemonInfo(); info != nil && info.Gen > max {
		max = info.Gen
	}
	entries, err := ioutil.ReadDir(r.fsmonitorDir())
	if err != nil {
		return max
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, fsmonitorJournalPrefix) {
			continue
		}
		g, err := strconv.ParseUint(strings.TrimPrefix(name, fsmonitorJournalPrefix), 16, 64)
		if err != nil {
			continue
		}
		if g > max {
			max = g
		}
	}
	return max
}

// sweepStaleJournals deletes journals old enough that no daemon can still be
// writing them.
//
// Rotation deletes its predecessor on the way out, but on Windows that removal
// fails while a reader holds the file open, so some always survive. Age is the
// safe discriminator: a journal being appended to has an mtime from moments
// ago.
func (r *Repository) sweepStaleJournals() {
	entries, err := ioutil.ReadDir(r.fsmonitorDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleJournalAge)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), fsmonitorJournalPrefix) {
			continue
		}
		if e.ModTime().After(cutoff) {
			continue
		}
		os.Remove(filepath.Join(r.fsmonitorDir(), e.Name()))
	}
	// Requests are removed by whoever answers them, so one surviving this long
	// belongs to a client that died mid-handshake. Nothing will ever read it.
	if entries, err := ioutil.ReadDir(r.controlDir()); err == nil {
		for _, e := range entries {
			if e.ModTime().Before(cutoff) {
				os.Remove(filepath.Join(r.controlDir(), e.Name()))
			}
		}
	}
}

// ---- the epoch loop ----

// daemon is one running change monitor: the state that spans epochs, plus the
// seams that make the loop testable.
type daemon struct {
	r    *Repository
	logf func(string, ...interface{})

	// newWatcher builds one epoch's backend. It is a field so tests can supply
	// a scripted watcher: everything interesting here is sequencing, and none
	// of it needs a real event API to be checked.
	newWatcher func() (watcher, error)

	// newControlWatcher builds the second backend, the one that watches for
	// client sync requests. It is separate from newWatcher because the two are
	// asked different questions: the tree watcher is chosen by configuration,
	// while this one must be event-driven to be worth anything, and is only
	// created when the tree watcher is too.
	newControlWatcher func() (watcher, error)

	// stop is closed to ask the daemon to exit. The CLI closes it from a
	// signal handler; tests close it directly.
	stop <-chan struct{}

	// fastTick, slowTick and rotateSize default to the constants above. They
	// are fields so tests can drive the loop at millisecond scale and reach
	// the rotation path without writing eight megabytes of paths.
	fastTick    time.Duration
	slowTick    time.Duration
	rotateSize  int64
	rebuildWait time.Duration

	prevGen uint64
}

func (d *daemon) rebuildPause() time.Duration {
	if d.rebuildWait > 0 {
		return d.rebuildWait
	}
	return rebuildBackoff
}

func (d *daemon) fast() time.Duration {
	if d.fastTick > 0 {
		return d.fastTick
	}
	return flushInterval
}

func (d *daemon) slow() time.Duration {
	if d.slowTick > 0 {
		return d.slowTick
	}
	return heartbeatInterval
}

func (d *daemon) rotateAt() int64 {
	if d.rotateSize > 0 {
		return d.rotateSize
	}
	return journalRotateSize
}

// FsmonitorDaemonRun is the daemon's foreground entry point.
//
// pollInterval > 0 selects the polling backend rather than the platform's
// event API. That backend is correct but pays unconditionally for what it
// finds, and its view lags by up to one interval — which is precisely the
// window a client would wrongly skip — so it is never chosen automatically and
// is meant for debugging the rest of the pipeline on a machine whose event API
// cannot be exercised.
func (r *Repository) FsmonitorDaemonRun(pollInterval time.Duration, stop <-chan struct{}, logf func(string, ...interface{})) error {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	d := &daemon{r: r, logf: logf, stop: stop}
	d.newWatcher = func() (watcher, error) { return selectWatcher(pollInterval) }
	// The control watcher is never the polling backend. A handshake answered
	// after a poll interval would be answering about a window the poller has
	// not looked at yet, and a client that stored the position would be storing
	// a promise the daemon cannot keep.
	d.newControlWatcher = newEventWatcher
	// Seeded from disk, not from zero: this daemon is not the first to run
	// here, and its generations have to be greater than the ones already
	// named by cursors it cannot see.
	d.prevGen = r.maxGenerationSeen()
	if d.prevGen == ^uint64(0) {
		return errors.New("fsmonitor: generation space exhausted")
	}
	return d.run()
}

func (d *daemon) run() error {
	// A stop request that predates this daemon was addressed to its
	// predecessor, which is gone. Honouring it would mean the daemon exits the
	// moment it starts, which reads as a crash loop.
	d.r.clearStopFile()
	d.r.sweepStaleJournals()

	quickLosses := 0
	for {
		select {
		case <-d.stop:
			return nil
		default:
		}

		start := time.Now()
		err := d.runEpoch()
		if errors.Is(err, errDaemonStopped) {
			// An asked-for stop is not a failure: the CLI should exit 0, and
			// nothing should be recorded against the next spawn.
			return nil
		}
		if err == nil {
			return nil
		}
		if errors.Is(err, errEpochNotStarted) {
			// Nothing was ever published, so there is no coverage to lose and
			// nothing to rebuild from. Record it and let the spawn backoff
			// keep this from being retried on every status.
			d.logf("fsmonitor: %v", err)
			d.r.recordLastFailure(err.Error())
			return err
		}

		// Coverage was lost mid-epoch. That is survivable — a new generation
		// is exactly the protocol's signal that a cursor is void — but a
		// backend that dies immediately every time is not going to settle, and
		// holding a live heartbeat through that would have clients handshaking
		// against a generation that keeps changing under them.
		d.logf("fsmonitor: %v; rebuilding coverage", err)
		if time.Since(start) < rebuildQuickLoss {
			quickLosses++
			if quickLosses > rebuildMaxQuick {
				d.r.recordLastFailure(err.Error())
				return fmt.Errorf("fsmonitor: giving up after %d failed rebuilds: %w", quickLosses, err)
			}
		} else {
			quickLosses = 0
		}
		select {
		case <-d.stop:
			return nil
		case <-time.After(d.rebuildPause()):
		}
	}
}

// runEpoch establishes one coverage epoch and records changes until it ends.
func (d *daemon) runEpoch() error {
	gen := nextGeneration(d.prevGen, time.Now().UnixNano())
	d.prevGen = gen

	jw, err := createJournalWriter(d.r, gen)
	if err != nil {
		return fmt.Errorf("%w: %v", errEpochNotStarted, err)
	}
	defer jw.close()

	w, err := d.newWatcher()
	if err != nil {
		return fmt.Errorf("%w: %v", errEpochNotStarted, err)
	}
	defer w.Close()

	// Watch before READY, always. The journal exists first so that a client
	// which finds the file knows which generation to ask about; READY comes
	// only once the watches are actually registered, and a client whose cursor
	// sits below it is required to scan everything. That ordering is what makes
	// the window between "the daemon published itself" and "the kernel is
	// delivering events" harmless rather than merely short.
	if err := w.Watch(d.r.Path); err != nil {
		return fmt.Errorf("%w: %v", errEpochNotStarted, err)
	}

	// The control watch is established before READY too, and for the same
	// reason: READY is a promise that a client may store this position and skip
	// work, and that promise is only keepable if the daemon can also answer the
	// handshake that establishes it. A daemon that watched the tree but not the
	// requests would leave every client waiting out the timeout and scanning
	// everything — slower than having no daemon at all — so a control watch that
	// cannot be established fails the epoch rather than degrading into that.
	//
	// A polling backend gets none: no client will handshake with it, so there is
	// nothing to answer.
	var cw watcher
	if w.EventDriven() {
		cw, err = d.newControlWatcher()
		if err != nil {
			return fmt.Errorf("%w: control watcher: %v", errEpochNotStarted, err)
		}
		defer cw.Close()
		if err := os.MkdirAll(d.r.controlDir(), 0755); err != nil {
			return fmt.Errorf("%w: control directory: %v", errEpochNotStarted, err)
		}
		if err := cw.Watch(d.r.controlDir()); err != nil {
			return fmt.Errorf("%w: control directory: %v", errEpochNotStarted, err)
		}
	}

	if err := jw.writeRecord(recReady, []byte(w.Name())); err != nil {
		return fmt.Errorf("%w: %v", errEpochNotStarted, err)
	}

	info := &fsmonitorDaemonInfo{
		PID:       os.Getpid(),
		Gen:       gen,
		Backend:   w.Name(),
		Events:    w.EventDriven(),
		Watch:     d.r.Path,
		StartedAt: time.Now().UnixNano(),
	}
	// Heartbeat before the record: a client that finds the record goes on to
	// read the heartbeat, and must never find the one that is not there yet.
	if err := d.r.touchHeartbeat(); err != nil {
		return fmt.Errorf("%w: %v", errEpochNotStarted, err)
	}
	if err := d.r.saveDaemonInfo(info); err != nil {
		return fmt.Errorf("%w: %v", errEpochNotStarted, err)
	}
	// From here the daemon is discoverable and answering, so a start that got
	// this far is not a failure, whatever happens later.
	d.r.removeLastFailure()
	d.r.clearSpawnLock()

	return d.serve(w, cw, jw, info)
}

// serve records changes until the epoch ends.
func (d *daemon) serve(w, cw watcher, jw *journalWriter, info *fsmonitorDaemonInfo) error {
	// By closure, not by value: a rotation replaces the writer, and the one
	// that has to be closed at the end is whichever is current then.
	defer func() { jw.close() }()

	flush := time.NewTicker(d.fast())
	defer flush.Stop()
	beat := time.NewTicker(d.slow())
	defer beat.Stop()

	var pending watchBatch
	events := w.Events()
	// A nil channel blocks forever, which is exactly what a polling epoch wants
	// from a request stream it cannot serve.
	var requests <-chan []watchEvent
	if cw != nil {
		requests = cw.Events()
	}
	for {
		select {
		case <-d.stop:
			return errDaemonStopped

		case batch, ok := <-events:
			if !ok {
				// The backend closed its stream: an overflow, a dropped event,
				// a watch that vanished. None of those can be repaired in
				// place, and a backend that keeps running through one would be
				// silently missing paths — which the client would then skip
				// without stat-ing, i.e. a wrong answer rather than a slow one.
				return errCoverageLost
			}
			for _, e := range batch {
				pending.add(e.Path, e.Kind)
			}

		case _, ok := <-requests:
			if !ok {
				// Same reasoning as the tree stream: a control watch that has
				// stopped delivering means sync requests are going unanswered,
				// and clients cannot tell that from a daemon that is merely
				// slow — they would pay the timeout on every status and then
				// scan everything anyway.
				return errCoverageLost
			}
			// The paths in the batch are not read. The request file is the
			// message, and the directory is small enough to list on each
			// wakeup; that also survives a wakeup arriving for a file that
			// was removed, or a file appearing with no event at all.
			if err := d.serviceSyncRequests(jw, &pending); err != nil {
				return err
			}

		case <-flush.C:
			if err := d.checkStopFile(); err != nil {
				return err
			}
			if jw.size >= d.rotateAt() {
				if err := d.rotate(&jw, info, &pending); err != nil {
					return err
				}
			}
			if err := jw.writeChanges(pending.take()); err != nil {
				return err
			}

		case <-beat.C:
			if err := d.r.touchHeartbeat(); err != nil {
				return err
			}
			if err := d.supervise(jw.gen); err != nil {
				return err
			}
		}
	}
}

// supervise ends the epoch when the world has moved on.
//
// It runs on the slow tick, so it costs a handful of file reads every couple
// of seconds and is never on the path of a change.
func (d *daemon) supervise(gen uint64) error {
	if _, err := os.Stat(d.r.LoDir()); err != nil {
		// The repository itself is gone. There is nothing left to watch, and
		// nothing to report to.
		return errDaemonStopped
	}
	// Another daemon has published its own record: it owns the repository now,
	// and two daemons writing to different journals would leave clients
	// handshaking with whichever one they happened to read. Stepping aside is
	// how a duplicate start limits itself.
	if cur := d.r.loadDaemonInfo(); cur != nil && (cur.Gen != gen || cur.PID != os.Getpid()) {
		return errDaemonStopped
	}
	// The config was switched off. The daemon reads it from disk rather than
	// from its own copy, since its copy is as old as the process.
	if cfg, err := LoadConfig(d.r.Path); err == nil {
		v := strings.ToLower(strings.TrimSpace(cfg.Core.Fsmonitor))
		if v != FsmonitorOn {
			return errDaemonStopped
		}
	}
	return nil
}

// checkStopFile looks for the client's stop request.
//
// It is checked on the fast tick and not the slow one because it is the whole
// of how quickly an explicit `stop` takes effect: the client waits only a
// couple of seconds before escalating to killing the process, and a request
// that took a heartbeat interval to notice would always be escalated.
func (d *daemon) checkStopFile() error {
	if _, err := os.Stat(d.r.stopPath()); err == nil {
		return errDaemonStopped
	}
	return nil
}

// rotate starts a new generation.
//
// Rotation is not a special case of anything: a new generation *is* the
// protocol's signal that a stored cursor is void, so growing past the size
// limit, switching backends, and restarting all collapse into the same
// operation.
//
// The pending set is deliberately not flushed first. It is still in memory
// when this runs, so its paths are written after the new journal exists and
// land in the new generation, where the client that just scanned everything
// will read them as the over-approximation they are.
func (d *daemon) rotate(jw **journalWriter, info *fsmonitorDaemonInfo, pending *watchBatch) error {
	old := *jw
	gen := nextGeneration(d.prevGen, time.Now().UnixNano())
	d.prevGen = gen

	next, err := createJournalWriter(d.r, gen)
	if err != nil {
		// Nothing has changed yet: the current journal is intact and still
		// perfectly usable. Rotating is an optimisation, so failing to rotate
		// must not cost coverage.
		d.logf("fsmonitor: rotation to generation %s failed, staying on %s: %v",
			journalGenName(gen), journalGenName(old.gen), err)
		d.prevGen = old.gen
		return nil
	}
	if err := next.writeRecord(recReady, []byte(info.Backend)); err != nil {
		next.close()
		d.prevGen = old.gen
		d.logf("fsmonitor: rotation to generation %s failed, staying on %s: %v",
			journalGenName(gen), journalGenName(old.gen), err)
		return nil
	}

	// The record is replaced before the old journal is closed. A client that
	// reads the record in between finds the new generation, whose READY is
	// already written, and whose cursor check fails on the generation number
	// alone — so it scans everything. There is no ordering here that leaves a
	// client holding a cursor for a journal that is no longer being written.
	info.Gen = gen
	if err := d.r.saveDaemonInfo(info); err != nil {
		next.close()
		d.prevGen = old.gen
		d.logf("fsmonitor: rotation to generation %s failed, staying on %s: %v",
			journalGenName(gen), journalGenName(old.gen), err)
		return nil
	}

	*jw = next
	old.close()
	if err := os.Remove(old.path); err != nil {
		// Best effort. A reader holding the file prevents removal on Windows;
		// the startup sweep will collect it later, and until then it is inert.
		d.logf("fsmonitor: stale journal %s left behind: %v", filepath.Base(old.path), err)
	}
	return nil
}

func (r *Repository) clearStopFile() {
	os.Remove(r.stopPath())
}

func (r *Repository) clearSpawnLock() {
	os.Remove(r.spawnLockPath())
}

func (r *Repository) removeLastFailure() {
	os.Remove(r.lastFailurePath())
}

// ---- starting ----

// fsmonitorEventBackendAvailable reports whether this platform can serve the
// fast path at all.
//
// It asks the platform for a watcher and throws it away. That is the only
// honest answer available: "does this machine have a usable event API" is a
// question only the platform file can answer, and the alternative — spawning a
// daemon to find out — pays a process for the privilege of learning no.
//
// It is a var so a test can put this machine in the position of one without an
// event API, which is otherwise the one situation that cannot be arranged on a
// machine that has one.
var fsmonitorEventBackendAvailable = func() bool {
	w, err := newEventWatcher()
	if err != nil {
		return false
	}
	w.Close()
	return true
}

// fsmonitorSpawn starts the detached child.
//
// The seam exists because the alternative is worse than untestable: without
// it, any test that reaches this path forks a real daemon that outlives the
// test and watches a temporary directory that is about to be deleted. Tests
// replace it and count calls, which is what makes the spawn-once property of
// the lock checkable at all.
var fsmonitorSpawn = func(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// FsmonitorDaemonStart starts the change monitor for this repository.
func (r *Repository) FsmonitorDaemonStart() error {
	return r.spawnDaemon(0, true)
}

// EnsureFsmonitorDaemon makes sure a change monitor is running here, starting
// one if there is not.
//
// It returns as soon as the child is started. The daemon is detached and takes
// as long as a walk of the tree takes to publish itself — seconds, on a large
// repository — and the run that starts it cannot use a monitor that is not
// there yet, so waiting would buy nothing and cost that. That run scans
// everything, as it would have before this feature existed; the next one finds
// a daemon and a journal.
//
// A nil return covers both "a monitor is running" and "there is no reason for
// one" — the feature is off, a daemon described by somebody else's record owns
// this name, or another client is starting one right now. Anything else is an
// explanation worth showing, and none of them is a reason to fail a status.
func (r *Repository) EnsureFsmonitorDaemon() error {
	if !r.FsmonitorEnabled() {
		return nil
	}
	if info := r.loadDaemonInfo(); info != nil {
		if !r.belongsToThisRepo(info) {
			// A record carried in by a copy or a move. It describes a daemon
			// watching somewhere else, and starting one here would take the
			// name from a repository that is still using it.
			return nil
		}
		if r.liveDaemon() != nil {
			return nil
		}
	}
	err := r.spawnDaemon(0, true)
	if err == ErrDaemonBusy {
		// Another client holds the spawn lock: that is the race being resolved
		// correctly, and this run is one of the N-1 that scan everything.
		return nil
	}
	return err
}

// spawnDaemon starts a detached daemon process.
//
// The child is never waited for: it is meant to outlive the client that
// started it, and reaping it would also mean inheriting its lifetime.
func (r *Repository) spawnDaemon(pollInterval time.Duration, exclusive bool) error {
	if !r.FsmonitorEnabled() {
		return ErrNoMonitor
	}
	if pollInterval <= 0 && !fsmonitorEventBackendAvailable() {
		// A daemon that can only poll would keep a live heartbeat while
		// saving nothing, so it is not started by default. Running one by hand
		// with an explicit interval is still allowed, which is what the
		// debugging path does.
		return ErrNoMonitor
	}
	if f := r.spawnSuppressed(); f != nil {
		return fmt.Errorf("fsmonitor: not starting a change monitor: last attempt failed at %s: %s",
			time.Unix(0, f.At).Format(time.RFC3339), f.Reason)
	}

	spawned := false
	if exclusive {
		ok, err := r.acquireSpawnLock()
		if err != nil {
			return err
		}
		if !ok {
			return ErrDaemonBusy
		}
		defer func() {
			// The lock is the daemon's to clear, once it has published — and
			// that is the whole point: it is held across the daemon's startup,
			// which includes a full walk of the tree and can take seconds on a
			// large repository. Clearing it here instead would protect only the
			// fork, and let a second client spawn a duplicate daemon throughout
			// the walk. This only covers the paths where no daemon will ever
			// publish, so a failed spawn does not block the next attempt for
			// the whole staleness window.
			if !spawned {
				r.clearSpawnLock()
			}
		}()
	}

	exe, err := qinExecutable()
	if err != nil {
		return fmt.Errorf("fsmonitor: %w", err)
	}
	args := []string{"fsmonitor--daemon", "run", "--repo", r.Path}
	if pollInterval > 0 {
		args = append(args, "--poll-interval", pollInterval.String())
	}
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = detachSysProcAttr()

	// The child's output goes to a file, never to this process's stdio: an
	// inherited pipe would keep the caller's shell waiting for a process that
	// is designed not to end.
	//
	// Appended to, but only up to a point. A daemon that keeps losing coverage
	// writes a line per attempt and never exits, so an unbounded file would be
	// a slow leak that only shows up after the thing it is diagnosing has been
	// happening for a long time — which is precisely when the log is wanted.
	// Truncating at spawn bounds it without a rotation scheme.
	if fi, err := os.Stat(r.DaemonLogPath()); err == nil && fi.Size() > daemonLogMaxSize {
		os.Remove(r.DaemonLogPath())
	}
	logFile, err := os.OpenFile(r.DaemonLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("fsmonitor: open daemon log: %w", err)
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := fsmonitorSpawn(cmd); err != nil {
		return fmt.Errorf("fsmonitor: start daemon: %w", err)
	}
	spawned = true
	return nil
}

// qinExecutable locates this binary for re-execution.
func qinExecutable() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate this executable: %w", err)
	}
	// Linux appends " (deleted)" to /proc/self/exe when the file that was
	// exec'd has been unlinked — which is what rebuilding over a running
	// binary, or reinstalling over it, does. The suffix is part of the link
	// target, not of any name on disk, so it has to come off before the path
	// is used. Without this, the most ordinary sequence there is (rebuild,
	// then run status) fails to start the daemon.
	if strings.HasSuffix(p, deletedSuffix) {
		p = strings.TrimSuffix(p, deletedSuffix)
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("locate this executable: %w", err)
	}
	return p, nil
}

// acquireSpawnLock claims the right to start the daemon.
//
// N clients racing on a cold repository must produce exactly one child. O_EXCL
// gives that directly: one winner, and N-1 losers that do nothing and fall back
// to the full scan they were going to do anyway.
func (r *Repository) acquireSpawnLock() (bool, error) {
	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		return false, err
	}
	p := r.spawnLockPath()
	err := createExclusive(p)
	if err == nil {
		return true, nil
	}
	if !os.IsExist(err) {
		return false, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		// It vanished between the create failing and this stat: another
		// process is breaking the same stale lock. Doing nothing is right —
		// one of them will win, and this one is not the spawner.
		return false, nil
	}
	if time.Since(fi.ModTime()) < spawnLockStale {
		return false, nil
	}
	// Stale: the process that made it died without cleaning up. Both breakers
	// may remove it; exactly one of the two creates that follow wins, and the
	// other sees the other's file.
	os.Remove(p)
	if err := createExclusive(p); err != nil {
		return false, nil
	}
	return true, nil
}

func createExclusive(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	return f.Close()
}

// ---- stopping ----

// FsmonitorDaemonStop stops the daemon and drops the state it left behind.
func (r *Repository) FsmonitorDaemonStop() error {
	info := r.loadDaemonInfo()
	if r.belongsToThisRepo(info) {
		// Ask first. The request is a file, so a daemon that is still healthy
		// tidies up after itself (closing the journal properly) precisely
		// because it chose to stop.
		if err := r.requestStop(); err != nil {
			return err
		}
		if err := r.waitForDaemonExit(info.PID); err != nil {
			// It did not answer. Whatever the reason — killed, stopped by the
			// OS, or wedged — leaving it running would mean the next client
			// handshakes with a monitor that is not watching, so it is
			// terminated rather than waited for.
			if err := terminateDaemon(info.PID); err != nil {
				return fmt.Errorf("stop change monitor: %w", err)
			}
			_ = r.waitForDaemonExit(info.PID)
		}
	}
	return r.removeMonitorFiles()
}

// requestStop writes the stop file.
func (r *Repository) requestStop() error {
	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		return err
	}
	return ioutil.WriteFile(r.stopPath(), []byte("stop\n"), 0644)
}

// waitForDaemonExit waits for a daemon to be gone.
//
// Two signals, because neither is sufficient alone. A stale heartbeat is what
// actually decides whether clients will still try to talk to it, but it cannot
// go stale until the TTL has passed — so on its own it would make every clean
// stop look like a failure and escalate to killing a process that has already
// exited. The pid answers immediately and is what covers that case. It is
// checked second for exactly that reason: a pid is reused, so a live one is
// only interesting when the heartbeat says the daemon might still be there.
func (r *Repository) waitForDaemonExit(pid int) error {
	deadline := time.Now().Add(stopWait)
	for {
		if age, ok := r.heartbeatAge(); !ok || age > heartbeatTTL {
			return nil
		}
		if !daemonPidAlive(pid) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("change monitor did not exit")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// removeMonitorFiles deletes the daemon's on-disk footprint.
//
// The journals go last and only when no daemon is live, because a successor can
// have started in the meantime and its journal must not be taken out from under
// it. Generations left behind by a racing stop are inert — nothing reads a
// journal whose generation is not in the record — and the startup sweep
// collects them once they are old.
func (r *Repository) removeMonitorFiles() error {
	var firstErr error
	keep := func(err error) {
		if err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	_ = r.removeFsmonitorState()
	keep(os.Remove(r.daemonInfoPath()))
	keep(os.Remove(r.heartbeatPath()))
	keep(os.Remove(r.stopPath()))
	keep(os.Remove(r.spawnLockPath()))
	keep(os.Remove(r.lastFailurePath()))

	if r.liveDaemon() != nil {
		return firstErr
	}
	entries, err := ioutil.ReadDir(r.fsmonitorDir())
	if err != nil {
		return firstErr
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, fsmonitorJournalPrefix) && !strings.HasPrefix(name, fsmonitorSyncPrefix) {
			continue
		}
		keep(os.Remove(filepath.Join(r.fsmonitorDir(), name)))
	}
	// The control directory holds nothing but requests, and a request outlives
	// its usefulness the moment no daemon is running to answer it.
	keep(os.RemoveAll(r.controlDir()))
	return firstErr
}

// ---- reporting ----

// FsmonitorDaemonStatus describes the monitor's state for a human.
type FsmonitorDaemonStatus struct {
	Enabled     bool
	ConfigValue string

	// Available is whether this platform has an event-driven backend at all,
	// which is what decides whether a monitor can ever save work here.
	Available bool

	// Record is the daemon's published record, if any, and Alive whether it is
	// beating recently enough to try. The two disagree in the case a user
	// actually meets: a daemon that was killed leaves the record behind, so
	// Record is set and Alive is false.
	Record  *fsmonitorDaemonInfo
	Alive   bool
	HasBeat bool
	BeatAge time.Duration

	Gen         uint64
	JournalSize int64
	JournalOK   bool

	CursorGen     uint64
	CursorOffset  int64
	CursorBackend string
	CursorOK      bool

	Dirty int

	Failure *fsmonitorFailure
}

// FsmonitorDaemonStatus gathers the state the CLI reports.
func (r *Repository) FsmonitorDaemonStatus() *FsmonitorDaemonStatus {
	s := &FsmonitorDaemonStatus{
		Enabled:     r.FsmonitorEnabled(),
		Available:   fsmonitorEventBackendAvailable(),
		Record:      r.loadDaemonInfo(),
		Failure:     r.loadLastFailure(),
		ConfigValue: strings.TrimSpace(r.Config.Core.Fsmonitor),
	}
	if age, ok := r.heartbeatAge(); ok {
		s.HasBeat = true
		s.BeatAge = age
	}
	if r.liveDaemon() != nil {
		s.Alive = true
	}
	if st := r.loadFsmonitorState(); st != nil {
		s.CursorGen, s.CursorOffset, s.CursorBackend, s.CursorOK = st.Gen, st.Offset, st.Backend, true
		s.Dirty = len(st.Dirty)
	}
	if s.Record != nil && s.Record.Gen != 0 {
		s.Gen = s.Record.Gen
		// The journal's presence is what a client checks first; its absence
		// under a live record means the record is stale or the file was
		// removed.
		if fi, err := os.Stat(r.journalPath(s.Record.Gen)); err == nil {
			s.JournalOK = true
			s.JournalSize = fi.Size()
		}
	}
	return s
}
