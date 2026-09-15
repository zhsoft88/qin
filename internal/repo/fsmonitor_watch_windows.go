//go:build windows
// +build windows

package repo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// windowsWatcher watches a tree through ReadDirectoryChangesW.
//
// One handle covers the whole subtree, so unlike the Linux backend there is no
// per-directory registration to add as directories appear and nothing to keep
// in step. What this file does have to do is answer a question the kernel will
// not: whether a path is a directory. The records carry no such flag, and a
// path that has been removed cannot be asked. So the set of known directories
// is maintained here, and classifyDirEvent turns it into a decision.
type windowsWatcher struct {
	handle syscall.Handle
	done   chan struct{}
	stop   sync.Once

	events chan []watchEvent

	root string
	dirs map[string]bool

	// first is set until the reading goroutine has returned from its first
	// call. The change buffer does not exist before then; see run.
	first bool

	mu sync.Mutex // guards handle, so a blocked read can be cancelled
}

// windowsNotifyMask is what the handle reports.
//
// FILE_NOTIFY_CHANGE_SIZE and FILE_NOTIFY_CHANGE_LAST_WRITE are the ones that
// make a content edit visible. Without them an edit that leaves the directory
// entry list unchanged is silent, which is exactly the case this feature exists
// to catch. FILE_NAME and DIR_NAME cover creation, deletion and renaming.
const windowsNotifyMask = syscall.FILE_NOTIFY_CHANGE_FILE_NAME |
	syscall.FILE_NOTIFY_CHANGE_DIR_NAME |
	syscall.FILE_NOTIFY_CHANGE_SIZE |
	syscall.FILE_NOTIFY_CHANGE_LAST_WRITE |
	syscall.FILE_NOTIFY_CHANGE_CREATION

// windowsNotifyBufSize is the change buffer size.
//
// It is the largest that works everywhere: a synchronous call fails with
// ERROR_INVALID_PARAMETER above 64 KB when the watched directory is on a
// network share, because of a limit in the underlying file sharing protocol.
const windowsNotifyBufSize = 64 * 1024

// newEventWatcher returns the Windows event-driven watcher.
func newEventWatcher() (watcher, error) {
	return &windowsWatcher{
		handle: syscall.InvalidHandle,
		done:   make(chan struct{}),
		events: make(chan []watchEvent, 1),
		dirs:   make(map[string]bool),
		first:  true,
	}, nil
}

func (w *windowsWatcher) Name() string      { return "windows" }
func (w *windowsWatcher) EventDriven() bool { return true }
func (w *windowsWatcher) Events() <-chan []watchEvent {
	return w.events
}

// Watch opens the directory handle and starts reading.
//
// One handle fails as a whole or covers as a whole, so there is no partial
// registration to undo here — but the walk that fills in the directory set can
// fail, and then the handle is closed and the caller falls back to a full scan.
func (w *windowsWatcher) Watch(root string) error {
	select {
	case <-w.done:
		return errors.New("windows watcher already closed")
	default:
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("fsmonitor: resolve %s: %w", root, err)
	}
	p, err := syscall.UTF16PtrFromString(abs)
	if err != nil {
		return fmt.Errorf("fsmonitor: encode %s: %w", abs, err)
	}
	// FILE_SHARE_DELETE is the one that matters, and the one syscall.Open gets
	// wrong: it opens without it, and a directory held that way cannot be
	// renamed or removed for as long as the monitor runs. The monitor would
	// then break exactly the operations it exists to observe.
	handle, err := syscall.CreateFile(
		p,
		syscall.FILE_LIST_DIRECTORY,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS, // required to open a directory at all
		0,
	)
	if err != nil {
		return fmt.Errorf("fsmonitor: open %s for watching: %w", abs, err)
	}
	w.root = abs
	w.handle = handle
	if err := w.recordDirs(); err != nil {
		w.dropAll()
		return err
	}
	go w.run()
	return nil
}

