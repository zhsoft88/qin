package core

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
)

// Version is the current application version.
const Version = "0.1.0"

// Name is what the program calls itself in usage text, error messages and
// --version. It is the single definition: the CLI reads it rather than spelling
// the name out, so a rename cannot leave the banner and the messages disagreeing.
// The legacy name "lo" survives only where it is data rather than display — the
// "lo-lfs" placeholder written into lazily cloned files, which is a format value
// and not a label.
const Name = "qin"

type ObjectType uint8

const (
	ObjectBlob          ObjectType = iota + 1 // 1 - raw content or chunk data
	ObjectTree                                // 2 - directory tree
	ObjectCommit                              // 3 - commit snapshot
	ObjectChunkManifest                       // 4 - large file chunk mapping
)

func (t ObjectType) String() string {
	switch t {
	case ObjectBlob:
		return "blob"
	case ObjectTree:
		return "tree"
	case ObjectCommit:
		return "commit"
	case ObjectChunkManifest:
		return "chunk_manifest"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}

// SerializeObject serializes an object with header and compresses it.
// Format: header = type(1 byte) + varint content_size, body = JSON(content)
// On disk: gzip(header + body)
func SerializeObject(objType ObjectType, content []byte) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte(byte(objType))

	sizeBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(sizeBuf, uint64(len(content)))
	buf.Write(sizeBuf[:n])
	buf.Write(content)

	var compressed bytes.Buffer
	w, err := gzip.NewWriterLevel(&compressed, gzip.DefaultCompression)
	if err != nil {
		return nil, fmt.Errorf("create gzip writer: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return nil, fmt.Errorf("compress object: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close gzip writer: %w", err)
	}
	return compressed.Bytes(), nil
}

// DeserializeObject decompresses and parses object header.
func DeserializeObject(data []byte) (ObjectType, []byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return 0, nil, fmt.Errorf("create gzip reader: %w", err)
	}
	defer gr.Close()

	decompressed, err := ioutil.ReadAll(gr)
	if err != nil {
		return 0, nil, fmt.Errorf("decompress object: %w", err)
	}

	br := bytes.NewReader(decompressed)

	typeByte, err := br.ReadByte()
	if err != nil {
		return 0, nil, fmt.Errorf("read object type: %w", err)
	}
	objType := ObjectType(typeByte)

	contentSize, err := binary.ReadUvarint(br)
	if err != nil {
		return 0, nil, fmt.Errorf("read content size: %w", err)
	}

	content := make([]byte, contentSize)
	if _, err := io.ReadFull(br, content); err != nil {
		return 0, nil, fmt.Errorf("read object content: %w", err)
	}

	return objType, content, nil
}

// SerializeJSON is a helper to marshal content for storage.
func SerializeJSON(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// DeserializeJSON is a helper to unmarshal stored content.
func DeserializeJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
