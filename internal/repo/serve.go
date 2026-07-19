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

// RepoServer serves multiple repositories from a base directory.
// URL format: http://host/<repo-name>/refs, /objects/<hash>, etc.
type RepoServer struct {
	BasePath string
}

func (s *RepoServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 || parts[0] == "" {
		http.Error(w, "not found", 404)
		return
	}
	repoName := parts[0]
	innerPath := parts[1]

	repoPath := filepath.Join(s.BasePath, repoName)
	r, err := Open(repoPath)
	if err != nil {
		http.Error(w, "repository not found: "+repoName, 404)
		return
	}

	req.URL.Path = "/" + innerPath
	r.ServeHTTP(w, req)
}

// ServeHTTP handles HTTP requests for remote object and ref access.		// Routes:
//   GET  /refs             — list all refs (JSON map)
//   GET  /ref/<ref-path>   — read a ref value
//   PUT  /ref/<ref-path>   — update a ref
//   GET  /objects/<hash>   — download an object file
//   HEAD /objects/<hash>   — check object existence
//   PUT  /objects/<hash>   — upload an object file
func (r *Repository) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/")

	switch {
	case path == "refs" && req.Method == "GET":
		r.serveRefs(w, req)
	case strings.HasPrefix(path, "ref/"):
		ref := strings.TrimPrefix(path, "ref/")
		switch req.Method {
		case "GET":
			r.serveRefGet(w, req, ref)
		case "PUT":
			r.serveRefPut(w, req, ref)
		default:
			http.Error(w, "method not allowed", 405)
		}
	case strings.HasPrefix(path, "objects/"):
		hashStr := strings.TrimPrefix(path, "objects/")
		r.serveObject(w, req, hashStr)
	default:
		http.Error(w, "not found", 404)
	}
}

func (r *Repository) serveRefs(w http.ResponseWriter, req *http.Request) {
	refs := make(map[string]string)
	refsDir := r.RefsDir()
	filepath.Walk(refsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(refsDir, path)
		data, _ := ioutil.ReadFile(path)
		refs["refs/"+filepath.ToSlash(rel)] = strings.TrimSpace(string(data))
		return nil
	})
	head, _ := r.ReadHEAD()
	refs["HEAD"] = head

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(refs)
}

func (r *Repository) serveRefGet(w http.ResponseWriter, req *http.Request, ref string) {
	data, err := r.ReadRef(ref)
	if err != nil {
		http.Error(w, "ref not found: "+ref, 404)
		return
	}
	w.Write([]byte(data))
}

func (r *Repository) serveRefPut(w http.ResponseWriter, req *http.Request, ref string) {
	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := r.WriteRef(ref, strings.TrimSpace(string(data))); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(200)
}

func (r *Repository) serveObject(w http.ResponseWriter, req *http.Request, hashStr string) {
	hash, err := core.HashFromHex(hashStr)
	if err != nil {
		http.Error(w, "invalid hash", 400)
		return
	}

	switch req.Method {
	case "GET":
		data, err := ioutil.ReadFile(r.objectPath(hash))
		if err != nil {
			http.Error(w, "object not found", 404)
			return
		}
		w.Write(data)
	case "HEAD":
		_, err := os.Stat(r.objectPath(hash))
		if err != nil {
			http.Error(w, "object not found", 404)
			return
		}
		w.WriteHeader(200)
	case "PUT":
		data, err := ioutil.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if h := core.HashFromBytes(data); h != hash {
			http.Error(w, fmt.Sprintf("hash mismatch: got %s, expected %s", h.Short(), hash.Short()), 400)
			return
		}
		objPath := r.objectPath(hash)
		if err := os.MkdirAll(filepath.Dir(objPath), 0755); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := ioutil.WriteFile(objPath, data, 0644); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(200)
	default:
		http.Error(w, "method not allowed", 405)
	}
}
