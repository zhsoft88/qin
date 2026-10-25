package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

// Pack format. A pack is:
//
//	magic    "QINPACK1\n"                               9 bytes
//	count    uint64 big-endian                          8 bytes
//	record*  hash[32] + uvarint(len) + payload[len]
//
// A payload is the on-disk object file byte for byte. qin objects are already
// gzip-compressed (SerializeObject) and their ID is the SHA-256 of those
// compressed bytes (StoreObject), so a pack must carry them unchanged:
// recompressing would produce different IDs. Packing adds framing only;
// "decompression" happens when an object's content is read, long after the
// receiver has installed and verified it.
const (
	PackMagic      = "QINPACK1\n"
	PackHeaderSize = len(PackMagic) + 8

	// MaxPackObjects bounds a pack's record count. A receiver reads the count
	// before any record, so an unbounded count from a forged header would
	// otherwise size its work — and any preallocation — arbitrarily.
	MaxPackObjects = 2000000

	// maxPackPayload bounds one record. An object holds at most one CDC chunk
	// plus gzip overhead, so a gigabyte is far beyond what qin writes and still
	// keeps a forged length from being trusted as an allocation size.
	maxPackPayload = int64(1) << 30
)

// PackSize returns the framed length of a pack whose payloads have the given
// sizes. It reads no content, so a sender can work out the total — and where a
// resumed transfer starts — with nothing but os.Stat results.
func PackSize(sizes []int64) int64 {
	total := int64(PackHeaderSize)
	for _, size := range sizes {
		total += int64(HashSize) + int64(uvarintLen(uint64(size))) + size
	}
	return total
}

// NormalizePack puts records in pack order: sorted by the 32 hash bytes and
// free of duplicates. The order matters beyond tidiness — it is what makes the
// same object set produce byte-identical packs, which in turn is what lets a
// transfer resume at a byte offset.
func NormalizePack(hashes []Hash, sizes []int64) ([]Hash, []int64, error) {
	if len(hashes) != len(sizes) {
		return nil, nil, fmt.Errorf("pack: %d hashes but %d sizes", len(hashes), len(sizes))
	}
	type record struct {
		hash Hash
		size int64
	}
	records := make([]record, len(hashes))
	for i := range hashes {
		if hashes[i].IsZero() {
			return nil, nil, fmt.Errorf("pack: zero hash at index %d", i)
		}
		if sizes[i] < 0 {
			return nil, nil, fmt.Errorf("pack: negative size for %s", hashes[i].Short())
		}
		records[i] = record{hashes[i], sizes[i]}
	}
	sort.Slice(records, func(i, j int) bool {
		return bytes.Compare(records[i].hash[:], records[j].hash[:]) < 0
	})
	outHashes := make([]Hash, 0, len(records))
	outSizes := make([]int64, 0, len(records))
	for i, rec := range records {
		if i > 0 && rec.hash == records[i-1].hash {
			// Objects are immutable, so one hash cannot carry two sizes; if it
			// does, the caller's view of the store is inconsistent.
			if rec.size != records[i-1].size {
				return nil, nil, fmt.Errorf("pack: object %s listed with sizes %d and %d",
					rec.hash.Short(), records[i-1].size, rec.size)
			}
			continue
		}
		outHashes = append(outHashes, rec.hash)
		outSizes = append(outSizes, rec.size)
	}
	return outHashes, outSizes, nil
}

// PackID identifies the pack framing the given objects: a SHA-256 over the
// magic, the record count, and every hash and payload length in pack order.
//
// It is a resume key, not a content digest. It covers only framing metadata,
// so it can be computed without opening a single object — an uploader needs
// that to ask a receiver "how many bytes of this pack do you already have?"
// before sending anything. Payload integrity is proven by each record's own
// SHA-256 at install time; SHA256(part) == PackID must never stand in for it.
func PackID(hashes []Hash, sizes []int64) (Hash, error) {
	packHashes, packSizes, err := NormalizePack(hashes, sizes)
	if err != nil {
		return Hash{}, err
	}
	h := sha256.New()
	h.Write([]byte(PackMagic))
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(packHashes)))
	h.Write(count[:])
	var lenBuf [binary.MaxVarintLen64]byte
	for i, hash := range packHashes {
		h.Write(hash[:])
		n := binary.PutUvarint(lenBuf[:], uint64(packSizes[i]))
		h.Write(lenBuf[:n])
	}
	var id Hash
	copy(id[:], h.Sum(nil))
	return id, nil
}

