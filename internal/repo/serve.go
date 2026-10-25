package repo

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/zhsoft88/qin/internal/core"
)

// maxRequestBody bounds every request body the HTTP protocol accepts. The
// largest legitimate one is a hash list at the record-count limit (2,000,000
// × 32 bytes, under 64 MiB); anything bigger is refused before it is read.
const maxRequestBody = 64 << 20

// RepoServer serves multiple repositories from a base directory.
// URL format: http://host/<repo-name>/refs, /plan, /pack/..., etc.
type RepoServer struct {
	BasePath string
}

func (s *RepoServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 || !validRepoName(parts[0]) {
		http.Error(w, "not found", 404)
		return
	}
	repoName := parts[0]
	innerPath := parts[1]

	// openExactRoot, not Open: a name that does not name a repository here
	// must not be walked up into an ancestor that happens to be one, and the
	// name itself must not be able to escape the base directory.
	r, err := openExactRoot(filepath.Join(s.BasePath, repoName))
	if err != nil {
		http.Error(w, "repository not found: "+repoName, 404)
		return
	}

	req.URL.Path = "/" + innerPath
	r.ServeHTTP(w, req)
}

// validRepoName is the one path component between the server root and a
// repository. SplitN has already removed '/', so what is left to refuse is the
// Windows separator, the traversal names, and NUL — and '.'/'..' are refused by
// name because filepath.Join would clean them into the base directory.
func validRepoName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "\\\x00")
}

// packUploadLocks serializes writes of one pack id within this process. Two
// concurrent PUTs of the same id would otherwise interleave their bytes in one
// .part file. Cross-process writers are still assumed not to exist, the same
// assumption every other writer of a repository makes.
var packUploadLocks sync.Map // pack id -> *sync.Mutex

