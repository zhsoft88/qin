package repo

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
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

func sshReadRef(host, repoPath, ref string) (string, error) {
	cmd := fmt.Sprintf("cat %s", filepath.Join(repoPath, LoDir, ref))
	out, err := sshRun(host, cmd)
	if err != nil {
		return "", fmt.Errorf("read ref %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func sshListRefs(host, repoPath string) (map[string]string, error) {
	refsDir := filepath.Join(repoPath, LoDir, "refs")
	cmd := fmt.Sprintf("find %s -type f", refsDir)
	out, err := sshRun(host, cmd)
	if err != nil {
		return nil, fmt.Errorf("list refs: %w", err)
	}

	refs := make(map[string]string)
	for _, path := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		rel, _ := filepath.Rel(refsDir, path)
		data, err := sshRun(host, fmt.Sprintf("cat %s", path))
		if err != nil {
			continue
		}
		refs[filepath.ToSlash(rel)] = strings.TrimSpace(string(data))
	}
	return refs, nil
}

func sshHasObject(host, repoPath string, hash core.Hash) bool {
	objPath := rObjPath(repoPath, hash)
	_, err := sshRun(host, fmt.Sprintf("test -f %s", objPath))
	return err == nil
}

// rObjPath builds the XX/YYYYYY path for a hash, relative to the objects dir.
func rObjPath(repoPath string, hash core.Hash) string {
	s := hash.String()
	return filepath.Join(repoPath, LoDir, "objects", s[:2], s[2:])
}

func sshReadObject(host, repoPath string, hash core.Hash) ([]byte, error) {
	cmd := fmt.Sprintf("cat %s", rObjPath(repoPath, hash))
	return sshRun(host, cmd)
}

func sshWriteObject(host, repoPath string, hash core.Hash, data []byte) error {
	objPath := rObjPath(repoPath, hash)
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s", filepath.Dir(objPath), objPath)
	return sshStdin(host, cmd, data)
}

func sshWriteRef(host, repoPath, ref, hash string) error {
	refPath := filepath.Join(repoPath, LoDir, ref)
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s", filepath.Dir(refPath), refPath)
	return sshStdin(host, cmd, []byte(hash+"\n"))
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
			fmt.Fprintf(os.Stderr, "\r  warning: commit %s missing, skipping\n", h.Short())
			return nil
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
	if treeHash.IsZero() || set[treeHash] || sshHasObject(host, repoPath, treeHash) {
		return nil
	}

	tree, err := r.LoadTree(treeHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\r  warning: tree %s missing, skipping\n", treeHash.Short())
		return nil
	}
	set[treeHash] = true

	for _, entry := range tree.Entries {
		if entry.Hash.IsZero() || set[entry.Hash] || sshHasObject(host, repoPath, entry.Hash) {
			continue
		}

		objType, err := r.ObjectType(entry.Hash)
		if err != nil {
			// Object missing locally — skip it
			fmt.Fprintf(os.Stderr, "\r  warning: object %s (%s) missing, skipping\n", entry.Hash.Short(), entry.Name)
			continue
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
			fmt.Fprintf(os.Stderr, "  scanning: %d objects...", len(set))
		}
	}

	return nil
}

// ---- SSH fetch/push ----

func (r *Repository) fetchSSH(host, repoPath, remoteName string) error {
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
		objects, err := r.sshCollectRemoteDAG(host, repoPath, hash)
		if err != nil {
			return fmt.Errorf("collect for %s: %w", branchName, err)
		}
		for h := range objects {
			allObjects[h] = true
		}
	}

	// Download objects
	total := len(allObjects)
	i := 0
	for h := range allObjects {
		i++
		if total > 1 {
			fmt.Fprintf(os.Stderr, "\r  fetching objects: %d/%d", i, total)
		}
		data, err := sshReadObject(host, repoPath, h)
		if err != nil {
			return fmt.Errorf("read object %s: %w", h.Short(), err)
		}
		if dh := core.HashFromBytes(data); dh != h {
			return fmt.Errorf("integrity check failed for %s", h.Short())
		}
		dstPath := r.objectPath(h)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return fmt.Errorf("create dir: %w", err)
		}
		tmpPath := dstPath + ".tmp"
		if err := ioutil.WriteFile(tmpPath, data, 0644); err != nil {
			os.Remove(tmpPath)
			return err
		}
		if err := os.Rename(tmpPath, dstPath); err != nil {
			os.Remove(tmpPath)
			return err
		}
	}
	if total > 1 {
		fmt.Fprintf(os.Stderr, "\r  fetching objects: %d/%d done\n", i, total)
	}

	// Write remote-tracking refs
	for branchName, hash := range branchRefs {
		ref := "refs/remotes/" + remoteName + "/" + branchName
		if err := r.WriteRef(ref, hash.String()); err != nil {
			return fmt.Errorf("write ref: %w", err)
		}
	}

	return nil
}

func (r *Repository) pushSSH(host, repoPath, remoteName string) error {
	branches, _, err := r.ListBranches()
	if err != nil {
		return err
	}

	allObjects := make(map[core.Hash]bool)
	branchRefs := make(map[string]core.Hash)

	for _, branchName := range branches {
		hashStr, err := r.ReadRef("refs/heads/" + branchName)
		if err != nil {
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
	}

	// Upload objects
	total := len(allObjects)
	i := 0
	for h := range allObjects {
		i++
		if total > 1 {
			fmt.Fprintf(os.Stderr, "\r  pushing objects: %d/%d", i, total)
		}
		data, err := ioutil.ReadFile(r.objectPath(h))
		if err != nil {
			return fmt.Errorf("read %s: %w", h.Short(), err)
		}
		if err := sshWriteObject(host, repoPath, h, data); err != nil {
			return fmt.Errorf("upload %s: %w", h.Short(), err)
		}
	}
	if total > 1 {
		fmt.Fprintf(os.Stderr, "\r  pushing objects: %d/%d done\n", i, total)
	}

	// Update refs
	for branchName, hash := range branchRefs {
		if err := sshWriteRef(host, repoPath, "refs/heads/"+branchName, hash.String()); err != nil {
			return fmt.Errorf("update ref %s: %w", branchName, err)
		}
	}

	return nil
}

// sshCollectRemoteDAG walks the remote commit DAG via SSH, collecting objects
// not present locally.
func (r *Repository) sshCollectRemoteDAG(host, repoPath string, hash core.Hash) (map[core.Hash]bool, error) {
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
		treeSet, err := r.sshCollectRemoteTree(host, repoPath, commit.Tree)
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

func (r *Repository) sshCollectRemoteTree(host, repoPath string, treeHash core.Hash) (map[core.Hash]bool, error) {
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
				// Object missing locally — skip it
				fmt.Fprintf(os.Stderr, "\r  warning: object %s (%s) missing, skipping\n", entry.Hash.Short(), entry.Name)
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
