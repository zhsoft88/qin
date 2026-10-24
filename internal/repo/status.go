package repo

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhsoft88/qin/internal/core"
)

// spaces80 is used by clearLine and printProgress to overwrite progress
// text on the current line.
const spaces80 = "                                                                                "

// Status holds the complete working tree status.
type Status struct {
	Branch string
	Staged map[string]IndexEntry
	// StagedDeleted lists paths HEAD's tree has and the index does not — a
	// staged deletion. Deleted (below) is a different thing: a path the index
	// tracks that is missing from disk.
	StagedDeleted []string
	Untracked     []string
	Modified      []string
	Deleted       []string
	CommitHash    string
}

// WorkTreeStatus scans the working directory and compares against the index.
// Entries are filtered by the current runtime OS.
func (r *Repository) WorkTreeStatus() (*Status, error) {
	return r.WorkTreeStatusFiltered(nil, nil)
}

// WorkTreeStatusFiltered is like WorkTreeStatus but allows custom OS filtering.
// When include and exclude are both nil, the current OS is used as the filter.
//
// The scan is split into two phases:
//
//  1. Refresh: iterate the OS-visible index entries and lstat each path,
//     using the mtime/size fast path to skip content hashing.
//  2. Scan: walk the working tree for untracked files, skipping unchanged
//     directories via the on-disk untracked cache.
func (r *Repository) WorkTreeStatusFiltered(include, exclude map[uint8]bool, filterPaths ...string) (*Status, error) {
	defer endProgressLine()
	phase := "loading index"
	printProgress("%s...", phase)
	idx, err := r.LoadIndex()
	if err != nil {
		return nil, err
	}

	// Racily-clean bound: an entry whose mtime is >= the index's own mtime
	// may have been modified in the same timestamp tick as the index write,
	// so it must be re-hashed instead of trusting the stat fast path. The
	// index mtime also gates the untracked cache: any index change invalidates
	// previously cached untracked lists.
	var idxMtime int64
	if fi, err := os.Stat(r.indexPath()); err == nil {
		idxMtime = fi.ModTime().UnixNano()
	}

	// Optional change monitor (core.fsmonitor, built into qin). It only
	// tells us which paths may be skipped; every conclusion below is still
	// reached by the same checks as before. Restricted to unfiltered scans
	// for the same reason as the untracked cache: the persisted dirty set is
	// repo-wide, and a partial scan must not overwrite it as if it were
	// complete.
	var mon *fsmonitorChanges
	var mustCheck map[string]bool
	if len(filterPaths) == 0 && include == nil && exclude == nil {
		mon, _ = fsmonitorBackend(r)
		if mon != nil {
			mustCheck = mon.MustCheck
		}
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
	printProgress("%s...", phase)
	// Snapshot for deletion check (before filtering committed entries)
	allVisible := make(map[string]IndexEntry, len(visible))
	for k, v := range visible {
		allVisible[k] = v
	}

	// Remove entries that match HEAD's tree (already committed), and find the
	// paths HEAD has that the index does not.
	//
	// These failures used to be absorbed by an err == nil chain, and the result
	// was not a missing line of output: with no tree to compare against, no
	// index entry matches and every one of them is reported as staged, while
	// every deletion vanishes. That is a wrong answer wearing the shape of a
	// right one, so an unreadable HEAD is reported instead. An *empty* HEAD is
	// not an error — that is a repository with no commits yet, where there is
	// genuinely nothing to compare against.
	if headHashStr, err := r.ResolveHEAD(); err == nil && headHashStr != "" {
		h, err := core.HashFromHex(headHashStr)
		if err != nil {
			return nil, fmt.Errorf("HEAD is not a commit hash: %q", headHashStr)
		}
		commit, err := r.LoadCommit(h)
		if err != nil {
			return nil, fmt.Errorf("cannot read HEAD commit %s: %w", h.Short(), err)
		}
		tree, err := r.LoadTree(commit.Tree)
		if err != nil {
			return nil, fmt.Errorf("cannot read HEAD's tree %s: %w", commit.Tree.Short(), err)
		}

		treeMap := make(map[string]TreeEntry, len(tree.Entries))
		for _, te := range tree.Entries {
			treeMap[entryKey(te.Name, te.OSS)] = te
		}
		for path, entry := range visible {
			if te, ok := treeMap[entryKey(path, entry.OSS)]; ok && te.Hash == entry.Hash {
				delete(visible, path)
			}
		}

		// The mirror image of a staged add, and invisible to every map above:
		// Staged is built from index keys, and Modified and Deleted both
		// iterate index-derived paths. Nothing else enumerates the HEAD tree,
		// so this loop is the only place the paths can be seen.
		seen := make(map[string]bool, len(tree.Entries))
		for _, te := range tree.Entries {
			if include == nil && exclude == nil {
				if !osMatch(te.OSS, currentOS()) {
					continue
				}
			} else if !MatchOSExpr(te.OSS, include, exclude) {
				continue
			}
			// By name, not by composite key: a path HEAD stores as a default
			// variant (OSS 0) and the index stores as an OS-specific one is
			// still tracked, and entryKey would call it deleted.
			if _, ok := allVisible[te.Name]; ok {
				continue
			}
			if seen[te.Name] {
				continue // one report per path, however many variants
			}
			seen[te.Name] = true
			s.StagedDeleted = append(s.StagedDeleted, te.Name)
		}
	}

	headHash, err := r.ResolveHEAD()
	if err == nil {
		s.CommitHash = headHash
	}

	// tracked: only paths with a variant visible on this OS (or under the
	// given filter) count as tracked — a path tracked solely by other-OS
	// variants (e.g. a win-only file on linux) surfaces as untracked when
	// created locally.
	// allEntries follows the same visible view for submodule/empty-dir
	// entries. trackedDirs covers every variant, so a directory holding
	// any tracked content is descended into and the untracked file is
	// reported individually instead of pruning the whole directory.
	phase = "building maps"
	printProgress("%s...", phase)
	tracked := make(map[string]bool)
	trackedDirs := make(map[string]bool)
	allEntries := make(map[string]IndexEntry)
	for key, entry := range idx.Entries {
		name, _ := parseKey(key)
		if include == nil && exclude == nil {
			if osMatch(entry.OSS, currentOS()) {
				tracked[name] = true
				allEntries[name] = entry
			}
		} else if MatchOSExpr(entry.OSS, include, exclude) {
			tracked[name] = true
			allEntries[name] = entry
		}
		// path.Dir, not filepath.Dir: a stored name is always slash-separated,
		// and a backslash in one is an ordinary character. filepath.Dir would
		// split on it on Windows, inventing parent directories that do not
		// exist in the name.
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			trackedDirs[dir] = true
		}
	}

	ignorer, err := r.LoadIgnoreMatcher()
	if err != nil {
		return nil, err
	}
	var ignoreMtime int64
	if fi, err := os.Stat(filepath.Join(r.Path, ".qinignore")); err == nil {
		ignoreMtime = fi.ModTime().UnixNano()
	}

	// ---- Phase A: refresh tracked entries (modified detection) ----
	// Uses allVisible (before the committed-filter) so committed-but-changed
	// files are still checked, like the old single-pass walk did.
	phase = "comparing worktree"
	printProgress("%s...", phase)
	for path, entry := range allVisible {
		if len(filterPaths) > 0 && !matchFilterPath(path, filterPaths) {
			continue // not scanned; the deletion check below still applies
		}
		// Monitor fast path: the monitor reported no change for this path, so it
		// still exists with the content the index recorded. Skip the lstat
		// outright — that syscall is what this feature is for. Restricted to
		// entries that carry stat info, so entries the stat fast path below
		// would never trust are not skipped either.
		if mustCheck != nil && entry.Mtime != 0 && !mustCheck[path] {
			continue
		}
		fullPath := filepath.Join(r.Path, path)
		fi, err := os.Lstat(fullPath)
		if err != nil {
			continue // deleted — the deletion check below reports it
		}
		// Directory entries (empty dirs, submodules) have no content to hash
		if IsSubmoduleMode(entry.Mode) || entry.Hash.IsZero() {
			continue
		}
		if r.trackedEntryChanged(fullPath, entry, fi, idxMtime) {
			s.Modified = append(s.Modified, path)
		}
	}

	// Deleted check: every visible path (even outside the filter) must exist
	for path := range allVisible {
		// A path the monitor never reported as changed still exists; only
		// paths it did report need the lstat.
		if mustCheck != nil && !mustCheck[path] {
			continue
		}
		fullPath := filepath.Join(r.Path, path)
		if _, err := os.Lstat(fullPath); os.IsNotExist(err) {
			s.Deleted = append(s.Deleted, path)
		}
	}

	// ---- Phase B: untracked scan with per-directory cache ----
	var cache *untrackedCache
	useCache := len(filterPaths) == 0
	if useCache {
		cache = r.loadUntrackedCache()
		if cache.IndexMtime != idxMtime || cache.IgnoreMtime != ignoreMtime {
			cache = &untrackedCache{Dirs: make(map[string]*dirCacheEnt)}
		}
		cache.IndexMtime = idxMtime
		cache.IgnoreMtime = ignoreMtime
	}

	phase = "scanning"
	printProgress("%s...", phase)
	checked := 0
	walkRoot := r.Path
	rootRel := ""
	if len(filterPaths) == 1 {
		walkRoot = filepath.Join(r.Path, filterPaths[0])
		rootRel = filepath.ToSlash(filterPaths[0])
	}

	var walk func(absDir, relDir string)
	walk = func(absDir, relDir string) {
		checked++
		if checked%500 == 0 || checked == 1 {
			printProgress("scanned: %d dirs", checked)
		}
		fi, err := os.Stat(absDir)
		if err != nil {
			return
		}
		dirMtime := fi.ModTime().UnixNano()

		// Cache hit: the dir's own mtime is unchanged, so its untracked
		// children and child-dir list are exact. Subdirectories are still
		// visited and validated at their own level — a dir mtime only
		// changes on direct entry create/delete, so deeper changes don't
		// invalidate this entry.
		if useCache {
			if ent, ok := cache.Dirs[relDir]; ok && ent.Mtime == dirMtime {
				s.Untracked = append(s.Untracked, ent.Untracked...)
				for _, d := range ent.Dirs {
					walk(filepath.Join(absDir, filepath.Base(d)), d)
				}
				return
			}
		}

		entries, err := ioutil.ReadDir(absDir)
		if err != nil {
			return
		}

		var untrackedChildren []string
		var dirsToDescend []string
		for _, e := range entries {
			name := e.Name()
			if relDir != "" {
				name = relDir + "/" + name
			}
			if relDir == "" {
				// The same rule the add walks apply — at the root, the entry's
				// name is its whole repo-relative path — kept as one predicate
				// so the two cannot drift.
				//
				// .qinignore itself gets no case of its own. It was hidden here
				// on the reasoning that qin's own ignore file is not something
				// to report, but `add .` tracks it like any other file, so the
				// two disagreed about a file the user can see in the directory.
				if IsRepoMetadataPath(name) {
					continue
				}
			}
			if e.IsDir() {
				// Skip submodule directories — their content belongs to the submodule repo
				if entry, ok := allEntries[name]; ok && IsSubmoduleMode(entry.Mode) {
					continue
				}
				// Tracked empty directory — descend without reporting
				if _, ok := allEntries[name]; ok {
					dirsToDescend = append(dirsToDescend, name)
					continue
				}
				if !trackedDirs[name] {
					// No tracked content below — the whole subtree is either
					// untracked or ignored. Report it once and prune, except
					// when the dir is ignored but negate rules exist: children
					// may be re-included, so we must descend to find them.
					ignored := ignorer.Match(name, true)
					if ignored && !ignorer.hasNegate {
						continue
					}
					if !ignored {
						s.Untracked = append(s.Untracked, name+"/")
						untrackedChildren = append(untrackedChildren, name+"/")
						continue
					}
					// ignored with negate — fall through to descend
				}
				dirsToDescend = append(dirsToDescend, name)
				continue
			}

			if tracked[name] {
				continue
			}
			if ignorer.Match(name, false) {
				continue
			}
			s.Untracked = append(s.Untracked, name)
			untrackedChildren = append(untrackedChildren, name)
		}

		if useCache {
			cache.Dirs[relDir] = &dirCacheEnt{Mtime: dirMtime, Untracked: untrackedChildren, Dirs: dirsToDescend}
		}
		for _, d := range dirsToDescend {
			walk(filepath.Join(absDir, filepath.Base(d)), d)
		}
	}
	walk(walkRoot, rootRel)

	if useCache {
		r.saveUntrackedCache(cache)
	}
	sort.Strings(s.Untracked)
	sort.Strings(s.Modified)
	sort.Strings(s.Deleted)
	sort.Strings(s.StagedDeleted)

	// Advance the monitor's clock and carry the deviations forward. A
	// deviation is a persistent condition but the monitor reports it only
	// once, so the paths found deviant here are remembered and re-checked
	// until the index matches them again. A nil mon is a no-op.
	//
	// StagedDeleted is deliberately not among them. A deviation means "differs
	// from the index, re-check until add reconciles it"; a staged deletion is a
	// HEAD-vs-index fact that no filesystem event will ever clear, and the path
	// is not in the index, so mustCheck would never gate it anyway.
	mon.save(r, s.Deleted, s.Modified)

	return s, nil
}

