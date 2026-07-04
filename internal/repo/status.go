package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"

	"github.com/zhsoft88/lo/internal/core"
)

// Status holds the complete working tree status.
type Status struct {
	Branch     string
	Staged     map[string]IndexEntry
	Untracked  []string
	Modified   []string
	Deleted    []string
	CommitHash string
}

// WorkTreeStatus scans the working directory and compares against the index.
// Entries are filtered by the current runtime OS.
func (r *Repository) WorkTreeStatus() (*Status, error) {
	return r.WorkTreeStatusFiltered(nil, nil)
}

// WorkTreeStatusFiltered is like WorkTreeStatus but allows custom OS filtering.
// When include and exclude are both nil, the current OS is used as the filter.
func (r *Repository) WorkTreeStatusFiltered(include, exclude map[uint8]bool) (*Status, error) {
	idx, err := r.LoadIndex()
	if err != nil {
		return nil, err
	}

	s := &Status{
		Branch: r.CurrentBranch(),
	}

	var visible map[string]IndexEntry
	if include == nil && exclude == nil {
		visible = visibleEntries(idx.Entries, currentOS())
	} else {
		visible = VisibleEntriesExpr(idx.Entries, include, exclude)
	}
	s.Staged = visible

	headHash, err := r.ResolveHEAD()
	if err == nil {
		s.CommitHash = headHash
	}

	// Track all base paths (including non-visible OS variants) for directory tracking
	tracked := make(map[string]bool)
	for key := range idx.Entries {
		path, _ := parseKey(key)
		tracked[path] = true
	}

	ignorer, err := r.LoadIgnoreMatcher()
	if err != nil {
		return nil, err
	}

	filepath.Walk(r.Path, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(r.Path, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}

		if rel == LoDir || (len(rel) > len(LoDir) && rel[:len(LoDir)+1] == LoDir+string(filepath.Separator)) {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == ".loignore" {
			return nil
		}

		name := filepath.ToSlash(rel)

		if fi.IsDir() {
			// Skip submodule directories — their content belongs to the submodule repo
			if entry, ok := visible[name]; ok && IsSubmoduleMode(entry.Mode) {
				return filepath.SkipDir
			}
			if !tracked[name] && !isParentTracked(tracked, name) {
				if !ignorer.Match(name, true) {
					s.Untracked = append(s.Untracked, name+"/")
				}
				return filepath.SkipDir
			}
			return nil
		}

		if _, ok := visible[name]; ok {
			data, err := ioutil.ReadFile(path)
			if err != nil {
				return nil
			}
			entry := visible[name]
			contentHash := core.HashFromBytes(data)
			if contentHash != entry.ContentHash {
				s.Modified = append(s.Modified, name)
			}
		} else if !tracked[name] && !ignorer.Match(name, false) {
			s.Untracked = append(s.Untracked, name)
		}

		return nil
	})

	for path := range visible {
		fullPath := filepath.Join(r.Path, path)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			s.Deleted = append(s.Deleted, path)
		}
	}

	sort.Strings(s.Untracked)
	sort.Strings(s.Modified)
	sort.Strings(s.Deleted)

	return s, nil
}

func isParentTracked(tracked map[string]bool, path string) bool {
	for {
		path = filepath.Dir(path)
		if path == "." || path == "/" {
			return false
		}
		if tracked[path] || tracked[filepath.ToSlash(path)] {
			return true
		}
	}
}
