package repo

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhsoft88/lo/internal/core"
)

// DiffFile describes a single file change between two trees.
type DiffFile struct {
	Name       string
	OldHash    core.Hash
	NewHash    core.Hash
	OldSize    int64
	NewSize    int64
	Type       DiffType
	OS         uint8    // OS ID for OS-specific entries; 0 means all OSes
	OldContent []byte   // old content (for text diff display; empty if too large)
	NewContent []byte   // new content (for text diff display; empty if too large)
}

// DiffType classifies a file change.
type DiffType int

const (
	DiffAdded DiffType = iota + 1
	DiffDeleted
	DiffModified
)

func (d DiffType) String() string {
	switch d {
	case DiffAdded:
		return "added"
	case DiffDeleted:
		return "deleted"
	case DiffModified:
		return "modified"
	default:
		return "unknown"
	}
}

// Diff holds the result of comparing two trees.
type Diff struct {
	Files       []DiffFile
	maxSize  int64 // from config: content diff size limit
	maxLines int   // from config: line diff line limit
}

// DiffCommits compares two commits and returns the file-level diff.
func (r *Repository) DiffCommits(oldCommit, newCommit core.Hash) (*Diff, error) {
	oldTree, err := r.commitTree(oldCommit)
	if err != nil {
		return nil, fmt.Errorf("load old tree: %w", err)
	}
	newTree, err := r.commitTree(newCommit)
	if err != nil {
		return nil, fmt.Errorf("load new tree: %w", err)
	}
	diff := diffTrees(oldTree, newTree)
	diff.maxSize = int64(r.Config.Diff.MaxSize)
	diff.maxLines = r.Config.Diff.MaxLines

	// Enrich with file contents for text diff display
	for i, f := range diff.Files {
		switch f.Type {
		case DiffAdded:
			if f.NewSize > 0 && f.NewSize <= diff.maxSize {
				if e, ok := lookupTreeEntry(newTree, f.Name, f.OS); ok {
					blob, err := r.LoadFileContent(e.Hash)
					if err == nil {
						diff.Files[i].NewContent = blob
					}
				}
			}
		case DiffDeleted:
			if f.OldSize > 0 && f.OldSize <= diff.maxSize {
				if e, ok := lookupTreeEntry(oldTree, f.Name, f.OS); ok {
					blob, err := r.LoadFileContent(e.Hash)
					if err == nil {
						diff.Files[i].OldContent = blob
					}
				}
			}
		case DiffModified:
			if f.NewSize > 0 && f.NewSize <= diff.maxSize {
				if e, ok := lookupTreeEntry(newTree, f.Name, f.OS); ok {
					blob, err := r.LoadFileContent(e.Hash)
					if err == nil {
						diff.Files[i].NewContent = blob
					}
				}
				if e, ok := lookupTreeEntry(oldTree, f.Name, f.OS); ok {
					blob, err := r.LoadFileContent(e.Hash)
					if err == nil {
						diff.Files[i].OldContent = blob
					}
				}
			}
		}
	}

	return diff, nil
}

// DiffWorking compares the current index against the working tree.
// Only entries visible on the current OS are compared.
func (r *Repository) DiffWorking() (*Diff, error) {
	idx, err := r.LoadIndex()
	if err != nil {
		return nil, err
	}

	visible := visibleEntries(idx.Entries, currentOS())

	working := make(map[string]TreeEntry)
	for cleanPath, entry := range visible {
		fullPath := filepath.Join(r.Path, cleanPath)
		if IsSubmoduleMode(entry.Mode) {
			continue
		}
		data, err := ioutil.ReadFile(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue // file deleted on disk
			}
			return nil, err
		}
		contentHash := core.HashFromBytes(data)
		working[cleanPath] = TreeEntry{
			Name: cleanPath,
			Hash: contentHash,
			Size: int64(len(data)),
			Mode: entry.Mode,
		}
	}

	// Build comparison map using content hashes for index entries
	idxCompare := make(map[string]TreeEntry, len(visible))
	for cleanPath, entry := range visible {
		idxCompare[cleanPath] = TreeEntry{
			Name: cleanPath,
			Hash: entry.ContentHash,
			Size: entry.Size,
			Mode: entry.Mode,
		}
	}

	diff := diffTreeMap(idxCompare, working)
	diff.maxSize = int64(r.Config.Diff.MaxSize)
	diff.maxLines = r.Config.Diff.MaxLines

	// Enrich with file contents for text diff display
	for i, f := range diff.Files {
		switch f.Type {
		case DiffAdded, DiffModified:
			if f.NewSize > 0 && f.NewSize <= diff.maxSize {
				fullPath := filepath.Join(r.Path, f.Name)
				data, err := ioutil.ReadFile(fullPath)
				if err == nil {
					diff.Files[i].NewContent = data
				}
			}
		}
		if f.Type == DiffModified || f.Type == DiffDeleted {
			if entry, ok := visible[f.Name]; ok && entry.Size > 0 && entry.Size <= diff.maxSize {
				blob, err := r.LoadFileContent(entry.Hash)
				if err == nil {
					diff.Files[i].OldContent = blob
				}
			}
		}
	}

	return diff, nil
}

