package repo

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

// The fsmonitor lets status skip the per-file lstat for paths a change
// monitor proves unchanged. The monitor is built into qin — there is no
// external service to install.
//
// The monitor reports *deltas* ("these paths changed since cursor C"), but
// status must report *state* ("this file is still modified"). A deviation
// from the index is a persistent condition, not a one-shot event: a file that
// was modified stays modified across runs even though the monitor only
// mentions it once, at the moment of the change. The state file therefore
// carries a `dirty` set — paths known not to match the index — which is
// re-checked on every run until it matches again (normally after `add`).
//
// The monitor only ever removes work, never conclusions: absent a monitor, or
// on any error, it reports "unavailable" and status falls back to its full
// scan. Every path it claims is clean would have been found clean by the
// lstat path too; skipping it just avoids the syscall.
const (
	// fsmonitorDirName holds the monitor's on-disk state.
	fsmonitorDirName = "fsmonitor"

	// fsmonitorStateName is the client's cursor sidecar, inside that
	// directory.
	fsmonitorStateName = "state.json"

	// fsmonitorVersion is the state format version. Version 1 described a
	// watchman watch root and clock, neither of which exists any more; a
	// sidecar left over from that era must never be mistaken for a cursor.
	fsmonitorVersion = 2

	// FsmonitorOff and FsmonitorOn are the accepted values of the
	// core.fsmonitor config key. Empty is treated as off.
	FsmonitorOff = "false"
	FsmonitorOn  = "true"

	// fsmonitorMaxDirty bounds the persisted dirty set. Beyond this the
	// re-check dominates the run anyway, so the monitor is dropped rather
	// than letting the sidecar grow without limit.
	fsmonitorMaxDirty = 10000
)

// fsmonitorState is the on-disk cursor (.qin/fsmonitor/state.json). It is
// written atomically, mirroring the untracked cache, and is disposable: a
// missing or corrupt file merely costs a full scan.
//
// Gen and Offset together are a position in the change journal: Gen names the
// coverage epoch and Offset is a byte position within that epoch's journal
// file. A cursor is usable only while both still describe the live journal.
// Generation numbers are never reused, so a cursor left over from a previous
// daemon can never be mistaken for a current one — that is what makes a
// daemon restart safe rather than merely likely-correct.
type fsmonitorState struct {
	Version int             `json:"version"`
	Backend string          `json:"backend,omitempty"`
	Watch   string          `json:"watch,omitempty"`
	Gen     uint64          `json:"gen,omitempty"`
	Offset  int64           `json:"offset,omitempty"`
	Dirty   map[string]bool `json:"dirty,omitempty"`
}

// fsmonitorChanges is the result of one monitor query.
type fsmonitorChanges struct {
	// MustCheck is the set of repo-relative paths that still need a stat:
	// the persisted dirty set unioned with the paths the monitor reported as
	// changed since the stored cursor. Every other path is proven unchanged
	// and is skipped outright.
	//
	// A nil MustCheck on a non-nil fsmonitorChanges means "no usable cursor,
	// check every path" — an ordinary full scan — while still letting save()
	// advance the cursor so the next run can be incremental.
	MustCheck map[string]bool

	state *fsmonitorState
}

// fsmonitorDir is the monitor's state directory.
func (r *Repository) fsmonitorDir() string {
	return filepath.Join(r.LoDir(), fsmonitorDirName)
}

// fsmonitorPath is the cursor sidecar.
func (r *Repository) fsmonitorPath() string {
	return filepath.Join(r.fsmonitorDir(), fsmonitorStateName)
}

// FsmonitorEnabled reports whether the core.fsmonitor config key asks for a
// monitor. An empty value means "off", so an unread config never enables it.
func (r *Repository) FsmonitorEnabled() bool {
	if r == nil || r.Config == nil {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(r.Config.Core.Fsmonitor))
	return v == FsmonitorOn
}

// ValidFsmonitorValue reports whether v is an accepted core.fsmonitor value.
func ValidFsmonitorValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", FsmonitorOff, FsmonitorOn:
		return true
	}
	return false
}

// loadFsmonitorState reads the sidecar, returning nil when it is missing or
// corrupt — both simply mean "no usable cursor".
func (r *Repository) loadFsmonitorState() *fsmonitorState {
	data, err := ioutil.ReadFile(r.fsmonitorPath())
	if err != nil {
		return nil
	}
	var st fsmonitorState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil
	}
	if st.Version != fsmonitorVersion {
		return nil
	}
	return &st
}

// saveFsmonitorState writes the sidecar atomically (temp + rename).
func (r *Repository) saveFsmonitorState(st *fsmonitorState) error {
	st.Version = fsmonitorVersion
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal fsmonitor state: %w", err)
	}
	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		return fmt.Errorf("create fsmonitor state dir: %w", err)
	}
	tmp := r.fsmonitorPath() + ".tmp"
	if err := ioutil.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write fsmonitor state: %w", err)
	}
	if err := os.Rename(tmp, r.fsmonitorPath()); err != nil {
		return fmt.Errorf("replace fsmonitor state: %w", err)
	}
	return nil
}

