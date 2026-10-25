package repo

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhsoft88/qin/internal/core"
)

// RemoveOptions controls RemovePaths.
type RemoveOptions struct {
	// Cached removes only the index entries and leaves the working tree
	// alone. Like git rm --cached it skips both safety checks: nothing on
	// disk can be lost, so unstaging a modified or newly added file works.
	Cached bool
	// Recursive allows a pathspec to match tracked names beneath it.
	Recursive bool
	// Force skips the staged-change and local-modification checks.
	Force bool
}

// RemoveResult describes one removed path.
type RemoveResult struct {
	Name string
	// WorkTreeRemoved is false when only the index changed: --cached, a
	// variant invisible on this host, a path already missing from disk, or a
	// directory left in place because it still holds untracked content.
	WorkTreeRemoved bool
}

// RemovePaths removes tracked paths from the index and, unless Cached is set,
// from the working tree. pathspecs are resolved against the process working
// directory like every other CLI path argument.
//
// Every pathspec is validated before anything is mutated: a single unmatched
// name, directory without -r, or escape outside the repository fails the whole
// call with zero partial state. Matching is derived from index keys only —
// never from os.Stat on the argument — so a tracked symlink to a directory is
// still a file, and an argument that only exists as an untracked directory
// matches nothing rather than being walked into.
func (r *Repository) RemovePaths(paths []string, opts RemoveOptions) ([]RemoveResult, error) {
	if len(paths) == 0 {
		return nil, errors.New("no paths given")
	}

	idx, err := r.LoadIndex()
	if err != nil {
		return nil, err
	}

	// byName groups the composite keys of each path so removing a path removes
	// every OS variant, matching RemoveFile and StageTrackedChanges.
	byName := make(map[string][]string, len(idx.Entries))
	for key := range idx.Entries {
		name, _ := parseKey(key)
		byName[name] = append(byName[name], key)
	}

	matched := make(map[string]bool)
	var problems []string
	for _, arg := range paths {
		rel, err := r.resolveRemovePathspec(arg)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}

		exactKeys := byName[rel]
		exact := rel != "" && len(exactKeys) > 0

		var under []string
		for name := range byName {
			if rel == "" || strings.HasPrefix(name, rel+"/") {
				under = append(under, name)
			}
		}

		if !opts.Recursive && (len(under) > 0 || removeIndexHasDirEntry(idx, exactKeys)) {
			problems = append(problems, fmt.Sprintf("not removing '%s' recursively without -r", arg))
			continue
		}
		if !exact && len(under) == 0 {
			problems = append(problems, fmt.Sprintf("pathspec '%s' did not match any tracked files", arg))
			continue
		}

		// A corrupt index could name qin's own storage. Never remove it, and
		// never prune a parent into it.
		if exact && IsRepoMetadataPath(rel) {
			problems = append(problems, fmt.Sprintf("refusing to remove repository metadata: %s", rel))
			continue
		}
		if exact {
			matched[rel] = true
		}
		for _, name := range under {
			if IsRepoMetadataPath(name) {
				problems = append(problems, fmt.Sprintf("refusing to remove repository metadata: %s", name))
				continue
			}
			matched[name] = true
		}
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "\n"))
	}

	names := make([]string, 0, len(matched))
	for name := range matched {
		names = append(names, name)
	}
	sort.Strings(names)

	visible := visibleEntries(idx.Entries, currentOS())

	if !opts.Cached && !opts.Force {
		if err := r.checkRemoveUnchanged(names, idx, visible, byName); err != nil {
			return nil, err
		}
	}

	workTreeRemoved := make(map[string]bool)
	if !opts.Cached {
		// Preflight every deletion before touching the working tree, so a
		// symlinked parent or an invalid stored name fails the whole call
		// before the first file disappears. Names this host cannot materialize
		// have no worktree entry to remove; the index entry still goes.
		skip := make(map[string]bool)
		for _, name := range names {
			if _, ok := visible[name]; !ok {
				// A variant that is not visible here was never checked out on
				// this host. A file at that name belongs to the user, not to
				// the index, and must not be deleted.
				skip[name] = true
				continue
			}
			unrepresentable, err := checkRemoveWorkTreePath(r.Path, name)
			if err != nil {
				return nil, fmt.Errorf("cannot remove '%s': %w", name, err)
			}
			if unrepresentable {
				skip[name] = true
			}
		}
		for _, name := range names {
			if skip[name] {
				continue
			}
			removed, err := r.removeWorkTreePath(name, visible[name].Mode)
			if err != nil {
				return nil, fmt.Errorf("cannot remove '%s': %w", name, err)
			}
			workTreeRemoved[name] = removed
		}
	}

	for name := range matched {
		for _, key := range byName[name] {
			delete(idx.Entries, key)
		}
	}
	if err := r.SaveIndex(idx); err != nil {
		return nil, err
	}

	results := make([]RemoveResult, 0, len(names))
	for _, name := range names {
		results = append(results, RemoveResult{Name: name, WorkTreeRemoved: workTreeRemoved[name]})
	}
	return results, nil
}