// trackedEntryChanged reports whether the worktree file at fullPath differs
// from what entry records. The caller has established that fullPath exists
// and is not a submodule or empty-directory entry.
//
// This is status's per-entry comparison, factored out so StageTrackedChanges
// (commit -a) reaches the same conclusion without a second copy of the stat
// fast path, symlink reading or degraded-link rules. A read failure returns
// false: status reports such a path as neither modified nor deleted, and
// staging must not turn a transient error into a commit.
func (r *Repository) trackedEntryChanged(fullPath string, entry IndexEntry, fi os.FileInfo, idxMtime int64) bool {
	// Stat fast path: skip the content read+hash when size and mtime
	// match the index entry and the entry predates the index write.
	// Symlinks are excluded (their on-disk content can change without
	// touching the link's own stat). A mismatch only means extra work,
	// never a missed change.
	if !IsSymlinkMode(entry.Mode) && entry.Mtime != 0 &&
		fi.Size() == entry.Size &&
		fi.ModTime().UnixNano() == entry.Mtime &&
		entry.Mtime < idxMtime {
		return false
	}
	// A symlink is stored as a blob holding its target string (see
	// AddFileToIndex), so it has to be read back as a link. ReadFile would
	// follow it and hash whatever it points at — a link to a file would be
	// reported modified on every run, and one to a directory escaped that
	// only because ReadFile fails on a directory.
	var data []byte
	var err error
	if IsSymlinkMode(entry.Mode) {
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			var target string
			target, err = os.Readlink(fullPath)
			if err != nil {
				return false
			}
			data = []byte(linkTargetFromDisk(target))
		case !r.symlinksEnabled() && fi.Mode().IsRegular():
			// core.symlinks=false materializes the link as a regular file
			// whose bytes are the target text. Read those bytes, not
			// through the file: the recorded content is the target.
			data, err = ioutil.ReadFile(fullPath)
			if err != nil {
				return false
			}
		default:
			// The index records a link but the worktree holds neither a
			// link nor the degraded file: a type change is a modification.
			return true
		}
	} else {
		data, err = ioutil.ReadFile(fullPath)
		if err != nil {
			return false
		}
	}
	return core.HashFromBytes(data) != entry.ContentHash
}