func packUploadLock(id string) *sync.Mutex {
	mu, _ := packUploadLocks.LoadOrStore(id, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// ServeHTTP handles the remote protocol:
//
//	GET  /refs             — list all refs (JSON map)
//	GET  /ref/<ref-path>   — read a ref value
//	POST /refs             — apply a set of ref updates atomically
//	POST /plan             — plan a pack: object count, byte length, pack id
//	POST /has              — which of these objects does this repository have?
//	POST /pack?offset&id   — stream a pack, or its suffix from offset
//	HEAD /pack/<id>        — how much of this pack has been received?
//	PUT  /pack/<id>?offset — upload a pack, or the part after offset
//
// Objects travel inside packs only. The per-object endpoints of the previous
// protocol are gone: a peer speaking them is a different protocol version, not
// a degraded one, and has to be upgraded together with the server.
func (r *Repository) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// A crashed multi-ref update is finished before this repository answers
	// anything about its refs — or is written to.
	if err := r.recoverRefsTxn(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	path := strings.TrimPrefix(req.URL.Path, "/")

	switch {
	case path == "refs" && req.Method == "GET":
		r.serveRefs(w, req)
	case path == "refs" && req.Method == "POST":
		r.serveRefsPost(w, req)
	case strings.HasPrefix(path, "ref/") && req.Method == "GET":
		r.serveRefGet(w, req, strings.TrimPrefix(path, "ref/"))
	case path == "plan" && req.Method == "POST":
		r.servePlan(w, req)
	case path == "has" && req.Method == "POST":
		r.serveHas(w, req)
	case path == "pack" && req.Method == "POST":
		r.servePackStream(w, req)
	case strings.HasPrefix(path, "pack/"):
		id := strings.TrimPrefix(path, "pack/")
		switch req.Method {
		case "HEAD":
			r.servePackHead(w, id)
		case "PUT":
			r.servePackPut(w, req, id)
		default:
			http.Error(w, "method not allowed", 405)
		}
	default:
		http.Error(w, "not found", 404)
	}
}

func (r *Repository) serveRefs(w http.ResponseWriter, req *http.Request) {
	refs := make(map[string]string)
	refsDir := r.RefsDir()
	filepath.Walk(refsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(refsDir, path)
		if relErr != nil {
			return nil
		}
		// Dot-prefixed names are atomic writes in progress, not refs.
		if rel != "." && hasDotComponent(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		data, _ := ioutil.ReadFile(path)
		refs["refs/"+filepath.ToSlash(rel)] = strings.TrimSpace(string(data))
		return nil
	})
	head, _ := r.ReadHEAD()
	refs["HEAD"] = head
	// A pushing client has no other way to learn whether this is a target or a
	// checkout. It is not a ref, but it is the only extra fact the preflight
	// needs, and the clients filter the listing by the refs/heads/ prefix.
	refs["core.bare"] = strconv.FormatBool(r.Config.Core.Bare)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(refs)
}

func (r *Repository) serveRefGet(w http.ResponseWriter, req *http.Request, ref string) {
	// The ref is joined onto .qin, so "ref/../../../etc/passwd" would otherwise
	// read outside the repository. safeRefPath is the same rule writes use.
	if !safeRefPath(ref) {
		http.Error(w, "invalid ref name: "+ref, 400)
		return
	}
	data, err := r.ReadRef(ref)
	if err != nil {
		http.Error(w, "ref not found: "+ref, 404)
		return
	}
	w.Write([]byte(data))
}

// refsRequest is the batch update a push ends with. force overrides the
// fast-forward rule for every ref in the set, exactly as it does on a
// per-ref update; it never overrides the checked-out refusal.
type refsRequest struct {
	Updates map[string]string `json:"updates"`
	Force   bool              `json:"force"`
}

// serveRefsPost applies every update or none. Each ref is validated and
// checked against the same rules a pushing client runs — but this is the copy
// that matters, because a hand-written request has no client in front of it.
// The whole set is decided before the first ref is written, so a refusal
// leaves the target untouched.
func (r *Repository) serveRefsPost(w http.ResponseWriter, req *http.Request) {
	req.Body = http.MaxBytesReader(w, req.Body, maxRequestBody)
	var body refsRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request: "+err.Error(), 400)
		return
	}
	if len(body.Updates) == 0 {
		http.Error(w, "no ref updates", 400)
		return
	}

	refs := make([]string, 0, len(body.Updates))
	for ref := range body.Updates {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	for _, ref := range refs {
		if !validRefName(ref) {
			http.Error(w, "invalid ref name: "+ref, 400)
			return
		}
		if _, err := core.HashFromHex(strings.TrimSpace(body.Updates[ref])); err != nil {
			http.Error(w, fmt.Sprintf("invalid hash for %s", ref), 400)
			return
		}
	}

	// The checked-out refusal comes before any object is looked for: it is the
	// one the user has to act on, and a hand-written request may well name a
	// tip this target has never seen.
	st := targetState(r, refs)
	for _, ref := range refs {
		if err := r.checkPushRef(st, ref, strings.TrimSpace(body.Updates[ref]), body.Force); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
	}
	for _, ref := range refs {
		if !r.HasObject(mustHash(body.Updates[ref])) {
			http.Error(w, fmt.Sprintf("object not found for %s; upload the pack first", ref), 409)
			return
		}
	}

	updates := make(map[string]string, len(body.Updates))
	for ref, val := range body.Updates {
		updates[ref] = strings.TrimSpace(val)
	}
	if err := r.applyRefUpdates(updates); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(200)
}

// mustHash converts a hash string already validated by HashFromHex.
func mustHash(s string) core.Hash {
	h, _ := core.HashFromHex(strings.TrimSpace(s))
	return h
}

// validRefName reports whether a ref path from a request may be written. The
// path is handed to WriteRef, which joins it onto .qin, so without this a POST
// of "refs/../../evil" writes outside the repository entirely. Components
// follow the same rules git enforces on a ref name.
func validRefName(ref string) bool {
	var prefix string
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		prefix = "refs/heads/"
	case strings.HasPrefix(ref, "refs/tags/"):
		prefix = "refs/tags/"
	default:
		return false
	}
	name := ref[len(prefix):]
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || part == ".." {
			return false
		}
		for _, c := range part {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			case c == '.' || c == '-' || c == '_':
			default:
				return false
			}
		}
	}
	return true
}

// ---- pack planning ----

// planRequest is the JSON form of a /plan or /pack body: a DAG walk from
// wants, stopping at haves. The binary application/x-qin-hashes form instead
// names the objects outright; it is what pull uses for a chunk subset and what
// push uses, since both sides already know the set.
type planRequest struct {
	Wants []string `json:"wants"`
	Haves []string `json:"haves"`
	Lazy  bool     `json:"lazy"`
}

