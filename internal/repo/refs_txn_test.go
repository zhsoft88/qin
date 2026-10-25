package repo

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhsoft88/qin/internal/core"
)

func txnTestRepo(t *testing.T) (*Repository, core.Hash, core.Hash) {
	t.Helper()
	r, err := Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	h1, err := r.StoreObject(core.ObjectBlob, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := r.StoreObject(core.ObjectBlob, []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	return r, h1, h2
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(data))
}

func TestApplyRefUpdatesWritesAllAndRemovesJournal(t *testing.T) {
	r, h1, h2 := txnTestRepo(t)
	updates := map[string]string{
		"refs/heads/main":     h1.String(),
		"refs/heads/other":    h2.String(),
		"refs/remotes/o/main": h1.String(),
	}
	if err := r.applyRefUpdates(updates); err != nil {
		t.Fatalf("applyRefUpdates: %v", err)
	}
	for ref, want := range updates {
		if got := mustReadFile(t, filepath.Join(r.Path, QinDir, ref)); got != want {
			t.Errorf("%s = %s, want %s", ref, got, want)
		}
	}
	if _, err := os.Stat(r.refsTxnPath()); !os.IsNotExist(err) {
		t.Errorf("journal outlived a completed transaction: %v", err)
	}
}

func TestApplyRefUpdatesRejectsMissingObjectBeforeWriting(t *testing.T) {
	r, h1, _ := txnTestRepo(t)
	missing := core.HashFromBytes([]byte("never stored"))
	updates := map[string]string{
		"refs/heads/main":  h1.String(),
		"refs/heads/other": missing.String(),
	}
	if err := r.applyRefUpdates(updates); err == nil {
		t.Fatal("ref to a missing object was accepted")
	}
	if _, err := os.Stat(filepath.Join(r.Path, QinDir, "refs", "heads", "main")); !os.IsNotExist(err) {
		t.Error("a rejected transaction still wrote a ref")
	}
	if _, err := os.Stat(r.refsTxnPath()); !os.IsNotExist(err) {
		t.Error("a rejected transaction left a journal")
	}
}

func TestApplyRefUpdatesRejectsUnsafeRefName(t *testing.T) {
	r, h1, _ := txnTestRepo(t)
	if err := r.applyRefUpdates(map[string]string{"refs/../../escape": h1.String()}); err == nil {
		t.Fatal("traversing ref name was accepted")
	}
}

// A crash after the journal is durable but before the refs are written is the
// case the journal exists for: reopening the repository must finish the set.
func TestRepoAtReplaysInterruptedTransaction(t *testing.T) {
	r, h1, h2 := txnTestRepo(t)
	updates := map[string]string{
		"refs/heads/main":  h1.String(),
		"refs/heads/other": h2.String(),
	}
	data, err := json.Marshal(refsTxn{Version: refsTxnVersion, Updates: updates})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.txnDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(r.refsTxnPath(), data, 0644); err != nil {
		t.Fatal(err)
	}
	// Simulate the half-written state: one ref landed, the other did not.
	if err := r.WriteRef("refs/heads/main", h1.String()); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(r.Path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for ref, want := range updates {
		if got := mustReadFile(t, filepath.Join(reopened.Path, QinDir, ref)); got != want {
			t.Errorf("%s = %s, want %s", ref, got, want)
		}
	}
	if _, err := os.Stat(reopened.refsTxnPath()); !os.IsNotExist(err) {
		t.Errorf("replayed journal was not removed: %v", err)
	}
}

func TestRecoverRefsTxnFailsClosed(t *testing.T) {
	r, h1, _ := txnTestRepo(t)

	writeJournal := func(body []byte) {
		if err := os.MkdirAll(r.txnDir(), 0755); err != nil {
			t.Fatal(err)
		}
		if err := ioutil.WriteFile(r.refsTxnPath(), body, 0644); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name string
		body string
	}{
		{"corrupt json", `{"version":1,"updates":`},
		{"wrong version", `{"version":99,"updates":{"refs/heads/main":"` + h1.String() + `"}}`},
		{"empty updates", `{"version":1,"updates":{}}`},
		{"unsafe ref", `{"version":1,"updates":{"refs/../../x":"` + h1.String() + `"}}`},
		{"bad hash", `{"version":1,"updates":{"refs/heads/main":"xyz"}}`},
		{"missing object", `{"version":1,"updates":{"refs/heads/main":"` + core.HashFromBytes([]byte("gone")).String() + `"}}`},
	}
	for _, tc := range cases {
		writeJournal([]byte(tc.body))
		if err := r.recoverRefsTxn(); err == nil {
			t.Errorf("%s: recovery accepted it", tc.name)
		}
		if _, err := os.Stat(r.refsTxnPath()); err != nil {
			t.Errorf("%s: journal was removed despite the error: %v", tc.name, err)
		}
	}
}

func TestWriteRefIsAtomicAndLeavesNoTemp(t *testing.T) {
	r, h1, _ := txnTestRepo(t)
	if err := r.WriteRef("refs/heads/feat", h1.String()); err != nil {
		t.Fatal(err)
	}
	entries, err := ioutil.ReadDir(filepath.Join(r.Path, QinDir, "refs", "heads"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("atomic write left %s behind", e.Name())
		}
	}
}