// DiffIndex compares HEAD commit tree against the index.
func (r *Repository) DiffIndex() (*Diff, error) {
	headHash, err := r.ResolveHEAD()
	if err != nil || headHash == "" {
		return nil, fmt.Errorf("no commits to diff against")
	}

	head, err := core.HashFromHex(headHash)
	if err != nil {
		return nil, err
	}

	headCommit, err := r.LoadCommit(head)
	if err != nil {
		return nil, err
	}
	headTree, err := r.LoadTree(headCommit.Tree)
	if err != nil {
		return nil, err
	}

	headMap := make(map[string]TreeEntry, len(headTree.Entries))
	for _, e := range headTree.Entries {
		headMap[entryKey(e.Name, osIDForKey(e.OSS))] = e
	}

	idx, err := r.LoadIndex()
	if err != nil {
		return nil, err
	}

	idxEntries := make(map[string]TreeEntry, len(idx.Entries))
	for path, entry := range idx.Entries {
		idxEntries[path] = TreeEntry{
			Name: path,
			Hash: entry.Hash,
			Size: entry.Size,
			Mode: entry.Mode,
		}
	}

	diff := diffTreeMap(headMap, idxEntries)
	diff.maxSize = int64(r.Config.Diff.MaxSize)
	diff.maxLines = r.Config.Diff.MaxLines

	// Enrich with file contents for text diff display
	cOS := currentOS()
	idxVisible := visibleEntries(idx.Entries, cOS)
	for i, f := range diff.Files {
		switch f.Type {
		case DiffAdded:
			if f.NewSize > 0 && f.NewSize <= diff.maxSize {
				for _, e := range headTree.Entries {
					if e.Name == f.Name && osMatch(e.OSS, cOS) {
						blob, err := r.LoadFileContent(e.Hash)
						if err == nil {
							diff.Files[i].NewContent = blob
						}
						break
					}
				}
			}
		case DiffDeleted:
			if f.OldSize > 0 && f.OldSize <= diff.maxSize {
				for _, e := range headTree.Entries {
					if e.Name == f.Name && osMatch(e.OSS, cOS) {
						blob, err := r.LoadFileContent(e.Hash)
						if err == nil {
							diff.Files[i].OldContent = blob
						}
						break
					}
				}
			}
		case DiffModified:
			if f.NewSize > 0 && f.NewSize <= diff.maxSize {
				if entry, ok := idxVisible[f.Name]; ok {
					blob, err := r.LoadFileContent(entry.Hash)
					if err == nil {
						diff.Files[i].NewContent = blob
					}
				}
				for _, e := range headTree.Entries {
					if e.Name == f.Name && osMatch(e.OSS, cOS) {
						blob, err := r.LoadFileContent(e.Hash)
						if err == nil {
							diff.Files[i].OldContent = blob
						}
						break
					}
				}
			}
		}
	}

	return diff, nil
}

