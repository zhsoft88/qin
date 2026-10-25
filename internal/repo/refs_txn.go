package repo

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

// Ref updates that must land together are committed through a small journal.
//
// Writing refs one by one is not atomic across refs: a multi-branch push that
// dies halfway leaves some branches advanced and others not, and the caller
// cannot tell how far it got. The journal closes that window. The set of
// updates is made durable first, then each ref is written, then the journal is
// removed. A process that finds the journal at startup replays it — the writes
// are plain overwrites of known values, so replaying a partially applied set
// just finishes it.
//
// A corrupt journal, or one naming an object the repository does not have, is
// reported rather than skipped: silently dropping it would discard a push that
// had already been acknowledged to the sender's own object store, and guessing
// would write a ref the caller never asked for. Fail closed; the file is left
// in place for the operator to inspect.

const (
	refsTxnVersion   = 1
	refsLockStale    = time.Minute
	refsLockWait     = 5 * time.Second
	refsLockPollWait = 25 * time.Millisecond
)

// refsTxnProcessMu serializes journal work inside one process. The O_EXCL lock
// file is for other processes; without the mutex, two goroutines serving the
// same Repository (an HTTP server) would see each other's lock as contention.
var refsTxnProcessMu sync.Mutex

// refsTxn is the on-disk journal. Updates maps a full ref name to the hex hash
// it must hold after the transaction completes.
type refsTxn struct {
	Version int               `json:"version"`
	Updates map[string]string `json:"updates"`
}

func (r *Repository) txnDir() string {
	return filepath.Join(r.Path, QinDir, "txn")
}

func (r *Repository) refsTxnPath() string {
	return filepath.Join(r.txnDir(), "refs.json")
}

func (r *Repository) refsLockPath() string {
	return filepath.Join(r.txnDir(), "lock")
}

// safeRefPath reports whether a ref name may be joined onto .qin. It is the
// generic rule, shared by reads and writes: non-empty, relative, no NUL, no
// backslash (a separator on Windows, an ordinary byte here — allowing it would
// make the check platform-dependent in the unsafe direction), and still inside
// the repository after cleaning. Prefix rules (heads/tags only) belong to the
// caller that needs them.
func safeRefPath(ref string) bool {
	if ref == "" || strings.ContainsRune(ref, '\x00') || strings.Contains(ref, "\\") {
		return false
	}
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "/") {
		return false
	}
	clean := path.Clean(ref)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	return true
}

// writeFileAtomic replaces path with data by writing a temporary file in the
// same directory and renaming it. Rename within a directory is atomic, so a
// reader sees either the old file or the complete new one. The temp name is
// random and dot-prefixed: fixed names would collide between concurrent
// writers, and a visible temp name would briefly become a ref or an object to
// any listing that does not filter it.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := ioutil.TempFile(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// writeFileAtomicDurable is writeFileAtomic plus the fsyncs a commit point
// needs: the file's own contents before the rename, and the directory entry
// afterwards. Refs and the journal use it so a crash cannot leave a committed
// name pointing at a zero-length or missing file.
func writeFileAtomicDurable(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := ioutil.TempFile(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

// syncDir flushes a directory's entry list. Windows cannot open a directory
// for this, and its rename semantics already order the change; every other
// platform treats the fsync as part of the commit.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	return nil
}

// applyRefUpdates sets every ref in updates. Either all of them become
// durable, or (on return) none of the calls' own writes is the commit point;
// a crash in the middle leaves the journal, which the next command replays.
func (r *Repository) applyRefUpdates(updates map[string]string) error {
	if len(updates) == 0 {
		return nil
	}
	names := make([]string, 0, len(updates))
	for ref := range updates {
		if !safeRefPath(ref) {
			return fmt.Errorf("invalid ref name: %q", ref)
		}
		names = append(names, ref)
	}
	sort.Strings(names)

	// Validate everything before any durable state exists. An object that is
	// not here yet would make the journal unreplayable, and the whole point of
	// replaying it is that the values were known good when it was written.
	for _, ref := range names {
		h, err := core.HashFromHex(updates[ref])
		if err != nil {
			return fmt.Errorf("ref %s: invalid hash: %w", ref, err)
		}
		if !r.HasObject(h) {
			return fmt.Errorf("ref %s: object %s is not in this repository", ref, h.Short())
		}
	}

	return r.withRefsLock(func() error {
		// A predecessor crashed mid-commit. Finish it before writing a new
		// journal over it, or its updates would be lost.
		if err := r.replayRefsTxnLocked(); err != nil {
			return err
		}

		data, err := json.Marshal(refsTxn{Version: refsTxnVersion, Updates: updates})
		if err != nil {
			return fmt.Errorf("encode refs transaction: %w", err)
		}
		if err := writeFileAtomicDurable(r.refsTxnPath(), data, 0644); err != nil {
			return fmt.Errorf("write refs transaction: %w", err)
		}
		// The journal's directory entry must be durable before a ref points at
		// anything: recovery depends on being able to find it.
		if err := syncDir(r.txnDir()); err != nil {
			return fmt.Errorf("sync refs transaction directory: %w", err)
		}

		for _, ref := range names {
			if err := r.writeRefRaw(ref, updates[ref]); err != nil {
				return fmt.Errorf("write ref %s: %w", ref, err)
			}
		}

		// The refs are on disk; the journal has done its job.
		if err := os.Remove(r.refsTxnPath()); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove refs transaction: %w", err)
		}
		return syncDir(r.txnDir())
	})
}

