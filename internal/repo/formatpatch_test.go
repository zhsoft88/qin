package repo

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

func TestCommitsBetween(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	write := func(name, content string) string {
		f := filepath.Join(dir, name)
		if err := ioutil.WriteFile(f, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if err := r.AddFile(f); err != nil {
			t.Fatal(err)
		}
		h, err := r.WriteCommit("Test", name)
		if err != nil {
			t.Fatal(err)
		}
		return h.String()
	}

	hA, _ := core.HashFromHex(write("a.txt", "a"))
	hB, _ := core.HashFromHex(write("b.txt", "b"))
	hC, _ := core.HashFromHex(write("c.txt", "c"))

	// Linear range: (A, C] = [B, C], oldest first
	commits, err := r.CommitsBetween(hA, hC)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 || commits[0] != hB || commits[1] != hC {
		t.Fatalf("expected [B C], got %v", commits)
	}

	// base == head → empty
	commits, err = r.CommitsBetween(hC, hC)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 0 {
		t.Fatalf("expected empty range, got %v", commits)
	}

	// base not an ancestor → error
	if _, err := r.CommitsBetween(hC, hA); err == nil {
		t.Fatal("expected error for non-ancestor base")
	}

	// zero base → walk to root
	commits, err = r.CommitsBetween(core.Hash{}, hC)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 || commits[0] != hA || commits[2] != hC {
		t.Fatalf("expected [A B C], got %v", commits)
	}
}

func TestFormatPatchRoundTrip(t *testing.T) {
	srcDir, err := ioutil.TempDir("", "lo-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(srcDir)

	src, err := Init(srcDir)
	if err != nil {
		t.Fatal(err)
	}

	// Commit A: initial file
	fa := filepath.Join(srcDir, "a.txt")
	if err := ioutil.WriteFile(fa, []byte("hello a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := src.AddFile(fa); err != nil {
		t.Fatal(err)
	}
	hA, err := src.WriteCommit("Alice <alice@example.com>", "add a.txt")
	if err != nil {
		t.Fatal(err)
	}

	// Commit B: modify a.txt, add b.txt
	if err := ioutil.WriteFile(fa, []byte("hello a v2"), 0644); err != nil {
		t.Fatal(err)
	}
	fb := filepath.Join(srcDir, "b.txt")
	if err := ioutil.WriteFile(fb, []byte("hello b"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := src.AddFile(fa); err != nil {
		t.Fatal(err)
	}
	if err := src.AddFile(fb); err != nil {
		t.Fatal(err)
	}
	hB, err := src.WriteCommit("Alice <alice@example.com>", "modify a, add b")
	if err != nil {
		t.Fatal(err)
	}

	// Commit C: add a win-only OS variant
	fo := filepath.Join(srcDir, "os.txt")
	if err := ioutil.WriteFile(fo, []byte("win content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := src.AddFileOS(fo, "win"); err != nil {
		t.Fatal(err)
	}
	hC, err := src.WriteCommit("Bob <bob@example.com>", "add win os.txt")
	if err != nil {
		t.Fatal(err)
	}
	srcB, err := src.LoadCommit(hB)
	if err != nil {
		t.Fatal(err)
	}
	srcC, err := src.LoadCommit(hC)
	if err != nil {
		t.Fatal(err)
	}

	// Format (B, C] → 2 patch files, oldest first
	files, err := src.FormatPatch(hA, hC)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 patch files, got %d", len(files))
	}
	if files[0].Filename != "0001-modify-a-add-b.patch" {
		t.Fatalf("unexpected filename: %s", files[0].Filename)
	}
	if files[1].Filename != "0002-add-win-os-txt.patch" {
		t.Fatalf("unexpected filename: %s", files[1].Filename)
	}

	// The win variant must be encoded with its OS tag in the patch body
	// (path\x00<os_byte>). OSWin == 1.
	if !strings.Contains(files[1].Body, "os.txt\x00\x01") {
		t.Fatalf("win OS tag missing from patch body: %q", files[1].Body)
	}
	// Commit B's patch must carry a modification entry for a.txt
	if !strings.Contains(files[0].Body, "~ ") {
		t.Fatalf("expected modified entry in first patch: %q", files[0].Body)
	}

	// Apply the series to a fresh repo
	dstDir, err := ioutil.TempDir("", "lo-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dstDir)

	dst, err := Init(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		p := filepath.Join(dstDir, f.Filename)
		if err := ioutil.WriteFile(p, []byte(f.Render()), 0644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if err := dst.ApplyMailbox(paths); err != nil {
		t.Fatal(err)
	}

	// Working tree matches
	checkContent := func(name, want string) {
		data, err := ioutil.ReadFile(filepath.Join(dstDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q, want %q", name, string(data), want)
		}
	}
	checkContent("a.txt", "hello a v2")
	checkContent("b.txt", "hello b")
	checkContent("os.txt", "win content")

	// Two commits, newest first, preserving author/message/date
	headStr, err := dst.ResolveHEAD()
	if err != nil {
		t.Fatal(err)
	}
	hD2, _ := core.HashFromHex(headStr)
	c2, err := dst.LoadCommit(hD2)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Message != "add win os.txt" || c2.Author != "Bob <bob@example.com>" {
		t.Fatalf("commit 2 = %q by %q", c2.Message, c2.Author)
	}
	if !c2.Time.Equal(srcC.Time.Truncate(time.Second)) {
		t.Fatalf("commit 2 time %v, want %v", c2.Time, srcC.Time.Truncate(time.Second))
	}
	if len(c2.Parents) != 1 {
		t.Fatalf("commit 2 should have 1 parent, got %d", len(c2.Parents))
	}
	hD1 := c2.Parents[0]
	c1, err := dst.LoadCommit(hD1)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Message != "modify a, add b" || c1.Author != "Alice <alice@example.com>" {
		t.Fatalf("commit 1 = %q by %q", c1.Message, c1.Author)
	}
	if !c1.Time.Equal(srcB.Time.Truncate(time.Second)) {
		t.Fatalf("commit 1 time %v, want %v", c1.Time, srcB.Time.Truncate(time.Second))
	}

	// The OS variant survived into the index under its composite key
	idx, err := dst.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.Entries[entryKey("os.txt", OSWin)]; !ok {
		t.Fatalf("win variant of os.txt missing from replayed index")
	}

	// Replayed diff between the two commits matches the source commit C's diff
	diffD, err := dst.DiffCommits(hD1, hD2)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffD.Files) != 1 || diffD.Files[0].Name != "os.txt" || diffD.Files[0].OS != OSWin {
		t.Fatalf("replayed diff = %+v", diffD.Files)
	}
}

func TestParsePatchFileRoundTrip(t *testing.T) {
	when := time.Date(2026, 8, 30, 12, 34, 56, 0, time.UTC)
	body := "+ 5 f.txt\nZmlsZQ==\n====\n"

	cases := []struct {
		name    string
		message string
		want    string
	}{
		{"subject and body", "add feature\n\nThis adds a feature with details.\nSecond body line", "add feature\n\nThis adds a feature with details.\nSecond body line"},
		{"single line", "just a subject", "just a subject"},
		{"empty", "", "(no subject)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &PatchFile{
				Author:  "Alice <alice@example.com>",
				Date:    when,
				Message: tc.message,
				Body:    body,
			}
			parsed, err := ParsePatchFile([]byte(p.Render()))
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Author != p.Author {
				t.Fatalf("author = %q, want %q", parsed.Author, p.Author)
			}
			if !parsed.Date.Equal(when) {
				t.Fatalf("date = %v, want %v", parsed.Date, when)
			}
			if parsed.Message != tc.want {
				t.Fatalf("message = %q, want %q", parsed.Message, tc.want)
			}
			if parsed.Body != body {
				t.Fatalf("body = %q, want %q", parsed.Body, body)
			}
		})
	}

	// A patch without the --- separator must be rejected
	if _, err := ParsePatchFile([]byte("From: A\n\nno separator here\n")); err == nil {
		t.Fatal("expected error for missing --- separator")
	}
}

// TestApplyPatchOutsideRepo covers a patch as untrusted input. Its paths are
// joined straight onto the repository root, so without a guard a patch whose
// path is "../x" writes outside the working tree entirely.
func TestApplyPatchOutsideRepo(t *testing.T) {
	base, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	repoDir := filepath.Join(base, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	r, err := Init(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	victim := filepath.Join(base, "victim.txt")
	const original = "ORIGINAL SAFE CONTENT"
	if err := ioutil.WriteFile(victim, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// "PWNED\n" — the body is base64, as RenderPatch emits it.
	const body = "~ 6 ../victim.txt  (0000000000000000 -> 1111111111111111)\nUFdORUQK\n====\n"
	p := &PatchFile{
		Author:  "attacker <a@evil>",
		Date:    time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		Message: "innocuous",
		Body:    body,
	}

	// Go through ApplyPatchFile, the entry point `am` uses — it hands the
	// parsed Body to ApplyPatch, which is the text carrying the paths.
	if err := r.ApplyPatchFile([]byte(p.Render())); err == nil {
		t.Fatal("expected a patch path outside the repo to be refused")
	}

	got, err := ioutil.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("file outside the repo was overwritten: got %q", got)
	}

	// A delete line must be refused the same way.
	del := &PatchFile{
		Author:  "attacker <a@evil>",
		Date:    time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		Message: "innocuous",
		Body:    "- 6 ../victim.txt\n",
	}
	if err := r.ApplyPatchFile([]byte(del.Render())); err == nil {
		t.Fatal("expected a patch delete outside the repo to be refused")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("file outside the repo was removed: %v", err)
	}
}

// TestApplyPatchAcceptsFormatPatchEnvelope covers the `apply` entry point,
// which is handed whichever file the user names — a bare RenderPatch body, or
// a whole format-patch file.
//
// A format-patch file carries a mail envelope ahead of the body, and each of
// its lines reads as an operation: the bare "---" separator parses as a delete
// of a file named "--", whose separator-consumption loop then swallows the
// first real operation, and a message line beginning with "-" parses as a
// delete of whatever follows it. The envelope has to be stripped first.
func TestApplyPatchAcceptsFormatPatchEnvelope(t *testing.T) {
	r, dir := newTempRepo(t)

	content := []byte("NEWFILE\n")
	body := fmt.Sprintf("+ %d added.txt\n%s====\n", len(content), base64Encode(content))
	p := &PatchFile{
		Author: "Alice <alice@example.com>",
		Date:   time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		// Bullet lines exercise the message block, which sits between the
		// headers and the "---" separator.
		Message: "add a file\n\n- first bullet\n- second bullet",
		Body:    body,
	}

	if err := r.ApplyPatch([]byte(p.Render())); err != nil {
		t.Fatalf("apply of a format-patch file failed: %v", err)
	}

	got, err := ioutil.ReadFile(filepath.Join(dir, "added.txt"))
	if err != nil {
		t.Fatalf("body was not applied: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("wrote %q, want %q", got, content)
	}

	files, err := r.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["added.txt"]; !ok {
		t.Fatalf("added.txt missing from the index: %v", files)
	}
	// The envelope's own lines must not have been taken for operations.
	for _, bogus := range []string{"--", "first bullet", "second bullet", "add a file"} {
		if _, ok := files[bogus]; ok {
			t.Fatalf("envelope line %q was applied as a path", bogus)
		}
	}
}

// TestApplyPatchAcceptsBareBody is the counterpart of the envelope test: the
// body on its own, as ApplyPatchFile hands it to ApplyPatch, must still be
// applied directly. ParsePatchFile finds no "---" separator in it and returns
// an error, so the data is used as-is.
func TestApplyPatchAcceptsBareBody(t *testing.T) {
	r, dir := newTempRepo(t)

	content := []byte("BARE\n")
	body := fmt.Sprintf("+ %d bare.txt\n%s====\n", len(content), base64Encode(content))

	if err := r.ApplyPatch([]byte(body)); err != nil {
		t.Fatalf("apply of a bare body failed: %v", err)
	}

	got, err := ioutil.ReadFile(filepath.Join(dir, "bare.txt"))
	if err != nil {
		t.Fatalf("body was not applied: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("wrote %q, want %q", got, content)
	}
}
