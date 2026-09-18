package repo

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhsoft88/qin/internal/core"
)

// Remote describes a named remote repository URL.
type Remote struct {
	Name string
	URL  string
}

// ---- Remote URL CRUD ----

// SaveRemote writes a remote URL to .qin/remotes/<name>.
func (r *Repository) SaveRemote(name, url string) error {
	remotesDir := filepath.Join(r.LoDir(), "remotes")
	if err := os.MkdirAll(remotesDir, 0755); err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(remotesDir, name), []byte(url+"\n"), 0644)
}

// LoadRemote reads the URL for a named remote.
func (r *Repository) LoadRemote(name string) (string, error) {
	data, err := ioutil.ReadFile(filepath.Join(r.LoDir(), "remotes", name))
	if err != nil {
		return "", fmt.Errorf("remote not found: %s", name)
	}
	return strings.TrimSpace(string(data)), nil
}

// ListRemotes returns all configured remotes.
func (r *Repository) ListRemotes() ([]Remote, error) {
	remotesDir := filepath.Join(r.LoDir(), "remotes")
	entries, err := ioutil.ReadDir(remotesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var remotes []Remote
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := ioutil.ReadFile(filepath.Join(remotesDir, entry.Name()))
		if err != nil {
			continue
		}
		remotes = append(remotes, Remote{
			Name: entry.Name(),
			URL:  strings.TrimSpace(string(data)),
		})
	}

	sort.Slice(remotes, func(i, j int) bool {
		return remotes[i].Name < remotes[j].Name
	})
	return remotes, nil
}

// RemoveRemote deletes a remote configuration.
func (r *Repository) RemoveRemote(name string) error {
	return os.Remove(filepath.Join(r.LoDir(), "remotes", name))
}

// ---- LFS / lazy large file support ----

// LfsFile describes the status of a large file in the working tree.
type LfsFile struct {
	Path   string
	OS     uint8 // OS ID; 0 means all OSes
	Size   int64
	Hash   core.Hash
	OnDisk bool // true if all chunks available locally
}

// hasAllChunks checks whether all chunk blobs referenced by a manifest exist.
func (r *Repository) hasAllChunks(manifestHash core.Hash) bool {
	manifest, err := r.LoadChunkManifest(manifestHash)
	if err != nil {
		return false
	}
	for _, chunk := range manifest.Chunks {
		if chunk.Hash.IsZero() || !r.HasObject(chunk.Hash) {
			return false
		}
	}
	return true
}

// LfsStatus returns the status of all large (chunked) files in the index.
func (r *Repository) LfsStatus() ([]LfsFile, error) {
	idx, err := r.LoadIndex()
	if err != nil {
		return nil, err
	}

	var files []LfsFile
	for path, entry := range idx.Entries {
		objType, err := r.ObjectType(entry.Hash)
		if err != nil {
			continue
		}
		if objType != core.ObjectChunkManifest {
			continue
		}
		cleanPath, osTag := parseKey(path)
		files = append(files, LfsFile{
			Path:   cleanPath,
			OS:     osTag,
			Size:   entry.Size,
			Hash:   entry.Hash,
			OnDisk: r.hasAllChunks(entry.Hash),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].Path != files[j].Path {
			return files[i].Path < files[j].Path
		}
		return files[i].OS < files[j].OS
	})
	return files, nil
}