// StageTrackedChanges stages the changes `commit -a` is responsible for: a
// tracked file whose worktree content differs from the index is re-added under
// the variant this host sees, and a tracked path missing from disk loses every
// index entry for its name — exactly what `rm` removes. Untracked files are
// never considered, and no directory walk happens: the index alone says what
// to examine, so the untracked scan that dominates `status` on a large tree is
// not part of this operation.
//
// Ignore rules do not apply. These paths are already tracked, and qin's rules
// — like git's — only classify untracked files; refusing a tracked file just
// because it now matches a pattern would let .qinignore block a commit -a
// forever, with no way to reconcile the index.
//
// The index is saved once, after every path has been processed. On error no
// entry is persisted, so a failed commit -a leaves the staging area as it was.
func (r *Repository) StageTrackedChanges() (modified, deleted int, err error) {
	defer endProgressLine()
	printProgress("loading index...")
	idx, err := r.LoadIndex()
	if err != nil {
		return 0, 0, err
	}

	// Same racily-clean bound as status: an entry whose mtime is at or after
	// the index write must be re-hashed, not trusted.
	var idxMtime int64
	if fi, err := os.Stat(r.indexPath()); err == nil {
		idxMtime = fi.ModTime().UnixNano()
	}

	visible := visibleEntries(idx.Entries, currentOS())

	printProgress("comparing worktree...")
	var modifiedPaths, deletedPaths []string
	for name, entry := range visible {
		fullPath := filepath.Join(r.Path, name)
		fi, statErr := os.Lstat(fullPath)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				deletedPaths = append(deletedPaths, name)
			}
			continue
		}
		if IsSubmoduleMode(entry.Mode) || entry.Hash.IsZero() {
			continue // directory entries (empty dirs, submodules) have no content
		}
		if r.trackedEntryChanged(fullPath, entry, fi, idxMtime) {
			modifiedPaths = append(modifiedPaths, name)
		}
	}
	// Deterministic order: the first reported error must not depend on map
	// iteration. Removing a path deletes its variants, which has no order to
	// preserve beyond the name itself.
	sort.Strings(modifiedPaths)
	sort.Strings(deletedPaths)

	changed := false
	for _, name := range modifiedPaths {
		staged, err := r.addFileToIndex(filepath.Join(r.Path, name), 0, true, true, idx)
		if err != nil {
			return 0, 0, fmt.Errorf("stage %s: %w", name, err)
		}
		if staged {
			modified++
			changed = true
		}
	}
	for _, name := range deletedPaths {
		removed := false
		for key := range idx.Entries {
			if p, _ := parseKey(key); p == name {
				delete(idx.Entries, key)
				removed = true
			}
		}
		if removed {
			deleted++
			changed = true
		}
	}

	if changed {
		if err := r.SaveIndex(idx); err != nil {
			return 0, 0, err
		}
	}
	return modified, deleted, nil
}

