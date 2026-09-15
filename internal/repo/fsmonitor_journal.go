package repo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strings"
)

// The change journal is how the daemon tells the client what changed. It is a
// plain append-only file, one per generation, read with ordinary file I/O —
// there is no socket and no port.
//
// Everything in this file is pure byte manipulation with no concurrency and no
// platform API, so the riskiest part of the feature — the encoding, and the
// rules that decide whether a stored cursor may be trusted — is also the part
// that is fully testable on any machine.
//
// Two files matter at rest:
//
//	journal.<gen>  written by the daemon, one per coverage epoch
//	state.json     written by the client, holding (gen, offset, dirty)
//
// The generation is what makes a cursor safe across daemon restarts. A daemon
// that starts up mints a generation strictly greater than any it has seen, and
// a cursor is trusted only while its generation still describes the live
// journal. So a cursor left over from a daemon that died can never be mistaken
// for a current one — it simply fails to match, and the run falls back to a
// full scan. That is why generations must never be reused: reuse would turn a
// stale cursor back into a plausible one.
const (
	// journalMagic identifies the file and its byte order in one go.
	journalMagic = "QJRNL1\x00\x00"

	// journalFmtVer is checked exactly. A journal written by a different
	// build is refused outright rather than parsed on a hope — that is the
	// valve against a stale daemon from before an upgrade feeding a new
	// client, or the reverse.
	journalFmtVer = 1

	// journalHeaderSize is the fixed size of the file header.
	journalHeaderSize = 32

	// journalMaxPayload bounds a single record's payload. A frame claiming
	// more than this is corrupt, not merely large.
	journalMaxPayload = 65535

	// Record kinds, stored as payload byte 0.
	recChange = 1 // a repo-relative path that changed
	recReady  = 2 // written once the watches cover the whole tree
	recSync   = 3 // echoes a client nonce, proving the daemon flushed
)

// Errors distinguishing why a journal could not be used. The caller's
// response differs: corruption may have swallowed a completed change and so
// must not advance the cursor, whereas "not ready yet" is a normal state of a
// daemon that is still starting up.
var (
	errJournalCorrupt = errors.New("fsmonitor journal is corrupt")
	errJournalNoReady = errors.New("fsmonitor journal has no READY record")
)

// ---- generation ----

// nextGeneration mints a generation number strictly greater than prev.
//
// Monotonicity is the load-bearing property, not any particular value, so this
// asserts it rather than assuming the clock cooperates — a backwards clock
// step would otherwise reissue a generation that a stale cursor still names.
// Generation 0 is never issued: the client reads it as "no cursor".
func nextGeneration(prev uint64, nowNano int64) uint64 {
	if prev == ^uint64(0) {
		return prev // exhausted; nothing greater exists
	}
	g := uint64(nowNano)
	if g <= prev {
		g = prev + 1
	}
	if g == 0 {
		g = 1
	}
	return g
}

// journalGenName renders a generation as the journal's filename component: 16
// lowercase hex digits, so the names sort in generation order too.
func journalGenName(gen uint64) string {
	return fmt.Sprintf("%016x", gen)
}

// ---- header ----

// encodeJournalHeader builds the 32-byte header for one generation's journal.
//
// The header is written by itself when the journal is created, before any
// record — a reader that finds only a header knows the daemon has not yet
// established coverage.
func encodeJournalHeader(gen uint64) []byte {
	b := make([]byte, journalHeaderSize)
	copy(b[0:8], journalMagic)
	binary.LittleEndian.PutUint16(b[8:10], journalFmtVer)
	binary.LittleEndian.PutUint16(b[10:12], 0) // flags, reserved
	binary.LittleEndian.PutUint64(b[12:20], gen)
	binary.LittleEndian.PutUint32(b[20:24], crc32.ChecksumIEEE(b[0:20]))
	// 24..31 reserved, left zero.
	return b
}

// parseJournalHeader validates b as a journal header and returns its
// generation. Any mismatch is reported as a plain false: the caller's response
// to every failure is the same full scan.
func parseJournalHeader(b []byte) (gen uint64, ok bool) {
	if len(b) < journalHeaderSize {
		return 0, false
	}
	if string(b[0:8]) != journalMagic {
		return 0, false
	}
	if binary.LittleEndian.Uint16(b[8:10]) != journalFmtVer {
		return 0, false
	}
	if binary.LittleEndian.Uint16(b[10:12]) != 0 {
		return 0, false
	}
	gen = binary.LittleEndian.Uint64(b[12:20])
	if gen == 0 {
		return 0, false
	}
	if binary.LittleEndian.Uint32(b[20:24]) != crc32.ChecksumIEEE(b[0:20]) {
		return 0, false
	}
	return gen, true
}

// ---- records ----

// journalRecord is one decoded record, with its position in the file so a
// reader can name the offset it stopped at.
type journalRecord struct {
	Kind   byte
	Body   []byte // payload after the kind byte
	Offset int64  // start of the record's frame
	End    int64  // just past the record
}

