package repo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

// HTTP transfer tuning. Transport-level failures are retried with these
// delays; a pack that stops partway resumes from what the receiver kept.
const (
	httpTransferAttempts = 3
	httpHasBatch         = 20000
	httpProgressInterval = 200 * time.Millisecond
)

// httpClient is used for every remote call. It carries timeouts on connecting
// and on waiting for response headers, but no total timeout: a large pack
// legitimately takes minutes, and a total timeout would cut it off. The header
// timeout is generous because an upload's response only arrives after the
// receiver has installed everything in the pack.
var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 10 * time.Minute,
	},
}

// httpError is a response with a 4xx/5xx status. Keeping the code lets callers
// act on 409 (re-plan, or resume from the offset the receiver reports).
type httpError struct {
	Status int
	Path   string
	Body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("http %d %s: %s", e.Status, e.Path, e.Body)
}

func httpStatus(err error) int {
	if e, ok := err.(*httpError); ok {
		return e.Status
	}
	return 0
}

// httpURL joins a base URL and a path.
func httpURL(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

// httpDoStream sends one request and returns the response for the caller to
// consume. The body is not buffered: a pack can be far larger than memory.
// A non-2xx status is an *httpError and the response is closed.
func httpDoStream(method, url string, body io.Reader, length int64, contentType string) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = length
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http %s: %w", url, err)
	}
	if resp.StatusCode >= 400 {
		msg, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		return nil, &httpError{Status: resp.StatusCode, Path: url, Body: strings.TrimSpace(string(msg))}
	}
	return resp, nil
}

func httpDo(baseURL, method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	resp, err := httpDoStream(method, httpURL(baseURL, path), reader, int64(len(body)), "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return ioutil.ReadAll(resp.Body)
}

func httpGet(baseURL, path string) ([]byte, error) { return httpDo(baseURL, "GET", path, nil) }

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

// ---- planning and hash queries ----

type httpPackPlan struct {
	Count int
	Total int64
	ID    core.Hash
}

func httpPlan(baseURL string, body []byte, contentType string) (*httpPackPlan, error) {
	resp, err := httpDoStream("POST", httpURL(baseURL, "plan"), bytes.NewReader(body), int64(len(body)), contentType)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var pr planResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("parse plan: %w", err)
	}
	id, err := core.HashFromHex(pr.PackID)
	if err != nil {
		return nil, fmt.Errorf("plan returned an invalid pack id %q", pr.PackID)
	}
	if pr.Count < 0 || pr.TotalSize < int64(core.PackHeaderSize) {
		return nil, fmt.Errorf("plan returned an impossible shape (count %d, size %d)", pr.Count, pr.TotalSize)
	}
	return &httpPackPlan{Count: pr.Count, Total: pr.TotalSize, ID: id}, nil
}

// planDAGBody is the JSON request both /plan and /pack take in DAG mode.
func planDAGBody(wants, haves []core.Hash, lazy bool) []byte {
	data, _ := json.Marshal(planRequest{
		Wants: hexHashes(wants),
		Haves: hexHashes(haves),
		Lazy:  lazy,
	})
	return data
}

func hexHashes(hashes []core.Hash) []string {
	out := make([]string, len(hashes))
	for i, h := range hashes {
		out[i] = h.String()
	}
	return out
}

// httpPlanDAG asks the server what a fetch from wants (stopping at haves)
// would transfer.
func httpPlanDAG(baseURL string, wants, haves []core.Hash, lazy bool) (*httpPackPlan, error) {
	return httpPlan(baseURL, planDAGBody(wants, haves, lazy), "application/json")
}

// httpPlanHashes asks for an explicit object set — a pull's chunks, or the
// objects a push found missing. A missing object is a 409 naming it.
func httpPlanHashes(baseURL string, hashes []core.Hash) (*httpPackPlan, error) {
	return httpPlan(baseURL, core.EncodeHashes(hashes), "application/x-qin-hashes")
}

// httpHas asks which of the given objects the server holds. It is batched by
// the caller; the response keeps request order.
func httpHas(baseURL string, hashes []core.Hash) (map[core.Hash]bool, error) {
	resp, err := httpDoStream("POST", httpURL(baseURL, "has"), bytes.NewReader(core.EncodeHashes(hashes)),
		int64(8+len(hashes)*core.HashSize), "application/x-qin-hashes")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /has response: %w", err)
	}
	present, err := core.DecodeHashes(data)
	if err != nil {
		return nil, fmt.Errorf("parse /has response: %w", err)
	}
	have := make(map[core.Hash]bool, len(present))
	for _, h := range present {
		have[h] = true
	}
	return have, nil
}