// LfsPull fetches the chunk blobs for a single large file from the remote
// and replaces the placeholder in the working tree with the real content.
func (r *Repository) LfsPull(remoteName string, filePath string) error {
	// The path is joined onto the working tree root below, so it must not
	// escape. The index lookup that follows already filters to known entries,
	// but checking the argument directly keeps this function safe on its own.
	if !safeRepoPath(filePath) {
		return fmt.Errorf("path outside repository: %s", filePath)
	}

	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	// Find entry by clean path (handles OS-tagged composite keys)
	entry, ok := idx.Entries[filePath]
	if !ok {
		key := entryKey(filePath, currentOS())
		entry, ok = idx.Entries[key]
	}
	if !ok {
		for k, e := range idx.Entries {
			if cleanPath, _ := parseKey(k); cleanPath == filePath {
				entry = e
				ok = true
				break
			}
		}
	}
	if !ok {
		return fmt.Errorf("file not in index: %s", filePath)
	}

	objType, err := r.ObjectType(entry.Hash)
	if err != nil {
		return fmt.Errorf("load object type: %w", err)
	}
	if objType != core.ObjectChunkManifest {
		return fmt.Errorf("not a large file: %s", filePath)
	}

	remoteURL, err := r.LoadRemote(remoteName)
	if err != nil {
		return fmt.Errorf("remote not found: %s", remoteName)
	}

	// Load manifest and copy missing chunks
	manifest, err := r.LoadChunkManifest(entry.Hash)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	total := len(manifest.Chunks)
	downloaded := 0
	for i, chunk := range manifest.Chunks {
		if !r.HasObject(chunk.Hash) {
			if total > 1 {
				printProgress(" pulling chunks: %d/%d", i+1, total)
			}
			if err := r.copyObjectFromRemote(remoteURL, chunk.Hash); err != nil {
				return fmt.Errorf("copy chunk %s: %w", chunk.Hash.Short(), err)
			}
			downloaded++
		}
	}
	if total > 1 && downloaded > 0 {
		printProgress("pulled %d chunk(s)", downloaded)
		endProgressLine()
	}

	// Reconstruct and write real file content
	fileData, err := r.LoadChunkedFile(entry.Hash)
	if err != nil {
		return fmt.Errorf("reconstruct file: %w", err)
	}
	fullPath, err := r.writeWorkTreeFile(filePath, fileData, entry.Mode)
	if err != nil {
		return err
	}

	var mt int64
	if fi, err := os.Lstat(fullPath); err == nil {
		mt = fi.ModTime().UnixNano()
	}

	// Update index — mark as no longer lazy
	idx.Entries[filePath] = IndexEntry{
		Hash:        entry.Hash,
		ContentHash: core.HashFromBytes(fileData),
		Size:        entry.Size,
		Mode:        entry.Mode,
		Lazy:        false,
		Mtime:       mt,
		OSS:         entry.OSS,
	}
	return r.SaveIndex(idx)
}

// ---- Object discovery ----

// collectObjects walks the commit DAG from hash in r, collecting all object
// hashes (commits, trees, blobs, chunk manifests, chunks) that are NOT
// already present in boundary. Stops recursing at objects boundary has.
// When lazy is true, chunk blob hashes are skipped (only manifests are collected).
func (r *Repository) collectObjects(boundary *Repository, hash core.Hash, lazy bool) (map[core.Hash]bool, error) {
	set := make(map[core.Hash]bool)
	visited := make(map[core.Hash]bool) // DAG cycle detection

	var walk func(h core.Hash) error
	walk = func(h core.Hash) error {
		if h.IsZero() || visited[h] {
			return nil
		}
		visited[h] = true

		if boundary.HasObject(h) {
			return nil
		}

		commit, err := r.LoadCommit(h)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\rwarning: commit %s missing, skipping\n", h.Short())
			return nil
		}

		set[h] = true

		if err := r.collectTreeRec(boundary, set, commit.Tree, lazy); err != nil {
			return err
		}

		for _, p := range commit.Parents {
			if err := walk(p); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(hash); err != nil {
		return nil, err
	}
	return set, nil
}

// collectTreeRec adds a tree and all its entry hashes to set.
// For chunk manifests, also adds the referenced chunk blobs (unless lazy).
func (r *Repository) collectTreeRec(boundary *Repository, set map[core.Hash]bool, treeHash core.Hash, lazy bool) error {
	if treeHash.IsZero() || set[treeHash] || boundary.HasObject(treeHash) {
		return nil
	}

	tree, err := r.LoadTree(treeHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\rwarning: tree %s missing, skipping\n", treeHash.Short())
		return nil
	}

	set[treeHash] = true

	for _, entry := range tree.Entries {
		if entry.Hash.IsZero() || set[entry.Hash] || boundary.HasObject(entry.Hash) {
			continue
		}

		// Submodule hashes live in submodule repos, not in this store
		if IsSubmoduleMode(entry.Mode) {
			continue
		}

		objType, err := r.ObjectType(entry.Hash)
		if err != nil {
			// Object missing locally — skip it
			fmt.Fprintf(os.Stderr, "\rwarning: object %s (%s) missing, skipping\n", entry.Hash.Short(), entry.Name)
			continue
		}

		if objType == core.ObjectChunkManifest {
			set[entry.Hash] = true
			if !lazy {
				manifest, err := r.LoadChunkManifest(entry.Hash)
				if err != nil {
					return err
				}
				for _, chunk := range manifest.Chunks {
					if !chunk.Hash.IsZero() && !set[chunk.Hash] && !boundary.HasObject(chunk.Hash) {
						set[chunk.Hash] = true
					}
				}
			}
		} else {
			set[entry.Hash] = true
		}
		if len(set)%5000 < 10 {
			printProgress("scanning: %d objects...", len(set))
		}

	}

	return nil
}

// ---- Object transfer ----

// copyObject copies a raw object file from src to dst with integrity
// checking and atomic write (temp file + rename) to prevent corruption
// from interrupted transfers.
func copyObject(src, dst *Repository, hash core.Hash) error {
	srcPath := src.objectPath(hash)

	data, err := ioutil.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source object %s: %w", hash.Short(), err)
	}

	// Verify content integrity against its content-addressable hash
	if h := core.HashFromBytes(data); h != hash {
		return fmt.Errorf("integrity check failed for %s: hash mismatch", hash.Short())
	}

	dstPath := dst.objectPath(hash)
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("create object dir: %w", err)
	}

	// Atomic write: temp file + rename to prevent partial files
	tmpPath := dstPath + ".tmp"
	if err := ioutil.WriteFile(tmpPath, data, 0644); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write object %s: %w", hash.Short(), err)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename object %s: %w", hash.Short(), err)
	}

	return nil
}

