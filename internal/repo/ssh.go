package repo

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhsoft88/qin/internal/core"
)

// ---- SSH helpers ----

// sshParseURL parses ssh://user@host/path or user@host:path style URLs.
func sshParseURL(raw string) (userHost, repoPath string, err error) {
	// ssh://user@host/path
	if strings.HasPrefix(raw, "ssh://") {
		rest := strings.TrimPrefix(raw, "ssh://")
		idx := strings.IndexByte(rest, '/')
		if idx < 0 {
			return "", "", fmt.Errorf("invalid ssh url: %s", raw)
		}
		host := rest[:idx]
		path := rest[idx:]
		if len(path) <= 1 {
			return "", "", fmt.Errorf("invalid ssh url: %s", raw)
		}
		return host, path, nil
	}
	// scp-style: user@host:path
	if idx := strings.IndexByte(raw, ':'); idx > 0 && idx < len(raw)-1 {
		host := raw[:idx]
		path := raw[idx+1:]
		if !strings.Contains(host, "/") {
			return host, path, nil
		}
	}
	return "", "", fmt.Errorf("invalid ssh url: %s (use ssh://user@host/path or user@host:path)", raw)
}

func sshRun(host, cmd string) ([]byte, error) {
	return exec.Command("ssh", host, cmd).Output()
}

