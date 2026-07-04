package repo

import (
	"fmt"
	"sort"
	"time"

	"github.com/zhsoft88/lo/internal/core"
)

// TreeEntry is a single file entry in a tree snapshot.
type TreeEntry struct {
	Name string    `json:"name"` // repo-relative path, forward slashes
	Hash core.Hash `json:"hash"`
	Size int64     `json:"size"`
	Mode uint32    `json:"mode"`
		OSS []uint8     // list of OS IDs the entry applies to; empty = all OSes
}

// Tree is a directory snapshot — an ordered list of file entries.
type Tree struct {
	Entries []TreeEntry `json:"entries"`
}

// BuildTree builds a tree object from the staged index entries.
func (r *Repository) BuildTree() (*Tree, error) {
	files, err := r.ListFiles()
	if err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("nothing staged")
	}

	entries := make([]TreeEntry, 0, len(files))
	for key, entry := range files {
		path, _ := parseKey(key)
		entries = append(entries, TreeEntry{
			Name:   path,
			Hash:   entry.Hash,
			Size:   entry.Size,
			Mode:   entry.Mode,
			OSS:    entry.OSS,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return &Tree{Entries: entries}, nil
}

// WriteTree serializes the staged index into a tree object and stores it.
// Returns the tree object hash.
func (r *Repository) WriteTree() (core.Hash, error) {
	tree, err := r.BuildTree()
	if err != nil {
		return core.Hash{}, err
	}

	content, err := core.SerializeJSON(tree)
	if err != nil {
		return core.Hash{}, fmt.Errorf("serialize tree: %w", err)
	}

	h, err := r.StoreObject(core.ObjectTree, content)
	if err != nil {
		return core.Hash{}, fmt.Errorf("store tree: %w", err)
	}

	return h, nil
}

// LoadTree reads and deserializes a tree object from the store.
func (r *Repository) LoadTree(hash core.Hash) (*Tree, error) {
	objType, content, err := r.LoadObject(hash)
	if err != nil {
		return nil, err
	}
	if objType != core.ObjectTree {
		return nil, fmt.Errorf("not a tree object: %s", objType)
	}

	var tree Tree
	if err := core.DeserializeJSON(content, &tree); err != nil {
		return nil, fmt.Errorf("deserialize tree: %w", err)
	}
	return &tree, nil
}

// Commit represents a snapshot in the repository.
type Commit struct {
	Tree    core.Hash   `json:"tree"`
	Parents []core.Hash `json:"parents,omitempty"`
	Author  string      `json:"author"`
	Message string      `json:"message"`
	Time    time.Time   `json:"time"`
}

// WriteCommit creates and stores a commit from the staged index.
// Uses the current HEAD as the parent commit.
func (r *Repository) WriteCommit(author, message string) (core.Hash, error) {
	treeHash, err := r.WriteTree()
	if err != nil {
		return core.Hash{}, err
	}

	var parents []core.Hash
	headHash, err := r.ResolveHEAD()
	if err != nil {
		return core.Hash{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	if headHash != "" {
		parent, err := core.HashFromHex(headHash)
		if err != nil {
			return core.Hash{}, fmt.Errorf("parse parent hash: %w", err)
		}
		parents = append(parents, parent)
	}

	commit := Commit{
		Tree:    treeHash,
		Parents: parents,
		Author:  author,
		Message: message,
		Time:    time.Now(),
	}

	content, err := core.SerializeJSON(commit)
	if err != nil {
		return core.Hash{}, fmt.Errorf("serialize commit: %w", err)
	}

	commitHash, err := r.StoreObject(core.ObjectCommit, content)
	if err != nil {
		return core.Hash{}, fmt.Errorf("store commit: %w", err)
	}

	// Update HEAD and branch ref
	head, err := r.ReadHEAD()
	if err != nil {
		return core.Hash{}, fmt.Errorf("read HEAD: %w", err)
	}

	if len(head) > 5 && head[:4] == "ref:" {
		ref := head[5:]
		if err := r.WriteRef(ref, commitHash.String()); err != nil {
			return core.Hash{}, fmt.Errorf("update ref: %w", err)
		}
	} else {
		if err := r.SetHEAD(commitHash.String()); err != nil {
			return core.Hash{}, fmt.Errorf("update HEAD: %w", err)
		}
	}

	return commitHash, nil
}

// LoadCommit reads and deserializes a commit object from the store.
func (r *Repository) LoadCommit(hash core.Hash) (*Commit, error) {
	objType, content, err := r.LoadObject(hash)
	if err != nil {
		return nil, err
	}
	if objType != core.ObjectCommit {
		return nil, fmt.Errorf("not a commit object: %s", objType)
	}

	var commit Commit
	if err := core.DeserializeJSON(content, &commit); err != nil {
		return nil, fmt.Errorf("deserialize commit: %w", err)
	}
	return &commit, nil
}
