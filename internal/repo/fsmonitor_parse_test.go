package repo

import (
	"encoding/binary"
	"testing"
)

// inotifyRaw is one event as the kernel would queue it.
type inotifyRaw struct {
	wd   int32
	mask uint32
	name string
}

// inotifyBuf assembles a read() buffer the way the kernel lays one out: a
// 16-byte header, then the name NUL-terminated and padded so the whole event
// is a multiple of 4 bytes — with the recorded length counting that padding.
func inotifyBuf(evs ...inotifyRaw) []byte {
	var out []byte
	for _, e := range evs {
		nameLen := 0
		if e.name != "" {
			nameLen = len(e.name) + 1
			if r := nameLen % 4; r != 0 {
				nameLen += 4 - r
			}
		}
		var hdr [inotifyEventSize]byte
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(e.wd))
		binary.LittleEndian.PutUint32(hdr[4:8], e.mask)
		binary.LittleEndian.PutUint32(hdr[8:12], 0) // cookie
		binary.LittleEndian.PutUint32(hdr[12:16], uint32(nameLen))
		out = append(out, hdr[:]...)
		if nameLen > 0 {
			padded := make([]byte, nameLen)
			copy(padded, e.name)
			out = append(out, padded...)
		}
	}
	return out
}

func TestParseInotifyEvents(t *testing.T) {
	names := map[int32]string{1: "", 2: "sub", 3: "sub/deep"}

	cases := []struct {
		name string
		buf  []byte
		want []watchEvent
	}{
		{
			// A name in a watched subdirectory is joined to that directory's
			// repo-relative path, not left as a bare basename.
			name: "modify in a subdirectory",
			buf:  inotifyBuf(inotifyRaw{wd: 2, mask: inModify, name: "a.txt"}),
			want: []watchEvent{{Path: "sub/a.txt", Kind: watchChanged}},
		},
		{
			// A watch on the root maps to the empty prefix, so no slash is
			// introduced at the front.
			name: "create at the root",
			buf:  inotifyBuf(inotifyRaw{wd: 1, mask: inCreate, name: "a.txt"}),
			want: []watchEvent{{Path: "a.txt", Kind: watchChanged}},
		},
		{
			name: "deeper nesting",
			buf:  inotifyBuf(inotifyRaw{wd: 3, mask: inCloseWrite, name: "f.go"}),
			want: []watchEvent{{Path: "sub/deep/f.go", Kind: watchChanged}},
		},
		{
			// Padding is counted in the length field, so an untrimmed name
			// would carry NULs into every path comparison. "a.txt" is 5 bytes
			// → 6 with the terminator → padded to 8, so this fails loudly if
			// the trim is dropped.
			name: "name longer than it is, unpadded",
			buf:  inotifyBuf(inotifyRaw{wd: 1, mask: inCreate, name: "ab.txt"}),
			want: []watchEvent{{Path: "ab.txt", Kind: watchChanged}},
		},
		{
			// Removals are distinguished: both kinds lead to the same stat,
			// but the backends' bookkeeping depends on the distinction.
			name: "delete, move-from and move-to",
			buf: inotifyBuf(
				inotifyRaw{wd: 1, mask: inDelete, name: "gone.txt"},
				inotifyRaw{wd: 1, mask: inMovedFrom, name: "old.txt"},
				inotifyRaw{wd: 1, mask: inMovedTo, name: "new.txt"},
			),
			want: []watchEvent{
				{Path: "gone.txt", Kind: watchRemoved},
				{Path: "old.txt", Kind: watchRemoved},
				{Path: "new.txt", Kind: watchChanged},
			},
		},
		{
			// A directory event is marked as such: the backend needs to know
			// whether a new subtree requires watching, and the mask already
			// says so without another stat.
			name: "a created directory is marked",
			buf:  inotifyBuf(inotifyRaw{wd: 1, mask: inCreate | inIsDir, name: "newdir"}),
			want: []watchEvent{{Path: "newdir", Kind: watchChanged, Dir: true}},
		},
		{
			// Removing a directory reports every child first, so it is fully
			// covered and must not end the epoch: removing a build directory
			// is routine, and ending the epoch would mean a full scan.
			name: "delete of a directory is not a loss",
			buf:  inotifyBuf(inotifyRaw{wd: 1, mask: inDelete | inIsDir, name: "build"}),
			want: []watchEvent{{Path: "build", Kind: watchRemoved, Dir: true}},
		},
		{
			// Several events in one read.
			name: "several events",
			buf: inotifyBuf(
				inotifyRaw{wd: 1, mask: inCreate, name: "a"},
				inotifyRaw{wd: 2, mask: inModify, name: "b"},
				inotifyRaw{wd: 2, mask: inAttrib, name: "c"},
			),
			want: []watchEvent{
				{Path: "a", Kind: watchChanged},
				{Path: "sub/b", Kind: watchChanged},
				{Path: "sub/c", Kind: watchChanged},
			},
		},
		{
			// An event about the watched directory itself carries no name.
			name: "bare delete-self is ignored, not a loss",
			buf:  inotifyBuf(inotifyRaw{wd: 2, mask: inDeleteSelf}),
			want: nil,
		},
	}

	for _, tc := range cases {
		got, err := parseInotifyEvents(tc.buf, copyNames(names))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %d events %v, want %d %v", tc.name, len(got), got, len(tc.want), tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("%s: event %d = %+v, want %+v", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

func copyNames(m map[int32]string) map[int32]string {
	out := make(map[int32]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// TestParseInotifyEventsLosses covers the cases that must end a coverage
// epoch. Each one is a situation where the events do not say everything that
// happened, so continuing to serve the fast path would mean reporting "no
// change" for a change nobody saw.
func TestParseInotifyEventsLosses(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
	}{
		{
			// The queue dropped events; what was lost is unknowable.
			name: "queue overflow",
			buf:  inotifyBuf(inotifyRaw{wd: -1, mask: inQOverflow}),
		},
		{
			// A subtree moved away is reported only as the directory name on
			// its parent — never file by file — so the index paths inside it
			// would go unchecked.
			name: "a subdirectory moved away",
			buf:  inotifyBuf(inotifyRaw{wd: 1, mask: inMovedFrom | inIsDir, name: "sub"}),
		},
		{
			// The watched directory itself moved, taking its contents with it.
			name: "the watched directory moved",
			buf:  inotifyBuf(inotifyRaw{wd: 2, mask: inMoveSelf}),
		},
		{
			// An event for a watch we do not know cannot be turned into a
			// path at all.
			name: "unknown watch descriptor",
			buf:  inotifyBuf(inotifyRaw{wd: 99, mask: inModify, name: "a.txt"}),
		},
	}
	for _, tc := range cases {
		_, err := parseInotifyEvents(tc.buf, map[int32]string{1: "", 2: "sub"})
		if err == nil {
			t.Errorf("%s: expected coverage to be reported as lost", tc.name)
			continue
		}
		var le *lostEvent
		if !asLost(err, &le) {
			t.Errorf("%s: expected a lostEvent, got %T: %v", tc.name, err, err)
		}
	}
}

func asLost(err error, out **lostEvent) bool {
	le, ok := err.(*lostEvent)
	if ok {
		*out = le
	}
	return ok
}

// TestParseInotifyIgnoredDropsWatch checks that IN_IGNORED removes the watch
// from the mapping rather than being treated as a loss — the kernel sends it
// whenever a watched directory is deleted, which is routine — and that a later
// event for the same descriptor is then a loss, since it can no longer be
// resolved to a path.
func TestParseInotifyIgnoredDropsWatch(t *testing.T) {
	names := map[int32]string{1: "", 2: "sub"}

	got, err := parseInotifyEvents(inotifyBuf(inotifyRaw{wd: 2, mask: inIgnored}), names)
	if err != nil {
		t.Fatalf("IN_IGNORED should not end the epoch: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("IN_IGNORED produced %v, want no events", got)
	}
	if _, ok := names[2]; ok {
		t.Fatal("expected the watch descriptor to be dropped from the mapping")
	}
	if _, err := parseInotifyEvents(inotifyBuf(inotifyRaw{wd: 2, mask: inModify, name: "x"}), names); err == nil {
		t.Fatal("expected a later event on a dropped descriptor to be a loss")
	}
}

// TestParseInotifyMalformed covers buffers that do not decode. A short read
// must be an error rather than a truncated slice: the next event's offset is
// taken from the length just read, so stepping over one would misparse
// everything after it.
func TestParseInotifyMalformed(t *testing.T) {
	good := inotifyBuf(inotifyRaw{wd: 1, mask: inModify, name: "a.txt"})

	cases := []struct {
		name string
		buf  []byte
	}{
		{"fewer bytes than a header", good[:8]},
		// The header still claims an 8-byte name, so the name is truncated.
		{"header claiming more name than present", good[:20]},
		{"header with the name missing entirely", good[:inotifyEventSize]},
	}
	for _, tc := range cases {
		if _, err := parseInotifyEvents(tc.buf, map[int32]string{1: ""}); err != errInotifyMalformed {
			t.Errorf("%s: got %v, want errInotifyMalformed", tc.name, err)
		}
	}

	// A nameless event is legal, and is not malformed: it is how the kernel
	// reports something about the watched directory itself.
	if _, err := parseInotifyEvents(
		inotifyBuf(inotifyRaw{wd: 1, mask: inDeleteSelf}), map[int32]string{1: ""}); err != nil {
		t.Errorf("a nameless event should decode: %v", err)
	}

	// A length that runs past the end of the buffer must be caught rather than
	// sliced.
	bad := inotifyBuf(inotifyRaw{wd: 1, mask: inModify, name: "a.txt"})
	binary.LittleEndian.PutUint32(bad[12:16], 0xffff)
	if _, err := parseInotifyEvents(bad, map[int32]string{1: ""}); err != errInotifyMalformed {
		t.Errorf("oversized name length: got %v, want errInotifyMalformed", err)
	}
}
