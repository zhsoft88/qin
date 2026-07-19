package repo

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhsoft88/qin/internal/core"
)

// ---- HTTP client helpers ----

func httpDo(baseURL, method, path string, body []byte) ([]byte, error) {
	url := strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Body = ioutil.NopCloser(strings.NewReader(string(body)))
		req.ContentLength = int64(len(body))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		msg, _ := ioutil.ReadAll(resp.Body)
		return nil, fmt.Errorf("http %d %s: %s", resp.StatusCode, path, strings.TrimSpace(string(msg)))
	}

	return ioutil.ReadAll(resp.Body)
}

func httpGet(baseURL, path string) ([]byte, error)   { return httpDo(baseURL, "GET", path, nil) }
func httpPut(baseURL, path string, body []byte) error {
	_, err := httpDo(baseURL, "PUT", path, body)
	return err
}

func httpListRefs(baseURL string) (map[string]string, error) {
	data, err := httpGet(baseURL, "refs")
	if err != nil {
		return nil, err
	}
	var refs map[string]string
	if err := json.Unmarshal(data, &refs); err != nil {
		return nil, fmt.Errorf("parse refs: %w", err)
	}
	return refs, nil
}

func httpReadRef(baseURL, ref string) (string, error) {
	data, err := httpGet(baseURL, "ref/"+ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func httpHasObject(baseURL string, hash core.Hash) bool {
	_, err := httpDo(baseURL, "HEAD", "objects/"+hash.String(), nil)
	return err == nil
}

func httpReadObject(baseURL string, hash core.Hash) ([]byte, error) {
	return httpGet(baseURL, "objects/"+hash.String())
}

// ---- HTTP DAG walk (collect missing objects on remote) ----

// httpCollectMissing walks the commit DAG on the remote via HTTP, collecting
// all object hashes NOT present in the local repo (boundary).
func (r *Repository) httpCollectMissing(baseURL string, hash core.Hash, lazy bool) (map[core.Hash]bool, error) {
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

		// Fetch and parse commit
		raw, err := httpReadObject(baseURL, h)
		if err != nil {
			return fmt.Errorf("fetch commit %s: %w", h.Short(), err)
		}
		objType, content, err := core.DeserializeObject(raw)
		if err != nil {
			return fmt.Errorf("deserialize commit %s: %w", h.Short(), err)
		}
		if objType != core.ObjectCommit {
			return fmt.Errorf("expected commit at %s, got %s", h.Short(), objType)
		}
		var commit Commit
		if err := core.DeserializeJSON(content, &commit); err != nil {
			return fmt.Errorf("parse commit %s: %w", h.Short(), err)
		}

		set[h] = true

		// Collect tree
		treeSet, err := r.httpCollectTree(baseURL, commit.Tree, lazy)
		if err != nil {
			return err
		}
		for th := range treeSet {
			set[th] = true
		}

		// Recurse into parents
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

// httpCollectTree fetches a tree object via HTTP and collects all entry hashes.
func (r *Repository) httpCollectTree(baseURL string, treeHash core.Hash, lazy bool) (map[core.Hash]bool, error) {
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

		raw, err := httpReadObject(baseURL, th)
		if err != nil {
			return fmt.Errorf("fetch tree %s: %w", th.Short(), err)
		}
		objType, content, err := core.DeserializeObject(raw)
		if err != nil {
			return fmt.Errorf("deserialize tree %s: %w", th.Short(), err)
		}
		if objType != core.ObjectTree {
			return fmt.Errorf("expected tree at %s, got %s", th.Short(), objType)
		}
		var tree Tree
		if err := core.DeserializeJSON(content, &tree); err != nil {
			return fmt.Errorf("parse tree %s: %w", th.Short(), err)
		}

		set[th] = true

		for _, entry := range tree.Entries {
			if entry.Hash.IsZero() || set[entry.Hash] || r.HasObject(entry.Hash) {
				continue
			}

			// Need to check type — fetch first few bytes via full GET
			entryRaw, err := httpReadObject(baseURL, entry.Hash)
			if err != nil {
				return fmt.Errorf("fetch entry %s: %w", entry.Hash.Short(), err)
			}
			eType, _, err := core.DeserializeObject(entryRaw)
			if err != nil {
				return fmt.Errorf("deserialize entry %s: %w", entry.Hash.Short(), err)
			}

			if eType == core.ObjectChunkManifest {
				set[entry.Hash] = true
				if !lazy {
					var manifest ChunkManifest
					_, eContent, _ := core.DeserializeObject(entryRaw)
					if err := core.DeserializeJSON(eContent, &manifest); err != nil {
						return fmt.Errorf("parse manifest %s: %w", entry.Hash.Short(), err)
					}
					for _, chunk := range manifest.Chunks {
						if !chunk.Hash.IsZero() && !set[chunk.Hash] && !r.HasObject(chunk.Hash) {
							set[chunk.Hash] = true
						}
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

// ---- HTTP fetch ----

func (r *Repository) fetchHTTP(baseURL, remoteName string, lazy bool) error {
	refs, err := httpListRefs(baseURL)
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

		objects, err := r.httpCollectMissing(baseURL, hash, lazy)
		if err != nil {
			return fmt.Errorf("collect missing for %s: %w", branchName, err)
		}
		for h := range objects {
			allObjects[h] = true
		}
	}

	// Download all missing objects
	for h := range allObjects {
		data, err := httpReadObject(baseURL, h)
		if err != nil {
			return fmt.Errorf("read object %s: %w", h.Short(), err)
		}
		// Verify + write atomically
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
			return fmt.Errorf("write %s: %w", h.Short(), err)
		}
		if err := os.Rename(tmpPath, dstPath); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("rename %s: %w", h.Short(), err)
		}
	}

	// Write remote-tracking refs
	for branchName, hash := range branchRefs {
		ref := "refs/remotes/" + remoteName + "/" + branchName
		if err := r.WriteRef(ref, hash.String()); err != nil {
			return fmt.Errorf("write ref %s: %w", ref, err)
		}
	}

	return nil
}

// ---- HTTP push ----

func (r *Repository) pushHTTP(baseURL, remoteName string) error {
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

		// Collect objects the remote doesn't have
		objects, err := r.httpCollectMissingWithBoundary(baseURL, hash)
		if err != nil {
			return fmt.Errorf("collect objects for %s: %w", branchName, err)
		}
		for h := range objects {
			allObjects[h] = true
		}
	}

	// Upload all missing objects
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
		if err := httpPut(baseURL, "objects/"+h.String(), data); err != nil {
			return fmt.Errorf("upload %s: %w", h.Short(), err)
		}
	}
	if total > 1 {
		fmt.Fprintf(os.Stderr, "\r  pushing objects: %d/%d done\n", i, total)
	}

	// Update remote refs
	for branchName, hash := range branchRefs {
		if err := httpPut(baseURL, "ref/refs/heads/"+branchName, []byte(hash.String())); err != nil {
			return fmt.Errorf("update ref %s: %w", branchName, err)
		}
	}

	return nil
}

// httpCollectMissingWithBoundary walks the DAG from hash in the LOCAL repo,
// collecting objects NOT present on the remote (boundary checked via HTTP).
func (r *Repository) httpCollectMissingWithBoundary(baseURL string, hash core.Hash) (map[core.Hash]bool, error) {
	set := make(map[core.Hash]bool)
	visited := make(map[core.Hash]bool)

	var walk func(h core.Hash) error
	walk = func(h core.Hash) error {
		if h.IsZero() || visited[h] {
			return nil
		}
		visited[h] = true

		if httpHasObject(baseURL, h) {
			return nil
		}

		commit, err := r.LoadCommit(h)
		if err != nil {
			return err
		}

		set[h] = true

		if err := r.collectTreeHTTPBoundary(baseURL, set, commit.Tree); err != nil {
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

// collectTreeHTTPBoundary collects tree + entry hashes not present on remote (HTTP).
func (r *Repository) collectTreeHTTPBoundary(baseURL string, set map[core.Hash]bool, treeHash core.Hash) error {
	if treeHash.IsZero() || set[treeHash] || httpHasObject(baseURL, treeHash) {
		return nil
	}

	tree, err := r.LoadTree(treeHash)
	if err != nil {
		return err
	}

	set[treeHash] = true

	for _, entry := range tree.Entries {
		if entry.Hash.IsZero() || set[entry.Hash] || httpHasObject(baseURL, entry.Hash) {
			continue
		}

		objType, err := r.ObjectType(entry.Hash)
		if err != nil {
			return err
		}

		if objType == core.ObjectChunkManifest {
			set[entry.Hash] = true
			manifest, err := r.LoadChunkManifest(entry.Hash)
			if err != nil {
				return err
			}
			for _, chunk := range manifest.Chunks {
				if !chunk.Hash.IsZero() && !set[chunk.Hash] && !httpHasObject(baseURL, chunk.Hash) {
					set[chunk.Hash] = true
				}
			}
		} else {
			set[entry.Hash] = true
		}
	}

	return nil
}