// resolveRemovePathspec turns a CLI pathspec into a repo-relative slash path.
// "" means the repository root. It never evaluates symlinks: a link argument
// addresses the link entry, not its target.
func (r *Repository) resolveRemovePathspec(arg string) (string, error) {
	if strings.TrimSpace(arg) == "" {
		return "", errors.New("empty pathspec")
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", arg, err)
	}
	rel, err := filepath.Rel(r.Path, abs)
	if err != nil || isOutsideRepo(rel) {
		return "", fmt.Errorf("path outside repository: %s", arg)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		rel = ""
	}
	return rel, nil
}

func removeIndexHasDirEntry(idx *Index, keys []string) bool {
	for _, key := range keys {
		if IsDirMode(idx.Entries[key].Mode) {
			return true
		}
	}
	return false
}

// checkRemoveUnchanged refuses to discard staged content or local edits
// unless -f was given. It only reads; the caller has not mutated anything yet.
func (r *Repository) checkRemoveUnchanged(names []string, idx *Index, visible map[string]IndexEntry, byName map[string][]string) error {
	head, err := r.removeHeadEntries()
	if err != nil {
		return err
	}
	var idxMtime int64
	if fi, err := os.Stat(r.indexPath()); err == nil {
		idxMtime = fi.ModTime().UnixNano()
	}

	var lines []string
	for _, name := range names {
		var reasons []string

		// Staged changes are judged per variant: rm drops every variant, so a
		// difference on a platform this host does not see still counts. With
		// no HEAD every entry is a staged addition, exactly as in git.
		staged := head == nil
		if head != nil {
			for _, key := range byName[name] {
				_, osID := parseKey(key)
				entry := idx.Entries[key]
				te, ok := head[entryKey(name, osID)]
				if !ok || te.Hash != entry.Hash || te.Mode != entry.Mode {
					staged = true
					break
				}
			}
		}
		if staged {
			reasons = append(reasons, "changes staged in the index")
		}

		// Local modifications are only defined for the variant this host
		// sees; that is also the only one with a worktree file here.
		if entry, ok := visible[name]; ok {
			changed, err := r.removeWorkTreeChanged(name, entry, idxMtime)
			if err != nil {
				return fmt.Errorf("cannot inspect '%s': %w", name, err)
			}
			if changed {
				reasons = append(reasons, "local modifications")
			}
		}

		if len(reasons) > 0 {
			lines = append(lines, fmt.Sprintf("cannot remove '%s': %s", name, strings.Join(reasons, "; ")))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	lines = append(lines, "(use --cached to keep the file, or -f to force removal)")
	return errors.New(strings.Join(lines, "\n"))
}

// removeWorkTreeChanged mirrors status's Phase A for one path. A missing file
// is not a modification: rm may stage that deletion, just as git does.
func (r *Repository) removeWorkTreeChanged(name string, entry IndexEntry, idxMtime int64) (bool, error) {
	full := filepath.Join(r.Path, filepath.FromSlash(name))
	fi, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if IsDirMode(entry.Mode) || IsSubmoduleMode(entry.Mode) {
		// The index records a directory here. Anything else is a type change.
		return !fi.IsDir(), nil
	}
	if entry.Hash.IsZero() {
		// status's Phase A skips these too; there is no content to compare.
		return false, nil
	}
	if fi.IsDir() {
		// trackedEntryChanged would try to ReadFile a directory, fail, and
		// report "unchanged". A directory where a file was is a change.
		return true, nil
	}
	return r.trackedEntryChanged(full, entry, fi, idxMtime), nil
}

// removeHeadEntries loads HEAD's tree keyed by entryKey(Name, OSS), the same
// map DiffIndex and status build. nil means the repository has no commit yet.
func (r *Repository) removeHeadEntries() (map[string]TreeEntry, error) {
	headStr, err := r.ResolveHEAD()
	if err != nil {
		return nil, fmt.Errorf("resolve HEAD: %w", err)
	}
	if headStr == "" {
		return nil, nil
	}
	h, err := core.HashFromHex(headStr)
	if err != nil {
		return nil, fmt.Errorf("HEAD is not a commit hash: %q", headStr)
	}
	commit, err := r.LoadCommit(h)
	if err != nil {
		return nil, fmt.Errorf("cannot read HEAD commit %s: %w", h.Short(), err)
	}
	tree, err := r.LoadTree(commit.Tree)
	if err != nil {
		return nil, fmt.Errorf("cannot read HEAD's tree %s: %w", commit.Tree.Short(), err)
	}
	m := make(map[string]TreeEntry, len(tree.Entries))
	for _, te := range tree.Entries {
		m[entryKey(te.Name, te.OSS)] = te
	}
	return m, nil
}

// checkRemoveWorkTreePath runs the read-only half of removeWorkTreeFile.
// unrepresentable reports that this host has no worktree entry for the name
// (a Linux filename on Windows, or a name Windows would read as an escape), so
// only the index should change.
func checkRemoveWorkTreePath(root, name string) (unrepresentable bool, err error) {
	if !safeRepoPath(name) {
		return false, fmt.Errorf("%w: %s", errOutsideWorkTree, name)
	}
	if herr := hostPathError(name, hostGOOS()); herr != nil {
		if errors.Is(herr, errUnrepresentable) || errors.Is(herr, errOutsideWorkTree) {
			return true, nil
		}
		return false, herr
	}
	return false, checkParentsNotSymlinks(root, name)
}

// removeWorkTreePath deletes one entry. Non-empty directories (untracked
// content inside an empty-dir entry, a submodule's checkout) are the success
// case for -r: rm never descends into them.
func (r *Repository) removeWorkTreePath(name string, mode uint32) (bool, error) {
	err := r.removeWorkTreeFile(name)
	if err == nil {
		r.pruneEmptyParents(name)
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	if IsDirMode(mode) || IsSubmoduleMode(mode) {
		return false, nil
	}
	return false, err
}

// pruneEmptyParents removes directories that became empty because the file
// was just removed. It stops at the first non-empty parent and never reaches
// the repository root.
//
// The directory the process runs in is never removed, even when it is empty
// now: without a working directory the command's own later relative paths
// fail in os.Getwd, and the user's shell is left in the same state. A
// non-empty ancestor cannot be removed anyway (os.Remove refuses), so
// equality is the only case that needs guarding.
func (r *Repository) pruneEmptyParents(name string) {
	cwd, cwdErr := os.Getwd()
	for dir := path.Dir(name); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if dir == QinDir {
			return
		}
		full := filepath.Join(r.Path, filepath.FromSlash(dir))
		if cwdErr == nil && sameDir(cwd, full) {
			return
		}
		if err := os.Remove(full); err != nil {
			return
		}
	}
}

// sameDir reports whether a and b name the same directory. It compares
// identity rather than spelling so a working directory reached through a
// symlink, or spelled with different but equivalent components, is still
// recognized.
func sameDir(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}