// ---- download ----

// httpDownloadPack receives a pack into .qin/incoming and installs it.
//
// The part file is the resume state: every attempt starts at its current
// length, and only a complete install removes it. replan is called once if the
// server says the pack id changed, because the remote moved between planning
// and transferring.
func (r *Repository) httpDownloadPack(baseURL string, body []byte, contentType string, plan *httpPackPlan, replan func() (*httpPackPlan, error)) error {
	defer endProgressLine()
	dir := r.incomingDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	cleanIncoming(dir)

	replanned := false
	announced := false
	installAnnounced := false
	var lastErr error
	for attempt := 1; attempt <= httpTransferAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt-1) * time.Second)
		}

		partPath := r.incomingPartPath(plan.ID.String())
		offset := int64(0)
		if fi, err := os.Stat(partPath); err == nil {
			offset = fi.Size()
		}
		if !announced {
			announced = true
			if offset > 0 {
				stage("download", "%s (resuming at %s)", formatBytes(plan.Total), formatBytes(offset))
			} else {
				stage("download", "%s", formatBytes(plan.Total))
			}
		}
		if offset > plan.Total {
			// Not this pack's bytes; start over rather than trust them.
			os.Remove(partPath)
			offset = 0
		}

		if offset < plan.Total {
			url := fmt.Sprintf("%s?offset=%d&id=%s", httpURL(baseURL, "pack"), offset, plan.ID)
			resp, err := httpDoStream("POST", url, bytes.NewReader(body), int64(len(body)), contentType)
			if err != nil {
				lastErr = err
				if httpStatus(err) == 409 && !replanned && replan != nil {
					replanned = true
					newPlan, perr := replan()
					if perr != nil {
						return perr
					}
					// The partial file belongs to the old plan.
					os.Remove(partPath)
					plan = newPlan
					announced = false
					installAnnounced = false
					if plan.Count == 0 || plan.Total == 0 {
						return nil
					}
					attempt-- // the re-plan does not consume an attempt
					continue
				}
				if httpStatus(err) != 0 {
					return err // a real refusal; retrying cannot help
				}
				continue
			}

			f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				resp.Body.Close()
				return err
			}
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				resp.Body.Close()
				f.Close()
				return err
			}
			pw := &progressWriter{w: f, label: "download", done: offset, total: plan.Total}
			n, copyErr := io.Copy(pw, resp.Body)
			resp.Body.Close()
			closeErr := f.Close()
			if copyErr != nil {
				lastErr = fmt.Errorf("download pack: %w", copyErr)
				continue
			}
			if closeErr != nil {
				return closeErr
			}
			if offset+n < plan.Total {
				lastErr = fmt.Errorf("download pack: received %s of %s bytes", formatBytes(offset+n), formatBytes(plan.Total))
				continue
			}
		}

		if !installAnnounced {
			installAnnounced = true
			stage("install", "%d objects", plan.Count)
		}
		if _, err := r.unpackPack(partPath); err != nil {
			// The bytes on disk are not a valid pack. They cannot be repaired
			// by appending, so the next attempt starts from zero.
			os.Remove(partPath)
			lastErr = err
			continue
		}
		endProgressLine()
		os.Remove(partPath)
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("download did not complete")
	}
	return lastErr
}

// progressWriter reports bytes written in place, throttled so a fast local
// link does not spend its time formatting progress lines.
type progressWriter struct {
	w     io.Writer
	label string
	done  int64
	total int64
	last  time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if time.Since(p.last) >= httpProgressInterval {
		p.last = time.Now()
		printProgress("[%s] %s/%s", p.label, formatBytes(p.done), formatBytes(p.total))
	}
	return n, err
}

// httpDownloadObjects transfers an explicit object set — pull's chunk subset,
// or a single object — as one pack. The set goes to /plan first, so an object
// the server does not have fails before any bytes move.
func (r *Repository) httpDownloadObjects(baseURL string, hashes []core.Hash) error {
	plan, err := httpPlanHashes(baseURL, hashes)
	if err != nil {
		return err
	}
	if plan.Count == 0 {
		return nil
	}
	return r.httpDownloadPack(baseURL, core.EncodeHashes(hashes), "application/x-qin-hashes", plan, nil)
}

// ---- upload ----