// recoverRefsTxn finishes a ref transaction interrupted by a crash. It is
// called wherever a repository is opened, so no command can observe the
// half-applied state.
func (r *Repository) recoverRefsTxn() error {
	if _, err := os.Stat(r.refsTxnPath()); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect refs transaction: %w", err)
	}
	return r.withRefsLock(r.replayRefsTxnLocked)
}

// withRefsLock serializes journal work in this process and then against other
// processes through an O_EXCL file. A lock older than refsLockStale is
// assumed to belong to a process that died; breaking it and retrying is safe
// because the journal is replayed idempotently.
func (r *Repository) withRefsLock(fn func() error) error {
	refsTxnProcessMu.Lock()
	defer refsTxnProcessMu.Unlock()

	if err := os.MkdirAll(r.txnDir(), 0755); err != nil {
		return err
	}
	lockPath := r.refsLockPath()
	deadline := time.Now().Add(refsLockWait)
	for {
		err := createExclusive(lockPath)
		if err == nil {
			defer os.Remove(lockPath)
			return fn()
		}
		if !os.IsExist(err) {
			return fmt.Errorf("lock refs transaction: %w", err)
		}
		fi, statErr := os.Stat(lockPath)
		if statErr != nil {
			// It vanished between our create and our stat: another process is
			// breaking the same stale lock. One of us wins the retry.
			continue
		}
		if time.Since(fi.ModTime()) >= refsLockStale {
			os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("refs transaction lock %s is held by another process", lockPath)
		}
		time.Sleep(refsLockPollWait)
	}
}

// replayRefsTxnLocked reads the journal, if any, writes every ref it names and
// removes it. The caller holds the refs lock.
func (r *Repository) replayRefsTxnLocked() error {
	txnPath := r.refsTxnPath()
	data, err := ioutil.ReadFile(txnPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read refs transaction %s: %w", txnPath, err)
	}

	var txn refsTxn
	if err := json.Unmarshal(data, &txn); err != nil {
		return fmt.Errorf("refs transaction %s is corrupt: %w; inspect and remove it to recover", txnPath, err)
	}
	if txn.Version != refsTxnVersion {
		return fmt.Errorf("refs transaction %s has unsupported version %d", txnPath, txn.Version)
	}
	if len(txn.Updates) == 0 {
		return fmt.Errorf("refs transaction %s names no refs", txnPath)
	}

	names := make([]string, 0, len(txn.Updates))
	for ref := range txn.Updates {
		if !safeRefPath(ref) {
			return fmt.Errorf("refs transaction %s names invalid ref %q", txnPath, ref)
		}
		names = append(names, ref)
	}
	sort.Strings(names)
	for _, ref := range names {
		h, err := core.HashFromHex(txn.Updates[ref])
		if err != nil {
			return fmt.Errorf("refs transaction %s: ref %s has invalid hash: %w", txnPath, ref, err)
		}
		if !r.HasObject(h) {
			// A ref must never advance to a commit this repository cannot
			// read. The objects should have been installed before the journal
			// was written, so this is storage damage, not a race.
			return fmt.Errorf("refs transaction %s: ref %s names missing object %s",
				txnPath, ref, h.Short())
		}
	}
	for _, ref := range names {
		if err := r.writeRefRaw(ref, txn.Updates[ref]); err != nil {
			return fmt.Errorf("replay ref %s: %w", ref, err)
		}
	}
	if err := os.Remove(txnPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove refs transaction: %w", err)
	}
	return syncDir(r.txnDir())
}

// writeRefRaw writes one ref value with the durable atomic write the journal
// path uses. It is separate from WriteRef so the journal can write without
// re-entering recovery.
func (r *Repository) writeRefRaw(ref, hash string) error {
	refPath := filepath.Join(r.Path, QinDir, ref)
	return writeFileAtomicDurable(refPath, []byte(hash+"\n"), 0644)
}