// copyObjectFromRemote reads an object from a remote URL (local, HTTP, or SSH)
// and writes it to the local object store with integrity checking and atomic write.
func (r *Repository) copyObjectFromRemote(remoteURL string, hash core.Hash) error {
	var data []byte
	var err error

	switch {
	case strings.HasPrefix(remoteURL, "http://") || strings.HasPrefix(remoteURL, "https://"):
		data, err = httpReadObject(remoteURL, hash)
	case strings.HasPrefix(remoteURL, "ssh://") || (strings.Contains(remoteURL, "@") && strings.Contains(remoteURL, ":")):
		var host, repoPath string
		host, repoPath, err = sshParseURL(remoteURL)
		if err == nil {
			data, err = sshReadObject(host, repoPath, hash)
		}
	default:
		var remoteRepo *Repository
		remoteRepo, err = Open(remoteURL)
		if err == nil {
			data, err = ioutil.ReadFile(remoteRepo.objectPath(hash))
		}
	}
	if err != nil {
		return fmt.Errorf("read %s from remote: %w", hash.Short(), err)
	}

	// Verify content integrity
	if h := core.HashFromBytes(data); h != hash {
		return fmt.Errorf("integrity check failed for %s: hash mismatch", hash.Short())
	}

	dstPath := r.objectPath(hash)
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("create object dir: %w", err)
	}

	// Atomic write
	tmpPath := dstPath + ".tmp"
	if err := ioutil.WriteFile(tmpPath, data, 0644); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write object %s: %w", hash.Short(), err)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename object %s: %w", hash.Short(), err)
	}

	return nil
}

// ---- High-level operations ----

// Fetch fetches remote-tracking refs and all non-LFS objects from the named
// remote. LFS chunk data is skipped — use LfsPull to retrieve on demand.
func (r *Repository) Fetch(remoteName string) error {
	return r.fetch(remoteName, true)
}