// PackSource opens the stored bytes of one object. The returned reader should
// also be an io.Seeker: WritePackFrom seeks past the part a resumed transfer
// has already delivered rather than reading and discarding it.
type PackSource func(h Hash) (io.ReadSeeker, error)

// WritePackFrom writes the pack for the given objects starting at byte `from`
// of the full pack, so that concatenating what successive calls produce (with
// increasing offsets) reconstructs the pack exactly. `from` may land anywhere:
// in the header, in a record's hash or length, or inside a payload. Record
// framing that lies entirely before `from` costs only arithmetic; the one
// payload that straddles it is opened and seeked.
//
// hashes and sizes need not be sorted — they are put in pack order here, the
// same way PackID does.
func WritePackFrom(w io.Writer, hashes []Hash, sizes []int64, from int64, open PackSource) (int64, error) {
	packHashes, packSizes, err := NormalizePack(hashes, sizes)
	if err != nil {
		return 0, err
	}
	total := PackSize(packSizes)
	if from < 0 || from > total {
		return 0, fmt.Errorf("pack offset %d out of range [0,%d]", from, total)
	}

	var written int64
	pos := int64(0) // stream position of the byte the next segment starts at

	// segment emits the part of buf at or after `from`. Every framed byte
	// passes through here, so pos always names the next byte's offset.
	segment := func(buf []byte) error {
		end := pos + int64(len(buf))
		if end <= from {
			pos = end
			return nil
		}
		start := 0
		if from > pos {
			start = int(from - pos)
		}
		n, err := w.Write(buf[start:])
		written += int64(n)
		pos = end
		if err != nil {
			return err
		}
		if n != len(buf)-start {
			return io.ErrShortWrite
		}
		return nil
	}

	header := make([]byte, PackHeaderSize)
	copy(header, PackMagic)
	binary.BigEndian.PutUint64(header[len(PackMagic):], uint64(len(packHashes)))
	if err := segment(header); err != nil {
		return written, err
	}

	for i, hash := range packHashes {
		rec := make([]byte, HashSize+binary.MaxVarintLen64)
		copy(rec, hash[:])
		n := binary.PutUvarint(rec[HashSize:], uint64(packSizes[i]))
		if err := segment(rec[:HashSize+n]); err != nil {
			return written, err
		}

		payloadStart := pos
		payloadEnd := pos + packSizes[i]
		if payloadEnd <= from {
			pos = payloadEnd
			continue
		}
		skip := int64(0)
		if from > payloadStart {
			skip = from - payloadStart
		}
		if err := writePackPayload(w, open, hash, skip, packSizes[i]-skip, &written); err != nil {
			return written, err
		}
		pos = payloadEnd
	}
	return written, nil
}

// writePackPayload copies one record's payload, starting `skip` bytes into it.
// The length comes from the caller's stat, so a short file means the store
// changed under us; CopyN reports that rather than shifting every later frame.
func writePackPayload(w io.Writer, open PackSource, hash Hash, skip, n int64, written *int64) error {
	if n == 0 {
		return nil
	}
	src, err := open(hash)
	if err != nil {
		return fmt.Errorf("open object %s: %w", hash.Short(), err)
	}
	// os.File closes; an in-memory reader in a test need not implement it.
	if c, ok := src.(io.Closer); ok {
		defer c.Close()
	}
	if skip > 0 {
		if _, err := src.Seek(skip, io.SeekStart); err != nil {
			return fmt.Errorf("seek object %s: %w", hash.Short(), err)
		}
	}
	cw := &countingWriter{w: w}
	_, err = io.CopyN(cw, src, n)
	*written += cw.n
	if err != nil {
		return fmt.Errorf("read object %s: %w", hash.Short(), err)
	}
	return nil
}

