package repo

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The journal is one-way: the daemon writes, the client reads. That is enough
// to report changes, and not enough to report them *safely*, because a
// one-way channel has no notion of "now".
//
// The gap it leaves: the client reads the journal to its end and then walks the
// tree. Anything the daemon has already been told about but has not yet written
// — it batches, and flushes on a ticker — is missing from what the client read,
// while the walk that follows will not look at those paths, because the client
// believes the monitor covered them. A change made seconds earlier is reported
// as no change at all. That is not a narrow race; it is the ordinary case.
//
// The fix is a request and a reply on the same file channel: the client leaves
// a nonce, the daemon writes everything it has pending and then echoes the
// nonce, and the client reads up to that echo. It is the synchronous query of a
// watcher-style protocol, expressed in a file. The remaining window — changes
// between the echo and the end of the client's walk — is the one every change
// monitor has, and it is closed by the cursor rather than by this handshake:
// those records land after the position the client stores, so the next run
// reports them.
const (
	// syncTimeout bounds the wait for the daemon's echo.
	//
	// The reply path is microseconds — an inotify wakeup, a directory read, a
	// write — so this is not a budget the daemon normally spends. It matters
	// only when the daemon is wedged or dead, and there the price of waiting
	// is one slow status against the price of not waiting, which is skipping
	// the handshake and trusting a journal that will never be complete.
	syncTimeout = 250 * time.Millisecond

	// syncPollInterval is how often the client looks for the echo. The wait is
	// on a file changing, and polling is the only way to wait for that: the
	// alternative is a second watcher in every client process.
	syncPollInterval = 2 * time.Millisecond

	// syncMaxReadBounds one poll's read. The daemon's reply is a few bytes
	// past the position the client started at, so this is only ever reached
	// when the tree is being changed faster than the handshake completes.
	syncMaxRead = 64 << 10

	// syncNonceBytes is the size of the request nonce. It only has to be
	// unlikely to collide with a stale echo left in the journal, so that a
	// reply meant for an earlier request cannot be mistaken for this one's.
	syncNonceBytes = 16

	// syncMaxRequest bounds a request file's contents. The daemon echoes what
	// it finds there into its own journal, so the file is treated as
	// untrusted like everything else on disk.
	syncMaxRequest = 128
)

// fsmonitorSyncTimeout is syncTimeout as a variable, so a test can shorten the
// wait instead of spending it: the timeout exists for a daemon that is not
// answering, and a test that has arranged exactly that should not have to sit
// through the real budget to observe it.
var fsmonitorSyncTimeout = syncTimeout

// fsmonitorSync is what a completed handshake established: a position in a
// generation that the daemon has promised is complete.
type fsmonitorSync struct {
	Gen      uint64
	Backend  string
	ReadyEnd int64
	// SyncEnd is the offset just past the daemon's echo. Every change the
	// daemon had seen when it answered is at or before it.
	SyncEnd int64
}

// controlDirName is the directory clients leave requests in.
//
// A subdirectory rather than .qin/fsmonitor itself, which is where the daemon
// writes its journal and heartbeat: watching the parent would mean the daemon
// woke itself on every one of its own writes to look for requests that are
// almost never there. Keeping the requests apart means a wakeup is always
// somebody else's.
const controlDirName = "ctl"

func (r *Repository) controlDir() string {
	return filepath.Join(r.fsmonitorDir(), controlDirName)
}

// syncRequestPath is where a client leaves its nonce. One file per process, so
// two clients — or the same client twice — cannot overwrite each other's
// request; the nonce is what distinguishes them anyway.
func (r *Repository) syncRequestPath() string {
	return filepath.Join(r.controlDir(), fmt.Sprintf("%s%d", fsmonitorSyncPrefix, os.Getpid()))
}

// ---- the client ----

