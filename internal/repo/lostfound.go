package repo

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

// DanglingCommit describes an unreachable commit.
type DanglingCommit struct {
	Hash    core.Hash
	Author  string
	Message string
	Time    time.Time
	Parents int
}

// FindDanglingCommits returns commits that exist in the object store
// but are not reachable from any branch or tag ref.
func (r *Repository) FindDanglingCommits() ([]DanglingCommit, error) {
	// Mark all reachable commits from refs
	reachable := make(map[core.Hash]bool)
	if err := r.markReachableRefs(reachable); err != nil {
		return nil, err
	}

	// Enumerate all commit objects in the store
	all, err := r.enumerateCommits()
	if err != nil {
		return nil, err
	}

	var dangling []DanglingCommit
	for _, h := range all {
		if reachable[h] {
			continue
		}
		commit, err := r.LoadCommit(h)
		if err != nil {
			continue
		}
		dangling = append(dangling, DanglingCommit{
			Hash:    h,
			Author:  commit.Author,
			Message: commit.Message,
			Time:    commit.Time,
			Parents: len(commit.Parents),
		})
	}

	sort.Slice(dangling, func(i, j int) bool {
		return dangling[i].Time.After(dangling[j].Time)
	})

	return dangling, nil
}

func (r *Repository) markReachableRefs(reachable map[core.Hash]bool) error {
	// Walk all refs under refs/heads/ and refs/tags/
	refsDir := r.RefsDir()
	return filepath.Walk(refsDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
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
		r.markReachableCommit(h, reachable)
		return nil
	})
}

func (r *Repository) markReachableCommit(hash core.Hash, reachable map[core.Hash]bool) {
	if reachable[hash] {
		return
	}
	objType, err := r.ObjectType(hash)
	if err != nil || objType != core.ObjectCommit {
		return
	}
	reachable[hash] = true
	commit, err := r.LoadCommit(hash)
	if err != nil {
		return
	}
	for _, parent := range commit.Parents {
		r.markReachableCommit(parent, reachable)
	}
}

func (r *Repository) enumerateCommits() ([]core.Hash, error) {
	objectsDir := r.ObjectsDir()
	dirEntries, err := ioutil.ReadDir(objectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read objects dir: %w", err)
	}

	var commits []core.Hash
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
			objType, err := r.ObjectType(h)
			if err != nil {
				continue
			}
			if objType == core.ObjectCommit {
				commits = append(commits, h)
			}
		}
	}
	return commits, nil
}
