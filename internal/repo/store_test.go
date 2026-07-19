package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
)

func TestStoreRoundTrip(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	content := []byte(`{"message": "hello world"}`)
	h, err := repo.StoreObject(core.ObjectBlob, content)
	if err != nil {
		t.Fatal(err)
	}

	if h.IsZero() {
		t.Fatal("expected non-zero hash")
	}

	objType, loaded, err := repo.LoadObject(h)
	if err != nil {
		t.Fatal(err)
	}

	if objType != core.ObjectBlob {
		t.Fatalf("expected blob, got %s", objType)
	}

	if string(loaded) != string(content) {
		t.Fatalf("content mismatch: got %s, want %s", loaded, content)
	}

	if !repo.HasObject(h) {
		t.Fatal("expected HasObject to be true")
	}

	// Verify file layout: .lo/objects/XX/YYYYYY
	objPath := filepath.Join(repo.ObjectsDir(), h.String()[:2], h.String()[2:])
	if _, err := os.Stat(objPath); err != nil {
		t.Fatalf("object file not found at %s: %v", objPath, err)
	}
}

func TestStoreLoadNonExistent(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	var h core.Hash
	h[0] = 0x01
	_, _, err = repo.LoadObject(h)
	if err == nil {
		t.Fatal("expected error for non-existent object")
	}

	if repo.HasObject(h) {
		t.Fatal("expected HasObject to be false for non-existent object")
	}
}

func TestStoreAllObjectTypes(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	types := []core.ObjectType{core.ObjectBlob, core.ObjectTree, core.ObjectCommit, core.ObjectChunkManifest}
	for _, objType := range types {
		content := []byte(`{"type": "` + objType.String() + `"}`)
		h, err := repo.StoreObject(objType, content)
		if err != nil {
			t.Fatalf("store %s: %v", objType, err)
		}

		loadedType, loaded, err := repo.LoadObject(h)
		if err != nil {
			t.Fatalf("load %s: %v", objType, err)
		}

		if loadedType != objType {
			t.Fatalf("type mismatch: expected %s, got %s", objType, loadedType)
		}

		if string(loaded) != string(content) {
			t.Fatalf("content mismatch for %s", objType)
		}
	}
}