// httpPackState reports what the receiver holds for a pack id.
func httpPackState(baseURL string, id core.Hash) (offset int64, done bool, err error) {
	req, err := http.NewRequest("HEAD", httpURL(baseURL, "pack/"+id.String()), nil)
	if err != nil {
		return 0, false, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("http HEAD pack: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 404:
		return 0, false, nil
	case 200:
		if resp.Header.Get("X-Done") == "1" {
			return 0, true, nil
		}
		off, perr := strconv.ParseInt(resp.Header.Get("X-Received"), 10, 64)
		if perr != nil || off < 0 {
			off = 0
		}
		return off, false, nil
	default:
		return 0, false, &httpError{Status: resp.StatusCode, Path: "HEAD pack", Body: resp.Status}
	}
}

// packOffsetError reports the receiver's real offset when our own offset ran
// past it, so the next attempt can continue without another HEAD.
type packOffsetError struct {
	received int64
	body     string
}

func (e *packOffsetError) Error() string { return e.body }

// httpPutPackOnce sends the pack's bytes from offset, streaming them straight
// from the object store. No temporary pack file is ever written on the sender.
func (r *Repository) httpPutPackOnce(baseURL string, plan *packPlan, offset int64) error {
	url := fmt.Sprintf("%s?offset=%d", httpURL(baseURL, "pack/"+plan.ID.String()), offset)
	remaining := plan.Total - offset

	pr, pw := io.Pipe()
	writeErr := make(chan error, 1)
	go func() {
		var err error
		if remaining > 0 {
			w := &progressWriter{w: pw, label: "upload", done: offset, total: plan.Total}
			_, err = r.writePackFrom(w, plan, offset)
		}
		pw.CloseWithError(err)
		writeErr <- err
	}()

	req, err := http.NewRequest("PUT", url, pr)
	if err != nil {
		pr.Close()
		<-writeErr
		return err
	}
	req.ContentLength = remaining
	req.Header.Set("X-Pack-Size", strconv.FormatInt(plan.Total, 10))
	req.Header.Set("Content-Type", "application/x-qin-pack")

	resp, err := httpClient.Do(req)
	// If the server answered before consuming the body, the writer is blocked
	// on the pipe; closing the read end makes its next Write fail at once.
	pr.Close()
	wErr := <-writeErr

	if err != nil {
		if wErr != nil {
			return wErr
		}
		return fmt.Errorf("http PUT pack: %w", err)
	}
	if resp.StatusCode >= 400 {
		msg, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		body := strings.TrimSpace(string(msg))
		if resp.StatusCode == 409 {
			if rec, perr := strconv.ParseInt(resp.Header.Get("X-Pack-Received"), 10, 64); perr == nil && rec >= 0 {
				return &packOffsetError{received: rec, body: body}
			}
		}
		return &httpError{Status: resp.StatusCode, Path: url, Body: body}
	}
	io.Copy(ioutil.Discard, io.LimitReader(resp.Body, 8192))
	resp.Body.Close()
	if wErr != nil {
		return wErr
	}
	return nil
}

// httpUploadPack sends every object the receiver does not have yet. The
// receiver may already hold part of the pack from an interrupted attempt;
// HEAD says how much, and only that suffix is sent.
func (r *Repository) httpUploadPack(baseURL string, plan *packPlan) error {
	var lastErr error
	for attempt := 1; attempt <= httpTransferAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt-1) * time.Second)
		}
		offset, done, err := httpPackState(baseURL, plan.ID)
		if err != nil {
			lastErr = err
			continue
		}
		if done {
			return nil
		}
		if offset > plan.Total {
			offset = 0 // not this pack's bytes; overwrite from the start
		}
		if err := r.httpPutPackOnce(baseURL, plan, offset); err != nil {
			lastErr = err
			if po, ok := err.(*packOffsetError); ok {
				// The receiver has fewer bytes than we assumed. Resend from
				// its offset; it is not a failure of this attempt.
				lastErr = nil
				if po.received > plan.Total {
					continue
				}
			}
			if httpStatus(err) != 0 && httpStatus(err) != 409 {
				return err
			}
			continue
		}
	}

	offset, done, err := httpPackState(baseURL, plan.ID)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("upload did not complete")
	}
	return fmt.Errorf("upload of %d objects incomplete (%s of %s bytes received): %v",
		len(plan.Hashes), formatBytes(offset), formatBytes(plan.Total), lastErr)
}

// ---- push-side collection ----

