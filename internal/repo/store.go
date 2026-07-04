package repo

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhsoft88/lo/internal/core"
)

// StoreObject serializes, compresses, and writes an object to the content-addressable store.
// Returns the SHA256 hash of the uncompressed content (before the header/compression).
func (r *Repository) StoreObject(objType core.ObjectType, content []byte) (core.Hash, error) {
	data, err := core.SerializeObject(objType, content)
	if err != nil {
		return core.Hash{}, fmt.Errorf("serialize object: %w", err)
	}

	h := core.HashFromBytes(data)
	objPath := r.objectPath(h)

	if err := os.MkdirAll(filepath.Dir(objPath), 0755); err != nil {
		return core.Hash{}, fmt.Errorf("create object dir: %w", err)
	}

	if err := ioutil.WriteFile(objPath, data, 0644); err != nil {
		return core.Hash{}, fmt.Errorf("write object: %w", err)
	}

	return h, nil
}

// LoadObject reads, decompresses, and deserializes an object from the store.
func (r *Repository) LoadObject(hash core.Hash) (core.ObjectType, []byte, error) {
	objPath := r.objectPath(hash)
	data, err := ioutil.ReadFile(objPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil, fmt.Errorf("object not found: %s", hash)
		}
		return 0, nil, fmt.Errorf("read object: %w", err)
	}

	objType, content, err := core.DeserializeObject(data)
	if err != nil {
		return 0, nil, fmt.Errorf("deserialize object: %w", err)
	}

	return objType, content, nil
}

// HasObject checks if an object exists in the store.
func (r *Repository) HasObject(hash core.Hash) bool {
	_, err := os.Stat(r.objectPath(hash))
	return err == nil
}

// FindObjectByPrefix resolves a short hex prefix to a full object hash.
// The prefix must be at least 2 characters. It first tries the git-style
// XX/YYYYYY lookup (prefix[:2] as directory), then falls back to scanning
// all object directories for the full prefix.
// Returns an error if no match or multiple matches are found.
func (r *Repository) FindObjectByPrefix(prefix string) (core.Hash, error) {
	if len(prefix) < 2 {
		return core.Hash{}, fmt.Errorf("hash prefix too short: %q", prefix)
	}
	if len(prefix) > core.HashSize*2 {
		return core.Hash{}, fmt.Errorf("hash prefix too long: %q", prefix)
	}

	// Try standard XX/YYYYYY lookup first
	if h, err := r.findObjectByDirPrefix(prefix); err == nil {
		return h, nil
	}

	// Fallback: scan all object directories
	objectsDir := r.ObjectsDir()
	dirs, err := ioutil.ReadDir(objectsDir)
	if err != nil {
		return core.Hash{}, fmt.Errorf("object not found: %s", prefix)
	}
	var match string
	for _, dir := range dirs {
		if !dir.IsDir() || len(dir.Name()) != 2 {
			continue
		}
		fullPrefix := dir.Name() + prefix
		if h, err := r.findObjectByDirPrefix(fullPrefix); err == nil {
			s := h.String()
			if match != "" {
				return core.Hash{}, fmt.Errorf("ambiguous hash prefix: %s", prefix)
			}
			match = s
		}
	}
	if match == "" {
		return core.Hash{}, fmt.Errorf("object not found: %s", prefix)
	}
	return core.HashFromHex(match)
}

// findObjectByDirPrefix resolves prefix using standard XX/YYYYYY layout.
func (r *Repository) findObjectByDirPrefix(prefix string) (core.Hash, error) {
	dir := filepath.Join(r.ObjectsDir(), prefix[:2])
	entries, err := ioutil.ReadDir(dir)
	if err != nil {
		return core.Hash{}, err
	}
	suffix := prefix[2:]
	var match string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, suffix) {
			if match != "" {
				return core.Hash{}, fmt.Errorf("ambiguous hash prefix: %s", prefix)
			}
			match = prefix[:2] + name
		}
	}
	if match == "" {
		return core.Hash{}, fmt.Errorf("no match in %s", prefix[:2])
	}
	return core.HashFromHex(match)
}

// objectPath returns the filesystem path for an object hash.
// Uses the git-style XX/YYYYYY layout: first two hex chars as directory.
func (r *Repository) objectPath(hash core.Hash) string {
	s := hash.String()
	return filepath.Join(r.ObjectsDir(), s[:2], s[2:])
}

// ObjectType reads the type of a stored object by peeking at the first
// byte of the uncompressed header without fully decompressing the content.
func (r *Repository) ObjectType(hash core.Hash) (core.ObjectType, error) {
	data, err := ioutil.ReadFile(r.objectPath(hash))
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, fmt.Errorf("empty object file")
	}

	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("decompress header: %w", err)
	}
	defer gr.Close()

	typeByte := make([]byte, 1)
	if _, err := io.ReadFull(gr, typeByte); err != nil {
		return 0, fmt.Errorf("read type byte: %w", err)
	}
	return core.ObjectType(typeByte[0]), nil
}