// fetch is the internal implementation of Fetch. When lazy is true,
// chunk blobs are skipped (only manifests are copied).
func (r *Repository) fetch(remoteName string, lazy bool) error {
	remoteURL, err := r.LoadRemote(remoteName)
	if err != nil {
		return fmt.Errorf("remote not found: %s", remoteName)
	}

	if strings.HasPrefix(remoteURL, "http://") || strings.HasPrefix(remoteURL, "https://") {
		return r.fetchHTTP(remoteURL, remoteName, lazy)
	}
	if strings.HasPrefix(remoteURL, "ssh://") || strings.Contains(remoteURL, "@") && strings.Contains(remoteURL, ":") {
		host, repoPath, err := sshParseURL(remoteURL)
		if err != nil {
			return err
		}
		return r.fetchSSH(host, repoPath, remoteName)
	}

	// Local path
	remoteRepo, err := Open(remoteURL)
	if err != nil {
		return fmt.Errorf("open remote %s: %w", remoteURL, err)
	}

	branches, _, err := remoteRepo.ListBranches()
	if err != nil {
		return err
	}

	allObjects := make(map[core.Hash]bool)
	branchRefs := make(map[string]core.Hash)

	for _, branchName := range branches {
		hashStr, err := remoteRepo.ReadRef("refs/heads/" + branchName)
		if err != nil {
			continue
		}
		hash, err := core.HashFromHex(hashStr)
		if err != nil {
			continue
		}
		branchRefs[branchName] = hash

		objects, err := remoteRepo.collectObjects(r, hash, lazy)
		if err != nil {
			return fmt.Errorf("collect objects for %s: %w", branchName, err)
		}
		for h := range objects {
			allObjects[h] = true
		}
	}

	total := len(allObjects)
	i := 0
	for h := range allObjects {
		i++
		if total > 1 {
			printProgress("fetching objects: %d/%d", i, total)
		}
		if err := copyObject(remoteRepo, r, h); err != nil {
			return fmt.Errorf("copy object %s: %w", h.Short(), err)
		}
	}
	if total > 1 {
		printProgress("fetching objects: %d/%d done", i, total)
		endProgressLine()
	}

	for branchName, hash := range branchRefs {
		ref := "refs/remotes/" + remoteName + "/" + branchName
		if err := r.WriteRef(ref, hash.String()); err != nil {
			return fmt.Errorf("write ref %s: %w", ref, err)
		}
	}

	return nil
}

// Push pushes all local branches to the named remote. force allows a branch
// whose update is not a fast-forward to overwrite the target's commits.
func (r *Repository) Push(remoteName string, force bool) error {
	remoteURL, err := r.LoadRemote(remoteName)
	if err != nil {
		return fmt.Errorf("remote not found: %s", remoteName)
	}

	if strings.HasPrefix(remoteURL, "http://") || strings.HasPrefix(remoteURL, "https://") {
		return r.pushHTTP(remoteURL, remoteName, force)
	}
	if strings.HasPrefix(remoteURL, "ssh://") || strings.Contains(remoteURL, "@") && strings.Contains(remoteURL, ":") {
		host, repoPath, err := sshParseURL(remoteURL)
		if err != nil {
			return err
		}
		return r.pushSSH(host, repoPath, remoteName, force)
	}

	// Local path
	remoteRepo, err := Open(remoteURL)
	if err != nil {
		return fmt.Errorf("open remote %s: %w", remoteURL, err)
	}

	branches, _, err := r.ListBranches()
	if err != nil {
		return err
	}
	sort.Strings(branches)

	// Preflight every branch before transferring anything, so a refusal leaves
	// the target untouched. refNames is sorted, which is what makes the branch
	// named in the error the same on every run now that one bad branch aborts
	// the whole push.
	refNames, tips := r.localTips(branches)
	st := targetState(remoteRepo, refNames)
	for _, ref := range refNames {
		if err := r.checkPushRef(st, ref, tips[ref], force); err != nil {
			return err
		}
	}

	allObjects := make(map[core.Hash]bool)
	branchRefs := make(map[string]core.Hash)

	fmt.Fprintf(os.Stderr, "scanning repository...\n")
	for _, branchName := range branches {
		hashStr, ok := tips["refs/heads/"+branchName]
		if !ok {
			continue
		}
		hash, err := core.HashFromHex(hashStr)
		if err != nil {
			continue
		}
		branchRefs[branchName] = hash

		objects, err := r.collectObjects(remoteRepo, hash, false)
		if err != nil {
			return fmt.Errorf("collect objects for %s: %w", branchName, err)
		}
		for h := range objects {
			allObjects[h] = true
		}
	}

	total := len(allObjects)
	if total == 0 {
		// No object has to move, but a ref may still be missing: an
		// interrupted push writes objects before refs, and this retry has to
		// finish that rather than report success and stop.
		fmt.Fprintf(os.Stderr, "everything up to date\n")
	}
	if total > 1 {
		printProgress("found %d objects to push", total)
		endProgressLine()
	}
	i := 0
	for h := range allObjects {
		i++
		if total > 1 {
			printProgress("pushing objects: %d/%d", i, total)
		}
		if err := copyObject(r, remoteRepo, h); err != nil {
			return fmt.Errorf("copy object %s: %w", h.Short(), err)
		}
	}
	if total > 1 {
		printProgress("pushing objects: %d/%d done", i, total)
		endProgressLine()
	}
	// Write the refs in the order the preflight used. A ref already pointing
	// where it should is left alone; every other one is written even when no
	// object had to move.
	wroteRef := false
	for _, branchName := range branches {
		hash, ok := branchRefs[branchName]
		if !ok {
			continue
		}
		ref := "refs/heads/" + branchName
		if st.Refs[ref] == hash.String() {
			continue
		}
		if err := remoteRepo.WriteRef(ref, hash.String()); err != nil {
			return fmt.Errorf("write ref %s: %w", ref, err)
		}
		wroteRef = true
	}
	if total > 0 || wroteRef {
		fmt.Fprintf(os.Stderr, "pushed to %s\n", remoteName)
	}

	return nil
}

