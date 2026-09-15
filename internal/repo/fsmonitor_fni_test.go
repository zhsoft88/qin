package repo

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// fniRaw is one change record as ReadDirectoryChangesW would write it.
type fniRaw struct {
	action uint32
	name   string
}

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, v := range u {
		binary.LittleEndian.PutUint16(b[2*i:], v)
	}
	return b
}

// fniBuf lays records out the way the system does: a 12-byte header, then the
// name, then padding to the next 4-byte boundary. The chain is walked by the
// recorded offset and never by the name's length, which is why the offset can
// exceed header+name.
func fniBuf(recs ...fniRaw) []byte {
	return fniChain(4, recs...)
}

// fniChain builds the same chain at an arbitrary alignment. Records are only
// 4-byte aligned in practice, not by contract, so the decoder must not depend
// on it — and this is how the ragged case gets built.
func fniChain(align int, recs ...fniRaw) []byte {
	var out []byte
	for i, r := range recs {
		name := utf16le(r.name)
		size := fniHeaderSize + len(name)
		if r := size % align; r != 0 {
			size += align - r
		}
		next := 0
		if i < len(recs)-1 {
			next = size
		}
		var hdr [fniHeaderSize]byte
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(next))
		binary.LittleEndian.PutUint32(hdr[4:8], r.action)
		binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(name)))
		out = append(out, hdr[:]...)
		out = append(out, name...)
		for j := len(name); j < size-fniHeaderSize; j++ {
			out = append(out, 0)
		}
	}
	return out
}

func TestParseWindowsNotify(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		want []watchEvent
	}{
		{
			name: "one record",
			buf:  fniBuf(fniRaw{fniActionAdded, "a.txt"}),
			want: []watchEvent{{Path: "a.txt", Kind: watchChanged}},
		},
		{
			// The records are a chain, and only a zero offset ends it. Reading
			// one record per buffer would silently drop the rest of a busy
			// interval.
			name: "a chain ends only at a zero offset",
			buf: fniBuf(
				fniRaw{fniActionAdded, "a.txt"},
				fniRaw{fniActionModified, "b.txt"},
				fniRaw{fniActionRemoved, "c.txt"},
			),
			want: []watchEvent{
				{Path: "a.txt", Kind: watchChanged},
				{Path: "b.txt", Kind: watchChanged},
				{Path: "c.txt", Kind: watchRemoved},
			},
		},
		{
			// Every action is mapped, including the two that make up a rename —
			// which arrives as a pair, so the old name has to be reported as
			// gone or the index would keep believing in it.
			name: "every action maps to a kind",
			buf: fniBuf(
				fniRaw{fniActionAdded, "added"},
				fniRaw{fniActionRemoved, "removed"},
				fniRaw{fniActionModified, "modified"},
				fniRaw{fniActionRenamedOldName, "old"},
				fniRaw{fniActionRenamedNewName, "new"},
			),
			want: []watchEvent{
				{Path: "added", Kind: watchChanged},
				{Path: "removed", Kind: watchRemoved},
				{Path: "modified", Kind: watchChanged},
				{Path: "old", Kind: watchRemoved},
				{Path: "new", Kind: watchChanged},
			},
		},
		{
			// Names arrive relative to the watched directory, in the platform's
			// separator. The index is slash-separated, so a path that keeps its
			// backslashes matches nothing.
			name: "backslashes become slashes",
			buf:  fniBuf(fniRaw{fniActionModified, `sub\deep\a.txt`}),
			want: []watchEvent{{Path: "sub/deep/a.txt", Kind: watchChanged}},
		},
		{
			// The monitor's own state churns constantly, so a record for it is
			// dropped rather than treated as a fault — refusing the batch would
			// mean a full scan after every commit.
			name: "the object store and its own state are dropped",
			buf: fniBuf(
				fniRaw{fniActionModified, `.qin\objects\ab\cdef`},
				fniRaw{fniActionModified, `.qin`},
				fniRaw{fniActionModified, "a.txt"},
			),
			want: []watchEvent{{Path: "a.txt", Kind: watchChanged}},
		},
		{
			// A name is UTF-16 with lengths in bytes, so a decoder that counted
			// characters would cut these in half.
			name: "non-ascii and non-bmp names",
			buf: fniBuf(
				fniRaw{fniActionAdded, "日本語.txt"},        // 3 code units, 6 bytes
				fniRaw{fniActionAdded, "\U0001F600.txt"}, // a surrogate pair
			),
			want: []watchEvent{
				{Path: "日本語.txt", Kind: watchChanged},
				{Path: "\U0001F600.txt", Kind: watchChanged},
			},
		},
		{
			// Padding is not part of the record layout, but a length that
			// counted it would otherwise turn "a.txt" into a name nothing can
			// match — every event for that file, reported for a path that does
			// not exist.
			name: "a trailing NUL counted into the length is trimmed",
			buf:  fniNil(fniRaw{fniActionModified, "a.txt"}),
			want: []watchEvent{{Path: "a.txt", Kind: watchChanged}},
		},
	}

	for _, tc := range cases {
		got, err := parseWindowsNotify(tc.buf)
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

// fniNil builds a single record whose declared length includes a trailing NUL,
// which the ABI does not promise but does not forbid either.
func fniNil(r fniRaw) []byte {
	name := utf16le(r.name)
	var out []byte
	var hdr [fniHeaderSize]byte
	binary.LittleEndian.PutUint32(hdr[0:4], 0)
	binary.LittleEndian.PutUint32(hdr[4:8], r.action)
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(name)+2))
	out = append(out, hdr[:]...)
	out = append(out, name...)
	return append(out, 0, 0)
}

