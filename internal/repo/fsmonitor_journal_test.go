package repo

import (
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"io/ioutil"
	"os"
	"strings"
	"testing"
)

// buildJournal assembles a journal the way the daemon will: header, then the
// given records in order. It returns the bytes and the offset just past the
// READY record, which every cursor test needs.
func buildJournal(t *testing.T, gen uint64, recs ...journalRecord) ([]byte, int64) {
	t.Helper()
	b := encodeJournalHeader(gen)
	var readyEnd int64
	for _, rec := range recs {
		var err error
		b, err = appendJournalRecord(b, rec.Kind, rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Kind == recReady && readyEnd == 0 {
			readyEnd = int64(len(b))
		}
	}
	return b, readyEnd
}

// TestJournalHeaderGoldenBytes pins the on-disk encoding. The format is a
// contract between two processes that are not upgraded together — a daemon
// left running from an older build keeps writing, and a client from a newer
// one keeps reading — so a change to this layout must be a deliberate change
// to this test, not an accident of a refactor.
func TestJournalHeaderGoldenBytes(t *testing.T) {
	gen := uint64(0x0123456789abcdef)
	got := encodeJournalHeader(gen)

	if len(got) != 32 {
		t.Fatalf("expected a 32-byte header, got %d", len(got))
	}
	// magic | fmtVer 1 | flags 0 | gen | crc32(0:20) | reserved zeros
	want := "514a524e4c310000" + // "QJRNL1\0\0"
		"0100" + // fmtVer 1, little-endian
		"0000" + // flags
		"efcdab8967452301" + // gen, little-endian
		hex.EncodeToString(u32le(crc32.ChecksumIEEE(got[0:20]))) +
		"0000000000000000" // reserved
	if hex.EncodeToString(got) != want {
		t.Fatalf("header encoding changed:\n got %s\nwant %s", hex.EncodeToString(got), want)
	}

	if g, ok := parseJournalHeader(got); !ok || g != gen {
		t.Fatalf("header did not round trip: gen=%d ok=%v", g, ok)
	}
}

// TestJournalRecordGoldenBytes pins a single record's framing.
func TestJournalRecordGoldenBytes(t *testing.T) {
	b, err := appendJournalRecord(nil, recChange, []byte("a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// payloadLen 6 | crc32 | kind 1 | "a.txt"
	payload := append([]byte{recChange}, []byte("a.txt")...)
	want := hex.EncodeToString(u32le(6)) +
		hex.EncodeToString(u32le(crc32.ChecksumIEEE(payload))) +
		hex.EncodeToString(payload)
	if hex.EncodeToString(b) != want {
		t.Fatalf("record encoding changed:\n got %s\nwant %s", hex.EncodeToString(b), want)
	}
}

func u32le(v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return b[:]
}

func TestJournalRoundTrip(t *testing.T) {
	raw, readyEnd := buildJournal(t, 99,
		journalRecord{Kind: recReady, Body: []byte("inotify")},
		journalRecord{Kind: recChange, Body: []byte("src/main.go")},
		journalRecord{Kind: recChange, Body: []byte("a.txt")},
	)

	v, err := openJournal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.Gen != 99 || v.Backend != "inotify" {
		t.Fatalf("got gen=%d backend=%q, want 99/inotify", v.Gen, v.Backend)
	}
	if v.ReadyEnd != readyEnd {
		t.Fatalf("ReadyEnd = %d, want %d", v.ReadyEnd, readyEnd)
	}
	paths, err := v.changesSince(readyEnd, v.Size)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "src/main.go" || paths[1] != "a.txt" {
		t.Fatalf("got %v, want [src/main.go a.txt]", paths)
	}

	// Records at or before the cursor are already accounted for.
	paths, err = v.changesSince(v.Size, v.Size)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("expected no paths at the end offset, got %v", paths)
	}
}

// ---- header rejection ----

func TestJournalHeaderRejected(t *testing.T) {
	base := encodeJournalHeader(42)

	corrupt := func(mutate func([]byte) []byte) []byte {
		cp := make([]byte, len(base))
		copy(cp, base)
		return mutate(cp)
	}

	cases := []struct {
		name string
		in   []byte
	}{
		{"truncated", base[:20]},
		{"empty", nil},
		{"bad magic", corrupt(func(b []byte) []byte { b[0] = 'X'; return b })},
		{"bad format version", corrupt(func(b []byte) []byte {
			binary.LittleEndian.PutUint16(b[8:10], journalFmtVer+1)
			return b
		})},
		{"nonzero flags", corrupt(func(b []byte) []byte { b[10] = 1; return b })},
		{"zero generation", corrupt(func(b []byte) []byte {
			binary.LittleEndian.PutUint64(b[12:20], 0)
			return b
		})},
		{"flipped generation with stale crc", corrupt(func(b []byte) []byte {
			b[12] ^= 0xff
			return b
		})},
		{"bad crc", corrupt(func(b []byte) []byte { b[20] ^= 0xff; return b })},
	}
	for _, tc := range cases {
		if _, ok := parseJournalHeader(tc.in); ok {
			t.Errorf("%s: expected the header to be refused", tc.name)
		}
	}
}

// ---- record framing ----

func TestJournalRecordFramingErrors(t *testing.T) {
	raw, _ := buildJournal(t, 7,
		journalRecord{Kind: recReady, Body: []byte("polling")},
		journalRecord{Kind: recChange, Body: []byte("a.txt")},
	)

	t.Run("truncated tail", func(t *testing.T) {
		// A partly-written record at the end must be an error, never skipped:
		// the reader takes its next offset from the length it just read.
		if _, err := parseJournalRecords(raw[:len(raw)-3]); err != errJournalCorrupt {
			t.Fatalf("expected errJournalCorrupt, got %v", err)
		}
	})
	t.Run("no room for a frame header", func(t *testing.T) {
		if _, err := parseJournalRecords(raw[:journalHeaderSize+4]); err != errJournalCorrupt {
			t.Fatalf("expected errJournalCorrupt, got %v", err)
		}
	})
	t.Run("oversized length", func(t *testing.T) {
		bad := append([]byte{}, raw...)
		binary.LittleEndian.PutUint32(bad[journalHeaderSize:journalHeaderSize+4], 0xffffffff)
		if _, err := parseJournalRecords(bad); err != errJournalCorrupt {
			t.Fatalf("expected errJournalCorrupt, got %v", err)
		}
	})
	t.Run("zero length", func(t *testing.T) {
		bad := append([]byte{}, raw...)
		binary.LittleEndian.PutUint32(bad[journalHeaderSize:journalHeaderSize+4], 0)
		if _, err := parseJournalRecords(bad); err != errJournalCorrupt {
			t.Fatalf("expected errJournalCorrupt, got %v", err)
		}
	})
	t.Run("bit flip in payload", func(t *testing.T) {
		bad := append([]byte{}, raw...)
		bad[len(bad)-1] ^= 0xff
		if _, err := parseJournalRecords(bad); err != errJournalCorrupt {
			t.Fatalf("expected errJournalCorrupt, got %v", err)
		}
	})
}

func TestJournalWithoutReady(t *testing.T) {
	// A daemon that has written its header and some changes but not yet
	// finished registering watches. Not damage — just not usable yet.
	raw, _ := buildJournal(t, 7, journalRecord{Kind: recChange, Body: []byte("a.txt")})
	if _, err := openJournal(raw); err != errJournalNoReady {
		t.Fatalf("expected errJournalNoReady, got %v", err)
	}
	// Header alone is the same state.
	if _, err := openJournal(encodeJournalHeader(7)); err != errJournalNoReady {
		t.Fatalf("expected errJournalNoReady for a bare header, got %v", err)
	}
}

// ---- change path validation ----

func TestValidChangePath(t *testing.T) {
	valid := []string{"a.txt", "src/main.go", "a/b/c/d.txt", "a..b", ".qinignore", "src/.qin/x"}
	invalid := []string{
		"",                 // empty
		"/etc/passwd",      // absolute
		"../outside",       // escapes
		"a/../../outside",  // escapes after cleaning
		"..",               // the parent itself
		".",                // not a file
		"a\\b",             // backslash: never canonical
		"C:foo",            // Windows drive-relative
		"a\x00b",           // NUL would break the composite index key
		".qin",             // the monitor's own state
		".qin/fsmonitor/x", // ...and its contents
	}
	for _, p := range valid {
		if !validChangePath(p) {
			t.Errorf("expected %q to be accepted", p)
		}
	}
	for _, p := range invalid {
		if validChangePath(p) {
			t.Errorf("expected %q to be rejected", p)
		}
	}
}

// TestJournalRejectsBadChangePaths checks the validation is actually wired
// into the read path, not merely available.
func TestJournalRejectsBadChangePaths(t *testing.T) {
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd", ".qin/fsmonitor/state.json"} {
		raw, readyEnd := buildJournal(t, 7,
			journalRecord{Kind: recReady, Body: []byte("inotify")},
			journalRecord{Kind: recChange, Body: []byte(bad)},
		)
		v, err := openJournal(raw)
		if err != nil {
			t.Fatalf("%s: %v", bad, err)
		}
		if _, err := v.changesSince(readyEnd, v.Size); err != errJournalCorrupt {
			t.Errorf("%s: expected errJournalCorrupt, got %v", bad, err)
		}
	}
}

// ---- generation ----

func TestNextGeneration(t *testing.T) {
	if g := nextGeneration(0, 5000); g != 5000 {
		t.Fatalf("expected 5000, got %d", g)
	}
	// A clock that has not advanced (or has stepped back) must still yield a
	// strictly greater generation: reusing one would revalidate a stale cursor.
	if g := nextGeneration(5000, 5000); g != 5001 {
		t.Fatalf("expected 5001, got %d", g)
	}
	if g := nextGeneration(5000, 1); g != 5001 {
		t.Fatalf("expected 5001 from a backwards clock, got %d", g)
	}
	if g := nextGeneration(0, 0); g != 1 {
		t.Fatalf("expected 1, never 0, got %d", g)
	}
	if g := nextGeneration(^uint64(0)-1, 0); g != ^uint64(0) {
		t.Fatalf("expected saturation, got %d", g)
	}
	// At the ceiling there is nothing greater to return, and wrapping would
	// hand back a generation that a stale cursor still names. Refusing to move
	// is the safe failure: the caller sees no usable cursor.
	if g := nextGeneration(^uint64(0), 0); g != ^uint64(0) {
		t.Fatalf("expected the ceiling to hold, got %d", g)
	}
	// ...and the client treats it as no cursor at all.
	if d, _ := decideCursor(nil, 0, 0, 0); d != cursorUntrusted {
		t.Fatalf("expected cursorUntrusted, got %d", d)
	}
	if name := journalGenName(0x0123456789abcdef); name != "0123456789abcdef" {
		t.Fatalf("got %q", name)
	}
}

// ---- cursor rules ----

func TestDecideCursor(t *testing.T) {
	const readyEnd, syncEnd = 100, 900

	v2 := &fsmonitorState{Version: fsmonitorVersion, Gen: 5, Offset: 500}

	cases := []struct {
		name    string
		st      *fsmonitorState
		gen     uint64
		ready   int64
		sync    int64
		want    cursorDecision
		wantGen uint64
	}{
		{"no cursor at all", nil, 5, readyEnd, syncEnd, cursorCovered, 5},
		{"usable cursor", v2, 5, readyEnd, syncEnd, cursorIncremental, 5},
		{"cursor at the sync boundary", &fsmonitorState{Version: 2, Gen: 5, Offset: syncEnd}, 5, readyEnd, syncEnd, cursorIncremental, 5},
		{"cursor exactly at coverage start", &fsmonitorState{Version: 2, Gen: 5, Offset: readyEnd}, 5, readyEnd, syncEnd, cursorIncremental, 5},
		{"generation from a previous daemon", &fsmonitorState{Version: 2, Gen: 4, Offset: 500}, 5, readyEnd, syncEnd, cursorCovered, 5},
		{"cursor predates coverage", &fsmonitorState{Version: 2, Gen: 5, Offset: readyEnd - 1}, 5, readyEnd, syncEnd, cursorCovered, 5},
		{"cursor past the end of the journal", &fsmonitorState{Version: 2, Gen: 5, Offset: syncEnd + 1}, 5, readyEnd, syncEnd, cursorCovered, 5},
		{"version 1 sidecar", &fsmonitorState{Version: 1, Gen: 5, Offset: 500}, 5, readyEnd, syncEnd, cursorCovered, 5},
		{"zero version sidecar", &fsmonitorState{Gen: 5, Offset: 500}, 5, readyEnd, syncEnd, cursorCovered, 5},
		// No synchronization happened, so nothing about the journal is
		// trustworthy and no cursor may be recorded.
		{"no handshake, gen zero", v2, 0, readyEnd, syncEnd, cursorUntrusted, 0},
		{"sync before coverage", v2, 5, readyEnd, readyEnd - 1, cursorUntrusted, 0},
	}
	for _, tc := range cases {
		got, gen := decideCursor(tc.st, tc.gen, tc.ready, tc.sync)
		if got != tc.want {
			t.Errorf("%s: decision = %d, want %d", tc.name, got, tc.want)
		}
		if gen != tc.wantGen {
			t.Errorf("%s: cursor generation = %d, want %d", tc.name, gen, tc.wantGen)
		}
	}

	// The incremental decision reads from the stored offset but stores the
	// handshake's: storing the former is the difference between a cursor that
	// advances and one that re-reads the same window on every run for as long
	// as the generation lasts. The decision cannot express that distinction,
	// which is the point of not returning a position at all.
	if _, gen := decideCursor(v2, 5, readyEnd, syncEnd); gen != 5 {
		t.Fatalf("generation = %d, want the one the handshake named", gen)
	}
}

// TestJournalChangesSinceRefusesPreCoverageCursor pins the rule that makes a
// partly-started daemon harmless.
func TestJournalChangesSinceRefusesPreCoverageCursor(t *testing.T) {
	raw, readyEnd := buildJournal(t, 7,
		journalRecord{Kind: recReady, Body: []byte("inotify")},
		journalRecord{Kind: recChange, Body: []byte("a.txt")},
	)
	v, err := openJournal(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.changesSince(0, v.Size)
	if err == nil {
		t.Fatal("expected a cursor below ReadyEnd to be refused")
	}
	if !strings.Contains(err.Error(), "predates coverage") {
		t.Fatalf("expected a coverage error, got %v", err)
	}
	// At exactly ReadyEnd it is fine: coverage begins there.
	if _, err := v.changesSince(readyEnd, v.Size); err != nil {
		t.Fatalf("cursor at ReadyEnd should be usable: %v", err)
	}
}

// installJournal writes raw journal bytes as a generation's file.
func installJournal(t *testing.T, r *Repository, gen uint64, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(r.fsmonitorDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(r.journalPath(gen), raw, 0644); err != nil {
		t.Fatal(err)
	}
}

// appendJournalBytes appends raw frames to a generation's journal, the way a
// running daemon would.
func appendJournalBytes(t *testing.T, r *Repository, gen uint64, b []byte) {
	t.Helper()
	f, err := os.OpenFile(r.journalPath(gen), os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestJournalChangesReadsTheWindowAndOnlyTheWindow covers the client's one read
// of the journal. Both halves of it can go wrong silently: the wrong number of
// bytes from the right offset truncates a record, and the right number from the
// wrong offset either fails to parse or yields paths that look plausible and
// are not the window that was asked for. Only the second of those is a bug a
// CRC cannot catch, so the paths are pinned exactly rather than merely counted.
func TestJournalChangesReadsTheWindowAndOnlyTheWindow(t *testing.T) {
	r, _ := daemonRepo(t)
	const gen = 0x77

	first, readyEnd := buildJournal(t, gen,
		journalRecord{Kind: recReady, Body: []byte("inotify")},
		journalRecord{Kind: recChange, Body: []byte("before.txt")},
	)
	installJournal(t, r, gen, first)

	// From the coverage boundary: everything recorded after it.
	paths, err := r.journalChanges(gen, readyEnd, int64(len(first)))
	if err != nil {
		t.Fatalf("window from the coverage boundary: %v", err)
	}
	if len(paths) != 1 || paths[0] != "before.txt" {
		t.Fatalf("paths = %v, want [before.txt]", paths)
	}

	// From a cursor the previous run left behind, which is the ordinary case.
	full, _ := buildJournal(t, gen,
		journalRecord{Kind: recReady, Body: []byte("inotify")},
		journalRecord{Kind: recChange, Body: []byte("before.txt")},
		journalRecord{Kind: recSync, Body: []byte("an echo")},
		journalRecord{Kind: recChange, Body: []byte("after.txt")},
	)
	installJournal(t, r, gen, full)

	cursor := int64(len(first))
	paths, err = r.journalChanges(gen, cursor, int64(len(full)))
	if err != nil {
		t.Fatalf("window from a mid-file cursor: %v", err)
	}
	if len(paths) != 1 || paths[0] != "after.txt" {
		t.Fatalf("paths = %v, want [after.txt] — the window, not the whole journal", paths)
	}

	// An empty window is empty, not an error: it is what every run after a
	// quiet one asks for.
	if paths, err := r.journalChanges(gen, int64(len(full)), int64(len(full))); err != nil || len(paths) != 0 {
		t.Fatalf("empty window: paths = %v, err = %v", paths, err)
	}
}

func TestJournalChangesRefusesAWindowItCannotPlace(t *testing.T) {
	r, _ := daemonRepo(t)
	const gen = 0x78
	full, readyEnd := buildJournal(t, gen,
		journalRecord{Kind: recReady, Body: []byte("inotify")},
		journalRecord{Kind: recChange, Body: []byte("a.txt")},
	)
	installJournal(t, r, gen, full)

	// A window below the header cannot be a position the daemon named.
	if _, err := r.journalChanges(gen, 0, int64(len(full))); err == nil {
		t.Fatal("expected a window below the header to be refused")
	}
	// An offset that is not a record boundary — reading from it would return
	// garbage rather than the frames that follow.
	if _, err := r.journalChanges(gen, readyEnd+1, int64(len(full))); err == nil {
		t.Fatal("expected a window that starts mid-record to be refused")
	}
	// A different generation's bytes are not this generation's journal.
	if _, err := r.journalChanges(gen+1, readyEnd, int64(len(full))); err == nil {
		t.Fatal("expected a mismatched generation to be refused")
	}
	// A window running past the end of the file.
	if _, err := r.journalChanges(gen, readyEnd, int64(len(full))+64); err == nil {
		t.Fatal("expected a window past the end of the file to be refused")
	}
}