// collectOSChunks walks the commit tree and collects chunk blob hashes
// that are needed for the current OS but not yet present locally.
func (r *Repository) collectOSChunks(commitHash core.Hash) (map[core.Hash]bool, error) {
	needed := make(map[core.Hash]bool)
	cOS := currentOS()

	commit, err := r.LoadCommit(commitHash)
	if err != nil {
		return nil, err
	}
	tree, err := r.LoadTree(commit.Tree)
	if err != nil {
		return nil, err
	}
	chunkThreshold := int64(r.Config.Core.ChunkThreshold)
	for i, entry := range tree.Entries {
		if !osMatch(entry.OSS, cOS) {
			continue
		}
		if IsSubmoduleMode(entry.Mode) || entry.Hash.IsZero() {
			continue
		}
		if i%10000 == 0 {
			fmt.Fprintf(os.Stderr, "  scanning tree: %d/%d entries...", i, len(tree.Entries))
		}
		// Files below chunk threshold are never stored as chunk manifests
		if entry.Size < chunkThreshold {
			continue
		}
		objType, err := r.ObjectType(entry.Hash)
		if err != nil || objType != core.ObjectChunkManifest {
			continue
		}
		manifest, err := r.LoadChunkManifest(entry.Hash)
		if err != nil {
			continue
		}
		for _, chunk := range manifest.Chunks {
			if !chunk.Hash.IsZero() && !r.HasObject(chunk.Hash) {
				needed[chunk.Hash] = true
			}
		}
	}
	return needed, nil
}

// Pull fetches lightweight data first, then selectively pulls chunk blobs
// needed for the current OS, then merges.
func (r *Repository) Pull(remoteName string) (*MergeResult, error) {
	remoteURL, err := r.LoadRemote(remoteName)
	if err != nil {
		return nil, fmt.Errorf("remote not found: %s", remoteName)
	}

	// Step 1: Lightweight fetch — manifests and small files only, no chunk blobs
	if err := r.fetch(remoteName, true); err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	branch := r.CurrentBranch()
	if branch == "" {
		return nil, fmt.Errorf("not on any branch")
	}

	trackingRef := "refs/remotes/" + remoteName + "/" + branch
	hashStr, err := r.ReadRef(trackingRef)
	if err != nil {
		return nil, fmt.Errorf("no tracking ref for %s/%s", remoteName, branch)
	}
	hash, err := core.HashFromHex(hashStr)
	if err != nil {
		return nil, err
	}

	// Step 2: Collect chunk hashes needed for current OS
	fmt.Fprintf(os.Stderr, "scanning tree...\n")
	needed, err := r.collectOSChunks(hash)
	if err != nil {
		return nil, fmt.Errorf("collect needed chunks: %w", err)
	}

	// Step 3: Fetch only those chunk blobs from remote
	total := len(needed)
	if total > 0 {
		fmt.Fprintf(os.Stderr, "pulling %d large file chunks...\n", total)
		i := 0
		for h := range needed {
			i++
			if total > 1 {
				printProgress("  chunks: %d/%d", i, total)
			}
			if err := r.copyObjectFromRemote(remoteURL, h); err != nil {
				fmt.Fprintf(os.Stderr, "\nwarning: failed to fetch chunk %s: %s\n", h.Short(), err)
			}
		}
		if total > 1 {
			printProgress("  chunks: %d/%d done", i, total)
			endProgressLine()
		}
	}

	// Step 4: Merge
	fmt.Fprintf(os.Stderr, "merging...\n")
	return r.mergeCommit(hash, remoteName+"/"+branch)
}