// collectMissingHTTP finds the objects reachable from tips that the server
// does not have. It is a layered BFS over /has: each round asks about the
// whole frontier in batches, and only objects the server lacks are expanded
// locally. An already-synchronised push asks one batch and stops; a first push
// costs one round per commit/tree level, instead of sending the local object
// universe to the server.
func (r *Repository) collectMissingHTTP(baseURL string, tips []core.Hash) (map[core.Hash]bool, error) {
	missing := make(map[core.Hash]bool)
	asked := make(map[core.Hash]bool)
	defer endProgressLine()

	frontier := uniqueHashes(tips)
	for len(frontier) > 0 {
		var expand []core.Hash
		for start := 0; start < len(frontier); start += httpHasBatch {
			end := start + httpHasBatch
			if end > len(frontier) {
				end = len(frontier)
			}
			batch := frontier[start:end]
			have, err := httpHas(baseURL, batch)
			if err != nil {
				return nil, err
			}
			for _, h := range batch {
				asked[h] = true
				if !have[h] {
					missing[h] = true
					expand = append(expand, h)
				}
			}
		}

		var next []core.Hash
		for _, h := range expand {
			children, err := r.pushChildren(h)
			if err != nil {
				return nil, err
			}
			for _, child := range children {
				if child.IsZero() || asked[child] || missing[child] {
					continue
				}
				next = append(next, child)
			}
		}
		printProgress("[collect] checking remote: %d objects", len(asked))
		frontier = uniqueHashes(next)
	}
	return missing, nil
}

// pushChildren returns the objects a missing object refers to. A blob refers
// to none; a commit to its tree and parents; a tree to its entries' objects
// (a chunk manifest's chunks included); a manifest to its chunks.
func (r *Repository) pushChildren(hash core.Hash) ([]core.Hash, error) {
	objType, err := r.ObjectType(hash)
	if err != nil {
		return nil, fmt.Errorf("cannot read object %s: %w", hash.Short(), err)
	}
	switch objType {
	case core.ObjectCommit:
		commit, err := r.LoadCommit(hash)
		if err != nil {
			return nil, fmt.Errorf("cannot read commit %s: %w", hash.Short(), err)
		}
		return append([]core.Hash{commit.Tree}, commit.Parents...), nil
	case core.ObjectTree:
		tree, err := r.LoadTree(hash)
		if err != nil {
			return nil, fmt.Errorf("cannot read tree %s: %w", hash.Short(), err)
		}
		children := make([]core.Hash, 0, len(tree.Entries))
		for _, entry := range tree.Entries {
			if IsSubmoduleMode(entry.Mode) {
				continue
			}
			children = append(children, entry.Hash)
		}
		return children, nil
	case core.ObjectChunkManifest:
		manifest, err := r.LoadChunkManifest(hash)
		if err != nil {
			return nil, err
		}
		children := make([]core.Hash, 0, len(manifest.Chunks))
		for _, chunk := range manifest.Chunks {
			children = append(children, chunk.Hash)
		}
		return children, nil
	default:
		return nil, nil
	}
}

func uniqueHashes(hashes []core.Hash) []core.Hash {
	seen := make(map[core.Hash]bool, len(hashes))
	out := make([]core.Hash, 0, len(hashes))
	for _, h := range hashes {
		if h.IsZero() || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i][:], out[j][:]) < 0
	})
	return out
}

// localTipHashes lists every tip this repository already has: local branches
// and remote-tracking refs. They are the haves of a fetch plan.
func (r *Repository) localTipHashes() []core.Hash {
	var out []core.Hash
	refsDir := r.RefsDir()
	filepath.Walk(refsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(refsDir, path)
		if relErr != nil {
			return nil
		}
		if rel != "." && hasDotComponent(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		data, err := ioutil.ReadFile(path)
		if err != nil {
			return nil
		}
		if h, err := core.HashFromHex(strings.TrimSpace(string(data))); err == nil {
			out = append(out, h)
		}
		return nil
	})
	return uniqueHashes(out)
}

// ---- fetch ----

