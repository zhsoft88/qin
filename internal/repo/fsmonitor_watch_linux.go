//go:build linux
// +build linux

package repo

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// inotifyABI checks the parser's constants against the syscall package's.
//
// fsmonitor_parse.go declares the kernel's values itself so that it can live in
// a file with no build tag and be tested on any machine. This is what keeps
// that second copy honest: a divergence would leave the parser decoding masks
// nobody sent, and a watcher that reports nothing is indistinguishable from a
// tree that never changes.
func inotifyABI() error {
	for _, c := range []struct {
		name        string
		ours        uint32
		fromSyscall uint32
	}{
		{"IN_MODIFY", inModify, syscall.IN_MODIFY},
		{"IN_ATTRIB", inAttrib, syscall.IN_ATTRIB},
		{"IN_CLOSE_WRITE", inCloseWrite, syscall.IN_CLOSE_WRITE},
		{"IN_MOVED_FROM", inMovedFrom, syscall.IN_MOVED_FROM},
		{"IN_MOVED_TO", inMovedTo, syscall.IN_MOVED_TO},
		{"IN_CREATE", inCreate, syscall.IN_CREATE},
		{"IN_DELETE", inDelete, syscall.IN_DELETE},
		{"IN_DELETE_SELF", inDeleteSelf, syscall.IN_DELETE_SELF},
		{"IN_MOVE_SELF", inMoveSelf, syscall.IN_MOVE_SELF},
		{"IN_Q_OVERFLOW", inQOverflow, syscall.IN_Q_OVERFLOW},
		{"IN_IGNORED", inIgnored, syscall.IN_IGNORED},
		{"IN_ISDIR", inIsDir, syscall.IN_ISDIR},
		{"IN_ONLYDIR", inOnlyDir, syscall.IN_ONLYDIR},
		{"IN_EXCL_UNLINK", inExclUnlink, syscall.IN_EXCL_UNLINK},
	} {
		if c.ours != c.fromSyscall {
			return fmt.Errorf("fsmonitor: %s is 0x%x here but 0x%x in syscall",
				c.name, c.ours, c.fromSyscall)
		}
	}
	return nil
}

// inotifyWatcher watches a tree through inotify.
//
// One descriptor holds every watch, so the kernel reports a change against a
// watch descriptor rather than a path, and this file's real job is keeping the
// descriptor-to-directory mapping current. It is a translation layer: the
// buffer decoding lives in fsmonitor_parse.go and the batching in
// fsmonitor_watch.go, both untagged and both tested elsewhere.
type inotifyWatcher struct {
	fd   int
	done chan struct{}
	stop sync.Once

	events chan []watchEvent

	root  string
	names map[int32]string // watch descriptor -> repo-relative directory
}

// newEventWatcher returns the Linux event-driven watcher.
//
// The descriptor is opened by Watch rather than here, so that constructing a
// watcher that is never used holds nothing to release.
func newEventWatcher() (watcher, error) {
	if err := inotifyABI(); err != nil {
		return nil, err
	}
	return &inotifyWatcher{
		fd:     -1,
		done:   make(chan struct{}),
		events: make(chan []watchEvent, 1),
		names:  make(map[int32]string),
	}, nil
}

func (w *inotifyWatcher) Name() string      { return "inotify" }
func (w *inotifyWatcher) EventDriven() bool { return true }
func (w *inotifyWatcher) Events() <-chan []watchEvent {
	return w.events
}

// inotifyMask is what a watched directory reports.
//
// IN_MODIFY is included even though IN_CLOSE_WRITE would cover the common case
// of an editor saving a file: a file under a long write — a build artefact, a
// database, dd — is invisible until it is closed, so a status run taken during
// the write would report it unchanged. That is a wrong conclusion of exactly
// the kind a monitor must never produce, and it is one the plain stat path
// would have caught.
const inotifyMask = inCreate | inDelete | inMovedFrom | inMovedTo |
	inModify | inCloseWrite | inAttrib | inDeleteSelf | inMoveSelf |
	inExclUnlink | inOnlyDir

// Watch registers the whole tree, or fails.
//
// Registration is all-or-nothing: a directory that could not be watched would
// be a subtree whose changes are never reported, and a change that is never
// reported is a path status skips rather than an error it surfaces. So a
// failure part-way discards everything already registered and returns, and the
// caller falls back to a full scan.
func (w *inotifyWatcher) Watch(root string) error {
	select {
	case <-w.done:
		return errors.New("inotify watcher already closed")
	default:
	}
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return fmt.Errorf("inotify_init1: %w", err)
	}
	w.fd = fd
	w.root = root
	if err := w.addTree(root, nil); err != nil {
		w.dropAll()
		return err
	}
	go w.run()
	return nil
}

