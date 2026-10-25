package repo

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

// Pack transfer, local half. The wire format lives in internal/core/pack.go;
// this file supplies what it cannot know: where objects are on disk, how a
// received object is verified and installed, and where a partially received
// pack waits for the rest.

const (
	// incomingMaxAge bounds how long an orphaned .part/.done file is kept.
	// A pack that has not moved in a day is not a transfer in progress.
	incomingMaxAge = 24 * time.Hour

	// maxObjectContentSize bounds the content length a received object header
	// may claim. qin's largest object is one CDC chunk (8 MiB by default).
	maxObjectContentSize = uint64(1) << 30
)

// packPlan is a normalized pack: the exact record order, the byte length the
// framing produces, and the id both sides use to agree they mean the same
// pack. None of it requires reading a payload.
type packPlan struct {
	Hashes []core.Hash
	Sizes  []int64
	Total  int64
	ID     core.Hash
}

func planPack(hashes []core.Hash, sizes []int64) (*packPlan, error) {
	hs, ss, err := core.NormalizePack(hashes, sizes)
	if err != nil {
		return nil, err
	}
	id, err := core.PackID(hs, ss)
	if err != nil {
		return nil, err
	}
	return &packPlan{Hashes: hs, Sizes: ss, Total: core.PackSize(ss), ID: id}, nil
}

// planPackForObjects stats the given objects and builds their pack plan.
func (r *Repository) planPackForObjects(hashes []core.Hash) (*packPlan, error) {
	sizes, err := r.objectSizes(hashes)
	if err != nil {
		return nil, err
	}
	return planPack(hashes, sizes)
}

// objectSizes reads the stored byte length of each object, the only input a
// pack plan needs. A missing object is an error: a pack naming it would fail
// halfway through its own framing.
func (r *Repository) objectSizes(hashes []core.Hash) ([]int64, error) {
	sizes := make([]int64, len(hashes))
	for i, hash := range hashes {
		fi, err := os.Stat(r.objectPath(hash))
		if err != nil {
			return nil, fmt.Errorf("object %s: %w", hash.Short(), err)
		}
		sizes[i] = fi.Size()
	}
	return sizes, nil
}

// packSource opens stored objects for the streaming writer. The file is
// seekable, so resuming in the middle of one object does not reread its head.
func (r *Repository) packSource() core.PackSource {
	return func(hash core.Hash) (io.ReadSeeker, error) {
		return os.Open(r.objectPath(hash))
	}
}

// writePackFrom streams the pack for the given records starting at `from`.
func (r *Repository) writePackFrom(w io.Writer, plan *packPlan, from int64) (int64, error) {
	return core.WritePackFrom(w, plan.Hashes, plan.Sizes, from, r.packSource())
}

// installPackObject writes one received object into the store, verifying it
// first. The payload is the stored file's bytes, so its SHA-256 must equal the
// hash the record names; the bounded header check then confirms those bytes
// are a qin object at all — the hash proves agreement about bytes, not that
// they mean anything.
//
// core.DeserializeObject is deliberately not used here. It allocates from the
// record's own uvarint, so a forged object could make the receiver allocate a
// gigabyte before it has verified a single byte, and the uint64 → int
// conversion truncates on 32-bit builds.
func (r *Repository) installPackObject(hash core.Hash, src io.Reader, size int64) error {
	objPath := r.objectPath(hash)

	// An object already present is still consumed and verified. The record
	// has to be read for the framing to advance, and a pack whose payloads
	// disagree with their hashes is corrupt even where the local copy is fine.
	if _, err := os.Stat(objPath); err == nil {
		hasher := sha256.New()
		if _, err := io.CopyN(hasher, src, size); err != nil {
			return fmt.Errorf("read object %s: %w", hash.Short(), err)
		}
		if got := hashFromSum(hasher.Sum(nil)); got != hash {
			return fmt.Errorf("object %s has hash %s", hash.Short(), got.Short())
		}
		return nil
	}

	dir := filepath.Dir(objPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create object directory: %w", err)
	}
	// A unique dot-prefixed temp file: a fixed "<final>.tmp" would let two
	// concurrent installs of the same object overwrite each other's bytes.
	tmp, err := ioutil.TempFile(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp object: %w", err)
	}
	tmpPath := tmp.Name()
	remove := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	hasher := sha256.New()
	head := &headCapture{max: 512}
	if _, err := io.CopyN(io.MultiWriter(tmp, hasher, head), src, size); err != nil {
		remove()
		return fmt.Errorf("read object %s: %w", hash.Short(), err)
	}
	if got := hashFromSum(hasher.Sum(nil)); got != hash {
		remove()
		return fmt.Errorf("object %s has hash %s", hash.Short(), got.Short())
	}
	if err := validateObjectHeader(head.buf); err != nil {
		remove()
		return fmt.Errorf("object %s: %w", hash.Short(), err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp object: %w", err)
	}
	if err := os.Rename(tmpPath, objPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("install object %s: %w", hash.Short(), err)
	}
	return nil
}

// unpackPack installs every object in a complete part file. The caller has
// verified the pack's length; ReadPack verifies its framing, and each record
// is verified before it lands. Objects installed before a failure stay: they
// are content-addressed and unreachable until a ref names them.
func (r *Repository) unpackPack(partPath string) (int, error) {
	defer endProgressLine()

	f, err := os.Open(partPath)
	if err != nil {
		return 0, fmt.Errorf("open pack: %w", err)
	}
	defer f.Close()

	count, err := packRecordCount(f)
	if err != nil {
		return 0, err
	}

	installed := 0
	var progress progressThrottle
	err = core.ReadPack(f, func(hash core.Hash, src io.Reader, size int64) error {
		installed++
		progress.print("[install] %d/%d objects", installed, count)
		return r.installPackObject(hash, src, size)
	})
	if err != nil {
		return installed, err
	}
	if installed != count {
		return installed, fmt.Errorf("pack header promised %d objects, %d were installed", count, installed)
	}
	return installed, nil
}

// packRecordCount reads just the pack header, then rewinds so ReadPack can
// parse the whole file. It validates the magic and the count bound, so a
// truncated or forged header fails before any record work starts.
func packRecordCount(f *os.File) (int, error) {
	var header [core.PackHeaderSize]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return 0, fmt.Errorf("read pack header: %w", err)
	}
	if string(header[:len(core.PackMagic)]) != core.PackMagic {
		return 0, fmt.Errorf("invalid pack: bad magic")
	}
	count := binary.BigEndian.Uint64(header[len(core.PackMagic):])
	if count > core.MaxPackObjects {
		return 0, fmt.Errorf("invalid pack: %d records exceeds limit %d", count, core.MaxPackObjects)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("rewind pack: %w", err)
	}
	return int(count), nil
}