// Clone initializes a new repository, adds a remote, fetches its objects,
// and checks out the default branch. When lazy is true, large file chunks
// are not fetched (placeholders are written instead).
func Clone(url, dir string, lazy bool) (*Repository, error) {
	r, err := Init(dir)
	if err != nil {
		return nil, fmt.Errorf("init: %w", err)
	}

	if err := r.SaveRemote("origin", url); err != nil {
		return nil, err
	}

	if err := r.fetch("origin", lazy); err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	// Determine the default branch from the remote's HEAD
	headRef, err := remoteRef(url, "HEAD")
	if err != nil {
		headRef = "ref: refs/heads/main"
	}

	branchName := "main"
	if len(headRef) > 5 && headRef[:4] == "ref:" {
		ref := strings.TrimSpace(headRef[5:])
		if strings.HasPrefix(ref, "refs/heads/") {
			branchName = ref[11:]
		}
	}

	// Create local branch from remote-tracking ref
	trackingRef := "refs/remotes/origin/" + branchName
	hashStr, err := r.ReadRef(trackingRef)
	if err != nil {
		// Fall back to any available remote-tracking ref
		branches, _ := r.remoteBranches(url, "origin")
		if len(branches) > 0 {
			branchName = branches[0]
			trackingRef = "refs/remotes/origin/" + branchName
			hashStr, err = r.ReadRef(trackingRef)
		}
	}

	if hashStr != "" {
		// Remote has commits — create local branch and check it out
		if err := r.WriteRef("refs/heads/"+branchName, hashStr); err != nil {
			return nil, fmt.Errorf("create local branch: %w", err)
		}
		if err := r.SwitchBranch(branchName); err != nil {
			return nil, fmt.Errorf("checkout branch: %w", err)
		}
	} else {
		// Empty remote — set HEAD to match the remote's default branch name
		fmt.Printf("warning: cloned an empty repository\n")
		if err := r.SetHEAD("ref: refs/heads/" + branchName); err != nil {
			return nil, err
		}
	}

	return r, nil
}

// ---- push preflight ----

// remoteState is a snapshot of a push target, gathered before any object
// transfer. Each transport builds it its own way; the decisions below are
// written once.
type remoteState struct {
	// Bare is the target's core.bare. A missing or unreadable value reads as
	// false, i.e. as a checkout — an old server that does not publish the key
	// is refused conservatively rather than trusted.
	Bare bool
	// HeadBranch is the branch the target has checked out, or "" when HEAD is
	// detached, unreadable or not a branch. Nothing is protected then.
	HeadBranch string
	Refs       map[string]string // full ref name -> hash
}

// targetState snapshots the repository a push is about to write into. Both
// sides that have the target open — the local-path transport and the HTTP
// server — build their state here.
func targetState(t *Repository, refs []string) remoteState {
	st := remoteState{
		Bare: t.Config.Core.Bare,
		// CurrentBranch is "" for a detached HEAD, which protects nothing.
		HeadBranch: t.CurrentBranch(),
		Refs:       make(map[string]string, len(refs)),
	}
	for _, ref := range refs {
		v, err := t.ReadRef(ref)
		if err != nil {
			continue // absent: the ref is being created
		}
		if v = strings.TrimSpace(v); v != "" {
			st.Refs[ref] = v
		}
	}
	return st
}

// headBranchFromHEAD extracts the branch from a raw symbolic HEAD value
// ("ref: refs/heads/main") for the transports that read the file rather than
// asking the repository. A detached HEAD holds a hash instead, and that is not
// a branch: it protects nothing, so it answers the empty string — as does
// anything else that is not under refs/heads/.
func headBranchFromHEAD(head string) string {
	ref := strings.TrimSpace(head)
	if !strings.HasPrefix(ref, "ref:") {
		return ""
	}
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "ref:"))
	if !strings.HasPrefix(ref, "refs/heads/") {
		return ""
	}
	return strings.TrimPrefix(ref, "refs/heads/")
}

// localTips reads the tip of every named branch, returned in the order given
// (the callers sort first) alongside a ref-name lookup. The tips are read once
// and then used both to decide and to write, so the value a refusal describes
// is the value that would have been pushed rather than a second read a
// concurrent commit could have separated from the first.
//
// A branch whose ref cannot be read is left out entirely, so it is neither
// checked nor written — an unreadable ref is not a licence to skip the check.
func (r *Repository) localTips(branches []string) ([]string, map[string]string) {
	refs := make([]string, 0, len(branches))
	tips := make(map[string]string, len(branches))
	for _, branchName := range branches {
		ref := "refs/heads/" + branchName
		tip, err := r.ReadRef(ref)
		if err != nil {
			continue
		}
		refs = append(refs, ref)
		tips[ref] = tip
	}
	return refs, tips
}

