package repo

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/zhsoft88/qin/internal/core"
)

// GCReport summarizes what gc pruned.
type GCReport struct {
	Pruned int
	Freed  int64 // bytes
}

// GC prunes dangling objects (not reachable from any ref).
func (r *Repository) GC() (*GCReport, error) {
	reachable := make(map[core.Hash]bool)

	// Mark all objects reachable from refs (branches, tags, stash, etc.)
	if err := r.markReachableRefsFull(reachable); err != nil {
		return nil, err
	}

	// Also mark HEAD if detached (points directly to a commit)
	headRef, err := r.ReadHEAD()
	if err == nil && len(headRef) == 64 {
		h, err := core.HashFromHex(headRef)
		if err == nil {
			r.markReachableObject(h, reachable)
		}
	}

	// Enumerate all objects in the store
	allObjects, err := r.enumerateAllObjects()
	if err != nil {
		return nil, err
	}

	var pruned int
	var freed int64
	for _, h := range allObjects {
		if reachable[h] {
			continue
		}
		path := r.objectPath(h)
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		if err := os.Remove(path); err != nil {
			continue
		}
		pruned++
		freed += fi.Size()
	}

	return &GCReport{Pruned: pruned, Freed: freed}, nil
}

// markReachableRefsFull walks all ref files and marks reachable objects.
func (r *Repository) markReachableRefsFull(reachable map[core.Hash]bool) error {
	refsDir := r.RefsDir()
	return filepath.Walk(refsDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		data, err := ioutil.ReadFile(path)
		if err != nil {
			return nil
		}
		hashStr := string(data)
		if len(hashStr) > 0 && hashStr[len(hashStr)-1] == '\n' {
			hashStr = hashStr[:len(hashStr)-1]
		}
		h, err := core.HashFromHex(hashStr)
		if err != nil {
			return nil
		}
		r.markReachableObject(h, reachable)
		return nil
	})
}

// markReachableObject recursively marks a hash and all objects it references.
func (r *Repository) markReachableObject(hash core.Hash, reachable map[core.Hash]bool) {
	if reachable[hash] {
		return
	}
	objType, err := r.ObjectType(hash)
	if err != nil {
		return
	}
	reachable[hash] = true

	switch objType {
	case core.ObjectCommit:
		commit, err := r.LoadCommit(hash)
		if err != nil {
			return
		}
		r.markReachableObject(commit.Tree, reachable)
		for _, parent := range commit.Parents {
			r.markReachableObject(parent, reachable)
		}
	case core.ObjectTree:
		tree, err := r.LoadTree(hash)
		if err != nil {
			return
		}
		for _, entry := range tree.Entries {
			if IsSubmoduleMode(entry.Mode) {
				continue // submodule hash is in external repo
			}
			r.markReachableObject(entry.Hash, reachable)
		}
	// ObjectBlob and ObjectChunkManifest are leaf nodes
	}
}

// enumerateAllObjects returns all object hashes in the store.
func (r *Repository) enumerateAllObjects() ([]core.Hash, error) {
	objectsDir := r.ObjectsDir()
	dirEntries, err := ioutil.ReadDir(objectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read objects dir: %w", err)
	}

	var objects []core.Hash
	for _, dirEntry := range dirEntries {
		if !dirEntry.IsDir() || len(dirEntry.Name()) != 2 {
			continue
		}
		subDir := filepath.Join(objectsDir, dirEntry.Name())
		files, err := ioutil.ReadDir(subDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			fullHex := dirEntry.Name() + f.Name()
			h, err := core.HashFromHex(fullHex)
			if err != nil {
				continue
			}
			objects = append(objects, h)
		}
	}
	return objects, nil
}