func (r *Repository) commitTree(hash core.Hash) (map[string]TreeEntry, error) {
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

func diffTrees(oldTree, newTree map[string]TreeEntry) *Diff {
	return diffTreeMap(oldTree, newTree)
}

func diffTreeMap(oldMap, newMap map[string]TreeEntry) *Diff {
	var files []DiffFile
	seen := make(map[string]bool)

	for key, entry := range newMap {
		seen[key] = true
		cleanName, osTag := parseKey(key)
		old, exists := oldMap[key]
		if !exists {
			files = append(files, DiffFile{
				Name:    cleanName,
				NewHash: entry.Hash,
				NewSize: entry.Size,
				Type:    DiffAdded,
				OS:      osTag,
			})
		} else if old.Hash != entry.Hash {
			files = append(files, DiffFile{
				Name:    cleanName,
				OldHash: old.Hash,
				NewHash: entry.Hash,
				OldSize: old.Size,
				NewSize: entry.Size,
				Type:    DiffModified,
				OS:      osTag,
			})
		}
	}

	for key, entry := range oldMap {
		if !seen[key] {
			cleanName, osTag := parseKey(key)
			files = append(files, DiffFile{
				Name:    cleanName,
				OldHash: entry.Hash,
				OldSize: entry.Size,
				Type:    DiffDeleted,
				OS:      osTag,
			})
		}
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].Name != files[j].Name {
			return files[i].Name < files[j].Name
		}
		return files[i].OS < files[j].OS
	})

	return &Diff{Files: files}
}

// Render returns a human-readable diff string.
func (d *Diff) Render() string {
	if len(d.Files) == 0 {
		return "no changes"
	}

	var b strings.Builder
	for _, f := range d.Files {
		displayName := f.Name
		if f.OS != 0 {
			displayName = f.Name + " [" + OSNameOrStar(f.OS) + "]"
		}
		switch f.Type {
		case DiffAdded:
			fmt.Fprintf(&b, "+ %-8s %s\n", humanDiffSize(f.NewSize), displayName)
			if len(f.NewContent) > 0 {
				b.WriteString(lineDiff(nil, f.NewContent, d.maxLines))
			}
		case DiffDeleted:
			fmt.Fprintf(&b, "- %-8s %s\n", humanDiffSize(f.OldSize), displayName)
			if len(f.OldContent) > 0 {
				b.WriteString(lineDiff(f.OldContent, nil, d.maxLines))
			}
		case DiffModified:
			fmt.Fprintf(&b, "~ %-8s %s  (%s -> %s)\n",
				humanDiffSize(f.NewSize), displayName,
				f.OldHash.Short(), f.NewHash.Short())
			if len(f.OldContent) > 0 || len(f.NewContent) > 0 {
				b.WriteString(lineDiff(f.OldContent, f.NewContent, d.maxLines))
			}
		}
	}
	return b.String()
}

func humanDiffSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%d KB", bytes/1024)
	}
	return fmt.Sprintf("%d MB", bytes/(1024*1024))
}

// lookupTreeEntry finds a tree entry by name and OS, falling back to OS=0.
func lookupTreeEntry(tree map[string]TreeEntry, name string, osID uint8) (TreeEntry, bool) {
	key := entryKey(name, osID)
	if e, ok := tree[key]; ok {
		return e, true
	}
	key = entryKey(name, 0)
	e, ok := tree[key]
	return e, ok
}

// lineDiff computes a line-level diff between old and new content.
func lineDiff(oldContent, newContent []byte, maxLines int) string {
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	if maxLines > 0 && (len(oldLines) > maxLines || len(newLines) > maxLines) {
		return ""
	}

	m, n := len(oldLines), len(newLines)

	// Build LCS length table
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := 0; i < m; i++ {
		for j := 0; j < n; j++ {
			if oldLines[i] == newLines[j] {
				lcs[i+1][j+1] = lcs[i][j] + 1
			} else if lcs[i][j+1] >= lcs[i+1][j] {
				lcs[i+1][j+1] = lcs[i][j+1]
			} else {
				lcs[i+1][j+1] = lcs[i+1][j]
			}
		}
	}

	var b strings.Builder
	var backtrack func(i, j int)
	backtrack = func(i, j int) {
		if i > 0 && j > 0 && oldLines[i-1] == newLines[j-1] {
			backtrack(i-1, j-1)
			b.WriteString("  ")
			b.WriteString(oldLines[i-1])
			b.WriteByte('\n')
		} else if j > 0 && (i == 0 || lcs[i][j-1] >= lcs[i-1][j]) {
			backtrack(i, j-1)
			b.WriteString("+ ")
			b.WriteString(newLines[j-1])
			b.WriteByte('\n')
		} else if i > 0 {
			backtrack(i-1, j)
			b.WriteString("- ")
			b.WriteString(oldLines[i-1])
			b.WriteByte('\n')
		}
	}
	backtrack(m, n)

	return b.String()
}

// splitLines splits content into lines, stripping trailing empty line.
func splitLines(content []byte) []string {
	s := string(content)
	if len(s) == 0 {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