// TestParseWindowsNotifyUnaligned pins the reason this decoder reads bytes
// instead of overlaying a struct.
//
// The name's length is in bytes, and nothing about the layout promises more
// than even alignment, so a record's successor can begin at an offset that is
// not a multiple of 4 — and a five-character name is enough to produce one.
// A struct overlay would also be wrong for a fixed reason: the struct the
// standard library declares for this record gives the name field a single
// uint16, so its size is 16 where the real record's is 12, and the name would
// be read from the wrong place on every record.
func TestParseWindowsNotifyUnaligned(t *testing.T) {
	recs := []fniRaw{
		{fniActionAdded, "hello"},    // 5 code units: 10 bytes + 12 = 22, aligned to 24
		{fniActionModified, "world"}, // and again
		{fniActionRemoved, "three"},  // 5 again
	}
	want := []watchEvent{
		{Path: "hello", Kind: watchChanged},
		{Path: "world", Kind: watchChanged},
		{Path: "three", Kind: watchRemoved},
	}
	for _, align := range []int{2, 4, 8} {
		got, err := parseWindowsNotify(fniChain(align, recs...))
		if err != nil {
			t.Fatalf("align %d: %v", align, err)
		}
		if len(got) != len(want) {
			t.Fatalf("align %d: got %v, want %v", align, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("align %d: event %d = %+v, want %+v", align, i, got[i], want[i])
			}
		}
	}
}

// TestParseWindowsNotifyMalformed covers buffers that must be refused. Each
// one is a case where continuing would mean slicing past the end or inventing
// a path, and inventing a path means reporting a change for something that was
// never named — or worse, quietly not reporting the one that was.
func TestParseWindowsNotifyMalformed(t *testing.T) {
	good := fniBuf(fniRaw{fniActionAdded, "a.txt"})

	// A record whose declared name runs past the end of the buffer.
	overrun := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(overrun[8:12], 0xffff)

	// An odd length: the name is UTF-16, so half a code unit is not a name.
	odd := fniBuf(fniRaw{fniActionAdded, "a"})
	binary.LittleEndian.PutUint32(odd[8:12], 3)

	// A successor that exists but does not lie past this record's header: it
	// would walk backwards, and either loop forever or read the same record
	// twice.
	backward := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(backward[0:4], 4)

	// A successor that is beyond the end of the buffer.
	past := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(past[0:4], uint32(len(good)+64))

	// An action outside the documented set. Guessing what it meant would be
	// guessing whether a path changed.
	unknown := fniBuf(fniRaw{99, "a.txt"})

	cases := []struct {
		name string
		buf  []byte
	}{
		{"empty", nil},
		{"fewer bytes than a header", good[:fniHeaderSize-1]},
		{"a header with no room for its name", good[:fniHeaderSize]},
		{"a name that runs past the end", overrun},
		{"an odd name length", odd},
		{"a successor inside the header", backward},
		{"a successor past the end", past},
		{"an unknown action", unknown},
	}
	for _, tc := range cases {
		got, err := parseWindowsNotify(tc.buf)
		if err != errWindowsNotifyMalformed {
			t.Errorf("%s: got (%v, %v), want errWindowsNotifyMalformed", tc.name, got, err)
		}
	}
}

func TestWindowsNotifyKind(t *testing.T) {
	cases := []struct {
		action uint32
		want   watchKind
		ok     bool
	}{
		{fniActionAdded, watchChanged, true},
		{fniActionRemoved, watchRemoved, true},
		{fniActionModified, watchChanged, true},
		{fniActionRenamedOldName, watchRemoved, true},
		{fniActionRenamedNewName, watchChanged, true},
		{0, 0, false},
		{6, 0, false},
	}
	for _, tc := range cases {
		got, ok := windowsNotifyKind(tc.action)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("action %d: got (%v, %v), want (%v, %v)", tc.action, got, ok, tc.want, tc.ok)
		}
	}
}

// TestClassifyDirEvent pins the rule both backends share, in the one place it
// can be tested without either of them.
func TestClassifyDirEvent(t *testing.T) {
	cases := []struct {
		name string
		e    dirEvent
		want dirAction
	}{
		{
			// A known directory is gone. The names of what it held cannot be
			// recovered from the events, so the only honest answer is a full
			// scan.
			name: "a known directory removed",
			e:    dirEvent{Removed: true, WasDir: true},
			want: dirActionEndEpoch,
		},
		{
			// A directory that is new to us. Whatever is inside it was never
			// announced, so it has to be named rather than assumed unchanged.
			name: "a directory that is new",
			e:    dirEvent{WasDir: false, IsDir: true},
			want: dirActionEnumerate,
		},
		{
			// The ordinary case: a file changed, or a file was removed. Both
			// are looked up by exact path, and neither hides another name.
			name: "an ordinary file change",
			e:    dirEvent{},
			want: dirActionNone,
		},
		{
			name: "a file removed",
			e:    dirEvent{Removed: true},
			want: dirActionNone,
		},
		{
			// A directory event that is neither new nor gone — an attribute
			// change, say — affects only its own path.
			name: "a directory that was already known changed",
			e:    dirEvent{WasDir: true, IsDir: true},
			want: dirActionNone,
		},
		{
			// Was a directory, is a directory, but the path went away: still
			// covered by the first case, which must win over the others.
			name: "a known directory removed and recreated is still a loss",
			e:    dirEvent{Removed: true, WasDir: true, IsDir: true},
			want: dirActionEndEpoch,
		},
	}
	for _, tc := range cases {
		if got := classifyDirEvent(tc.e); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