// removeFsmonitorState drops the cursor sidecar.
func (r *Repository) removeFsmonitorState() error {
	if err := os.Remove(r.fsmonitorPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// FsmonitorCursor returns the persisted journal position and the backend that
// recorded it. ok is false when nothing usable is stored.
func (r *Repository) FsmonitorCursor() (gen uint64, offset int64, backend string, ok bool) {
	st := r.loadFsmonitorState()
	if st == nil {
		return 0, 0, "", false
	}
	return st.Gen, st.Offset, st.Backend, true
}

// fsmonitorBackend is the monitor seam. status calls it; tests replace it to
// inject a change set. A nil result means no usable monitor — status must
// then check every path, exactly as it did before the feature existed.
//
// It answers from a running daemon's journal and nothing else: it never starts
// one. Starting a daemon is a side effect on the machine, and the place for it
// is the command the user actually ran — see EnsureFsmonitorDaemon, which the
// CLI calls before asking. Leaving it here would also mean every test that
// happens to call WorkTreeStatus with the monitor switched on forks a process.
var fsmonitorBackend = func(r *Repository) (*fsmonitorChanges, error) {
	if !r.FsmonitorEnabled() {
		return nil, nil
	}
	return r.fsmonitorQuery(), nil
}

// fsmonitorQuery turns a running daemon's journal into a change set.
//
// Every exit is a full scan that persists nothing, except the one where the
// daemon proved — in this run, with this handshake — that its journal is
// complete up to a position. That position is the only thing worth storing,
// and it is what makes the next run able to skip work; everything else about
// this run is the same as it was before the feature existed.
func (r *Repository) fsmonitorQuery() *fsmonitorChanges {
	info := r.liveDaemon()
	if info == nil {
		// No daemon, or one that has stopped proving it is alive. Either way
		// there is nothing to ask, and nothing to hand the position to.
		return nil
	}
	sync := r.syncWithDaemon(info)
	if sync == nil {
		return nil
	}

	st := r.loadFsmonitorState()
	decision, gen := decideCursor(st, sync.Gen, sync.ReadyEnd, sync.SyncEnd)
	if decision == cursorUntrusted {
		return nil
	}

	// The position stored is always the one this run's handshake named. Both
	// outcomes below reach their conclusions as of a moment at or after it, so
	// a later run reading from here can only be told about changes already
	// accounted for — the over-approximation the whole scheme rests on.
	//
	// The dirty set is the state half of the contract, and it is independent
	// of the journal: a path that differs from the index still differs after a
	// daemon restart, and must keep being re-checked until `add` reconciles it.
	next := &fsmonitorState{Backend: sync.Backend, Watch: r.Path, Gen: gen, Offset: sync.SyncEnd}
	if st != nil {
		next.Dirty = st.Dirty
	}

	if decision != cursorIncremental {
		// Nothing here can be skipped — there was no usable cursor — but the
		// run does establish one, so the position is recorded and the next run
		// is the first that could be incremental.
		return &fsmonitorChanges{state: next}
	}

	paths, err := r.journalChanges(gen, st.Offset, sync.SyncEnd)
	if err != nil {
		// A window that cannot be read may be hiding a completed change, so
		// nothing may be carried past it — not even the position, which is why
		// this stores nothing rather than the over-approximation the case
		// above gets away with.
		return nil
	}
	must := make(map[string]bool, len(next.Dirty)+len(paths))
	for p := range next.Dirty {
		must[p] = true
	}
	for _, p := range paths {
		must[p] = true
	}
	return &fsmonitorChanges{MustCheck: must, state: next}
}

// save advances the stored cursor and replaces the dirty set with the paths
// this run found to still deviate from the index. It is called once per run
// with the final Deleted and Modified lists, so a deviation stays visible on
// every subsequent status until `add` makes the index match again.
func (c *fsmonitorChanges) save(r *Repository, deleted, modified []string) {
	if c == nil || c.state == nil {
		return
	}
	total := len(deleted) + len(modified)
	if total > fsmonitorMaxDirty {
		// Past this point the re-check dominates the run, so the monitor
		// would not pay for itself anyway. Drop the dirty set to keep the
		// sidecar bounded; the cursor stays, so recovery is automatic.
		c.state.Dirty = nil
	} else {
		dirty := make(map[string]bool, total)
		for _, p := range deleted {
			dirty[p] = true
		}
		for _, p := range modified {
			dirty[p] = true
		}
		c.state.Dirty = dirty
	}
	r.saveFsmonitorState(c.state)
}

// The daemon's lifecycle — spawning it, watching it, stopping it — lives in
// fsmonitor_daemon.go.