// addTree watches dir and everything below it.
//
// The order is load-bearing: the watch is added before the directory is read,
// never after. Reading first and watching second leaves a window in which a
// name is known to exist but is not yet watched — anything created in it is
// never reported at all, which is precisely the silent miss this design exists
// to prevent.
//
// When a batch is supplied, every path the walk enumerates is also reported as
// changed. That closes the same window one level up: a directory created or
// moved in becomes watched part-way through being populated, so the files that
// arrived before the watch did are announced by name instead of being assumed
// unchanged. The initial scan passes no batch, because there is no cursor yet
// for that assumption to be wrong about.
func (w *inotifyWatcher) addTree(dir string, b *watchBatch) error {
	rel, ok := relWatchPath(w.root, dir)
	if !ok && dir != w.root {
		return nil // outside the repository, or the monitor's own state
	}
	wd, err := syscall.InotifyAddWatch(w.fd, dir, inotifyMask)
	if err != nil {
		return fmt.Errorf("inotify_add_watch %s: %w", dir, err)
	}
	if wd < 0 {
		return fmt.Errorf("inotify_add_watch %s: descriptor %d", dir, wd)
	}
	w.names[int32(wd)] = rel

	entries, err := ioutil.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // vanished mid-walk; its removal was reported
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		child := filepath.Join(dir, e.Name())
		childRel, ok := relWatchPath(w.root, child)
		if !ok {
			continue
		}
		if b != nil {
			b.add(childRel, watchChanged)
		}
		if e.IsDir() {
			if err := w.addTree(child, b); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropAll closes the descriptor, which is the only way to release every watch
// at once. Individual removals would be N syscalls to no benefit: the
// descriptor is going away either way.
func (w *inotifyWatcher) dropAll() {
	if w.fd >= 0 {
		syscall.Close(w.fd)
		w.fd = -1
	}
	w.names = make(map[int32]string)
}

// run reads the descriptor until it is closed or coverage is lost.
//
// The descriptor is non-blocking, so a read that has nothing to return says so
// immediately and the loop waits instead of spinning. The wait is bounded so
// that a Close from another goroutine is noticed promptly — the descriptor is
// closed by this goroutine alone, which is what keeps the file descriptor
// number from being reused underneath an in-flight read.
func (w *inotifyWatcher) run() {
	defer close(w.events)
	defer w.dropAll() // the reader owns the descriptor, so it closes it
	buf := make([]byte, 64*1024)
	for {
		select {
		case <-w.done:
			return
		default:
		}

		n, err := syscall.Read(w.fd, buf)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EINTR {
				// Nothing queued. Sleep briefly rather than block, so the
				// stop flag is checked at a bounded interval.
				select {
				case <-w.done:
					return
				case <-time.After(50 * time.Millisecond):
				}
				continue
			}
			return // coverage lost: the descriptor itself failed
		}
		if n <= 0 {
			continue
		}

		var batch watchBatch
		events, perr := parseInotifyEvents(buf[:n], w.names)
		if perr != nil {
			return // overflow, a moved subtree, or a malformed buffer
		}
		for _, e := range events {
			// A directory that has just appeared needs a watch of its own,
			// and everything already inside it is announced rather than
			// assumed unchanged — the watch was added part-way through the
			// subtree being populated, so its earlier contents were never
			// reported to us and cannot be inferred to be unchanged.
			if e.Dir && e.Kind == watchChanged {
				full := filepath.Join(w.root, filepath.FromSlash(e.Path))
				if err := w.addTree(full, &batch); err != nil {
					return // coverage lost
				}
			}
			batch.add(e.Path, e.Kind)
		}
		out := batch.flush()
		if out == nil {
			continue
		}
		select {
		case w.events <- out:
		case <-w.done:
			return
		}
	}
}

func (w *inotifyWatcher) Close() error {
	w.stop.Do(func() {
		close(w.done)
		// The reader owns the descriptor and closes it when it next wakes,
		// which is within one wait interval. Waiting for the events channel to
		// close here would make Close depend on the reader making progress,
		// which is what it exists to allow.
	})
	return nil
}
