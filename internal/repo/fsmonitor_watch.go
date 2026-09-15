package repo

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A watcher turns platform change notifications into a stream of repo-relative
// paths. Everything in this file is platform-independent on purpose.
//
// The plan is for each platform's own file to be a thin translation layer —
// open the kernel handle, decode its buffer, emit watchEvents — with no
// batching, no deduplication, and no degrade decisions of its own. Those live
// here, in files with no build tag, so they can be tested on a machine that
// runs only one of the three platforms. What is left in a platform file is the
// syscall plumbing, which is the part that genuinely cannot be exercised
// anywhere but its own OS.
type watchKind uint8

const (
	// watchChanged covers a create, a content write, or a metadata change.
	watchChanged watchKind = iota
	// watchRemoved covers a delete or a move away.
	watchRemoved
)

// watchEvent names one path the monitor believes changed. Both kinds collapse
// to the same action downstream — stat the path and see — so the distinction
// exists for the backends' own bookkeeping, not for the consumer.
type watchEvent struct {
	Path string // repo-relative, slash-separated; never ".qin/...", never absolute
	Kind watchKind
	// Dir marks an event about a directory, so a backend can decide whether a
	// new subtree needs watching without asking the filesystem again — the
	// kernel already said so.
	Dir bool
}

// watcher is one coverage epoch's view of the working tree.
//
// Watch must either cover the whole tree or fail. Partial registration is
// worse than no monitor at all, and silently so: a path missing from the
// change set is a path status.go skips without stat-ing it, so an uncovered
// subtree does not mean "slower", it means "wrong".
type watcher interface {
	// Watch begins watching root. Any registration failure returns non-nil,
	// and the caller must discard whatever was registered.
	Watch(root string) error

	// Events yields batches of changes. A closed channel means coverage was
	// lost — an overflow, a dropped event, a watch that vanished — and the
	// caller must rebuild from a fresh generation rather than keep trusting
	// the stream. Close also closes it.
	Events() <-chan []watchEvent

	// Name identifies the backend in the journal's READY record.
	Name() string

	// EventDriven reports whether changes arrive by notification. A polling
	// backend's view lags by up to one interval, which is precisely the window
	// a client would wrongly skip, so it may not serve the fast path.
	EventDriven() bool

	// Close stops watching. It is idempotent and must wake a blocked read.
	Close() error
}

// errNoEventBackend is returned on platforms with no usable event API, either
// because none exists or because this build cannot reach it (darwin without
// cgo cannot reach FSEvents). status stays correct; it just always scans.
var errNoEventBackend = errors.New("no event-driven change monitor is available on this platform")

// ---- backend selection ----

// selectWatcher picks the backend for one coverage epoch.
//
// Event-driven is preferred. pollInterval > 0 forces the polling backend
// instead, which is a debugging aid — it is correct but pays unconditionally
// for what it finds, so it is never chosen automatically.
func selectWatcher(pollInterval time.Duration) (watcher, error) {
	if pollInterval > 0 {
		return newPollWatcher(pollInterval), nil
	}
	return newEventWatcher()
}

// ---- shared policy ----

// relWatchPath maps an absolute path under root to the repo-relative slash
// form the index uses. It reports false for anything a backend should not
// report: paths outside the root, the root itself, and the monitor's own state
// directory.
//
// Backends all funnel through this rather than formatting paths themselves, so
// there is exactly one definition of what a path in the change stream looks
// like.
func relWatchPath(root, abs string) (string, bool) {
	rel, err := filepath.Rel(root, abs)
	if err != nil || isOutsideRepo(rel) {
		return "", false
	}
	if rel == "." {
		return "", false
	}
	p := filepath.ToSlash(rel)
	return p, validChangePath(p)
}

// watchBatch collapses events by path between two reads.
//
// The consumer's only question is "which paths need a stat", and that is a set,
// not a sequence. A build writing a file fifty thousand times becomes one
// entry; a create-then-delete becomes one entry that simply fails its stat. So
// batching is not an optimisation layered on top of correctness here — the set
// is the actual contract.
type watchBatch struct {
	order []string
	kinds map[string]watchKind
}

// add records a change to path. A second event for a path keeps the newer
// kind, so a create-then-remove reports as removed and a remove-then-recreate
// as changed: later knowledge wins.
func (b *watchBatch) add(path string, kind watchKind) {
	if b.kinds == nil {
		b.kinds = make(map[string]watchKind)
	}
	if _, seen := b.kinds[path]; !seen {
		b.order = append(b.order, path)
	}
	b.kinds[path] = kind
}

