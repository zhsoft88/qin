package repo

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

type packRecord struct {
	hash    core.Hash
	payload []byte
}

func encodePack(t *testing.T, records []packRecord) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString(core.PackMagic)
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(records)))
	buf.Write(count[:])
	for _, rec := range records {
		buf.Write(rec.hash[:])
		var lenBuf [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(lenBuf[:], uint64(len(rec.payload)))
		buf.Write(lenBuf[:n])
		buf.Write(rec.payload)
	}
	return buf.Bytes()
}

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func objectBytes(t *testing.T, objType core.ObjectType, content []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	raw.WriteByte(byte(objType))
	var lenBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lenBuf[:], uint64(len(content)))
	raw.Write(lenBuf[:n])
	raw.Write(content)
	return gzipBytes(t, raw.Bytes())
}

func decodeObject(t *testing.T, data []byte) (core.ObjectType, []byte) {
	t.Helper()
	objType, content, err := core.DeserializeObject(data)
	if err != nil {
		t.Fatalf("DeserializeObject: %v", err)
	}
	return objType, content
}

func installPackFile(t *testing.T, r *Repository, pack []byte) (int, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pack.part")
	if err := ioutil.WriteFile(path, pack, 0644); err != nil {
		t.Fatal(err)
	}
	return r.unpackPack(path)
}

func TestUnpackPackInstallsEveryObject(t *testing.T) {
	src, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blob1, err := src.StoreObject(core.ObjectBlob, []byte("hello pack"))
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("chunk-data"), 5000)
	blob2, err := src.StoreObject(core.ObjectBlob, big)
	if err != nil {
		t.Fatal(err)
	}

	hashes := []core.Hash{blob1, blob2}
	plan, err := src.planPackForObjects(hashes)
	if err != nil {
		t.Fatal(err)
	}
	var pack bytes.Buffer
	if _, err := src.writePackFrom(&pack, plan, 0); err != nil {
		t.Fatal(err)
	}
	if int64(pack.Len()) != plan.Total {
		t.Fatalf("wrote %d bytes, plan says %d", pack.Len(), plan.Total)
	}

	dst, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n, err := installPackFile(t, dst, pack.Bytes())
	if err != nil {
		t.Fatalf("unpackPack: %v", err)
	}
	if n != 2 {
		t.Fatalf("installed %d objects, want 2", n)
	}
	for _, h := range hashes {
		if !dst.HasObject(h) {
			t.Errorf("object %s was not installed", h.Short())
		}
	}
	objType, content, err := dst.LoadObject(blob2)
	if err != nil {
		t.Fatal(err)
	}
	if objType != core.ObjectBlob || !bytes.Equal(content, big) {
		t.Error("installed object content differs")
	}
}

func TestInstallPackObjectRejectsWrongHash(t *testing.T) {
	r, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := objectBytes(t, core.ObjectBlob, []byte("content"))
	wrong := core.HashFromBytes([]byte("not the payload"))
	pack := encodePack(t, []packRecord{{hash: wrong, payload: payload}})

	if _, err := installPackFile(t, r, pack); err == nil {
		t.Fatal("record with a mismatched hash was accepted")
	}
	if r.HasObject(wrong) {
		t.Error("rejected object landed in the store")
	}
}