func sshStdin(host, cmd string, stdinData []byte) error {
	cmdObj := exec.Command("ssh", host, cmd)
	pipe, err := cmdObj.StdinPipe()
	if err != nil {
		return err
	}
	go func() {
		pipe.Write(stdinData)
		pipe.Close()
	}()
	out, err := cmdObj.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh %s: %s", host, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- SSH remote operations ----

// shellQuote protects one argument from the remote shell. Remote paths come
// from the user's URL or repository name; without quoting a space splits the
// command and a metacharacter runs whatever it spells.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func sshReadRef(host, repoPath, ref string) (string, error) {
	cmd := fmt.Sprintf("cat %s", shellQuote(filepath.Join(repoPath, QinDir, ref)))
	out, err := sshRun(host, cmd)
	if err != nil {
		return "", fmt.Errorf("read ref %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// sshRefKey maps a path the remote's find printed under .qin to the ref name
// callers select on. The result is what makes an SSH listing the same shape as
// the HTTP and local ones — full ref names, "refs/heads/main".
//
// It is a function of its own because getting it wrong is invisible: the keys
// were once relative to .qin/refs, so they read "heads/main", matched no
// caller's "refs/heads/" prefix, and SSH fetch took no objects while SSH clone
// produced an empty repository — every command exiting 0. A shape like that can
// be tested without an SSH server, and is.
func sshRefKey(qinDir, path string) string {
	rel, err := filepath.Rel(qinDir, path)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

func sshListRefs(host, repoPath string) (map[string]string, error) {
	qinDir := filepath.Join(repoPath, QinDir)
	refsDir := filepath.Join(qinDir, "refs")
	cmd := fmt.Sprintf("find %s -type f", shellQuote(refsDir))
	out, err := sshRun(host, cmd)
	if err != nil {
		return nil, fmt.Errorf("list refs: %w", err)
	}

	refs := make(map[string]string)
	for _, path := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path = strings.TrimSpace(path)
		if path == "" || hasDotComponent(path) {
			continue // an atomic write's temp file, not a ref
		}
		ref := sshRefKey(qinDir, path)
		if ref == "" {
			continue
		}
		data, err := sshRun(host, fmt.Sprintf("cat %s", shellQuote(path)))
		if err != nil {
			continue
		}
		refs[ref] = strings.TrimSpace(string(data))
	}
	return refs, nil
}

// bareFromConfigJSON reports whether a .qin/config body marks the repository
// bare. Anything unreadable or unrecognised answers false. That direction is
// the safe one here: the SSH target has no receiving-side code to refuse a bad
// push, so "unknown" has to mean "treat it as a checkout".
func bareFromConfigJSON(data []byte) bool {
	var cfg struct {
		Core struct {
			Bare bool `json:"bare"`
		} `json:"core"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return cfg.Core.Bare
}

// sshReadBare reads the target's core.bare, the one fact about it that the
// refs cannot tell us. The cat is plumbing; the decision is in
// bareFromConfigJSON.
func sshReadBare(host, repoPath string) bool {
	out, err := sshRun(host, fmt.Sprintf("cat %s", shellQuote(filepath.Join(repoPath, QinDir, "config"))))
	if err != nil {
		return false
	}
	return bareFromConfigJSON(out)
}

func sshHasObject(host, repoPath string, hash core.Hash) bool {
	objPath := rObjPath(repoPath, hash)
	_, err := sshRun(host, fmt.Sprintf("test -f %s", shellQuote(objPath)))
	return err == nil
}

// rObjPath builds the XX/YYYYYY path for a hash, relative to the objects dir.
func rObjPath(repoPath string, hash core.Hash) string {
	s := hash.String()
	return filepath.Join(repoPath, QinDir, "objects", s[:2], s[2:])
}

func sshReadObject(host, repoPath string, hash core.Hash) ([]byte, error) {
	cmd := fmt.Sprintf("cat %s", shellQuote(rObjPath(repoPath, hash)))
	return sshRun(host, cmd)
}

// sshAtomicWriteCmd is the remote command that replaces finalPath's content
// with stdin. Writing straight to the final name leaves a truncated object
// behind when the connection drops, and existence checks only look at the
// name, so a later push would skip it forever. The temp name is dot-prefixed
// so a concurrent ref listing does not mistake it for a ref; $$ makes it
// unique per connection.
func sshAtomicWriteCmd(finalPath string) string {
	dir := filepath.Dir(finalPath)
	base := filepath.Base(finalPath)
	return fmt.Sprintf("mkdir -p %s && cat > %s/.tmp.$$.%s && mv %s/.tmp.$$.%s %s",
		shellQuote(dir), shellQuote(dir), shellQuote(base),
		shellQuote(dir), shellQuote(base), shellQuote(finalPath))
}

func sshWriteObject(host, repoPath string, hash core.Hash, data []byte) error {
	return sshStdin(host, sshAtomicWriteCmd(rObjPath(repoPath, hash)), data)
}

func sshWriteRef(host, repoPath, ref, hash string) error {
	refPath := filepath.Join(repoPath, QinDir, ref)
	return sshStdin(host, sshAtomicWriteCmd(refPath), []byte(hash+"\n"))
}

// hasDotComponent reports whether any path component starts with a dot. The
// remote's find interleaves temp files with refs, and a "." component would
// also mean the listing escaped the refs directory.
func hasDotComponent(p string) bool {
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// ---- SSH collect missing (for push: walk local DAG, check remote via SSH) ----

func (r *Repository) sshCollectMissing(host, repoPath string, hash core.Hash) (map[core.Hash]bool, error) {
	set := make(map[core.Hash]bool)
	visited := make(map[core.Hash]bool)

	var walk func(h core.Hash) error
	walk = func(h core.Hash) error {
		if h.IsZero() || visited[h] {
			return nil
		}
		visited[h] = true

		if sshHasObject(host, repoPath, h) {
			return nil
		}

		commit, err := r.LoadCommit(h)
		if err != nil {
			return fmt.Errorf("cannot read commit %s: %w", h.Short(), err)
		}

		set[h] = true

		if err := r.sshCollectTree(host, repoPath, set, commit.Tree); err != nil {
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

func (r *Repository) sshCollectTree(host, repoPath string, set map[core.Hash]bool, treeHash core.Hash) error {
	defer endProgressLine()
	if treeHash.IsZero() || set[treeHash] || sshHasObject(host, repoPath, treeHash) {
		return nil
	}

	tree, err := r.LoadTree(treeHash)
	if err != nil {
		return fmt.Errorf("cannot read tree %s: %w", treeHash.Short(), err)
	}
	set[treeHash] = true

	for _, entry := range tree.Entries {
		if entry.Hash.IsZero() || set[entry.Hash] || sshHasObject(host, repoPath, entry.Hash) {
			continue
		}

		objType, err := r.ObjectType(entry.Hash)
		if err != nil {
			return fmt.Errorf("cannot read object %s (%s): %w", entry.Hash.Short(), entry.Name, err)
		}

		if objType == core.ObjectChunkManifest {
			set[entry.Hash] = true
			manifest, err := r.LoadChunkManifest(entry.Hash)
			if err != nil {
				return err
			}
			for _, chunk := range manifest.Chunks {
				if !chunk.Hash.IsZero() && !set[chunk.Hash] && !sshHasObject(host, repoPath, chunk.Hash) {
					set[chunk.Hash] = true
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

// ---- SSH fetch/push ----

func (r *Repository) fetchSSH(host, repoPath, remoteName string, lazy bool) error {
	refs, err := sshListRefs(host, repoPath)
	if err != nil {
		return fmt.Errorf("list refs: %w", err)
	}

	allObjects := make(map[core.Hash]bool)
	branchRefs := make(map[string]core.Hash)

	for ref, hashStr := range refs {
		if !strings.HasPrefix(ref, "refs/heads/") {
			continue
		}
		branchName := strings.TrimPrefix(ref, "refs/heads/")
		hash, err := core.HashFromHex(hashStr)
		if err != nil {
			continue
		}
		branchRefs[branchName] = hash
	}

	// For each branch, collect all missing objects by walking the remote DAG
	for branchName, hash := range branchRefs {
		objects, err := r.sshCollectRemoteDAG(host, repoPath, hash, lazy)
		if err != nil {
			return fmt.Errorf("collect for %s: %w", branchName, err)
		}
		for h := range objects {
			allObjects[h] = true
		}
	}

	// Download objects
	total := len(allObjects)
	if total == 0 {
		stage("collect", "everything up to date")
	} else {
		stage("collect", "%d objects", total)
		i := 0
		var progress progressThrottle
		for h := range allObjects {
			i++
			progress.print("[transfer] %d/%d objects", i, total)
			data, err := sshReadObject(host, repoPath, h)
			if err != nil {
				endProgressLine()
				return fmt.Errorf("read object %s: %w", h.Short(), err)
			}
			if dh := core.HashFromBytes(data); dh != h {
				endProgressLine()
				return fmt.Errorf("integrity check failed for %s", h.Short())
			}
			dstPath := r.objectPath(h)
			if err := writeFileAtomic(dstPath, data, 0644); err != nil {
				endProgressLine()
				return fmt.Errorf("write object %s: %w", h.Short(), err)
			}
		}
		endProgressLine()
	}

	// Write remote-tracking refs
	updates := make(map[string]string, len(branchRefs))
	for branchName, hash := range branchRefs {
		updates["refs/remotes/"+remoteName+"/"+branchName] = hash.String()
	}
	if len(updates) > 0 {
		stage("refs", "updating %d refs", len(updates))
	}
	if err := r.applyRefUpdates(updates); err != nil {
		return err
	}

	return nil
}

func (r *Repository) pushSSH(host, repoPath, remoteName string, force bool) error {
	branches, _, err := r.ListBranches()
	if err != nil {
		return err
	}
	sort.Strings(branches)

	// The receiving side is a shell — there is no qin there to refuse
	// anything — so this preflight is the only guard an SSH target gets. It
	// reads each ref by its exact path, one ssh call apiece, rather than
	// listing refs: it needs HEAD and core.bare too, which are not refs, and
	// it needs only the refs it is about to write.
	refNames, tips := r.localTips(branches)
	head, _ := sshReadRef(host, repoPath, "HEAD")
	st := remoteState{
		Bare:       sshReadBare(host, repoPath),
		HeadBranch: headBranchFromHEAD(head),
		Refs:       make(map[string]string, len(refNames)),
	}
	for _, ref := range refNames {
		if v, err := sshReadRef(host, repoPath, ref); err == nil {
			if v = strings.TrimSpace(v); v != "" {
				st.Refs[ref] = v
			}
		}
	}
	for _, ref := range refNames {
		if err := r.checkPushRef(st, ref, tips[ref], force); err != nil {
			return err
		}
	}

	allObjects := make(map[core.Hash]bool)
	branchRefs := make(map[string]core.Hash)

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

		objects, err := r.sshCollectMissing(host, repoPath, hash)
		if err != nil {
			return fmt.Errorf("collect for %s: %w", branchName, err)
		}
		for h := range objects {
			allObjects[h] = true
		}
		printProgress("[collect] scanning: %d objects", len(allObjects))
	}

	// Upload objects
	total := len(allObjects)
	if total == 0 {
		stage("collect", "everything up to date")
	} else {
		stage("collect", "%d objects", total)
		i := 0
		var progress progressThrottle
		for h := range allObjects {
			i++
			progress.print("[transfer] %d/%d objects", i, total)
			data, err := ioutil.ReadFile(r.objectPath(h))
			if err != nil {
				endProgressLine()
				return fmt.Errorf("read %s: %w", h.Short(), err)
			}
			if err := sshWriteObject(host, repoPath, h, data); err != nil {
				endProgressLine()
				return fmt.Errorf("upload %s: %w", h.Short(), err)
			}
		}
		endProgressLine()
	}

	// Update refs, in the preflight's order.
	var updateRefs, updateHashes []string
	for _, branchName := range branches {
		hash, ok := branchRefs[branchName]
		if !ok {
			continue
		}
		ref := "refs/heads/" + branchName
		if st.Refs[ref] == hash.String() {
			continue
		}
		updateRefs = append(updateRefs, ref)
		updateHashes = append(updateHashes, hash.String())
	}
	if len(updateRefs) > 0 {
		stage("refs", "updating %d refs", len(updateRefs))
	}
	for i, ref := range updateRefs {
		if err := sshWriteRef(host, repoPath, ref, updateHashes[i]); err != nil {
			return fmt.Errorf("update ref %s: %w", ref, err)
		}
	}
	if total > 0 || len(updateRefs) > 0 {
		fmt.Fprintf(os.Stderr, "pushed to %s\n", remoteName)
	}

	return nil
}

// sshCollectRemoteDAG walks the remote commit DAG via SSH, collecting objects
// not present locally.
func (r *Repository) sshCollectRemoteDAG(host, repoPath string, hash core.Hash, lazy bool) (map[core.Hash]bool, error) {
	set := make(map[core.Hash]bool)
	visited := make(map[core.Hash]bool)

	var walk func(h core.Hash) error
	walk = func(h core.Hash) error {
		if h.IsZero() || visited[h] {
			return nil
		}
		visited[h] = true

		if r.HasObject(h) {
			return nil
		}

		raw, err := sshReadObject(host, repoPath, h)
		if err != nil {
			return fmt.Errorf("fetch commit %s: %w", h.Short(), err)
		}
		objType, content, err := core.DeserializeObject(raw)
		if err != nil {
			return err
		}
		if objType != core.ObjectCommit {
			return fmt.Errorf("expected commit at %s", h.Short())
		}
		var commit Commit
		if err := core.DeserializeJSON(content, &commit); err != nil {
			return err
		}

		set[h] = true

		// Collect tree via SSH
		treeSet, err := r.sshCollectRemoteTree(host, repoPath, commit.Tree, lazy)
		if err != nil {
			return err
		}
		for th := range treeSet {
			set[th] = true
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

func (r *Repository) sshCollectRemoteTree(host, repoPath string, treeHash core.Hash, lazy bool) (map[core.Hash]bool, error) {
	set := make(map[core.Hash]bool)
	visited := make(map[core.Hash]bool)

	var walk func(th core.Hash) error
	walk = func(th core.Hash) error {
		if th.IsZero() || visited[th] {
			return nil
		}
		visited[th] = true
		if r.HasObject(th) {
			return nil
		}

		raw, err := sshReadObject(host, repoPath, th)
		if err != nil {
			return fmt.Errorf("fetch tree %s: %w", th.Short(), err)
		}
		objType, content, err := core.DeserializeObject(raw)
		if err != nil {
			return err
		}
		if objType != core.ObjectTree {
			return fmt.Errorf("expected tree at %s", th.Short())
		}
		var tree Tree
		if err := core.DeserializeJSON(content, &tree); err != nil {
			return err
		}

		set[th] = true

		for _, entry := range tree.Entries {
			if entry.Hash.IsZero() || set[entry.Hash] || r.HasObject(entry.Hash) {
				continue
			}

			eRaw, err := sshReadObject(host, repoPath, entry.Hash)
			if err != nil {
				return fmt.Errorf("fetch entry %s: %w", entry.Hash.Short(), err)
			}
			eType, eContent, err := core.DeserializeObject(eRaw)
			if err != nil {
				return err
			}

			if eType == core.ObjectChunkManifest {
				// A manifest, not a file. The manifest itself always travels;
				// its chunks are the lazy part.
				set[entry.Hash] = true
				if lazy {
					continue
				}
				var manifest ChunkManifest
				if err := core.DeserializeJSON(eContent, &manifest); err != nil {
					return err
				}
				for _, chunk := range manifest.Chunks {
					if !chunk.Hash.IsZero() && !set[chunk.Hash] && !r.HasObject(chunk.Hash) {
						set[chunk.Hash] = true
					}
				}
			} else {
				set[entry.Hash] = true
			}
		}
		return nil
	}

	return set, walk(treeHash)
}
