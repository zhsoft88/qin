package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/zhsoft88/lo/internal/core"
)

// MergeResult describes the outcome of a merge.
type MergeResult struct {
	FastForward bool     // was this a fast-forward merge?
	Merged      bool     // was a merge commit created?
	Diff        *Diff    // changes between original HEAD and result
	Conflicts   []string // conflicted file paths
}

// FindMergeBase finds the common ancestor of two commits using BFS.
func (r *Repository) FindMergeBase(a, b core.Hash) (core.Hash, error) {
	// Collect all ancestors of a
	ancestors := make(map[core.Hash]bool)
	var walk func(h core.Hash)
	walk = func(h core.Hash) {
		if ancestors[h] || h.IsZero() {
			return
		}
		ancestors[h] = true
		commit, err := r.LoadCommit(h)
		if err != nil {
			return
		}
		for _, p := range commit.Parents {
			walk(p)
		}
	}
	walk(a)

	// BFS from b to find first ancestor also in a's ancestry
	visited := make(map[core.Hash]bool)
	queue := []core.Hash{b}
	visited[b] = true

	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]

		if ancestors[h] {
			return h, nil
		}

		commit, err := r.LoadCommit(h)
		if err != nil {
			return core.Hash{}, err
		}
		for _, p := range commit.Parents {
			if !visited[p] && !p.IsZero() {
				visited[p] = true
				queue = append(queue, p)
			}
		}
	}

	return core.Hash{}, fmt.Errorf("no common ancestor found")
}

// Merge merges the given branch into the current branch.
func (r *Repository) Merge(branch string) (*MergeResult, error) {
	targetHashStr, err := r.ReadRef("refs/heads/" + branch)
	if err != nil {
		return nil, fmt.Errorf("branch not found: %s", branch)
	}
	target, err := core.HashFromHex(targetHashStr)
	if err != nil {
		return nil, err
	}
	return r.mergeCommit(target, branch)
}

// mergeCommit performs a merge of the given target commit into the current HEAD.
// label is used in the merge commit message (e.g., branch name or "origin/main").
func (r *Repository) mergeCommit(target core.Hash, label string) (*MergeResult, error) {
	// Resolve HEAD
	headHashStr, err := r.ResolveHEAD()
	if err != nil || headHashStr == "" {
		return nil, fmt.Errorf("nothing to merge")
	}
	head, err := core.HashFromHex(headHashStr)
	if err != nil {
		return nil, err
	}

	// Find merge base
	base, err := r.FindMergeBase(head, target)
	if err != nil {
		return nil, fmt.Errorf("find merge base: %w", err)
	}

	if base == target {
		return &MergeResult{
			FastForward: false,
			Merged:      false,
			Conflicts:   nil,
		}, fmt.Errorf("already up to date")
	}

	if base == head {
		// Fast-forward: move HEAD to target
		return r.fastForwardMerge(head, target)
	}

	// Three-way merge
	return r.threeWayMerge(label, head, target, base)
}

func (r *Repository) fastForwardMerge(head, target core.Hash) (*MergeResult, error) {
	diff, err := r.DiffCommits(head, target)
	if err != nil {
		return nil, fmt.Errorf("compute diff: %w", err)
	}

	if err := r.restoreCommit(target); err != nil {
		return nil, fmt.Errorf("restore target: %w", err)
	}

	// Update branch ref
	cur := r.CurrentBranch()
	if cur != "" {
		if err := r.WriteRef("refs/heads/"+cur, target.String()); err != nil {
			return nil, fmt.Errorf("update ref: %w", err)
		}
	} else {
		if err := r.SetHEAD(target.String()); err != nil {
			return nil, fmt.Errorf("update HEAD: %w", err)
		}
	}

	return &MergeResult{
		FastForward: true,
		Merged:      true,
		Diff:        diff,
	}, nil
}

