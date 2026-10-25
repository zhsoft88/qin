package core

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"testing"
)

// packFixture builds a small object set with payloads of varied lengths,
// including a zero-length one and one long enough to cross the 128-byte
// uvarint boundary of the framing.
func packFixture() ([]Hash, []int64, map[Hash][]byte) {
	payloads := [][]byte{
		{},
		[]byte("a"),
		[]byte("commit content"),
		bytes.Repeat([]byte("0123456789"), 30),
		bytes.Repeat([]byte{0xff}, 1000),
	}
	var hashes []Hash
	var sizes []int64
	data := make(map[Hash][]byte)
	for _, p := range payloads {
		h := HashFromBytes(p)
		hashes = append(hashes, h)
		sizes = append(sizes, int64(len(p)))
		data[h] = p
	}
	return hashes, sizes, data
}

func packSourceFor(data map[Hash][]byte) PackSource {
	return func(h Hash) (io.ReadSeeker, error) {
		p, ok := data[h]
		if !ok {
			return nil, fmt.Errorf("no object %s", h.Short())
		}
		return bytes.NewReader(p), nil
	}
}

func buildPack(t *testing.T, hashes []Hash, sizes []int64, data map[Hash][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := WritePackFrom(&buf, hashes, sizes, 0, packSourceFor(data)); err != nil {
		t.Fatalf("WritePackFrom: %v", err)
	}
	return buf.Bytes()
}

func TestPackRoundTrip(t *testing.T) {
	hashes, sizes, data := packFixture()
	pack := buildPack(t, hashes, sizes, data)

	got := make(map[Hash][]byte)
	err := ReadPack(bytes.NewReader(pack), func(h Hash, src io.Reader, size int64) error {
		buf := make([]byte, size)
		if _, err := io.ReadFull(src, buf); err != nil {
			return err
		}
		got[h] = buf
		return nil
	})
	if err != nil {
		t.Fatalf("ReadPack: %v", err)
	}
	if len(got) != len(data) {
		t.Fatalf("installed %d objects, want %d", len(got), len(data))
	}
	for h, want := range data {
		if !bytes.Equal(got[h], want) {
			t.Errorf("object %s round-tripped as %q, want %q", h.Short(), got[h], want)
		}
	}
	if want := PackSize(sizes); int64(len(pack)) != want {
		t.Errorf("pack is %d bytes, PackSize says %d", len(pack), want)
	}
}

func TestPackEmpty(t *testing.T) {
	pack := buildPack(t, nil, nil, nil)
	if len(pack) != PackHeaderSize {
		t.Fatalf("empty pack is %d bytes, want header size %d", len(pack), PackHeaderSize)
	}
	called := 0
	if err := ReadPack(bytes.NewReader(pack), func(Hash, io.Reader, int64) error {
		called++
		return nil
	}); err != nil {
		t.Fatalf("ReadPack: %v", err)
	}
	if called != 0 {
		t.Fatalf("empty pack yielded %d objects", called)
	}
}

// The same object set must produce byte-identical packs regardless of the
// order it is presented in, and duplicates must not be framed twice. Resume by
// byte offset is only meaningful if both sides agree on every byte.
func TestPackOrderAndDedup(t *testing.T) {
	hashes, sizes, data := packFixture()
	want := buildPack(t, hashes, sizes, data)

	revHashes := make([]Hash, len(hashes))
	revSizes := make([]int64, len(sizes))
	for i := range hashes {
		revHashes[i] = hashes[len(hashes)-1-i]
		revSizes[i] = sizes[len(sizes)-1-i]
	}
	revHashes = append(revHashes, revHashes[0], hashes[2])
	revSizes = append(revSizes, revSizes[0], sizes[2])
	got := buildPack(t, revHashes, revSizes, data)
	if !bytes.Equal(got, want) {
		t.Fatalf("reordered/duplicated input produced a different pack")
	}

	id1, err := PackID(hashes, sizes)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := PackID(revHashes, revSizes)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("PackID depends on input order: %s vs %s", id1.Short(), id2.Short())
	}

	// A different length is different framing, so it must be a different plan.
	changed := append([]int64(nil), sizes...)
	changed[0]++
	id3, err := PackID(hashes, changed)
	if err != nil {
		t.Fatal(err)
	}
	if id3 == id1 {
		t.Fatal("PackID ignored a size change")
	}
}

func TestWritePackFromOffsets(t *testing.T) {
	hashes, sizes, data := packFixture()
	full := buildPack(t, hashes, sizes, data)

	for from := 0; from <= len(full); from++ {
		var buf bytes.Buffer
		n, err := WritePackFrom(&buf, hashes, sizes, int64(from), packSourceFor(data))
		if err != nil {
			t.Fatalf("from=%d: %v", from, err)
		}
		if want := int64(len(full) - from); n != want {
			t.Fatalf("from=%d: wrote %d bytes, want %d", from, n, want)
		}
		if !bytes.Equal(buf.Bytes(), full[from:]) {
			t.Fatalf("from=%d: suffix differs from full pack", from)
		}
	}

	if _, err := WritePackFrom(&bytes.Buffer{}, hashes, sizes, int64(len(full))+1, packSourceFor(data)); err == nil {
		t.Fatal("offset past the end was accepted")
	}
	if _, err := WritePackFrom(&bytes.Buffer{}, hashes, sizes, -1, packSourceFor(data)); err == nil {
		t.Fatal("negative offset was accepted")
	}
}