func TestInstallPackObjectRejectsForgedHeader(t *testing.T) {
	raw := func(typeByte byte, size uint64, content []byte) []byte {
		var buf bytes.Buffer
		buf.WriteByte(typeByte)
		var lenBuf [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(lenBuf[:], size)
		buf.Write(lenBuf[:n])
		buf.Write(content)
		return gzipBytes(t, buf.Bytes())
	}

	cases := []struct {
		name    string
		payload []byte
	}{
		{"type out of range", raw(9, 3, []byte("abc"))},
		{"type zero", raw(0, 3, []byte("abc"))},
		{"content size over limit", raw(byte(core.ObjectBlob), 1<<40, []byte("abc"))},
		{"not gzip", []byte("this is not gzip at all")},
		{"gzip with no header", gzipBytes(t, nil)},
	}
	for _, tc := range cases {
		r, err := Init(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		h := core.HashFromBytes(tc.payload)
		pack := encodePack(t, []packRecord{{hash: h, payload: tc.payload}})
		if _, err := installPackFile(t, r, pack); err == nil {
			t.Errorf("%s: forged object was accepted", tc.name)
		}
		if r.HasObject(h) {
			t.Errorf("%s: forged object landed in the store", tc.name)
		}
	}
}

// A verified local copy is not rewritten, but the record is still consumed and
// checked so the framing stays aligned.
func TestInstallPackObjectKeepsExisting(t *testing.T) {
	r, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := r.StoreObject(core.ObjectBlob, []byte("already here"))
	if err != nil {
		t.Fatal(err)
	}
	objPath := r.objectPath(h)
	before, err := os.Stat(objPath)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	payload, err := ioutil.ReadFile(objPath)
	if err != nil {
		t.Fatal(err)
	}
	pack := encodePack(t, []packRecord{{hash: h, payload: payload}})
	if _, err := installPackFile(t, r, pack); err != nil {
		t.Fatalf("unpackPack: %v", err)
	}
	after, err := os.Stat(objPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("an existing object was rewritten")
	}
}

func TestUnpackPackKeepsObjectsInstalledBeforeFailure(t *testing.T) {
	r, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	good := objectBytes(t, core.ObjectBlob, []byte("good object"))
	goodHash := core.HashFromBytes(good)
	bad := []byte("not an object")
	badHash := core.HashFromBytes([]byte("different"))
	pack := encodePack(t, []packRecord{
		{hash: goodHash, payload: good},
		{hash: badHash, payload: bad},
	})

	n, err := installPackFile(t, r, pack)
	if err == nil {
		t.Fatal("corrupt second record was accepted")
	}
	if n != 2 {
		t.Fatalf("installed count %d, want 2 (the second one is attempted)", n)
	}
	if !r.HasObject(goodHash) {
		t.Error("object verified before the failure was rolled back")
	}
	if r.HasObject(badHash) {
		t.Error("corrupt object landed in the store")
	}
}

func TestUnpackPackRejectsTrailingData(t *testing.T) {
	r, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pack := encodePack(t, nil)
	pack = append(pack, 'x')
	if _, err := installPackFile(t, r, pack); err == nil {
		t.Fatal("trailing data was accepted")
	}
}

func TestPackPlanMatchesFullWrite(t *testing.T) {
	r, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var hashes []core.Hash
	for i := 0; i < 4; i++ {
		h, err := r.StoreObject(core.ObjectBlob, bytes.Repeat([]byte{byte(i)}, i*100))
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	plan, err := r.planPackForObjects(hashes)
	if err != nil {
		t.Fatal(err)
	}
	full, err := core.PackID(plan.Hashes, plan.Sizes)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ID != full {
		t.Fatal("planPackForObjects and core.PackID disagree on the id")
	}
	var buf bytes.Buffer
	n, err := r.writePackFrom(&buf, plan, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != plan.Total || int64(buf.Len()) != plan.Total {
		t.Fatalf("wrote %d bytes (buffer %d), plan says %d", n, buf.Len(), plan.Total)
	}

	// Resuming at any offset reproduces the same pack's suffix.
	for _, from := range []int64{0, 1, plan.Total / 2, plan.Total - 1, plan.Total} {
		var suffix bytes.Buffer
		if _, err := r.writePackFrom(&suffix, plan, from); err != nil {
			t.Fatalf("from %d: %v", from, err)
		}
		if !bytes.Equal(suffix.Bytes(), buf.Bytes()[from:]) {
			t.Errorf("from %d: suffix differs", from)
		}
	}
}

func TestCleanIncoming(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.part")
	oldDone := filepath.Join(dir, "old.done")
	recent := filepath.Join(dir, "recent.part")
	other := filepath.Join(dir, "keep.txt")
	for _, p := range []string{old, oldDone, recent, other} {
		if err := ioutil.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-25 * time.Hour)
	for _, p := range []string{old, oldDone} {
		if err := os.Chtimes(p, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}

	cleanIncoming(dir)

	for _, p := range []string{old, oldDone} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived cleanup", filepath.Base(p))
		}
	}
	for _, p := range []string{recent, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(p), err)
		}
	}
}

func TestValidPackID(t *testing.T) {
	if !validPackID(core.HashFromBytes([]byte("x")).String()) {
		t.Error("a real pack id was rejected")
	}
	for _, id := range []string{"", "abc", "../../escape", "ABCDEF" + core.HashFromBytes([]byte("x")).String()[6:], core.HashFromBytes([]byte("x")).String() + "0"} {
		if validPackID(id) {
			t.Errorf("%q was accepted as a pack id", id)
		}
	}
}
