package repo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

// ---- request helpers ----

func postJSON(t *testing.T, url string, body interface{}) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func postHashes(t *testing.T, url string, hashes []core.Hash) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/x-qin-hashes", bytes.NewReader(core.EncodeHashes(hashes)))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func planDAG(t *testing.T, baseURL string, wants, haves []core.Hash, lazy bool) (*http.Response, planResponse) {
	t.Helper()
	body := map[string]interface{}{"wants": hexList(wants), "haves": hexList(haves), "lazy": lazy}
	resp := postJSON(t, baseURL+"/plan", body)
	var plan planResponse
	data := readBody(t, resp)
	if resp.StatusCode == 200 {
		if err := json.Unmarshal(data, &plan); err != nil {
			t.Fatalf("decode plan: %v (body %q)", err, data)
		}
	}
	return resp, plan
}

func hexList(hashes []core.Hash) []string {
	out := make([]string, len(hashes))
	for i, h := range hashes {
		out[i] = h.String()
	}
	return out
}

// downloadPack fetches the pack (or its suffix from offset) named by body.
func downloadPack(t *testing.T, baseURL, contentType string, body []byte, offset int64, id string) (*http.Response, []byte) {
	t.Helper()
	url := fmt.Sprintf("%s/pack?offset=%d", baseURL, offset)
	if id != "" {
		url += "&id=" + id
	}
	resp, err := http.Post(url, contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp, readBody(t, resp)
}

func dagBody(t *testing.T, wants, haves []core.Hash, lazy bool) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]interface{}{
		"wants": hexList(wants), "haves": hexList(haves), "lazy": lazy,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// installPack installs received pack bytes into r through the same unpackPack
// the server runs on an upload.
func installPack(t *testing.T, r *Repository, pack []byte) (int, error) {
	t.Helper()
	return installPackFile(t, r, pack)
}

func serveTestCommit(t *testing.T, r *Repository) core.Hash {
	t.Helper()
	dir := r.Path
	if err := ioutil.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFile(filepath.Join(dir, "file.txt")); err != nil {
		t.Fatal(err)
	}
	h, err := r.WriteCommit("Test", "commit")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// ---- /plan and /pack download ----

func TestServePlanDAGAndDownload(t *testing.T) {
	dir := t.TempDir()
	src, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	tip := serveTestCommit(t, src)

	server := startRepoServer(t, dir)
	defer server.Close()

	resp, plan := planDAG(t, server.URL, []core.Hash{tip}, nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("plan: status %d", resp.StatusCode)
	}
	if plan.Count < 3 { // commit + tree + blob
		t.Fatalf("plan count = %d, want at least 3", plan.Count)
	}
	if plan.PackID == "" || plan.TotalSize <= int64(core.PackHeaderSize) {
		t.Fatalf("plan looks empty: %+v", plan)
	}

	body := dagBody(t, []core.Hash{tip}, nil, false)
	resp, pack := downloadPack(t, server.URL, "application/json", body, 0, plan.PackID)
	if resp.StatusCode != 200 {
		t.Fatalf("download: status %d, body %q", resp.StatusCode, pack)
	}
	if got := resp.Header.Get("X-Pack-Id"); got != plan.PackID {
		t.Fatalf("X-Pack-Id = %q, want %q", got, plan.PackID)
	}
	if int64(len(pack)) != plan.TotalSize {
		t.Fatalf("received %d bytes, plan says %d", len(pack), plan.TotalSize)
	}

	dst, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n, err := installPack(t, dst, pack)
	if err != nil {
		t.Fatalf("install downloaded pack: %v", err)
	}
	if n != plan.Count {
		t.Fatalf("installed %d objects, plan said %d", n, plan.Count)
	}
	if !dst.HasObject(tip) {
		t.Fatal("commit did not arrive")
	}

	// With the tip declared as a have, nothing is left to send.
	resp, plan = planDAG(t, server.URL, []core.Hash{tip}, []core.Hash{tip}, false)
	if resp.StatusCode != 200 {
		t.Fatalf("plan with haves: status %d", resp.StatusCode)
	}
	if plan.Count != 0 {
		t.Fatalf("plan with the tip as a have: %d objects, want 0", plan.Count)
	}
}

func TestServePackDownloadSuffixes(t *testing.T) {
	dir := t.TempDir()
	src, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	tip := serveTestCommit(t, src)
	server := startRepoServer(t, dir)
	defer server.Close()

	body := dagBody(t, []core.Hash{tip}, nil, false)
	_, plan := planDAG(t, server.URL, []core.Hash{tip}, nil, false)

	resp, full := downloadPack(t, server.URL, "application/json", body, 0, plan.PackID)
	if resp.StatusCode != 200 {
		t.Fatalf("full download: status %d", resp.StatusCode)
	}
	for _, offset := range []int64{1, int64(core.PackHeaderSize), plan.TotalSize / 2, plan.TotalSize - 1, plan.TotalSize} {
		resp, suffix := downloadPack(t, server.URL, "application/json", body, offset, plan.PackID)
		if resp.StatusCode != 200 {
			t.Fatalf("offset %d: status %d", offset, resp.StatusCode)
		}
		if !bytes.Equal(suffix, full[offset:]) {
			t.Errorf("offset %d: suffix differs from the full pack's tail", offset)
		}
		if got := resp.Header.Get("X-Pack-Offset"); got != strconv.FormatInt(offset, 10) {
			t.Errorf("offset %d: X-Pack-Offset = %q", offset, got)
		}
	}

	// An offset past the end is a client error, not a short pack.
	resp, _ = downloadPack(t, server.URL, "application/json", body, plan.TotalSize+1, plan.PackID)
	if resp.StatusCode != 400 {
		t.Fatalf("offset past the end: status %d, want 400", resp.StatusCode)
	}

	// A stale id means the plan changed; the client must re-plan.
	resp, _ = downloadPack(t, server.URL, "application/json", body, 0, strings.Repeat("0", 64))
	if resp.StatusCode != 409 {
		t.Fatalf("wrong id: status %d, want 409", resp.StatusCode)
	}
}

func TestServePlanExplicitHashes(t *testing.T) {
	dir := t.TempDir()
	src, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	tip := serveTestCommit(t, src)
	commit, err := src.LoadCommit(tip)
	if err != nil {
		t.Fatal(err)
	}
	hashes := []core.Hash{tip, commit.Tree}

	server := startRepoServer(t, dir)
	defer server.Close()

	want, err := src.planPackForObjects(hashes)
	if err != nil {
		t.Fatal(err)
	}

	resp := postHashes(t, server.URL+"/plan", hashes)
	var plan planResponse
	data := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("explicit plan: status %d, body %q", resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Count != 2 || plan.PackID != want.ID.String() || plan.TotalSize != want.Total {
		t.Fatalf("explicit plan = %+v, want count 2, id %s, total %d", plan, want.ID.Short(), want.Total)
	}

	missing := core.HashFromBytes([]byte("never stored"))
	resp = postHashes(t, server.URL+"/plan", append(hashes, missing))
	data = readBody(t, resp)
	if resp.StatusCode != 409 {
		t.Fatalf("plan with a missing object: status %d, want 409 (body %q)", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), missing.String()) {
		t.Errorf("409 body does not name the missing object: %q", data)
	}
}

func TestServePlanLazySkipsChunks(t *testing.T) {
	dir := t.TempDir()
	src, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	src.Config.Core.ChunkMinSize = 128
	src.Config.Core.ChunkThreshold = 512
	src.Config.Core.ChunkMaxSize = 1024

	data := make([]byte, 3000)
	for i := range data {
		data[i] = byte(i % 256)
	}
	manifestHash, err := src.StoreChunkedFile(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := src.LoadChunkManifest(manifestHash)
	if err != nil {
		t.Fatalf("expected a chunk manifest: %v", err)
	}
	if len(manifest.Chunks) < 2 {
		t.Fatalf("fixture has %d chunks, need at least 2", len(manifest.Chunks))
	}

	tree := &Tree{Entries: []TreeEntry{
		{Name: "large.bin", Hash: manifestHash, Size: int64(len(data)), Mode: 0644},
	}}
	treeContent, _ := core.SerializeJSON(tree)
	treeHash, err := src.StoreObject(core.ObjectTree, treeContent)
	if err != nil {
		t.Fatal(err)
	}
	commit := Commit{Tree: treeHash, Author: "Test", Message: "chunked", Time: time.Now()}
	commitContent, _ := core.SerializeJSON(commit)
	tip, err := src.StoreObject(core.ObjectCommit, commitContent)
	if err != nil {
		t.Fatal(err)
	}

	server := startRepoServer(t, dir)
	defer server.Close()

	_, lazyPlan := planDAG(t, server.URL, []core.Hash{tip}, nil, true)
	_, fullPlan := planDAG(t, server.URL, []core.Hash{tip}, nil, false)
	if fullPlan.Count <= lazyPlan.Count {
		t.Fatalf("lazy plan %d objects, full plan %d: chunks were not included", lazyPlan.Count, fullPlan.Count)
	}

	body := dagBody(t, []core.Hash{tip}, nil, true)
	resp, pack := downloadPack(t, server.URL, "application/json", body, 0, lazyPlan.PackID)
	if resp.StatusCode != 200 {
		t.Fatalf("lazy download: status %d", resp.StatusCode)
	}
	dst, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installPack(t, dst, pack); err != nil {
		t.Fatalf("install lazy pack: %v", err)
	}
	if !dst.HasObject(manifestHash) || !dst.HasObject(treeHash) || !dst.HasObject(tip) {
		t.Fatal("lazy pack omitted the manifest, tree or commit")
	}
	for _, chunk := range manifest.Chunks {
		if dst.HasObject(chunk.Hash) {
			t.Error("lazy pack carried a chunk blob")
		}
	}
}

// ---- /has ----

func TestServeHas(t *testing.T) {
	dir := t.TempDir()
	src, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	h1, err := src.StoreObject(core.ObjectBlob, []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := src.StoreObject(core.ObjectBlob, []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	missing := core.HashFromBytes([]byte("absent"))

	server := startRepoServer(t, dir)
	defer server.Close()

	resp := postHashes(t, server.URL+"/has", []core.Hash{missing, h1, h2})
	data := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("/has: status %d, body %q", resp.StatusCode, data)
	}
	got, err := core.DecodeHashes(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []core.Hash{h1, h2}
	if len(got) != len(want) {
		t.Fatalf("has = %d hashes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("has[%d] = %s, want %s (request order must be kept)", i, got[i].Short(), want[i].Short())
		}
	}
}

// ---- /pack upload ----

func buildTestPack(t *testing.T, src *Repository, hashes []core.Hash) (*packPlan, []byte) {
	t.Helper()
	plan, err := src.planPackForObjects(hashes)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := src.writePackFrom(&buf, plan, 0); err != nil {
		t.Fatal(err)
	}
	return plan, buf.Bytes()
}

func putPack(t *testing.T, baseURL, id string, offset int64, total int64, body []byte) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("PUT", fmt.Sprintf("%s/pack/%s?offset=%d", baseURL, id, offset), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Pack-Size", strconv.FormatInt(total, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, strings.TrimSpace(string(data))
}

func headPack(t *testing.T, baseURL, id string) *http.Response {
	t.Helper()
	resp, err := http.Head(baseURL + "/pack/" + id)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestServePackUploadResumesAndInstalls(t *testing.T) {
	src, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var hashes []core.Hash
	for i := 0; i < 4; i++ {
		h, err := src.StoreObject(core.ObjectBlob, bytes.Repeat([]byte{byte(i)}, 500))
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	plan, pack := buildTestPack(t, src, hashes)

	dstDir := t.TempDir()
	dst, err := Init(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	server := startRepoServer(t, dstDir)
	defer server.Close()

	if resp := headPack(t, server.URL, plan.ID.String()); resp.StatusCode != 404 {
		t.Fatalf("HEAD before upload: status %d, want 404", resp.StatusCode)
	}

	half := int64(len(pack)) / 2
	resp, body := putPack(t, server.URL, plan.ID.String(), 0, plan.Total, pack[:half])
	if resp.StatusCode != 200 {
		t.Fatalf("first half: status %d, body %q", resp.StatusCode, body)
	}
	if strings.Contains(body, "installed") {
		t.Fatalf("first half reported installation: %q", body)
	}
	if resp := headPack(t, server.URL, plan.ID.String()); resp.Header.Get("X-Received") != strconv.FormatInt(half, 10) {
		t.Fatalf("HEAD after first half: X-Received = %q, want %d", resp.Header.Get("X-Received"), half)
	}

	// An offset the server has not reached is refused with the real offset.
	resp, body = putPack(t, server.URL, plan.ID.String(), plan.Total, plan.Total, nil)
	if resp.StatusCode != 409 {
		t.Fatalf("offset past received: status %d, want 409 (body %q)", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Pack-Received"); got != strconv.FormatInt(half, 10) {
		t.Fatalf("409 X-Pack-Received = %q, want %d", got, half)
	}

	resp, body = putPack(t, server.URL, plan.ID.String(), half, plan.Total, pack[half:])
	if resp.StatusCode != 200 {
		t.Fatalf("second half: status %d, body %q", resp.StatusCode, body)
	}
	if !strings.Contains(body, "installed") {
		t.Fatalf("completed upload did not report installation: %q", body)
	}
	if resp := headPack(t, server.URL, plan.ID.String()); resp.Header.Get("X-Done") != "1" {
		t.Fatalf("HEAD after install: X-Done = %q", resp.Header.Get("X-Done"))
	}
	for _, h := range hashes {
		if !dst.HasObject(h) {
			t.Errorf("object %s not installed", h.Short())
		}
	}
	if _, err := os.Stat(dst.incomingPartPath(plan.ID.String())); !os.IsNotExist(err) {
		t.Errorf("part file survived a completed install: %v", err)
	}
}

// A body longer than the declared size is refused rather than silently
// truncated: the extra byte means the two sides disagree about the pack.
func TestServePackPutRejectsOversizedBody(t *testing.T) {
	src, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := src.StoreObject(core.ObjectBlob, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	plan, pack := buildTestPack(t, src, []core.Hash{h})

	dstDir := t.TempDir()
	if _, err := Init(dstDir); err != nil {
		t.Fatal(err)
	}
	server := startRepoServer(t, dstDir)
	defer server.Close()

	resp, body := putPack(t, server.URL, plan.ID.String(), 0, plan.Total, append(pack, 'x'))
	if resp.StatusCode != 400 {
		t.Fatalf("oversized body: status %d, want 400 (body %q)", resp.StatusCode, body)
	}
}

func TestServePackPutRejectsBadID(t *testing.T) {
	dstDir := t.TempDir()
	if _, err := Init(dstDir); err != nil {
		t.Fatal(err)
	}
	server := startRepoServer(t, dstDir)
	defer server.Close()

	resp, _ := putPack(t, server.URL, "not-a-pack-id", 0, 0, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("bad id: status %d, want 400", resp.StatusCode)
	}
}

// ---- removed endpoints and path rules ----

func TestServeObjectsEndpointIsGone(t *testing.T) {
	dir := t.TempDir()
	src, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := src.StoreObject(core.ObjectBlob, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	server := startRepoServer(t, dir)
	defer server.Close()

	for _, method := range []string{"GET", "HEAD", "PUT"} {
		req, err := http.NewRequest(method, server.URL+"/objects/"+h.String(), strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s /objects/...: status %d, want 404", method, resp.StatusCode)
		}
	}
}

func TestServeRefGetRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(repoDir); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(root, "secret"), []byte("do not serve"), 0644); err != nil {
		t.Fatal(err)
	}
	server := startRepoServer(t, repoDir)
	defer server.Close()

	for _, path := range []string{
		"/ref/../../secret",
		"/ref/%2e%2e/%2e%2e/secret",
		"/ref/refs/../../secret",
	} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		data := readBody(t, resp)
		if resp.StatusCode == 200 {
			t.Errorf("GET %s: status 200, body %q", path, data)
		}
		if strings.Contains(string(data), "do not serve") {
			t.Errorf("GET %s leaked the file outside the repository", path)
		}
	}
}

func TestRepoServerRejectsTraversalAndUpwardWalk(t *testing.T) {
	root := t.TempDir()
	// The base directory is itself a repository; a missing child must not be
	// walked up into it.
	base, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.WriteRef("refs/heads/main", core.HashFromBytes([]byte("x")).String()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(&RepoServer{BasePath: root})
	defer server.Close()

	for _, path := range []string{"/../refs", "/./refs", "/missing/refs", "/..%2frefs"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("GET %s: status %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestServeRefsSkipsAtomicTempFiles(t *testing.T) {
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := r.StoreObject(core.ObjectBlob, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, QinDir, "refs", "heads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, QinDir, "refs", "heads", ".tmp-123"), []byte(h.String()), 0644); err != nil {
		t.Fatal(err)
	}

	server := startRepoServer(t, dir)
	defer server.Close()

	refs, err := httpListRefs(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for ref := range refs {
		if strings.Contains(ref, ".tmp-") {
			t.Errorf("ref listing exposed an atomic-write temp file: %s", ref)
		}
	}
}

// TestCollectForPlanBoundaryIsTheHaveSnapshot pins the fix for /plan scaling
// with the whole repository: a client's have is taken as the snapshot its tip
// names, so a tip the client already has plans zero objects instead of walking
// the have's entire history, and a one-file change sends the new commit, its
// tree and the changed object only.
func TestCollectForPlanBoundaryIsTheHaveSnapshot(t *testing.T) {
	r, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storeBlob := func(content string) core.Hash {
		h, err := r.StoreObject(core.ObjectBlob, []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	storeTree := func(entries []TreeEntry) core.Hash {
		content, err := core.SerializeJSON(&Tree{Entries: entries})
		if err != nil {
			t.Fatal(err)
		}
		h, err := r.StoreObject(core.ObjectTree, content)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	storeCommit := func(tree core.Hash, parents []core.Hash) core.Hash {
		content, err := core.SerializeJSON(Commit{Tree: tree, Parents: parents, Author: "Test", Message: "m", Time: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		h, err := r.StoreObject(core.ObjectCommit, content)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	blobA := storeBlob("a")
	blobB1 := storeBlob("b1")
	tree1 := storeTree([]TreeEntry{
		{Name: "a.txt", Hash: blobA, Mode: 0644},
		{Name: "b.txt", Hash: blobB1, Mode: 0644},
	})
	commit1 := storeCommit(tree1, nil)

	blobB2 := storeBlob("b2")
	tree2 := storeTree([]TreeEntry{
		{Name: "a.txt", Hash: blobA, Mode: 0644},
		{Name: "b.txt", Hash: blobB2, Mode: 0644},
	})
	commit2 := storeCommit(tree2, []core.Hash{commit1})

	for _, lazy := range []bool{true, false} {
		plan, err := r.collectForPlan([]core.Hash{commit2}, []core.Hash{commit2}, lazy)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan) != 0 {
			t.Fatalf("lazy=%v: a tip the client already has planned %d objects", lazy, len(plan))
		}

		plan, err = r.collectForPlan([]core.Hash{commit2}, []core.Hash{commit1}, lazy)
		if err != nil {
			t.Fatal(err)
		}
		got := make(map[core.Hash]bool, len(plan))
		for _, h := range plan {
			got[h] = true
		}
		want := map[core.Hash]bool{commit2: true, tree2: true, blobB2: true}
		for h := range want {
			if !got[h] {
				t.Errorf("lazy=%v: plan missed %s", lazy, h.Short())
			}
		}
		for h := range got {
			if !want[h] {
				t.Errorf("lazy=%v: plan carried %s, which the have already covers", lazy, h.Short())
			}
		}
	}
}