// checkPushRef reports why writing localTip to ref must be refused, or nil.
// force overrides the fast-forward rule and nothing else.
//
// The fast-forward test needs no objects from the target: fast-forward means
// the target's current value is an ancestor of localTip, and every ancestor of
// localTip is in this repository. A target tip that is not here is therefore
// decisively not an ancestor — not merely unknown — which is exactly the
// ordinary case of pushing over someone else's commits.
func (r *Repository) checkPushRef(st remoteState, ref, localTip string, force bool) error {
	// Checked-out first, so that a push which trips both rules reports the one
	// the user has to act on.
	if st.HeadBranch != "" && !st.Bare {
		if ref == "refs/heads/"+st.HeadBranch {
			return fmt.Errorf("refusing to update %s: checked out in the target (non-bare repository); create push targets with 'qin init --bare'", ref)
		}
	}

	remoteTip := st.Refs[ref]
	if remoteTip == "" || remoteTip == localTip {
		return nil // creating the ref, or already up to date
	}

	anc, err := core.HashFromHex(remoteTip)
	if err != nil {
		return fmt.Errorf("refusing to update %s: the target has an unreadable value for it (%q)", ref, remoteTip)
	}
	desc, err := core.HashFromHex(localTip)
	if err != nil {
		return fmt.Errorf("refusing to update %s: %q is not a hash", ref, localTip)
	}
	if r.IsAncestor(anc, desc) || force {
		return nil
	}
	return fmt.Errorf("refusing to update %s: not a fast-forward (the target has commits that are not in your history); overwrite with --force (orphaned commits stay recoverable via 'qin lost-found' until 'qin gc' runs)", ref)
}

// remoteRef reads a ref from a remote URL (supports local path, HTTP, SSH).
func remoteRef(remoteURL, ref string) (string, error) {
	if strings.HasPrefix(remoteURL, "http://") || strings.HasPrefix(remoteURL, "https://") {
		refs, err := httpListRefs(remoteURL)
		if err != nil {
			return "", err
		}
		if v, ok := refs[ref]; ok {
			return v, nil
		}
		return "", fmt.Errorf("ref not found: %s", ref)
	}
	if strings.HasPrefix(remoteURL, "ssh://") || (strings.Contains(remoteURL, "@") && strings.Contains(remoteURL, ":")) {
		host, repoPath, err := sshParseURL(remoteURL)
		if err != nil {
			return "", err
		}
		return sshReadRef(host, repoPath, ref)
	}
	// Local path
	r, err := Open(remoteURL)
	if err != nil {
		return "", err
	}
	return r.ReadRef(ref)
}

// remoteBranches lists branch names from a remote URL (supports local path, HTTP, SSH).
func (r *Repository) remoteBranches(remoteURL, remoteName string) ([]string, error) {
	if strings.HasPrefix(remoteURL, "http://") || strings.HasPrefix(remoteURL, "https://") {
		refs, err := httpListRefs(remoteURL)
		if err != nil {
			return nil, err
		}
		var branches []string
		for ref := range refs {
			if strings.HasPrefix(ref, "refs/heads/") {
				branches = append(branches, strings.TrimPrefix(ref, "refs/heads/"))
			}
		}
		sort.Strings(branches)
		return branches, nil
	}
	if strings.HasPrefix(remoteURL, "ssh://") || (strings.Contains(remoteURL, "@") && strings.Contains(remoteURL, ":")) {
		host, repoPath, err := sshParseURL(remoteURL)
		if err != nil {
			return nil, err
		}
		refs, err := sshListRefs(host, repoPath)
		if err != nil {
			return nil, err
		}
		var branches []string
		for ref := range refs {
			if strings.HasPrefix(ref, "refs/heads/") {
				branches = append(branches, strings.TrimPrefix(ref, "refs/heads/"))
			}
		}
		sort.Strings(branches)
		return branches, nil
	}
	// Local path — open remote repo and list its branches
	remoteRepo, err := Open(remoteURL)
	if err != nil {
		return nil, err
	}
	branches, _, err := remoteRepo.ListBranches()
	return branches, err
}