// take returns the accumulated paths in first-seen order and empties the batch.
//
// The daemon records paths without their kinds: a CHANGE record answers "stat
// this", and whether the path was there or gone is what the stat says. Keeping
// the kinds would mean journaling a distinction the reader does not act on,
// and one it could act wrongly on — a recorded "removed" that a later create
// contradicts is a stale conclusion, whereas the path alone is only a hint to
// look.
func (b *watchBatch) take() []string {
	if len(b.order) == 0 {
		return nil
	}
	out := b.order
	b.order = nil
	b.kinds = nil
	return out
}

// flush returns the accumulated events in first-seen order, or nil when
// nothing was recorded — so a quiet interval sends no batch at all rather than
// an empty one.
func (b *watchBatch) flush() []watchEvent {
	if len(b.order) == 0 {
		return nil
	}
	out := make([]watchEvent, 0, len(b.order))
	for _, p := range b.order {
		out = append(out, watchEvent{Path: p, Kind: b.kinds[p]})
	}
	b.order = nil
	b.kinds = nil
	return out
}

// ---- polling backend ----

// pollEntry is what the polling backend compares between ticks. Every field is
// comparable, so two entries are equal exactly when nothing worth reporting
// changed.
type pollEntry struct {
	size  int64
	mtime int64
	mode  os.FileMode
	dir   bool
}

// pollWatcher notices changes by walking the tree and comparing snapshots.
//
// It exists so that a monitor is always available, but it is honest about what
// it is: every tick costs a full traversal, so it does not save the work status
// would have done — it relocates it. Its view also lags by up to one interval,
// which is a window during which its silence is not evidence. That is why it
// never reports itself as event-driven and therefore never enables the fast
// path; it is a correctness fallback and a debugging aid, not a speedup.
type pollWatcher struct {
	interval time.Duration
	done     chan struct{}
	events   chan []watchEvent
	closeOne sync.Once

	root string
	prev map[string]pollEntry
}

func newPollWatcher(interval time.Duration) *pollWatcher {
	return &pollWatcher{
		interval: interval,
		done:     make(chan struct{}),
		events:   make(chan []watchEvent, 1),
	}
}

func (w *pollWatcher) Name() string                { return "polling" }
func (w *pollWatcher) EventDriven() bool           { return false }
func (w *pollWatcher) Events() <-chan []watchEvent { return w.events }

// Watch takes the baseline snapshot and starts ticking.
//
// The baseline is taken here, not by the caller, so that no change can slip
// between "watching" and "comparing": the next tick diffs against a snapshot
// that already exists. The baseline itself emits nothing — it is the
// comparison point, not a set of changes.
func (w *pollWatcher) Watch(root string) error {
	snap, err := pollScan(root)
	if err != nil {
		return err
	}
	w.root = root
	w.prev = snap
	go w.run()
	return nil
}

func (w *pollWatcher) run() {
	defer close(w.events)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-t.C:
			cur, err := pollScan(w.root)
			if err != nil {
				// The comparison point can no longer be maintained, so there
				// is no version of the change set that can be called complete.
				// Closing the channel reports that; emitting a partial batch
				// would not.
				return
			}
			var b watchBatch
			pollDiff(w.prev, cur, &b)
			w.prev = cur
			batch := b.flush()
			if batch == nil {
				continue
			}
			select {
			case w.events <- batch:
			case <-w.done:
				return
			}
		}
	}
}

func (w *pollWatcher) Close() error {
	w.closeOne.Do(func() { close(w.done) })
	return nil
}

// pollScan snapshots every path under root except the object store.
//
// Unlike the event backends this does not prune ignored or untracked
// subtrees: the consumer only ever looks the reported paths up in the index, so
// reporting extra ones costs a map lookup, while pruning one wrongly would cost
// a change. Cheap-but-safe beats clever here, given the backend is not the
// default.
func pollScan(root string) (map[string]pollEntry, error) {
	out := make(map[string]pollEntry)
	err := filepath.Walk(root, func(p string, fi os.FileInfo, werr error) error {
		if werr != nil {
			// A file that vanished between the readdir and the stat is not a
			// failure; the next tick simply will not see it. Anything else
			// means a subtree went unread, which is exactly the silence that
			// must not pass for "unchanged".
			if os.IsNotExist(werr) {
				return nil
			}
			return werr
		}
		if p == root {
			return nil
		}
		rel, ok := relWatchPath(root, p)
		if !ok {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		out[rel] = pollEntry{
			size:  fi.Size(),
			mtime: fi.ModTime().UnixNano(),
			mode:  fi.Mode(),
			dir:   fi.IsDir(),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// pollDiff reports every path whose entry differs, plus every path that
// appeared or disappeared.
func pollDiff(prev, cur map[string]pollEntry, b *watchBatch) {
	for p, e := range cur {
		old, ok := prev[p]
		if !ok {
			b.add(p, watchChanged)
			continue
		}
		if old != e {
			b.add(p, watchChanged)
		}
	}
	for p := range prev {
		if _, ok := cur[p]; !ok {
			b.add(p, watchRemoved)
		}
	}
}
