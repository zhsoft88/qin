package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

// Rebase rewinds the current branch's commits and replays them
// on top of the given branch.
func (r *Repository) Rebase(branch string) error {
	// Resolve target branch
	targetHashStr, err := r.ReadRef("refs/heads/" + branch)
	if err != nil {
		return fmt.Errorf("branch not found: %s", branch)
	}
	target, err := core.HashFromHex(targetHashStr)
	if err != nil {
		return err
	}

	// Resolve HEAD
	headHashStr, err := r.ResolveHEAD()
	if err != nil || headHashStr == "" {
		return fmt.Errorf("nothing to rebase")
	}
	head, err := core.HashFromHex(headHashStr)
	if err != nil {
		return err
	}

	// Find merge base
	base, err := r.FindMergeBase(head, target)
	if err != nil {
		return fmt.Errorf("find merge base: %w", err)
	}

	if base == head {
		return fmt.Errorf("already up to date")
	}

	// Collect commits from HEAD down to (not including) merge base
	var commits []core.Hash
	for h := head; !h.IsZero() && h != base; {
		commits = append(commits, h)
		c, err := r.LoadCommit(h)
		if err != nil {
			break
		}
		if len(c.Parents) == 0 {
			break
		}
		h = c.Parents[0]
	}

	// Reverse to chronological order
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}

	if len(commits) == 0 {
		return fmt.Errorf("no commits to rebase")
	}

	curBranch := r.CurrentBranch()

	// Checkout target (restore working tree and index to target state)
	if err := r.restoreCommit(target); err != nil {
		return fmt.Errorf("checkout target: %w", err)
	}

	// Point HEAD at target
	if curBranch != "" {
		r.WriteRef("refs/heads/"+curBranch, target.String())
	} else {
		r.SetHEAD(target.String())
	}

	// Apply each commit on top of the new base
	for _, h := range commits {
		commit, err := r.LoadCommit(h)
		if err != nil {
			return fmt.Errorf("load commit: %w", err)
		}

		commitTree, err := r.commitTreeMap(h)
		if err != nil {
			return err
		}

		parentTree := make(map[string]TreeEntry)
		if !commit.Parents[0].IsZero() {
			pt, err := r.commitTreeMap(commit.Parents[0])
			if err == nil {
				parentTree = pt
			}
		}

		// Apply changes to working tree and index
		if err := r.applyCommitChanges(parentTree, commitTree); err != nil {
			return fmt.Errorf("apply commit %s: %w", h.Short(), err)
		}

		// Build new tree from updated index
		treeHash, err := r.WriteTree()
		if err != nil {
			return fmt.Errorf("build tree: %w", err)
		}

		// Get current HEAD as parent
		curHeadStr, _ := r.ResolveHEAD()
		curHead, _ := core.HashFromHex(curHeadStr)

		newCommit := Commit{
			Tree:    treeHash,
			Parents: []core.Hash{curHead},
			Author:  commit.Author,
			Message: commit.Message,
			Time:    time.Now(),
		}
		content, err := core.SerializeJSON(newCommit)
		if err != nil {
			return err
		}
		newHash, err := r.StoreObject(core.ObjectCommit, content)
		if err != nil {
			return err
		}

		// Update branch/HEAD ref
		if curBranch != "" {
			r.WriteRef("refs/heads/"+curBranch, newHash.String())
		} else {
			r.SetHEAD(newHash.String())
		}
	}

	return nil
}

// applyCommitChanges transitions the working tree and index from parentTree
// to commitTree: writes added/modified files, removes deleted files.
func (r *Repository) applyCommitChanges(parentTree, commitTree map[string]TreeEntry) error {
	allKeys := make(map[string]bool)
	for k := range parentTree {
		allKeys[k] = true
	}
	for k := range commitTree {
		allKeys[k] = true
	}

	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	for key := range allKeys {
		newEntry, inNew := commitTree[key]
		oldEntry, inOld := parentTree[key]
		cleanPath, _ := parseKey(key)

		switch {
		case !inNew:
			// Deleted in commit — remove from working tree and index
			delete(idx.Entries, key)
			os.Remove(filepath.Join(r.Path, cleanPath))

		case !inOld || oldEntry.Hash != newEntry.Hash || oldEntry.Mode != newEntry.Mode:
			// Added or modified — write to working tree and update index
			fullPath := filepath.Join(r.Path, cleanPath)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", cleanPath, err)
			}

			objType, _, err := r.LoadObject(newEntry.Hash)
			if err != nil {
				return fmt.Errorf("load object %s: %w", newEntry.Hash.Short(), err)
			}

			var fileData []byte
			if objType == core.ObjectChunkManifest {
				fileData, err = r.LoadChunkedFile(newEntry.Hash)
				if err != nil {
					return fmt.Errorf("load chunked %s: %w", cleanPath, err)
				}
			} else {
				_, blobData, err := r.LoadObject(newEntry.Hash)
				if err != nil {
					return fmt.Errorf("load blob %s: %w", cleanPath, err)
				}
				fileData = blobData
			}

			if err := writeFileFromEntry(fullPath, fileData, newEntry.Mode); err != nil {
				return fmt.Errorf("write %s: %w", cleanPath, err)
			}

			var contentHash core.Hash
			if objType != core.ObjectChunkManifest {
				contentHash = core.HashFromBytes(fileData)
			}
			idx.Entries[key] = IndexEntry{
				Hash:        newEntry.Hash,
				ContentHash: contentHash,
				Size:        newEntry.Size,
				Mode:        newEntry.Mode,
				OSS:         newEntry.OSS,
			}

		default:
			// Unchanged — keep existing index entry
		}
	}

	return r.SaveIndex(idx)
}