// ReadPack parses a complete pack from r, handing each record's payload to
// install. install must consume exactly size bytes; anything else leaves the
// stream out of frame, so ReadPack reports it rather than reading the next
// record from the wrong offset.
//
// ReadPack performs no integrity check of its own: it does not know what the
// bytes mean. install is expected to hash them and write atomically.
func ReadPack(r io.Reader, install func(hash Hash, src io.Reader, size int64) error) error {
	header := make([]byte, PackHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return fmt.Errorf("read pack header: %w", err)
	}
	if string(header[:len(PackMagic)]) != PackMagic {
		return fmt.Errorf("invalid pack: bad magic")
	}
	count := binary.BigEndian.Uint64(header[len(PackMagic):])
	if count > MaxPackObjects {
		return fmt.Errorf("invalid pack: %d records exceeds limit %d", count, MaxPackObjects)
	}

	hashBuf := make([]byte, HashSize)
	for i := uint64(0); i < count; i++ {
		if _, err := io.ReadFull(r, hashBuf); err != nil {
			return fmt.Errorf("read record %d hash: %w", i, err)
		}
		var hash Hash
		copy(hash[:], hashBuf)
		if hash.IsZero() {
			return fmt.Errorf("read record %d: zero hash", i)
		}
		size, err := binary.ReadUvarint(&packByteReader{r: r})
		if err != nil {
			return fmt.Errorf("read record %d size: %w", i, err)
		}
		if size > uint64(maxPackPayload) {
			return fmt.Errorf("read record %d: payload size %d exceeds limit %d", i, size, maxPackPayload)
		}
		cr := &countingReader{r: io.LimitReader(r, int64(size))}
		if err := install(hash, cr, int64(size)); err != nil {
			return fmt.Errorf("install %s: %w", hash.Short(), err)
		}
		if cr.n != int64(size) {
			return fmt.Errorf("record %d (%s): installer read %d of %d bytes, pack is corrupt",
				i, hash.Short(), cr.n, size)
		}
	}

	var extra [1]byte
	if _, err := io.ReadFull(r, extra[:]); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid pack: trailing data after %d records", count)
		}
		return fmt.Errorf("read pack trailer: %w", err)
	}
	return nil
}

// EncodeHashes is the binary request/response body naming a set of objects:
// a uint64 big-endian count followed by the raw 32-byte hashes. Fixed-width
// hashes keep a forged count from turning into a large allocation, and 200k
// chunk hashes cost 6.4 MB rather than the 12.8 MB their hex text would.
func EncodeHashes(hashes []Hash) []byte {
	buf := make([]byte, 8+len(hashes)*HashSize)
	binary.BigEndian.PutUint64(buf, uint64(len(hashes)))
	for i, hash := range hashes {
		copy(buf[8+i*HashSize:], hash[:])
	}
	return buf
}

// DecodeHashes parses an EncodeHashes body, rejecting counts above
// MaxPackObjects before allocating for them and any length that does not
// match the count exactly.
func DecodeHashes(data []byte) ([]Hash, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("hash list: %d bytes is shorter than its header", len(data))
	}
	count := binary.BigEndian.Uint64(data)
	if count > MaxPackObjects {
		return nil, fmt.Errorf("hash list: %d entries exceeds limit %d", count, MaxPackObjects)
	}
	if uint64(len(data)-8) != count*HashSize {
		return nil, fmt.Errorf("hash list: header says %d entries, body holds %d bytes",
			count, len(data)-8)
	}
	hashes := make([]Hash, int(count))
	for i := range hashes {
		copy(hashes[i][:], data[8+i*HashSize:])
	}
	return hashes, nil
}

func uvarintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

// packByteReader adapts an io.Reader to the io.ByteReader binary.ReadUvarint
// needs. The uvarint sits directly in the stream, so buffering ahead would
// steal the bytes of the record that follows.
type packByteReader struct {
	r io.Reader
}

func (b *packByteReader) ReadByte() (byte, error) {
	var buf [1]byte
	if _, err := io.ReadFull(b.r, buf[:]); err != nil {
		return 0, err
	}
	return buf[0], nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
