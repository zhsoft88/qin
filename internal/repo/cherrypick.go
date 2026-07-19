package repo

import (
	"fmt"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

// CherryPick applies the changes introduced by the given commit onto the
// current HEAD and creates a new commit with the original author and message.
func (r *Repository) CherryPick(hash core.Hash) error {
	commit, err := r.LoadCommit(hash)
	if err != nil {
		return fmt.Errorf("load commit: %w", err)
	}

	// Load commit tree and parent tree
	commitTree, err := r.commitTreeMap(hash)
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
		return fmt.Errorf("apply changes: %w", err)
	}

	// Build new tree from updated index
	treeHash, err := r.WriteTree()
	if err != nil {
		return fmt.Errorf("build tree: %w", err)
	}

	// Get current HEAD as parent
	curHeadStr, err := r.ResolveHEAD()
	if err != nil {
		return fmt.Errorf("resolve HEAD: %w", err)
	}
	var parents []core.Hash
	if curHeadStr != "" {
		curHead, err := core.HashFromHex(curHeadStr)
		if err == nil {
			parents = append(parents, curHead)
		}
	}

	newCommit := Commit{
		Tree:    treeHash,
		Parents: parents,
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

	// Update HEAD / branch ref
	head, err := r.ReadHEAD()
	if err != nil {
		return fmt.Errorf("read HEAD: %w", err)
	}
	if len(head) > 5 && head[:4] == "ref:" {
		ref := head[5:]
		if err := r.WriteRef(ref, newHash.String()); err != nil {
			return fmt.Errorf("update ref: %w", err)
		}
	} else {
		if err := r.SetHEAD(newHash.String()); err != nil {
			return fmt.Errorf("update HEAD: %w", err)
		}
	}

	return nil
}