type planResponse struct {
	Count     int    `json:"count"`
	TotalSize int64  `json:"total_size"`
	PackID    string `json:"pack_id"`
}

// readPlanRequest parses either body form. explicit is true when the body
// named the objects; otherwise wants/haves describe a DAG walk.
func readPlanRequest(w http.ResponseWriter, req *http.Request) (wants, haves []core.Hash, explicit, lazy bool, err error) {
	ct := req.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	req.Body = http.MaxBytesReader(w, req.Body, maxRequestBody)

	switch ct {
	case "application/x-qin-hashes":
		data, err := ioutil.ReadAll(req.Body)
		if err != nil {
			return nil, nil, true, false, fmt.Errorf("read hash list: %w", err)
		}
		hashes, err := core.DecodeHashes(data)
		if err != nil {
			return nil, nil, true, false, err
		}
		return hashes, nil, true, false, nil
	case "application/json", "":
		var body planRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, nil, false, false, fmt.Errorf("invalid request: %w", err)
		}
		wants, err = parseHashList(body.Wants)
		if err != nil {
			return nil, nil, false, false, fmt.Errorf("wants: %w", err)
		}
		haves, err = parseHashList(body.Haves)
		if err != nil {
			return nil, nil, false, false, fmt.Errorf("haves: %w", err)
		}
		return wants, haves, false, body.Lazy, nil
	default:
		return nil, nil, false, false, fmt.Errorf("unsupported content type %q", ct)
	}
}

func parseHashList(items []string) ([]core.Hash, error) {
	hashes := make([]core.Hash, 0, len(items))
	for _, s := range items {
		h, err := core.HashFromHex(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("invalid hash %q", s)
		}
		hashes = append(hashes, h)
	}
	return hashes, nil
}