// recordDirs takes the initial inventory of directories.
//
// Without it a directory's removal could not be told from a file's, and the
// disappearance of a subtree would be mistaken for the disappearance of one
// path. A backend that learns this from the kernel, as Linux does, does not
// need it; this one has no such source, and a path that is already gone cannot
// be asked after the fact.
func (w *windowsWatcher) recordDirs() error {
	return filepath.Walk(w.root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // vanished mid-walk; nothing to record
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

func (w *windowsWatcher) dropAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.handle != syscall.InvalidHandle {
		syscall.CloseHandle(w.handle)
		w.handle = syscall.InvalidHandle
	}
}

// abs joins a repo-relative slash path back onto the watched root.
func (w *windowsWatcher) abs(rel string) string {
	return filepath.Join(w.root, filepath.FromSlash(rel))
}

// run reads changes until the handle is closed or coverage is lost.
func (w *windowsWatcher) run() {
	defer close(w.events)
	defer w.dropAll()

	// A synchronous call, and it blocks this thread rather than the whole
	// program: the runtime gives the other goroutines a thread of their own.
	// No LockOSThread is wanted here — the point of CancelIoEx below is that it
	// cancels I/O issued on the handle from *any* thread, so the cancellation
	// does not depend on this goroutine staying where it is.
	buf := make([]byte, windowsNotifyBufSize)
	for {
		select {
		case <-w.done:
			return
		default:
		}

		var retlen uint32
		err := syscall.ReadDirectoryChanges(
			w.handle,
			&buf[0],
			uint32(len(buf)),
			true, // the whole subtree, in one handle
			windowsNotifyMask,
			&retlen,
			nil, // nil OVERLAPPED: a synchronous call
			0,
		)
		if err != nil {
			// The handle failed, or the system could not record everything
			// that happened (ERROR_NOTIFY_ENUM_DIR). Neither leaves anything
			// to trust, and this covers the cancellation Close performs too.
			return
		}
		if retlen == 0 {
			// Documented overflow behaviour, and the reason this is not the
			// error path above: the call still *succeeds*, the entire buffer is
			// discarded, and the byte count comes back zero. Treating that as
			// "no changes" would be a silent miss of everything in it.
			return
		}
		events, perr := parseWindowsNotify(buf[:retlen])
		if perr != nil {
			return
		}

		var batch watchBatch
		if w.first {
			// The system allocates the change buffer when the first call is
			// made, not when the handle is opened. Anything that happened
			// between the daemon publishing itself and this goroutine reaching
			// the kernel was therefore never recorded — and it is a real
			// interval, not a narrow race: this goroutine is newly spawned,
			// while the daemon has a file to write in the meantime. So the
			// first batch does not rely on the buffer at all: it enumerates the
			// tree, which reports current state rather than recorded deltas and
			// is therefore right about that interval however late it runs. It
			// costs one walk per epoch, at the first change after a start.
			w.first = false
			if err := w.enumerate("", &batch); err != nil {
				return
			}
		}
		for _, e := range events {
			switch w.classify(e) {
			case dirActionEndEpoch:
				return
			case dirActionEnumerate:
				if err := w.enumerate(e.Path, &batch); err != nil {
					return
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

// classify works out what an event means for the directory bookkeeping,
// supplying the two facts the records do not carry.
func (w *windowsWatcher) classify(e watchEvent) dirAction {
	wasDir := w.dirs[e.Path]
	removed := e.Kind == watchRemoved

	var isDir bool
	if !removed && !wasDir {
		// The records say nothing about whether a path is a directory, so a
		// path that is new to the bookkeeping has to be asked about — and only
		// such a path, so an ordinary edit to a known file costs no extra stat.
		fi, err := os.Lstat(w.abs(e.Path))
		if err != nil {
			// It appeared and is gone again inside one read interval, or it
			// cannot be read. Either way it cannot be classified, and the two
			// readings of it are not equivalent: for a file this is an ordinary
			// temp file and nothing is lost, but for a directory it means names
			// beneath it were never reported and cannot now be recovered — this
			// platform's records never say a path was a directory, and the path
			// can no longer be asked. The Linux backend tells the two apart from
			// the event mask; here there is nothing to tell them apart with, so
			// the ambiguity is resolved the only safe way. It costs a full scan
			// for a coincidence as narrow as this one, which is the same bargain
			// the rest of the design makes everywhere else.
			//
			// It is deliberately the conservative reading: a narrower rule could
			// spare the temp-file case, but only by an argument about what the
			// index can hold, and that argument would have to be right on a
			// platform where these tests have not been run. If this turns out to
			// cost full scans on a real workload, that is the place to loosen it
			// — with the measurement in hand.
			return dirActionEndEpoch
		}
		isDir = fi.IsDir()
	}
	switch {
	case removed:
		delete(w.dirs, e.Path)
	case isDir:
		w.dirs[e.Path] = true
	}
	return classifyDirEvent(dirEvent{Removed: removed, WasDir: wasDir, IsDir: isDir})
}

// enumerate reports every path under a directory.
//
// A directory that appears — created and populated, or moved in from outside —
// brings files that no event ever named, and they must be reported or the
// consumer would treat them as unchanged forever. Unlike the Linux backend
// nothing needs watching here: the handle already covers the subtree, so this
// is purely about naming what arrived.
//
// A failure that is not "it was already gone" ends the epoch. A subtree this
// walk could not read is a subtree whose contents were never named.
func (w *windowsWatcher) enumerate(rel string, b *watchBatch) error {
	return filepath.Walk(w.abs(rel), func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // removed again before the walk reached it
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

func (w *windowsWatcher) Close() error {
	w.stop.Do(func() {
		close(w.done)
		// A read blocked in the kernel is not woken by the stop flag, so it is
		// cancelled. CancelIoEx rather than CancelIo: the latter only cancels
		// I/O issued by the calling thread, which is not the thread that issued
		// this. Closing the handle underneath the blocked read instead would
		// free the number for reuse while the read was still outstanding.
		w.mu.Lock()
		if w.handle != syscall.InvalidHandle {
			syscall.CancelIoEx(w.handle, nil)
		}
		w.mu.Unlock()
	})
	return nil
}
