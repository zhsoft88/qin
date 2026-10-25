package repo

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

// mkWireCommit stores a blob, a tree and a commit directly, returning the
// commit hash. It keeps the transfer tests free of working-tree concerns.
func mkWireCommit(t *testing.T, r *Repository, name string, content []byte, parent []core.Hash) core.Hash {
	t.Helper()
	blob, err := r.StoreObject(core.ObjectBlob, content)
	if err != nil {
		t.Fatal(err)
	}
	tree := &Tree{Entries: []TreeEntry{{Name: name, Hash: blob, Size: int64(len(content)), Mode: 0644}}}
	treeContent, err := core.SerializeJSON(tree)
	if err != nil {
		t.Fatal(err)
	}
	treeHash, err := r.StoreObject(core.ObjectTree, treeContent)
	if err != nil {
		t.Fatal(err)
	}
	commit := Commit{Tree: treeHash, Parents: parent, Author: "Test", Message: name, Time: time.Now()}
	commitContent, err := core.SerializeJSON(commit)
	if err != nil {
		t.Fatal(err)
	}
	tip, err := r.StoreObject(core.ObjectCommit, commitContent)
	if err != nil {
		t.Fatal(err)
	}
	return tip
}

// TestCollectMissingHTTPMatchesCollectObjects pins the two definitions to each
// other: the layered /has walk must find exactly what a full local walk against
// an empty boundary finds, chunk manifests and all.
func TestCollectMissingHTTPMatchesCollectObjects(t *testing.T) {
	srcDir := t.TempDir()
	src, err := Init(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	src.Config.Core.ChunkMinSize = 128
	src.Config.Core.ChunkThreshold = 512
	src.Config.Core.ChunkMaxSize = 1024

	parent := mkWireCommit(t, src, "a.txt", []byte("first"), nil)
	blobA, err := src.StoreObject(core.ObjectBlob, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}

	data := make([]byte, 3000)
	for i := range data {
		data[i] = byte(i % 256)
	}
	manifestHash, err := src.StoreChunkedFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.LoadChunkManifest(manifestHash); err != nil {
		t.Fatalf("fixture should be chunked: %v", err)
	}
	tree := &Tree{Entries: []TreeEntry{
		{Name: "a.txt", Hash: blobA, Size: 5, Mode: 0644},
		{Name: "big.bin", Hash: manifestHash, Size: int64(len(data)), Mode: 0644},
	}}
	treeContent, _ := core.SerializeJSON(tree)
	treeHash, err := src.StoreObject(core.ObjectTree, treeContent)
	if err != nil {
		t.Fatal(err)
	}
	commit := Commit{Tree: treeHash, Parents: []core.Hash{parent}, Author: "Test", Message: "second", Time: time.Now()}
	commitContent, _ := core.SerializeJSON(commit)
	tip, err := src.StoreObject(core.ObjectCommit, commitContent)
	if err != nil {
		t.Fatal(err)
	}

	// The server is an empty push target: everything is missing from it.
	targetDir := t.TempDir()
	if _, err := InitBare(targetDir); err != nil {
		t.Fatal(err)
	}
	server := startRepoServer(t, targetDir)
	defer server.Close()

	empty, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want, err := src.collectObjects(empty, tip, false)
	if err != nil {
		t.Fatal(err)
	}

	got, err := src.collectMissingHTTP(server.URL, []core.Hash{tip})
	if err != nil {
		t.Fatalf("collectMissingHTTP: %v", err)
	}

	for h := range want {
		if !got[h] {
			t.Errorf("collectMissingHTTP missed %s", h.Short())
		}
	}
	for h := range got {
		if !want[h] {
			t.Errorf("collectMissingHTTP returned extra %s", h.Short())
		}
	}
}

// TestHTTPDownloadResumeAfterDisconnect interrupts the first pack response
// halfway. The client must keep the bytes it received, ask for the suffix, and
// install the result.
func TestHTTPDownloadResumeAfterDisconnect(t *testing.T) {
	srcDir := t.TempDir()
	src, err := Init(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	tip := mkWireCommit(t, src, "f.txt", bytes.Repeat([]byte("payload"), 2000), nil)
	if err := src.WriteRef("refs/heads/main", tip.String()); err != nil {
		t.Fatal(err)
	}

	inner, err := Open(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	var interrupted int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "POST" && req.URL.Path == "/pack" && req.URL.Query().Get("offset") == "0" {
			first := false
			once.Do(func() { first = true })
			if first {
				atomic.StoreInt32(&interrupted, 1)
				rec := httptest.NewRecorder()
				inner.ServeHTTP(rec, req)
				body := rec.Body.Bytes()
				w.Header().Set("Content-Type", "application/x-qin-pack")
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				w.WriteHeader(200)
				w.Write(body[:len(body)/2])
				w.(http.Flusher).Flush()
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
		}
		inner.ServeHTTP(w, req)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	local, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := local.SaveRemote("origin", server.URL); err != nil {
		t.Fatal(err)
	}
	if err := local.Fetch("origin"); err != nil {
		t.Fatalf("fetch should have resumed: %v", err)
	}
	if atomic.LoadInt32(&interrupted) != 1 {
		t.Fatal("the test never interrupted a download")
	}
	if !local.HasObject(tip) {
		t.Fatal("the commit did not arrive after the resumed download")
	}
	if _, err := local.ReadRef("refs/remotes/origin/main"); err != nil {
		t.Fatalf("tracking ref not written: %v", err)
	}
	if entries, err := ioutil.ReadDir(local.incomingDir()); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".part") {
				t.Errorf("a completed download left %s behind", e.Name())
			}
		}
	}
}

// TestHTTPUploadResumeAfterDisconnect interrupts the first pack upload partway.
// The receiver keeps what it read; the client's next attempt asks how much it
// has and sends only the rest.
func TestHTTPUploadResumeAfterDisconnect(t *testing.T) {
	targetDir := t.TempDir()
	target, err := InitBare(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := Open(targetDir)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	interrupted := false
	putRequests := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "PUT" && strings.HasPrefix(req.URL.Path, "/pack/") {
			mu.Lock()
			putRequests++
			first := !interrupted
			if first {
				interrupted = true
			}
			mu.Unlock()
			if first {
				half := req.ContentLength / 2
				if half <= 0 {
					t.Error("the first upload carried no bytes to interrupt")
				}
				req.Body = ioutil.NopCloser(io.LimitReader(req.Body, half))
				req.ContentLength = half
				inner.ServeHTTP(httptest.NewRecorder(), req)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
		}
		inner.ServeHTTP(w, req)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	localDir := t.TempDir()
	local, err := Init(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(localDir+"/f.txt", bytes.Repeat([]byte("x"), 20000), 0644); err != nil {
		t.Fatal(err)
	}
	if err := local.AddFile(localDir + "/f.txt"); err != nil {
		t.Fatal(err)
	}
	tip, err := local.WriteCommit("Test", "commit")
	if err != nil {
		t.Fatal(err)
	}
	if err := local.SaveRemote("origin", server.URL); err != nil {
		t.Fatal(err)
	}

	if err := local.Push("origin", false); err != nil {
		t.Fatalf("push should have resumed the upload: %v", err)
	}
	mu.Lock()
	requests := putRequests
	mu.Unlock()
	if !interrupted {
		t.Fatal("the test never interrupted an upload")
	}
	if requests < 2 {
		t.Fatalf("client sent %d PUTs, want an interrupted one plus a resume", requests)
	}
	got, err := target.ReadRef("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != tip.String() {
		t.Fatalf("target main = %s, want %s", strings.TrimSpace(got), tip.Short())
	}
	if !target.HasObject(tip) {
		t.Fatal("the commit did not reach the target")
	}
}

// TestHTTPUploadSkipsInstalledPack: once the receiver reports the pack done, a
// second upload of the same pack must not send another byte.
func TestHTTPUploadSkipsInstalledPack(t *testing.T) {
	targetDir := t.TempDir()
	target, err := InitBare(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := Open(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	puts := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "PUT" && strings.HasPrefix(req.URL.Path, "/pack/") {
			mu.Lock()
			puts++
			mu.Unlock()
		}
		inner.ServeHTTP(w, req)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	src, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var hashes []core.Hash
	for i := 0; i < 3; i++ {
		h, err := src.StoreObject(core.ObjectBlob, bytes.Repeat([]byte{byte(i)}, 100))
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	sizes, err := src.objectSizes(hashes)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planPack(hashes, sizes)
	if err != nil {
		t.Fatal(err)
	}

	if err := src.httpUploadPack(server.URL, plan); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	afterFirst := puts
	mu.Unlock()
	if afterFirst != 1 {
		t.Fatalf("first upload used %d PUT requests, want 1", afterFirst)
	}
	if err := src.httpUploadPack(server.URL, plan); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	afterSecond := puts
	mu.Unlock()
	if afterSecond != afterFirst {
		t.Fatalf("second upload sent %d more PUT requests, want 0", afterSecond-afterFirst)
	}
	for _, h := range hashes {
		if !target.HasObject(h) {
			t.Errorf("object %s is not on the target", h.Short())
		}
	}
}