func (r *Repository) threeWayMerge(label string, head, target, base core.Hash) (*MergeResult, error) {
	// Load trees
	baseTree, err := r.commitTreeMap(base)
	if err != nil {
		return nil, fmt.Errorf("load base tree: %w", err)
	}
	oursTree, err := r.commitTreeMap(head)
	if err != nil {
		return nil, fmt.Errorf("load ours tree: %w", err)
	}
	theirsTree, err := r.commitTreeMap(target)
	if err != nil {
		return nil, fmt.Errorf("load theirs tree: %w", err)
	}

	// Collect all file names
	allNames := make(map[string]bool)
	for name := range baseTree {
		allNames[name] = true
	}
	for name := range oursTree {
		allNames[name] = true
	}
	for name := range theirsTree {
		allNames[name] = true
	}

	mergedEntries := make(map[string]TreeEntry)
	var conflicts []string

	for name := range allNames {
		baseEntry, inBase := baseTree[name]
		oursEntry, inOurs := oursTree[name]
		theirsEntry, inTheirs := theirsTree[name]

		switch {
		case !inOurs && !inTheirs:
			// Not in either side anymore
			continue

		case !inOurs && inTheirs:
			// Deleted in ours, exists in theirs
			if !inBase {
				// Added in theirs only → take theirs
				mergedEntries[name] = theirsEntry
			} else {
				// Deleted in ours, but theirs has it → conflict?
				// If base and theirs are same, ours deleted it → conflict
				if inBase && baseEntry.Hash == theirsEntry.Hash {
					// Ours deleted, theirs unchanged → keep deleted
					continue
				}
				// Both sides changed it differently → conflict
				conflicts = append(conflicts, name)
			}

		case inOurs && !inTheirs:
			// Deleted in theirs, exists in ours
			if !inBase {
				// Added in ours only → keep ours
				mergedEntries[name] = oursEntry
			} else {
				// The changed it → conflict
				if inBase && baseEntry.Hash == oursEntry.Hash {
					// The unchanged, ours kept → conflict (theirs deleted)
					continue
				}
				conflicts = append(conflicts, name)
			}

		case inBase && baseEntry.Hash == oursEntry.Hash && baseEntry.Hash == theirsEntry.Hash:
			// All same
			mergedEntries[name] = oursEntry

		case inBase && baseEntry.Hash == oursEntry.Hash:
			// Only theirs changed
			mergedEntries[name] = theirsEntry

		case inBase && baseEntry.Hash == theirsEntry.Hash:
			// Only ours changed
			mergedEntries[name] = oursEntry

		case oursEntry.Hash == theirsEntry.Hash:
			// Both changed to the same thing
			mergedEntries[name] = oursEntry

		default:
			// Both changed differently → conflict
			conflicts = append(conflicts, name)
		}
	}

	if len(conflicts) > 0 {
		// Write our and their versions to working tree for resolution
		for _, name := range conflicts {
			oursEntry, _ := oursTree[name]
			theirsEntry, _ := theirsTree[name]
			baseEntry, _ := baseTree[name]
			cleanPath, osTag := parseKey(name)
			osSuffix := ""
			if osTag != 0 {
				osSuffix = "__" + OSName(osTag)
			}

			// Write OURS version
			if oursEntry.Hash.IsZero() {
				// Ours deleted it → write removal marker
				fullPath := filepath.Join(r.Path, cleanPath)
				os.Remove(fullPath)
			} else {
				if err := r.writeFileFromEntry(cleanPath, oursEntry); err != nil {
					return nil, fmt.Errorf("write ours %s: %w", cleanPath, err)
				}
			}

			// Write THEIRS version
			if !theirsEntry.Hash.IsZero() {
				theirPath := cleanPath + osSuffix + ".theirs"
				if err := r.writeFileFromEntry(theirPath, theirsEntry); err != nil {
					return nil, fmt.Errorf("write theirs %s: %w", cleanPath, err)
				}
			}
			if !baseEntry.Hash.IsZero() {
				basePath := cleanPath + osSuffix + ".base"
				if err := r.writeFileFromEntry(basePath, baseEntry); err != nil {
					return nil, fmt.Errorf("write base %s: %w", cleanPath, err)
				}
			}
			// Add ours version to index so the file is there
			if !oursEntry.Hash.IsZero() {
				mergedEntries[name] = oursEntry
			}
		}

		// Save index with merged entries (without conflict markers)
		// so user can see what we have
	}

	// Build new index from merged entries
	newIndex := &Index{Entries: make(map[string]IndexEntry, len(mergedEntries))}
	for name, entry := range mergedEntries {
		// Submodule entry: create directory, add to index, skip content
		if IsSubmoduleMode(entry.Mode) {
			fullPath := filepath.Join(r.Path, name)
			if err := os.MkdirAll(fullPath, 0755); err != nil {
				return nil, fmt.Errorf("create submodule dir %s: %w", name, err)
			}
			newIndex.Entries[name] = IndexEntry{
				Hash: entry.Hash,
				Size: 0,
				Mode: entry.Mode,
				OSS:    entry.OSS,
			}
			continue
		}

		objType, _, err := r.LoadObject(entry.Hash)
		if err != nil {
			return nil, fmt.Errorf("load object %s: %w", name, err)
		}

		var fileData []byte
		if objType == core.ObjectChunkManifest {
			fileData, err = r.LoadChunkedFile(entry.Hash)
			if err != nil {
				return nil, fmt.Errorf("load chunked file %s: %w", name, err)
			}
		} else {
			_, blobData, err := r.LoadObject(entry.Hash)
			if err != nil {
				return nil, fmt.Errorf("load blob %s: %w", name, err)
			}
			fileData = blobData
		}

		fullPath := filepath.Join(r.Path, name)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return nil, fmt.Errorf("create directory for %s: %w", name, err)
		}
		if err := writeFileFromEntry(fullPath, fileData, entry.Mode); err != nil {
			return nil, fmt.Errorf("write %s: %w", name, err)
		}

		newIndex.Entries[name] = IndexEntry{
			Hash:        entry.Hash,
			ContentHash: core.HashFromBytes(fileData),
			Size:        entry.Size,
			Mode:        entry.Mode,
		}
	}

	if err := r.SaveIndex(newIndex); err != nil {
		return nil, fmt.Errorf("save index: %w", err)
	}

	if len(conflicts) > 0 {
		return &MergeResult{
			Conflicts: conflicts,
		}, fmt.Errorf("merge conflicts in: %v", conflicts)
	}

	// No conflicts: create merge commit
	treeHash, err := r.buildTreeFromEntries(mergedEntries)
	if err != nil {
		return nil, fmt.Errorf("build merged tree: %w", err)
	}

	commit := Commit{
		Tree:    treeHash,
		Parents: []core.Hash{head, target},
		Author:  "Merge <merge>",
		Message: fmt.Sprintf("Merge branch '%s'", label),
		Time:    time.Now(),
	}

	content, err := core.SerializeJSON(commit)
	if err != nil {
		return nil, fmt.Errorf("serialize commit: %w", err)
	}

	commitHash, err := r.StoreObject(core.ObjectCommit, content)
	if err != nil {
		return nil, fmt.Errorf("store commit: %w", err)
	}

	// Update branch ref
	cur := r.CurrentBranch()
	if cur != "" {
		if err := r.WriteRef("refs/heads/"+cur, commitHash.String()); err != nil {
			return nil, fmt.Errorf("update ref: %w", err)
		}
	} else {
		if err := r.SetHEAD(commitHash.String()); err != nil {
			return nil, fmt.Errorf("update HEAD: %w", err)
		}
	}

	diff, err := r.DiffCommits(head, commitHash)
	if err != nil {
		return nil, fmt.Errorf("compute diff: %w", err)
	}

	return &MergeResult{
		FastForward: false,
		Merged:      true,
		Diff:        diff,
	}, nil
}