// appendJournalRecord frames a record and appends it to dst.
//
// Records are length-prefixed and CRC-checked because a reader takes its
// position from the length: a half-written record at the tail must be
// detected, never walked past, or the offsets after it would all be wrong.
func appendJournalRecord(dst []byte, kind byte, body []byte) ([]byte, error) {
	n := 1 + len(body)
	if n < 1 || n > journalMaxPayload {
		return nil, fmt.Errorf("fsmonitor journal: payload of %d bytes out of range", n)
	}
	payload := make([]byte, n)
	payload[0] = kind
	copy(payload[1:], body)

	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(n))
	binary.LittleEndian.PutUint32(hdr[4:8], crc32.ChecksumIEEE(payload))
	dst = append(dst, hdr[:]...)
	dst = append(dst, payload...)
	return dst, nil
}

// parseJournalRecords decodes the records following the header.
//
// A truncated or CRC-failing frame ends the walk with errJournalCorrupt rather
// than being skipped: the tail of an append-only file may be a record that was
// only partly written, and a reader that ignored it would step over a change
// and then trust every offset after it.
func parseJournalRecords(b []byte) ([]journalRecord, error) {
	if len(b) < journalHeaderSize {
		return nil, errJournalCorrupt
	}
	return parseJournalFrames(b[journalHeaderSize:], journalHeaderSize)
}

// parseJournalFrames decodes every frame in b, which begins at file offset
// base.
//
// Every byte of b has to be whole frames. Callers supply an extent whose shape
// they know — a file read to its end, or the window between two positions the
// daemon itself named — so a frame that does not fit is damage rather than a
// record still being written, and the walk stops there rather than guessing.
func parseJournalFrames(b []byte, base int64) ([]journalRecord, error) {
	var recs []journalRecord
	off := int64(0)
	for off < int64(len(b)) {
		if int64(len(b))-off < 8 {
			return nil, errJournalCorrupt
		}
		n := int64(binary.LittleEndian.Uint32(b[off : off+4]))
		want := binary.LittleEndian.Uint32(b[off+4 : off+8])
		if n < 1 || n > journalMaxPayload {
			return nil, errJournalCorrupt
		}
		if int64(len(b))-off-8 < n {
			return nil, errJournalCorrupt
		}
		payload := b[off+8 : off+8+n]
		if crc32.ChecksumIEEE(payload) != want {
			return nil, errJournalCorrupt
		}
		recs = append(recs, journalRecord{
			Kind:   payload[0],
			Body:   payload[1:],
			Offset: base + off,
			End:    base + off + 8 + n,
		})
		off += 8 + n
	}
	return recs, nil
}

// ---- change paths ----

// validChangePath reports whether a path from a CHANGE record may be used as a
// key into the index.
//
// The journal is a file on disk, so its contents are treated as untrusted: a
// path that escaped the repository, or that named the object store, would turn
// a "skip the stat" hint into something else entirely. safeRepoPath already
// rejects the escapes, in a form that means the same thing on every platform;
// this adds the rules that are specific to a path used as an index key.
func validChangePath(p string) bool {
	if !safeRepoPath(p) {
		return false
	}
	// A path used as an index key is never the monitor's own state, and never
	// carries a NUL — index keys are "path\0<oss_mask>", so an embedded NUL
	// would let one record name a different entry than it appears to.
	if strings.ContainsRune(p, 0) {
		return false
	}
	if p == LoDir || strings.HasPrefix(p, LoDir+"/") {
		return false
	}
	return true
}

// ---- the client's view ----

// journalView is a decoded journal, positioned for the client's decision.
type journalView struct {
	Gen     uint64
	Backend string // from the READY record
	// ReadyEnd is the offset just past the READY record. A stored cursor
	// below it describes a window the daemon was not yet watching, so it can
	// never be used — this is the rule that makes a partly-started daemon
	// harmless rather than subtly wrong.
	ReadyEnd int64
	// Size is the journal's length. A cursor past it is corruption rather than
	// a longer journal: a generation's file only ever grows.
	Size    int64
	records []journalRecord
}

// openJournal parses a journal file's bytes and finds its READY record.
//
// READY is written only after every watch is registered and the tree has been
// enumerated, so its presence is the statement "coverage is complete from here
// on". A journal without one is a daemon still starting up: usable for
// nothing, but not evidence of damage.
func openJournal(b []byte) (*journalView, error) {
	gen, ok := parseJournalHeader(b)
	if !ok {
		return nil, errJournalCorrupt
	}
	recs, err := parseJournalRecords(b)
	if err != nil {
		return nil, err
	}
	v := &journalView{Gen: gen, Size: int64(len(b)), records: recs}
	for _, rec := range recs {
		if rec.Kind == recReady {
			v.Backend = string(rec.Body)
			v.ReadyEnd = rec.End
			break
		}
	}
	if v.ReadyEnd == 0 {
		return nil, errJournalNoReady
	}
	return v, nil
}