// A resumed write must not read the payloads it has already delivered; only
// the record straddling the offset may be touched. This is the property that
// makes resuming cheap rather than a full reread.
func TestWritePackFromOpensOnlyStraddlingRecord(t *testing.T) {
	hashes, sizes, data := packFixture()

	// Locate the last byte of the final non-empty payload. The pack sorts, so
	// that record is not necessarily last: a zero-length payload after it
	// occupies framing only.
	packHashes, packSizes, err := NormalizePack(hashes, sizes)
	if err != nil {
		t.Fatal(err)
	}
	pos := int64(PackHeaderSize)
	lastPayloadEnd := int64(-1)
	for i := range packHashes {
		pos += HashSize + int64(uvarintLen(uint64(packSizes[i])))
		if packSizes[i] > 0 {
			lastPayloadEnd = pos + packSizes[i]
		}
		pos += packSizes[i]
	}
	if lastPayloadEnd <= 0 || lastPayloadEnd > pos {
		t.Fatalf("fixture has no usable payload (end %d, pack length %d)", lastPayloadEnd, pos)
	}

	opened := 0
	open := func(h Hash) (io.ReadSeeker, error) {
		opened++
		return bytes.NewReader(data[h]), nil
	}
	if _, err := WritePackFrom(&bytes.Buffer{}, hashes, sizes, lastPayloadEnd-1, open); err != nil {
		t.Fatal(err)
	}
	if opened != 1 {
		t.Fatalf("resumed write opened %d objects, want 1", opened)
	}

	opened = 0
	if _, err := WritePackFrom(&bytes.Buffer{}, hashes, sizes, int64(PackHeaderSize), open); err != nil {
		t.Fatal(err)
	}
	// A zero-length payload is framing only; nothing is opened for it.
	wantOpens := 0
	for _, size := range sizes {
		if size > 0 {
			wantOpens++
		}
	}
	if opened != wantOpens {
		t.Fatalf("write from first record opened %d objects, want %d", opened, wantOpens)
	}
}

func TestReadPackRejectsCorruption(t *testing.T) {
	hashes, sizes, data := packFixture()
	pack := buildPack(t, hashes, sizes, data)
	countNothing := func(Hash, io.Reader, int64) error { return nil }

	cases := []struct {
		name string
		data []byte
	}{
		{"truncated header", pack[:PackHeaderSize-1]},
		{"truncated record", pack[:len(pack)-1]},
		{"trailing data", append(append([]byte(nil), pack...), 'x')},
		{"bad magic", append([]byte("QINPACK2\n"), pack[len(PackMagic):]...)},
	}
	for _, tc := range cases {
		if err := ReadPack(bytes.NewReader(tc.data), countNothing); err == nil {
			t.Errorf("%s: ReadPack accepted a corrupt pack", tc.name)
		}
	}

	// A count that promises more records than the body holds.
	short := append([]byte(nil), pack...)
	binary.BigEndian.PutUint64(short[len(PackMagic):], uint64(len(hashes)+1))
	if err := ReadPack(bytes.NewReader(short), countNothing); err == nil {
		t.Error("short record count: ReadPack accepted it")
	}

	// A forged count above the limit is refused before any record is read.
	huge := make([]byte, PackHeaderSize)
	copy(huge, PackMagic)
	binary.BigEndian.PutUint64(huge[len(PackMagic):], MaxPackObjects+1)
	if err := ReadPack(bytes.NewReader(huge), countNothing); err == nil {
		t.Error("oversized count: ReadPack accepted it")
	}

	// A zero hash is never a stored object.
	zero := make([]byte, PackHeaderSize+HashSize)
	copy(zero, PackMagic)
	binary.BigEndian.PutUint64(zero[len(PackMagic):], 1)
	if err := ReadPack(bytes.NewReader(zero), countNothing); err == nil {
		t.Error("zero hash: ReadPack accepted it")
	}
}

func TestReadPackRejectsShortInstall(t *testing.T) {
	hashes, sizes, data := packFixture()
	pack := buildPack(t, hashes, sizes, data)

	err := ReadPack(bytes.NewReader(pack), func(h Hash, src io.Reader, size int64) error {
		if size == 0 {
			return nil
		}
		_, err := io.CopyN(ioutil.Discard, src, size-1)
		return err
	})
	if err == nil {
		t.Fatal("installer that left a byte unread was not reported")
	}
}

func TestEncodeDecodeHashes(t *testing.T) {
	hashes, _, _ := packFixture()
	round, err := DecodeHashes(EncodeHashes(hashes))
	if err != nil {
		t.Fatalf("DecodeHashes: %v", err)
	}
	if len(round) != len(hashes) {
		t.Fatalf("got %d hashes, want %d", len(round), len(hashes))
	}
	for i := range hashes {
		if round[i] != hashes[i] {
			t.Fatalf("hash %d differs", i)
		}
	}

	if got, err := DecodeHashes(EncodeHashes(nil)); err != nil || len(got) != 0 {
		t.Fatalf("empty list: got %v, %v", got, err)
	}
	if _, err := DecodeHashes([]byte{1, 2, 3}); err == nil {
		t.Error("short header accepted")
	}
	if _, err := DecodeHashes(EncodeHashes(hashes)[:8+HashSize-1]); err == nil {
		t.Error("body shorter than count accepted")
	}
	over := make([]byte, 8)
	binary.BigEndian.PutUint64(over, MaxPackObjects+1)
	if _, err := DecodeHashes(over); err == nil {
		t.Error("oversized count accepted")
	}
}

func TestNormalizePackRejectsMismatch(t *testing.T) {
	h, _, _ := packFixture()
	if _, _, err := NormalizePack(h, []int64{1}); err == nil {
		t.Error("mismatched hash/size counts accepted")
	}
	if _, _, err := NormalizePack([]Hash{{}}, []int64{1}); err == nil {
		t.Error("zero hash accepted")
	}
	if _, _, err := NormalizePack(h[:1], []int64{-1}); err == nil {
		t.Error("negative size accepted")
	}
}