// clearLine clears the current terminal line by printing spaces.
func clearLine(w io.Writer) {
	fmt.Fprintf(w, "\r%s\r", spaces80)
}

// lastProgressLen is the length of the most recent in-place progress line
// written to stderr. A follow-up progress line pads with spaces to at least
// this length, erasing the previous line's tail — a bare \r leaves residue
// when the previous line was longer than the current one.
var lastProgressLen int

// printProgress rewrites the current stderr line with progress text,
// erasing the tail of a previously longer line.
func printProgress(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	pad := lastProgressLen - len(msg)
	if pad < 0 {
		pad = 0
	}
	if pad > len(spaces80) {
		pad = len(spaces80)
	}
	fmt.Fprintf(os.Stderr, "\r%s%s\r", msg, spaces80[:pad])
	lastProgressLen = len(msg)
}

// endProgressLine scrolls the current progress line and resets the
// tail-padding state for the next progress line. It does nothing when no line
// is outstanding, so a function can defer it unconditionally and every return
// path — an error included — leaves the cursor at the start of a fresh line.
func endProgressLine() {
	if lastProgressLen == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\n")
	lastProgressLen = 0
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

// matchFilterPath returns true if name matches any filter pattern (exact or prefix).
func matchFilterPath(name string, filters []string) bool {
	for _, f := range filters {
		if name == f || strings.HasPrefix(name, f+"/") {
			return true
		}
	}
	return false
}