// changesSince collects the paths reported changed in (from, to].
//
// A from offset below ReadyEnd is refused rather than clamped: it names a
// window during which the daemon was not watching, so the paths missing from
// it are exactly the ones the caller would wrongly skip.
func (v *journalView) changesSince(from, to int64) ([]string, error) {
	if from < v.ReadyEnd {
		return nil, fmt.Errorf("fsmonitor cursor %d predates coverage at %d", from, v.ReadyEnd)
	}
	if to < from || to > v.Size {
		return nil, errJournalCorrupt
	}
	var paths []string
	for _, rec := range v.records {
		if rec.Offset < from {
			continue
		}
		if rec.End > to {
			break
		}
		if rec.Kind != recChange {
			continue
		}
		p := string(rec.Body)
		if !validChangePath(p) {
			return nil, errJournalCorrupt
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// journalChanges reads the paths a generation recorded in (from, to].
//
// Only that window is read, not the whole file. A journal lives until it
// reaches eight megabytes, and the client's question is always about what
// happened since it last looked — which is normally the last few bytes of it.
// Reading the whole thing would make every status pay for everything written
// during the epoch in order to learn about the last minute.
//
// Both ends come from a handshake, so both are positions the daemon named:
// `from` is one it answered with, `to` is one it had just written. A record
// straddling either is therefore not a case that arises, and is damage if it
// does.
func (r *Repository) journalChanges(gen uint64, from, to int64) ([]string, error) {
	if from < journalHeaderSize || to < from {
		return nil, errJournalCorrupt
	}
	f, err := os.Open(r.journalPath(gen))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	hdr := make([]byte, journalHeaderSize)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return nil, errJournalCorrupt
	}
	// The generation is checked again here rather than trusted from the name:
	// a file that is not this generation's journal would otherwise be read as
	// one, at offsets that mean nothing in it.
	if g, ok := parseJournalHeader(hdr); !ok || g != gen {
		return nil, errJournalCorrupt
	}

	// The window is read by absolute offset, not by reading forward from
	// wherever the header check left the file: reading `to-from` bytes from
	// the header's end yields a window that is real journal data at the wrong
	// offsets, and every frame in it then fails to parse. It presented as
	// permanent corruption, and as a client that silently scanned everything
	// forever while looking like it was using the monitor.
	buf := make([]byte, to-from)
	if _, err := f.ReadAt(buf, from); err != nil {
		return nil, errJournalCorrupt
	}
	recs, err := parseJournalFrames(buf, from)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, rec := range recs {
		if rec.Kind != recChange {
			continue
		}
		p := string(rec.Body)
		if !validChangePath(p) {
			return nil, errJournalCorrupt
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// ---- cursor rules ----

// cursorDecision is the verdict on a stored cursor, i.e. the "what to do" and
// "may this be persisted" columns of the validation table.
type cursorDecision int

const (
	// cursorUntrusted: scan everything and persist nothing. Either there is
	// nothing to trust yet, or the journal's state is ambiguous — an ambiguous
	// window may hide a completed change, so advancing past it would lose it.
	cursorUntrusted cursorDecision = iota
	// cursorCovered: scan everything, but a usable (gen, offset) was
	// established, so record it. The scan itself is ground truth as of a
	// moment at or after the cursor, so the cursor can only ever cause the
	// next run to read records it has already accounted for — over-approximate,
	// which is the safe direction.
	cursorCovered
	// cursorIncremental: only the reported paths need a stat.
	cursorIncremental
)

// decideCursor applies the cursor rules for a stored state against the values
// a synchronization round established.
//
// gen and syncEnd come from the handshake and are only ever set when the
// daemon proved it was alive and had flushed everything it had written;
// readyEnd is the journal's coverage start.
//
// The position to store is not returned, because there is only one it can be:
// syncEnd, the position the daemon named in this run. Returning it here as
// well would make it look like the answer depends on the case — and the
// incremental case is exactly the one where reaching for the *stored* offset
// instead is easy to do, leaves the cursor standing still, and shows up only
// as a client that reads the same window on every run forever.
func decideCursor(st *fsmonitorState, gen uint64, readyEnd, syncEnd int64) (cursorDecision, uint64) {
	if gen == 0 || syncEnd < readyEnd {
		return cursorUntrusted, 0
	}
	if st == nil || st.Version != fsmonitorVersion {
		return cursorCovered, gen
	}
	if st.Gen != gen {
		return cursorCovered, gen
	}
	if st.Offset < readyEnd {
		return cursorCovered, gen
	}
	if st.Offset > syncEnd {
		// A journal is append-only within its generation, so a cursor past the
		// end of the file it names is corruption, not a longer journal.
		return cursorCovered, gen
	}
	return cursorIncremental, gen
}
