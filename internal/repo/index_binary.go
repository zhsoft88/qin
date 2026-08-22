package repo

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Binary index format (version 1):
//
//	[0:6)   magic  "QINIDX"
//	[6:8)   version uint16 (LE) — 1
//	[8:12)  entry count uint32 (LE)
//	then, per entry (90 fixed bytes + key):
//	  key_len uint32 (LE), key bytes (the composite key verbatim:
//	  "path" or "path\0<oss_mask>")
//	  hash        32 bytes
//	  content_hash 32 bytes
//	  size        int64 (LE)
//	  mode        uint32 (LE)
//	  lazy        byte (0/1)
//	  mtime       int64 (LE)
//	  oss         byte
const (
	indexMagic         = "QINIDX"
	indexVersion       = 1
	indexHeaderLen     = 6 + 2 + 4                       // magic + version + count
	indexEntryFixedLen = 4 + 32 + 32 + 8 + 4 + 1 + 8 + 1 // key_len..oss
)

// encodeIndex serializes the index to the binary format.
func encodeIndex(idx *Index) ([]byte, error) {
	// Capacity estimate: header + fixed per entry + average key length.
	buf := make([]byte, indexHeaderLen, indexHeaderLen+len(idx.Entries)*(indexEntryFixedLen+64))
	copy(buf, indexMagic)
	binary.LittleEndian.PutUint16(buf[6:8], indexVersion)
	binary.LittleEndian.PutUint32(buf[8:12], uint32(len(idx.Entries)))

	var tail [22]byte // size(8) + mode(4) + lazy(1) + mtime(8) + oss(1)
	for key, e := range idx.Entries {
		var klen [4]byte
		binary.LittleEndian.PutUint32(klen[:], uint32(len(key)))
		buf = append(buf, klen[:]...)
		buf = append(buf, key...)
		buf = append(buf, e.Hash[:]...)
		buf = append(buf, e.ContentHash[:]...)
		binary.LittleEndian.PutUint64(tail[0:8], uint64(e.Size))
		binary.LittleEndian.PutUint32(tail[8:12], e.Mode)
		if e.Lazy {
			tail[12] = 1
		}
		binary.LittleEndian.PutUint64(tail[13:21], uint64(e.Mtime))
		tail[21] = e.OSS
		buf = append(buf, tail[:]...)
	}
	return buf, nil
}

// decodeIndex parses the binary format back into an Index.
// All bounds are checked so a corrupt file yields an error, not a panic.
func decodeIndex(data []byte) (*Index, error) {
	if len(data) < indexHeaderLen {
		return nil, errors.New("truncated index")
	}
	if string(data[:6]) != indexMagic {
		return nil, errors.New("bad index magic")
	}
	ver := binary.LittleEndian.Uint16(data[6:8])
	if ver != indexVersion {
		return nil, fmt.Errorf("unsupported index version %d", ver)
	}
	count := binary.LittleEndian.Uint32(data[8:12])

	// Sanity-check the count against the file size before allocating: each
	// entry needs at least indexEntryFixedLen bytes (empty key). A corrupt
	// count must error here, not balloon into a huge map allocation.
	if uint64(count) > uint64(len(data)-indexHeaderLen)/indexEntryFixedLen {
		return nil, errors.New("truncated index")
	}

	idx := &Index{Entries: make(map[string]IndexEntry, count)}
	c := indexHeaderLen
	for i := uint32(0); i < count; i++ {
		if c+4 > len(data) {
			return nil, errors.New("truncated index")
		}
		kl := int(binary.LittleEndian.Uint32(data[c : c+4]))
		c += 4
		if c+kl+indexEntryFixedLen-4 > len(data) {
			return nil, errors.New("truncated index")
		}
		key := string(data[c : c+kl])
		c += kl

		var e IndexEntry
		copy(e.Hash[:], data[c:c+32])
		c += 32
		copy(e.ContentHash[:], data[c:c+32])
		c += 32
		e.Size = int64(binary.LittleEndian.Uint64(data[c : c+8]))
		c += 8
		e.Mode = binary.LittleEndian.Uint32(data[c : c+4])
		c += 4
		e.Lazy = data[c] != 0
		c++
		e.Mtime = int64(binary.LittleEndian.Uint64(data[c : c+8]))
		c += 8
		e.OSS = data[c]
		c++

		idx.Entries[key] = e
	}
	return idx, nil
}