// syncWithDaemon asks a live daemon to prove how far its journal is complete,
// and returns the position it names.
//
// A nil result means "no usable position": the caller must scan everything and
// persist nothing. Every failure — no daemon, no reply, a journal that cannot
// be read, a generation that changed underneath — lands there, because the
// alternative is to trust a window the daemon has not vouched for.
func (r *Repository) syncWithDaemon(info *fsmonitorDaemonInfo) *fsmonitorSync {
	if !r.belongsToThisRepo(info) || info.Gen == 0 {
		return nil
	}
	// A polling backend's view lags by up to one interval, and this handshake
	// would only confirm that it is behind — not how far. So it is never
	// allowed to serve a client that would skip the window it is missing.
	if !info.Events {
		return nil
	}
	gen := info.Gen

	backend, readyEnd, err := r.journalReady(gen)
	if err != nil {
		return nil
	}

	// The size is taken before the request is written, and everything after it
	// is read from there. That ordering is what makes the reply findable
	// cheaply: the daemon answers only after it sees the request, so the echo
	// cannot be at an offset below this one, and a journal that has grown to
	// megabytes never has to be re-read to find a record at its end.
	from := readyEnd
	if fi, serr := os.Stat(r.journalPath(gen)); serr == nil && fi.Size() > from {
		from = fi.Size()
	}

	nonce, err := syncNonce()
	if err != nil {
		return nil
	}
	if err := os.MkdirAll(r.controlDir(), 0755); err != nil {
		return nil
	}
	req := r.syncRequestPath()
	if err := ioutil.WriteFile(req, nonce, 0644); err != nil {
		return nil
	}
	defer os.Remove(req)

	f, err := os.Open(r.journalPath(gen))
	if err != nil {
		return nil
	}
	defer f.Close()

	deadline := time.Now().Add(fsmonitorSyncTimeout)
	buf := make([]byte, syncMaxRead)
	for {
		// The record is re-read every round: a rotation or a takeover changes
		// the generation, and a cursor for a generation that is no longer
		// being written is the one thing that must not be produced here.
		if cur := r.loadDaemonInfo(); cur == nil || cur.Gen != gen {
			return nil
		}
		n, rerr := f.ReadAt(buf, from)
		if n > 0 {
			consumed, syncEnd, serr := scanForSync(buf[:n], from, nonce)
			if serr != nil {
				return nil // a complete frame with a bad checksum
			}
			if syncEnd > 0 {
				return &fsmonitorSync{Gen: gen, Backend: backend, ReadyEnd: readyEnd, SyncEnd: syncEnd}
			}
			from += int64(consumed)
		} else if rerr != nil && rerr != io.EOF {
			return nil
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(syncPollInterval)
	}
}

// journalReady reads a journal's header and its READY record.
//
// It stops at READY rather than reading the whole file, because READY is the
// second record a generation ever has — it is written immediately after the
// header — and the rest of the file can be megabytes.
func (r *Repository) journalReady(gen uint64) (backend string, readyEnd int64, err error) {
	b, err := readAtMost(r.journalPath(gen), journalReadyScan)
	if err != nil {
		return "", 0, err
	}
	if _, ok := parseJournalHeader(b); !ok {
		return "", 0, errJournalCorrupt
	}
	off := int64(journalHeaderSize)
	for {
		if int64(len(b))-off < 8 {
			return "", 0, errJournalNoReady
		}
		n := int64(binary.LittleEndian.Uint32(b[off : off+4]))
		want := binary.LittleEndian.Uint32(b[off+4 : off+8])
		if n < 1 || n > journalMaxPayload || int64(len(b))-off-8 < n {
			return "", 0, errJournalNoReady
		}
		payload := b[off+8 : off+8+n]
		if crc32.ChecksumIEEE(payload) != want {
			return "", 0, errJournalCorrupt
		}
		if payload[0] == recReady {
			return string(payload[1:]), off + 8 + n, nil
		}
		off += 8 + n
	}
}

// journalReadyScan is how much of a journal is read looking for READY. The
// record follows the header immediately, so anything larger means this is not
// a journal this code wrote.
const journalReadyScan = 4096

func readAtMost(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	got, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:got], nil
}

// scanForSync walks the complete records in b looking for a SYNC that echoes
// nonce.
//
// It reports how many bytes it consumed — all of them when every frame was
// whole — and the offset just past the matching record. A frame that runs past
// the end of b is left unconsumed rather than treated as damage: the daemon
// appends as it goes, so the tail of what a reader just read is routinely the
// front half of a record that is still being written, and waiting for the rest
// is the correct response to it.
//
// A frame that is entirely present but fails its checksum is different, and is
// reported as corruption. Nothing about an append explains that, and a reader
// that stepped over it would be trusting every offset after it.
func scanForSync(b []byte, base int64, nonce []byte) (consumed int, syncEnd int64, err error) {
	off := 0
	for {
		if len(b)-off < 8 {
			return off, 0, nil
		}
		n := int(binary.LittleEndian.Uint32(b[off : off+4]))
		want := binary.LittleEndian.Uint32(b[off+4 : off+8])
		if n < 1 || n > journalMaxPayload {
			// An out-of-range length is also a half-written prefix: the length
			// field itself can be torn. Waiting is safe, and the wait is
			// bounded by the handshake's timeout.
			return off, 0, nil
		}
		if len(b)-off-8 < n {
			return off, 0, nil
		}
		payload := b[off+8 : off+8+n]
		if crc32.ChecksumIEEE(payload) != want {
			return 0, 0, errJournalCorrupt
		}
		if payload[0] == recSync && bytes.Equal(payload[1:], nonce) {
			end := off + 8 + n
			return end, base + int64(end), nil
		}
		off += 8 + n
	}
}

// syncNonce returns the value that identifies one request.
//
// It is random rather than a counter or a timestamp because the journal is
// append-only and outlives the request: an echo from an earlier request by the
// same process, or by a process that once held the same pid, is sitting in the
// file, and the client has to be able to tell that reply from this one's.
func syncNonce() ([]byte, error) {
	b := make([]byte, syncNonceBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return []byte(hex.EncodeToString(b)), nil
}

// ---- the daemon ----

// serviceSyncRequests answers every request waiting in the control directory.
//
// Two writes, in this order and for this reason: everything the daemon has
// pending goes into the journal first, and only then the echo. That ordering is
// the entire value of the handshake — the echo's offset is a position the
// client can store as its cursor precisely because every change the daemon had
// already seen is below it.
//
// The request file is removed once it has been answered, so a daemon that
// restarts into a directory full of old requests does not replay them. A
// request whose file has already gone was one whose client gave up, and
// answering it would add a record nobody reads.
func (d *daemon) serviceSyncRequests(jw *journalWriter, pending *watchBatch) error {
	entries, err := ioutil.ReadDir(d.r.controlDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("fsmonitor: read control directory: %w", err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), fsmonitorSyncPrefix) || e.IsDir() {
			continue
		}
		p := filepath.Join(d.r.controlDir(), e.Name())
		nonce, err := ioutil.ReadFile(p)
		if err != nil {
			continue // removed between the readdir and here: nothing to answer
		}
		if len(nonce) == 0 || len(nonce) > syncMaxRequest {
			d.logf("fsmonitor: ignoring malformed sync request %s", e.Name())
			os.Remove(p)
			continue
		}
		if err := jw.writeChanges(pending.take()); err != nil {
			return err
		}
		if err := jw.writeRecord(recSync, nonce); err != nil {
			return err
		}
		os.Remove(p)
	}
	return nil
}