func (r *Repository) fetchHTTP(baseURL, remoteName string, lazy bool) error {
	refs, err := httpListRefs(baseURL)
	if err != nil {
		return fmt.Errorf("list refs: %w", err)
	}

	branchRefs := make(map[string]core.Hash)
	for ref, hashStr := range refs {
		if !strings.HasPrefix(ref, "refs/heads/") {
			continue
		}
		hash, err := core.HashFromHex(strings.TrimSpace(hashStr))
		if err != nil {
			continue
		}
		branchRefs[strings.TrimPrefix(ref, "refs/heads/")] = hash
	}

	branches := make([]string, 0, len(branchRefs))
	for name := range branchRefs {
		branches = append(branches, name)
	}
	sort.Strings(branches)
	wants := make([]core.Hash, 0, len(branches))
	for _, name := range branches {
		wants = append(wants, branchRefs[name])
	}

	plan, err := httpPlanDAG(baseURL, wants, r.localTipHashes(), lazy)
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}

	if plan.Count > 0 {
		stage("plan", "%d objects (%s)", plan.Count, formatBytes(plan.Total))
		body := planDAGBody(wants, r.localTipHashes(), lazy)
		replan := func() (*httpPackPlan, error) {
			return httpPlanDAG(baseURL, wants, r.localTipHashes(), lazy)
		}
		if err := r.httpDownloadPack(baseURL, body, "application/json", plan, replan); err != nil {
			return fmt.Errorf("download: %w", err)
		}
	} else {
		stage("plan", "everything up to date")
	}

	// What the tip names must be here now; anything else means the plan and
	// the pack disagreed about the repository's state.
	for _, hash := range wants {
		if !r.HasObject(hash) {
			return fmt.Errorf("fetch: object %s is still missing after the transfer", hash.Short())
		}
	}

	updates := make(map[string]string, len(branchRefs))
	for name, hash := range branchRefs {
		updates["refs/remotes/"+remoteName+"/"+name] = hash.String()
	}
	if len(updates) > 0 {
		stage("refs", "updating %d refs", len(updates))
	}
	return r.applyRefUpdates(updates)
}

// ---- push ----

func (r *Repository) pushHTTP(baseURL, remoteName string, force bool) error {
	branches, _, err := r.ListBranches()
	if err != nil {
		return err
	}
	sort.Strings(branches)

	// One listing gives the whole preflight its state, the server's HEAD and
	// core.bare included. The refs are compared below as full names, so the
	// two keys that are not refs simply never match.
	remoteRefs, err := httpListRefs(baseURL)
	if err != nil {
		return fmt.Errorf("read remote refs: %w", err)
	}
	st := remoteState{
		Bare:       remoteRefs["core.bare"] == "true",
		HeadBranch: headBranchFromHEAD(remoteRefs["HEAD"]),
		Refs:       remoteRefs,
	}

	refNames, tips := r.localTips(branches)
	for _, ref := range refNames {
		if err := r.checkPushRef(st, ref, tips[ref], force); err != nil {
			return err
		}
	}

	branchRefs := make(map[string]core.Hash)
	var tipHashes []core.Hash
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
		tipHashes = append(tipHashes, hash)
	}

	missing, err := r.collectMissingHTTP(baseURL, tipHashes)
	if err != nil {
		return fmt.Errorf("collect objects: %w", err)
	}

	if len(missing) == 0 {
		stage("collect", "everything up to date")
	} else {
		stage("collect", "%d objects", len(missing))
		hashes := make([]core.Hash, 0, len(missing))
		for h := range missing {
			hashes = append(hashes, h)
		}
		sizes, err := r.objectSizes(hashes)
		if err != nil {
			return err
		}
		plan, err := planPack(hashes, sizes)
		if err != nil {
			return err
		}
		stage("pack", "%d objects (%s)", len(plan.Hashes), formatBytes(plan.Total))
		if err := r.httpUploadPack(baseURL, plan); err != nil {
			return err
		}
	}

	updates := make(map[string]string)
	for _, branchName := range branches {
		hash, ok := branchRefs[branchName]
		if !ok {
			continue
		}
		ref := "refs/heads/" + branchName
		if st.Refs[ref] == hash.String() {
			continue
		}
		updates[ref] = hash.String()
	}
	if len(updates) == 0 {
		return nil
	}

	stage("refs", "updating %d refs", len(updates))
	data, err := json.Marshal(refsRequest{Updates: updates, Force: force})
	if err != nil {
		return err
	}
	resp, err := httpDoStream("POST", httpURL(baseURL, "refs"), bytes.NewReader(data), int64(len(data)), "application/json")
	if err != nil {
		return fmt.Errorf("update refs: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(ioutil.Discard, io.LimitReader(resp.Body, 8192))
	fmt.Fprintf(os.Stderr, "pushed to %s\n", remoteName)
	return nil
}