// buildPlan resolves a request into the exact pack it names. Explicit mode
// only stats; DAG mode walks the repository and then stats what it found.
func (r *Repository) buildPlan(wants, haves []core.Hash, explicit, lazy bool) (*packPlan, error) {
	if explicit {
		// Every named object must exist: a pull's chunk set is only useful
		// whole, and a pack naming a missing object would fail halfway through
		// its own framing. Naming them lets the client fail before transferring.
		var missing []string
		for _, h := range wants {
			if !r.HasObject(h) {
				missing = append(missing, h.String())
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return nil, &missingObjectsError{missing: missing}
		}
		return r.planPackForObjects(wants)
	}
	hashes, err := r.collectForPlan(wants, haves, lazy)
	if err != nil {
		return nil, err
	}
	return r.planPackForObjects(hashes)
}

// missingObjectsError is the 409 an explicit plan request earns when it names
// an object the server does not have.
type missingObjectsError struct {
	missing []string
}

func (e *missingObjectsError) Error() string {
	return "missing objects:\n" + strings.Join(e.missing, "\n")
}

func (r *Repository) servePlan(w http.ResponseWriter, req *http.Request) {
	wants, haves, explicit, lazy, err := readPlanRequest(w, req)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	plan, err := r.buildPlan(wants, haves, explicit, lazy)
	if err != nil {
		writePlanError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(planResponse{
		Count:     len(plan.Hashes),
		TotalSize: plan.Total,
		PackID:    plan.ID.String(),
	})
}

// writePlanError maps a planning failure to a status. A missing object is a
// state conflict the client can act on; anything else is the server's own
// store failing, which must not read as "these objects are all you need".
func writePlanError(w http.ResponseWriter, err error) {
	if m, ok := err.(*missingObjectsError); ok {
		http.Error(w, m.Error(), 409)
		return
	}
	http.Error(w, err.Error(), 500)
}

// collectForPlan is the server half of a fetch: everything reachable from
// wants that is not reachable from haves, in discovery order.
//
// It mirrors collectObjects (remote.go) but takes its boundary as a set rather
// than a *Repository, because haves are the client's tips and not a repository
// this process can open. The rule that a walk never skips an object it cannot
// read holds for wants. For haves it cannot: a have the server does not have
// is simply not a boundary, and the only consequence is that the client
// receives objects it already owns — the safe direction.
func (r *Repository) collectForPlan(wants, haves []core.Hash, lazy bool) ([]core.Hash, error) {
	seen := make(map[core.Hash]bool)
	for _, h := range haves {
		if h.IsZero() || !r.HasObject(h) {
			continue
		}
		if err := r.walkPlanObject(h, seen, lazy, nil); err != nil {
			return nil, err
		}
	}

	var collected []core.Hash
	add := func(h core.Hash) { collected = append(collected, h) }
	for _, h := range wants {
		if err := r.walkPlanObject(h, seen, lazy, add); err != nil {
			return nil, err
		}
	}
	return collected, nil
}

func (r *Repository) walkPlanObject(hash core.Hash, seen map[core.Hash]bool, lazy bool, add func(core.Hash)) error {
	if hash.IsZero() || seen[hash] {
		return nil
	}
	seen[hash] = true
	if add != nil {
		add(hash)
	}

	objType, err := r.ObjectType(hash)
	if err != nil {
		return fmt.Errorf("cannot read object %s: %w", hash.Short(), err)
	}

	switch objType {
	case core.ObjectCommit:
		commit, err := r.LoadCommit(hash)
		if err != nil {
			return fmt.Errorf("cannot read commit %s: %w", hash.Short(), err)
		}
		if err := r.walkPlanObject(commit.Tree, seen, lazy, add); err != nil {
			return err
		}
		for _, p := range commit.Parents {
			if err := r.walkPlanObject(p, seen, lazy, add); err != nil {
				return err
			}
		}
	case core.ObjectTree:
		tree, err := r.LoadTree(hash)
		if err != nil {
			return fmt.Errorf("cannot read tree %s: %w", hash.Short(), err)
		}
		for _, entry := range tree.Entries {
			if IsSubmoduleMode(entry.Mode) {
				continue
			}
			if err := r.walkPlanObject(entry.Hash, seen, lazy, add); err != nil {
				return err
			}
		}
	case core.ObjectChunkManifest:
		if lazy {
			return nil
		}
		manifest, err := r.LoadChunkManifest(hash)
		if err != nil {
			return fmt.Errorf("cannot read chunk manifest %s: %w", hash.Short(), err)
		}
		for _, chunk := range manifest.Chunks {
			if err := r.walkPlanObject(chunk.Hash, seen, lazy, add); err != nil {
				return err
			}
		}
	}
	return nil
}

// serveHas answers which of a binary hash list this repository already holds.
// It stats, never reads: the answer decides what the uploader will send, and
// reading a large object to say "yes" would cost more than the transfer.
func (r *Repository) serveHas(w http.ResponseWriter, req *http.Request) {
	req.Body = http.MaxBytesReader(w, req.Body, maxRequestBody)
	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read hash list: "+err.Error(), 400)
		return
	}
	hashes, err := core.DecodeHashes(data)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var have []core.Hash
	for _, h := range hashes {
		if r.HasObject(h) {
			have = append(have, h)
		}
	}
	w.Header().Set("Content-Type", "application/x-qin-hashes")
	w.Write(core.EncodeHashes(have))
}

// ---- pack streaming ----

// servePackStream answers POST /pack: the requested pack, or its suffix when a
// previous attempt stopped partway. The request repeats the plan body, so the
// server recomputes the plan and can prove it is the same pack the offset
// belongs to.
func (r *Repository) servePackStream(w http.ResponseWriter, req *http.Request) {
	wants, haves, explicit, lazy, err := readPlanRequest(w, req)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	offset, err := parseOffset(req.URL.Query().Get("offset"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	plan, err := r.buildPlan(wants, haves, explicit, lazy)
	if err != nil {
		writePlanError(w, err)
		return
	}
	if id := req.URL.Query().Get("id"); id != "" && id != plan.ID.String() {
		http.Error(w, fmt.Sprintf("pack id changed (%s != %s); re-plan", id, plan.ID.String()), 409)
		return
	}
	if offset > plan.Total {
		http.Error(w, fmt.Sprintf("offset %d is past the pack's %d bytes", offset, plan.Total), 400)
		return
	}

	w.Header().Set("Content-Type", "application/x-qin-pack")
	w.Header().Set("X-Pack-Id", plan.ID.String())
	w.Header().Set("X-Pack-Total", strconv.FormatInt(plan.Total, 10))
	w.Header().Set("X-Pack-Offset", strconv.FormatInt(offset, 10))
	w.Header().Set("Content-Length", strconv.FormatInt(plan.Total-offset, 10))
	w.WriteHeader(200)

	if plan.Total == offset {
		return
	}
	// Once the first byte is written the status is committed; a read failure
	// here ends the response and the client resumes from what it received.
	if _, err := r.writePackFrom(w, plan, offset); err != nil {
		return
	}
}

func parseOffset(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	offset, err := strconv.ParseInt(s, 10, 64)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid offset %q", s)
	}
	return offset, nil
}

// servePackHead reports what the receiver already holds for a pack id.
func (r *Repository) servePackHead(w http.ResponseWriter, id string) {
	if !validPackID(id) {
		http.Error(w, "invalid pack id", 400)
		return
	}
	if _, err := os.Stat(r.incomingDonePath(id)); err == nil {
		w.Header().Set("X-Done", "1")
		w.WriteHeader(200)
		return
	}
	if fi, err := os.Stat(r.incomingPartPath(id)); err == nil {
		w.Header().Set("X-Received", strconv.FormatInt(fi.Size(), 10))
		w.WriteHeader(200)
		return
	}
	http.Error(w, "pack not found", 404)
}

// servePackPut receives a pack in one or more requests. Each request names the
// offset it starts at; bytes before it must already be on disk. When the part
// file reaches the declared total the objects are installed, the .done marker
// is written, and the client's next step is the ref update.
func (r *Repository) servePackPut(w http.ResponseWriter, req *http.Request, id string) {
	if !validPackID(id) {
		http.Error(w, "invalid pack id", 400)
		return
	}
	offset, err := parseOffset(req.URL.Query().Get("offset"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	total, err := strconv.ParseInt(req.Header.Get("X-Pack-Size"), 10, 64)
	if err != nil || total < 0 {
		http.Error(w, "missing or invalid X-Pack-Size header", 400)
		return
	}
	if offset > total {
		http.Error(w, fmt.Sprintf("offset %d is past the pack's %d bytes", offset, total), 400)
		return
	}

	mu := packUploadLock(id)
	mu.Lock()
	defer mu.Unlock()

	dir := r.incomingDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	cleanIncoming(dir)

	partPath := r.incomingPartPath(id)
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		http.Error(w, err.Error(), 500)
		return
	}
	received := fi.Size()
	if offset > received {
		f.Close()
		// Tell the client the real offset in the refusal, so it can continue
		// without a second HEAD round trip.
		w.Header().Set("X-Pack-Received", strconv.FormatInt(received, 10))
		http.Error(w, fmt.Sprintf("offset %d is beyond the %d bytes already received", offset, received), 409)
		return
	}
	// Rewinding overwrites identical bytes: the id fixes the pack, and its
	// records are immutable.
	if offset < received {
		if err := f.Truncate(offset); err != nil {
			f.Close()
			http.Error(w, err.Error(), 500)
			return
		}
		received = offset
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		f.Close()
		http.Error(w, err.Error(), 500)
		return
	}

	remaining := total - offset
	// One byte past the declared length is read on purpose: it is how a body
	// that disagrees with X-Pack-Size is detected instead of silently
	// truncating a pack whose framing would then be wrong.
	n, copyErr := io.Copy(f, io.LimitReader(req.Body, remaining+1))
	received += n
	closeErr := f.Close()

	if copyErr != nil {
		w.Header().Set("X-Pack-Received", strconv.FormatInt(received, 10))
		http.Error(w, copyErr.Error(), 500)
		return
	}
	if closeErr != nil {
		http.Error(w, closeErr.Error(), 500)
		return
	}
	if n > remaining {
		os.Remove(partPath)
		http.Error(w, fmt.Sprintf("body exceeds the declared pack size %d", total), 400)
		return
	}

	if received < total {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int64{"received": received})
		return
	}

	// The whole pack is here. Installation verifies every object; only then
	// does the .done marker exist for the client to see.
	if _, err := r.unpackPack(partPath); err != nil {
		os.Remove(partPath)
		http.Error(w, err.Error(), 400)
		return
	}
	if err := writeFileAtomic(r.incomingDonePath(id), nil, 0644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	os.Remove(partPath)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"received": received, "installed": true})
}
