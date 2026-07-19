package repo

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
func (r *Repository) WorkTreeStatusFiltered(include, exclude map[uint8]bool, filterPaths ...string) (*Status, error) {
	phase := "loading index"
	fmt.Fprintf(os.Stdout, "\r  %s...", phase)
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

	// Filter staged entries when filter path is specified
	if len(filterPaths) > 0 {
		for path := range visible {
			if !matchFilterPath(path, filterPaths) {
				delete(visible, path)
			}
		}
	}

	phase = "comparing HEAD"
	fmt.Fprintf(os.Stdout, "\r  %s...", phase)
	// Snapshot for deletion check (before filtering committed entries)
	allVisible := make(map[string]IndexEntry, len(visible))
	for k, v := range visible {
		allVisible[k] = v
	}

	// Remove entries that match HEAD's tree (already committed)
	if headHashStr, err := r.ResolveHEAD(); err == nil && headHashStr != "" {
		if h, err := core.HashFromHex(headHashStr); err == nil {
			if commit, err := r.LoadCommit(h); err == nil {
				if tree, err := r.LoadTree(commit.Tree); err == nil {
					treeMap := make(map[string]TreeEntry, len(tree.Entries))
					for _, te := range tree.Entries {
						treeMap[te.Name] = te
					}
					for path, entry := range visible {
						if te, ok := treeMap[path]; ok && te.Hash == entry.Hash {
							delete(visible, path)
						}
					}
				}
			}
		}
	}

	headHash, err := r.ResolveHEAD()
	if err == nil {
		s.CommitHash = headHash
	}

	// Track all base paths (including non-visible OS variants) for directory tracking
	phase = "building maps"
	fmt.Fprintf(os.Stdout, "\r  %s...", phase)
	tracked := make(map[string]bool)
	trackedDirs := make(map[string]bool)
	allEntries := make(map[string]IndexEntry)
	for key, entry := range idx.Entries {
		path, _ := parseKey(key)
		tracked[path] = true
		allEntries[path] = entry
		for dir := filepath.Dir(path); dir != "."; dir = filepath.Dir(dir) {
			trackedDirs[filepath.ToSlash(dir)] = true
		}
	}

	ignorer, err := r.LoadIgnoreMatcher()
	if err != nil {
		return nil, err
	}

	checked := 0
	walkRoot := r.Path
	if len(filterPaths) == 1 {
		walkRoot = filepath.Join(r.Path, filterPaths[0])
	}
	filepath.Walk(walkRoot, func(path string, fi os.FileInfo, err error) error {
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

		checked++
		phase = "scanning"
		if checked%100 == 0 || checked == 1 {
			fmt.Fprintf(os.Stdout, "\r  scanned: %d", checked)
		}
		if fi.IsDir() {
			// Skip submodule directories — their content belongs to the submodule repo
			if entry, ok := allEntries[name]; ok && IsSubmoduleMode(entry.Mode) {
				return filepath.SkipDir
			}
			if !tracked[name] && !trackedDirs[name] && !isParentTracked(tracked, name) {
				if !ignorer.Match(name, true) {
					s.Untracked = append(s.Untracked, name+"/")
				}
				return filepath.SkipDir
			}
			return nil
		}

		if _, ok := allEntries[name]; ok {
			data, err := ioutil.ReadFile(path)
			if err != nil {
				return nil
			}
			entry := allEntries[name]
			contentHash := core.HashFromBytes(data)
			if contentHash != entry.ContentHash {
				s.Modified = append(s.Modified, name)
			}
		} else if !tracked[name] && !ignorer.Match(name, false) {
			s.Untracked = append(s.Untracked, name)
		}

		return nil
	})

	for path := range allVisible {
		fullPath := filepath.Join(r.Path, path)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			s.Deleted = append(s.Deleted, path)
		}
	}

	if checked > 0 {
		clearLine(os.Stdout)
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



// clearLine clears the current terminal line by printing spaces.
func clearLine(w io.Writer) {
	tw := termWidth()
	if tw <= 0 {
		fmt.Fprintf(w, "\r                          \r")
		return
	}
	fmt.Fprintf(w, "\r  %-*s\r", tw-2, " ")
}

// truncateName shortens a file path for display, keeping start and end.
func truncateName(name string, termWidth int) string {
	w := termWidth
	if w <= 0 {
		w = 80
	}
	max := w - 26
	if max <= 0 || len(name) <= max {
		return name
	}
	if max < 10 {
		return name[:max]
	}
	half := (max - 3) / 2
	return name[:half] + "..." + name[len(name)-half:]
}

// termWidth returns the terminal width in columns, defaulting to 80.
func termWidth() int {
	return TermWidth()
}

// matchFilterPath returns true if name matches any filter pattern (exact or prefix).
func matchFilterPath(name string, filters []string) bool {
	for _, f := range filters {
		if name == f || strings.HasPrefix(name, f+"/") {
			return true
		}
	}
	return false
}
