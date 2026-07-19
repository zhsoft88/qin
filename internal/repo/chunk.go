package repo

import (
	"fmt"

	"github.com/zhsoft88/qin/internal/core"
)

// ChunkRef references a single chunk within a chunked file.
type ChunkRef struct {
	Hash   core.Hash `json:"hash"`
	Size   int       `json:"size"`
	Offset int64     `json:"offset"`
}

// ChunkManifest maps a logical large file to its content-defined chunks.
type ChunkManifest struct {
	Size   int64      `json:"size"`   // total original file size
	Chunks []ChunkRef `json:"chunks"` // ordered chunk list
}

// StoreChunkedFile splits data into chunks, stores each as an ObjectBlob,
// then stores and returns a ChunkManifest hash. If the data fits in a single
// chunk, it stores it as a plain blob instead.
func (r *Repository) StoreChunkedFile(data []byte) (core.Hash, error) {
	cfg := r.Config.Core
	chunker := core.NewChunker(cfg.ChunkMinSize, cfg.ChunkThreshold, cfg.ChunkMaxSize)
	rawChunks := chunker.Chunks(data)

	if len(rawChunks) == 1 {
		// Single chunk — store as plain blob
		return r.StoreObject(core.ObjectBlob, data)
	}

	chunks := make([]ChunkRef, 0, len(rawChunks))
	var offset int64

	for _, chunk := range rawChunks {
		h, err := r.StoreObject(core.ObjectBlob, chunk)
		if err != nil {
			return core.Hash{}, fmt.Errorf("store chunk: %w", err)
		}
		chunks = append(chunks, ChunkRef{
			Hash:   h,
			Size:   len(chunk),
			Offset: offset,
		})
		offset += int64(len(chunk))
	}

	manifest := ChunkManifest{
		Size:   offset,
		Chunks: chunks,
	}

	content, err := core.SerializeJSON(manifest)
	if err != nil {
		return core.Hash{}, fmt.Errorf("serialize manifest: %w", err)
	}

	h, err := r.StoreObject(core.ObjectChunkManifest, content)
	if err != nil {
		return core.Hash{}, fmt.Errorf("store manifest: %w", err)
	}

	return h, nil
}

// LoadChunkManifest loads and deserializes a chunk manifest object.
func (r *Repository) LoadChunkManifest(hash core.Hash) (*ChunkManifest, error) {
	objType, content, err := r.LoadObject(hash)
	if err != nil {
		return nil, err
	}
	if objType != core.ObjectChunkManifest {
		return nil, fmt.Errorf("not a chunk manifest: %s", objType)
	}

	var manifest ChunkManifest
	if err := core.DeserializeJSON(content, &manifest); err != nil {
		return nil, fmt.Errorf("deserialize manifest: %w", err)
	}

	return &manifest, nil
}

// LoadChunkedFile reads a ChunkManifest object and reconstructs the original data.
func (r *Repository) LoadChunkedFile(manifestHash core.Hash) ([]byte, error) {
	manifest, err := r.LoadChunkManifest(manifestHash)
	if err != nil {
		return nil, err
	}

	data := make([]byte, 0, manifest.Size)
	for _, ref := range manifest.Chunks {
		_, chunkData, err := r.LoadObject(ref.Hash)
		if err != nil {
			return nil, fmt.Errorf("load chunk at offset %d: %w", ref.Offset, err)
		}
		data = append(data, chunkData...)
	}

	return data, nil
}

// LoadFileContent returns the raw file content for a hash, whether stored as
// a plain blob or a chunked file (ChunkManifest).
func (r *Repository) LoadFileContent(hash core.Hash) ([]byte, error) {
	objType, data, err := r.LoadObject(hash)
	if err != nil {
		return nil, err
	}
	if objType == core.ObjectChunkManifest {
		return r.LoadChunkedFile(hash)
	}
	return data, nil
}