// validateObjectHeader decompresses only the object header — type byte and
// content length — and checks both. head holds at most 512 bytes of the
// stored file, so nothing here can be made to allocate on a forger's say-so.
func validateObjectHeader(head []byte) error {
	if len(head) == 0 {
		return fmt.Errorf("empty object")
	}
	zr, err := gzip.NewReader(bytes.NewReader(head))
	if err != nil {
		return fmt.Errorf("invalid gzip header: %w", err)
	}
	defer zr.Close()

	var typeBuf [1]byte
	if _, err := io.ReadFull(zr, typeBuf[:]); err != nil {
		return fmt.Errorf("read object type: %w", err)
	}
	switch core.ObjectType(typeBuf[0]) {
	case core.ObjectBlob, core.ObjectTree, core.ObjectCommit, core.ObjectChunkManifest:
	default:
		return fmt.Errorf("invalid object type %d", typeBuf[0])
	}

	contentSize, err := binary.ReadUvarint(oneByteReader{zr})
	if err != nil {
		return fmt.Errorf("read object size: %w", err)
	}
	if contentSize > maxObjectContentSize {
		return fmt.Errorf("object content size %d exceeds limit %d", contentSize, maxObjectContentSize)
	}
	return nil
}

func hashFromSum(sum []byte) core.Hash {
	var h core.Hash
	copy(h[:], sum)
	return h
}

// headCapture keeps the first max bytes written to it and discards the rest.
type headCapture struct {
	buf []byte
	max int
}

func (h *headCapture) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		h.buf = append(h.buf, p[:room]...)
	}
	return len(p), nil
}

// oneByteReader adapts a stream to the io.ByteReader binary.ReadUvarint
// needs. The varint is immediately followed by the content, so nothing may be
// buffered past it.
type oneByteReader struct {
	r io.Reader
}

func (o oneByteReader) ReadByte() (byte, error) {
	var b [1]byte
	if _, err := io.ReadFull(o.r, b[:]); err != nil {
		return 0, err
	}
	return b[0], nil
}

// ---- resumable transfer state ----

func (r *Repository) incomingDir() string {
	return filepath.Join(r.Path, QinDir, "incoming")
}

func (r *Repository) incomingPartPath(id string) string {
	return filepath.Join(r.incomingDir(), id+".part")
}

func (r *Repository) incomingDonePath(id string) string {
	return filepath.Join(r.incomingDir(), id+".done")
}

// validPackID accepts only the hex form PackID produces, so an id taken from
// a URL cannot address anything but a file directly under .qin/incoming.
func validPackID(id string) bool {
	if len(id) != core.HashSize*2 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// cleanIncoming drops transfer state that has not been touched for a day.
// Both ends keep state here, and an abandoned upload or an already-consumed
// download should not accumulate forever.
func cleanIncoming(dir string) {
	entries, err := ioutil.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-incomingMaxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".part") && !strings.HasSuffix(name, ".done") {
			continue
		}
		if entry.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, name))
		}
	}
}
