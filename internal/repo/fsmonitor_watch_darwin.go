//go:build darwin && cgo
// +build darwin,cgo

package repo

/*
#cgo LDFLAGS: -framework CoreServices -framework CoreFoundation
#include "fsmonitor_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"
)

// fseventsWatcher watches a tree through FSEvents.
//
// A stream covers a subtree the way the Windows handle does, so there is no
// per-directory registration to keep in step and no descriptor to run out of —
// which is the reason this backend exists rather than one built on kqueue,
// where file content changes can only be seen with a descriptor per watched
// file, against a limit a few hundred deep.
//
// What is left for this file is the stream's lifetime and the one question the
// events do not always answer: what kind of path is this. The flags answer it
// when they are there, and the filesystem is asked when they are not. Every
// decision is classifyFsevents's, in a file with no build tag.
type fseventsWatcher struct {
	handle uintptr
	stream C.FSEventStreamRef
	queue  C.dispatch_queue_t

	done chan struct{}
	stop sync.Once
	end  sync.Once

	// events is closed when the epoch ends — by Close, or by the callback
	// finding an event it cannot describe. The consumer reads a closed channel
	// as "coverage is gone", which is the only reading that is ever safe.
	events chan []watchEvent

	root string
	dirs map[string]bool

	// lost is set by the callback when it ends the epoch itself, and read by no
	// one else: the channel closing is what the consumer acts on. It exists so
	// that a batch already in flight is not reported after the loss.
	lost bool

	// mu publishes the teardown pair so Close can take them without holding a
	// lock the callback needs. The callback never takes it; if it did, Close
	// waiting for a callback that is waiting for Close's lock would be a
	// deadlock rather than a delay.
	mu sync.Mutex
}

// fseventsLatency is how long the stream may hold events back while looking for
// more. NoDefer makes it a window that opens at the first event rather than a
// delay in front of it, so a quiet tree is reported at once and a busy one is
// reported in batches.
const fseventsLatency = 0.05

// newEventWatcher returns the macOS event-driven watcher.
func newEventWatcher() (watcher, error) {
	if err := fseventsABI(); err != nil {
		// The constants this build decodes with are not the ones this system
		// sends. There is no safe way to guess which is wrong, so no watcher is
		// offered and status scans everything.
		return nil, err
	}
	return &fseventsWatcher{
		done:   make(chan struct{}),
		events: make(chan []watchEvent, 1),
		dirs:   make(map[string]bool),
	}, nil
}

func (w *fseventsWatcher) Name() string      { return "fsevents" }
func (w *fseventsWatcher) EventDriven() bool { return true }
func (w *fseventsWatcher) Events() <-chan []watchEvent {
	return w.events
}

// fseventsABI checks the transcribed flag values against CoreServices'.
func fseventsABI() error {
	ours := make([]C.uint32_t, len(fsEventFlags))
	for i, f := range fsEventFlags {
		ours[i] = C.uint32_t(f)
	}
	// unsafe.Pointer rather than &ours[0]: cgo is strict about pointer types,
	// and the C parameter's `unsigned int *` need not be the same Go type as
	// C.uint32_t.
	bad := C.qin_fsevents_abi_check(unsafe.Pointer(&ours[0]), C.size_t(len(ours)))
	if bad != nil {
		return fmt.Errorf("fsmonitor: FSEvents flag %s does not match this system's CoreServices", C.GoString(bad))
	}
	return nil
}

// Watch starts a stream over the whole tree, or fails.
//
// One stream either covers the tree or is not created, so unlike the Linux
// backend there is nothing to unwind part-way; the walk that fills in the
// directory bookkeeping can fail, and then the stream is torn down and the
// caller falls back to a full scan.
func (w *fseventsWatcher) Watch(root string) error {
	select {
	case <-w.done:
		return errors.New("fsevents watcher already closed")
	default:
	}

	// The stream is rooted at a resolved path because FSEvents reports the
	// paths it resolved, not the ones it was given. /tmp and /var are symlinks
	// into /private on macOS, and a temporary repository is routinely under
	// one, so a stream created from the path as written reports every event
	// under a prefix filepath.Rel cannot strip. Each one would then look like
	// it came from outside the repository and be dropped — a monitor that
	// reports nothing while looking perfectly healthy. The resolution has to
	// happen here rather than at the caller, because it is this file that has
	// to match what the kernel will say.
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("fsmonitor: resolve %s: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("fsmonitor: resolve %s: %w", root, err)
	}

	croot := C.CString(resolved)
	defer C.free(unsafe.Pointer(croot))

	w.handle = fseventsRegister(w)
	stream := C.qin_fsevents_create(croot, C.uintptr_t(w.handle), C.double(fseventsLatency))
	if stream == nil {
		fseventsUnregister(w.handle)
		return fmt.Errorf("fsmonitor: could not create a stream over %s", resolved)
	}
	w.stream = stream

	// The inventory is taken before the stream is started, so that no callback
	// can run while it is being filled in. Anything that happens during the
	// walk is not lost by the ordering: the stream's starting point is fixed
	// when it is created, above, and every event after that point is delivered
	// once it starts. What it costs is nothing else — see the READY record.
	w.root = resolved
	if err := w.recordDirs(); err != nil {
		w.teardown()
		return err
	}

	// A serial queue, because the callback is a callback into Go: two of them at
	// once would be two goroutines sharing the directory set. Serial delivery is
	// the stream's own ordering guarantee, and using it is cheaper than
	// defending against the absence of it.
	queue := C.dispatch_queue_create(nil, nil)
	if queue == nil {
		w.teardown()
		return errors.New("fsmonitor: could not create a dispatch queue for the stream")
	}
	w.queue = queue
	C.FSEventStreamSetDispatchQueue(stream, queue)
	if C.FSEventStreamStart(stream) == 0 {
		w.teardown()
		return fmt.Errorf("fsmonitor: could not start a stream over %s", resolved)
	}
	return nil
}

// recordDirs takes the initial inventory of directories.
//
// The stream does not need it to cover the tree — it covers the tree by
// construction — but classifyDirEvent needs to be able to tell a directory we
// knew about from one that appeared while we watched: the first, on its way
// out, takes names with it that nothing will report, and the second brings
// names in. It is the same walk the Windows backend makes for the same reason.
func (w *fseventsWatcher) recordDirs() error {
	return filepath.Walk(w.root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // vanished mid-walk; its removal is reported
			}
			return err
		}
		if !fi.IsDir() || p == w.root {
			return nil
		}
		if rel, ok := relWatchPath(w.root, p); ok {
			w.dirs[rel] = true
		}
		return nil
	})
}

// abs joins a repo-relative slash path back onto the watched root.
func (w *fseventsWatcher) abs(rel string) string {
	return filepath.Join(w.root, filepath.FromSlash(rel))
}

// enumerate reports every path under a directory.
//
// A directory that appears — created and populated, or moved in from elsewhere —
// brings files that no event named, and they have to be reported or the consumer
// would treat them as unchanged for as long as the epoch lasts. It also records
// every directory it passes, which is how a directory that arrived inside one is
// known from then on.
//
// A failure that is not "it was already gone" ends the epoch: a subtree this
// walk could not read is a subtree whose contents were never named. A directory
// that was created and removed again inside one callback is the case the
// "already gone" arm is for — the walk reports nothing because there is nothing
// there, which is also correct.
func (w *fseventsWatcher) enumerate(rel string, b *watchBatch) error {
	return filepath.Walk(w.abs(rel), func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		r, ok := relWatchPath(w.root, p)
		if !ok {
			return nil
		}
		if fi.IsDir() {
			w.dirs[r] = true
		}
		b.add(r, watchChanged)
		return nil
	})
}

// teardown stops the stream and releases everything it holds. It is idempotent
// in the sense that matters — the fields are published before use, so a second
// call finds nothing to release.
func (w *fseventsWatcher) teardown() {
	w.mu.Lock()
	stream, queue := w.stream, w.queue
	w.stream, w.queue = nil, nil
	w.mu.Unlock()

	if stream != nil {
		C.FSEventStreamStop(stream)
		C.FSEventStreamInvalidate(stream)
	}
	// Between stop and release: a callback that was already running when the
	// stream was invalidated is still in this process, still holding the handle
	// and still touching the state the stream's own code is about to free.
	if queue != nil {
		C.qin_fsevents_drain_queue(queue)
	}
	if stream != nil {
		C.FSEventStreamRelease(stream)
	}
	if queue != nil {
		C.qin_fsevents_release_queue(queue)
	}
}

func (w *fseventsWatcher) Close() error {
	w.stop.Do(func() {
		// The flag first: a callback blocked handing over a batch is released by
		// this, and the drain below would otherwise be waiting for a callback
		// that is waiting for a consumer that has already gone away.
		close(w.done)
		w.teardown()
		fseventsUnregister(w.handle)
		w.endCoverage()
	})
	return nil
}

// endCoverage closes the event stream, once, whichever path got here first.
func (w *fseventsWatcher) endCoverage() {
	w.end.Do(func() { close(w.events) })
}

// deliver turns one callback's worth of events into at most one batch.
//
// It runs on the stream's dispatch queue, so the directory set is its alone to
// touch and the events it hands over are in the order the kernel recorded them.
func (w *fseventsWatcher) deliver(paths unsafe.Pointer, numEvents C.size_t, flags unsafe.Pointer) {
	select {
	case <-w.done:
		return
	default:
	}
	if w.lost {
		return
	}
	n := int(numEvents)
	var b watchBatch
	for i := 0; i < n; i++ {
		abs := C.GoString(C.qin_fsevents_path(paths, C.size_t(i)))
		f := uint32(C.qin_fsevents_flag(flags, C.size_t(i)))

		rel, known := relWatchPath(w.root, abs)
		wasDir := known && w.dirs[rel]
		out := classifyFsevents(f, wasDir)
		if out.Lost != "" {
			w.lose()
			return
		}
		if !known {
			// Outside the repository, or the monitor's own state directory.
			// Neither is a missing change: the object store is written
			// constantly, and reporting it would mean a full scan after every
			// commit.
			continue
		}
		if out.Unknown {
			// The flags named a path without saying what happened to it, so it
			// is asked about rather than guessed at — the same resolution the
			// Windows backend applies to every record it reads, because its
			// records never carry the answer.
			fi, err := os.Lstat(w.abs(rel))
			if err != nil {
				// It appeared and is gone again inside one window, or it cannot
				// be read at all. For a file that is an ordinary temporary file
				// and nothing is lost; for a directory it means names beneath it
				// were never reported and cannot now be recovered, and the two
				// are indistinguishable here. The ambiguity is resolved the only
				// safe way, which costs a full scan for a coincidence this
				// narrow.
				w.lose()
				return
			}
			if fi.IsDir() {
				// A directory whose contents changed without any file being
				// named — including one that is new to us. Either way the way to
				// learn what is inside it is to look.
				out.Action = dirActionEnumerate
				out.Dir = true
			}
		}
		switch out.Action {
		case dirActionEndEpoch:
			w.lose()
			return
		case dirActionEnumerate:
			if err := w.enumerate(rel, &b); err != nil {
				w.lose()
				return
			}
		default:
			if out.Kind == watchRemoved {
				delete(w.dirs, rel)
			} else if out.Dir {
				w.dirs[rel] = true
			}
		}
		if out.Emit {
			b.add(rel, out.Kind)
		}
	}

	batch := b.flush()
	if batch == nil {
		return
	}
	select {
	case w.events <- batch:
	case <-w.done:
	}
}

// lose ends the epoch from inside the callback.
func (w *fseventsWatcher) lose() {
	w.lost = true
	w.endCoverage()
}

// ---- the registry ----

// The callback arrives as an integer handle rather than a pointer, because the
// watcher is a Go value: the stream retains what it is given for as long as it
// lives, and would be retaining a Go pointer across collections and calling into
// it from a thread the runtime did not create. A number that indexes this map
// crosses that boundary instead, and nothing in C dereferences it.
var fseventsRegistry = struct {
	sync.Mutex
	next    uintptr
	entries map[uintptr]*fseventsWatcher
}{next: 1, entries: make(map[uintptr]*fseventsWatcher)}

func fseventsRegister(w *fseventsWatcher) uintptr {
	fseventsRegistry.Lock()
	defer fseventsRegistry.Unlock()
	// Zero is never issued: it is the null pointer on the far side, and it is
	// also what a watcher that never got this far carries.
	h := fseventsRegistry.next
	fseventsRegistry.next++
	fseventsRegistry.entries[h] = w
	return h
}

func fseventsUnregister(h uintptr) {
	fseventsRegistry.Lock()
	defer fseventsRegistry.Unlock()
	delete(fseventsRegistry.entries, h)
}

// fseventsLookup finds the watcher a callback is for. It takes the lock only to
// read the map, never while delivering: a callback that held this lock while
// blocked on the consumer would deadlock a Close that is waiting for the
// callback to finish before it releases the stream.
func fseventsLookup(h uintptr) *fseventsWatcher {
	fseventsRegistry.Lock()
	defer fseventsRegistry.Unlock()
	return fseventsRegistry.entries[h]
}

//export qinFseventsDispatch
func qinFseventsDispatch(handle C.uintptr_t, paths unsafe.Pointer, numEvents C.size_t, flags unsafe.Pointer) {
	w := fseventsLookup(uintptr(handle))
	if w == nil || numEvents == 0 {
		return
	}
	w.deliver(paths, numEvents, flags)
}
