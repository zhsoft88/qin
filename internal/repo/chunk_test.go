package repo

import (
	"bytes"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
)

func TestStoreAndLoadChunkedFile(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Use small chunk sizes for testing multiple chunks
	repo.Config.Core.ChunkMinSize = 1024
	repo.Config.Core.ChunkThreshold = 64 * 1024
	repo.Config.Core.ChunkMaxSize = 256 * 1024

	// Generate data large enough to create multiple chunks
	// Default config: avg 64KB, max 256KB, so 500KB will force multiple chunks
	data := make([]byte, 500000)
	for i := range data {
		data[i] = byte(i % 251)
	}

	h, err := repo.StoreChunkedFile(data)
	if err != nil {
		t.Fatal(err)
	}

	if h.IsZero() {
		t.Fatal("expected non-zero hash")
	}

	// Verify manifest exists and is correct type
	objType, content, err := repo.LoadObject(h)
	if err != nil {
		t.Fatal(err)
	}
	if objType != core.ObjectChunkManifest {
		t.Fatalf("expected chunk_manifest, got %s", objType)
	}

	var manifest ChunkManifest
	if err := core.DeserializeJSON(content, &manifest); err != nil {
		t.Fatal(err)
	}

	if manifest.Size != int64(len(data)) {
		t.Fatalf("expected size %d, got %d", len(data), manifest.Size)
	}

	if len(manifest.Chunks) < 2 {
		t.Fatalf("expected at least 2 chunks for 100KB file, got %d", len(manifest.Chunks))
	}

	// Verify reconstruction
	reconstructed, err := repo.LoadFileContent(h)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(reconstructed, data) {
		t.Fatal("reconstructed data does not match original")
	}
}

func TestStoreChunkedSmallFile(t *testing.T) {
	// Files smaller than min chunk should create a single chunk
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("small chunked file")
	h, err := repo.StoreChunkedFile(data)
	if err != nil {
		t.Fatal(err)
	}

	reconstructed, err := repo.LoadFileContent(h)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(reconstructed, data) {
		t.Fatal("reconstructed data mismatch")
	}
}

func TestAddFileAutoChunksLarge(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	repo.Config.Core.ChunkMinSize = 1024
	repo.Config.Core.ChunkThreshold = 4096
	repo.Config.Core.ChunkMaxSize = 8192

		// Create a file > chunk min size
	data := make([]byte, 10000)
	for i := range data {
		data[i] = byte(i * 7)
	}

	testFile := filepath.Join(dir, "large.bin")
	if err := ioutil.WriteFile(testFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	if err := repo.AddFile(testFile); err != nil {
		t.Fatal(err)
	}

	files, err := repo.ListFiles()
	if err != nil {
		t.Fatal(err)
	}

	entry, ok := files["large.bin"]
	if !ok {
		t.Fatal("expected large.bin in index")
	}

	// The hash should point to a ChunkManifest, not a blob
	objType, _, err := repo.LoadObject(entry.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if objType != core.ObjectChunkManifest {
		t.Fatalf("expected chunk_manifest for large file, got %s", objType)
	}

	// Verify reconstruction from index
	reconstructed, err := repo.LoadChunkedFile(entry.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reconstructed, data) {
		t.Fatal("reconstructed data does not match original")
	}
}

func TestAddFileSmallSkipsChunking(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(dir, "small.txt")
	if err := ioutil.WriteFile(testFile, []byte("small"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := repo.AddFile(testFile); err != nil {
		t.Fatal(err)
	}

	files, err := repo.ListFiles()
	if err != nil {
		t.Fatal(err)
	}

	entry := files["small.txt"]
	objType, _, err := repo.LoadObject(entry.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if objType != core.ObjectBlob {
		t.Fatalf("expected blob for small file, got %s", objType)
	}
}

func TestChunkedFileRoundTripWithModification(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	repo.Config.Core.ChunkMinSize = 128
	repo.Config.Core.ChunkThreshold = 512
	repo.Config.Core.ChunkMaxSize = 1024

	// Original large file
	data := make([]byte, 5000)
	for i := range data {
		data[i] = byte(i % 251)
	}

	f1 := filepath.Join(dir, "data.bin")
	if err := ioutil.WriteFile(f1, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddFile(f1); err != nil {
		t.Fatal(err)
	}

	// Load chunks from first version
	files, _ := repo.ListFiles()
	manifestHash1 := files["data.bin"].Hash
	manifest1, _ := repo.LoadFileContent(manifestHash1)

	// Modify the file (append)
	modified := append(data, []byte("some new content at the end")...)
	if err := ioutil.WriteFile(f1, modified, 0644); err != nil {
		t.Fatal(err)
	}
	repo.RemoveFile("data.bin")
	if err := repo.AddFile(f1); err != nil {
		t.Fatal(err)
	}

	files2, _ := repo.ListFiles()
	manifestHash2 := files2["data.bin"].Hash

	// Verify content
	reconstructed, err := repo.LoadChunkedFile(manifestHash2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reconstructed, modified) {
		t.Fatal("modified file content mismatch")
	}

	// With CDC, the first chunks may still be shared
	t.Logf("original manifest: %s, modified manifest: %s", manifestHash1.Short(), manifestHash2.Short())
	t.Logf("original data size: %d, from manifest: %d", len(manifest1), len(modified))
}