func (r *Repository) commitTreeMap(hash core.Hash) (map[string]TreeEntry, error) {
	commit, err := r.LoadCommit(hash)
	if err != nil {
		return nil, err
	}
	tree, err := r.LoadTree(commit.Tree)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]TreeEntry, len(tree.Entries))
	for _, e := range tree.Entries {
		entries[entryKey(e.Name, osIDForKey(e.OSS))] = e
	}
	return entries, nil
}

func (r *Repository) buildTreeFromEntries(entries map[string]TreeEntry) (core.Hash, error) {
	entryList := make([]TreeEntry, 0, len(entries))
	for key, entry := range entries {
		path, _ := parseKey(key)
		entry.Name = path
		// OSS carries OS info; no single OS field on TreeEntry
		entryList = append(entryList, entry)
	}
	sort.Slice(entryList, func(i, j int) bool {
		if entryList[i].Name != entryList[j].Name {
			return entryList[i].Name < entryList[j].Name
		}
		return osIDForKey(entryList[i].OSS) < osIDForKey(entryList[j].OSS)
	})
	tree := &Tree{Entries: entryList}
	content, err := core.SerializeJSON(tree)
	if err != nil {
		return core.Hash{}, fmt.Errorf("serialize tree: %w", err)
	}
	return r.StoreObject(core.ObjectTree, content)
}

func (r *Repository) writeFileFromEntry(name string, entry TreeEntry) error {
	fullPath := filepath.Join(r.Path, name)

	// Submodule entry: just create the directory
	if IsSubmoduleMode(entry.Mode) {
		return os.MkdirAll(fullPath, 0755)
	}

	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}

	objType, _, err := r.LoadObject(entry.Hash)
	if err != nil {
		return err
	}

	var fileData []byte
	if objType == core.ObjectChunkManifest {
		fileData, err = r.LoadChunkedFile(entry.Hash)
		if err != nil {
			return err
		}
	} else {
		_, blobData, err := r.LoadObject(entry.Hash)
		if err != nil {
			return err
		}
		fileData = blobData
	}

	return writeFileFromEntry(fullPath, fileData, entry.Mode)
}
