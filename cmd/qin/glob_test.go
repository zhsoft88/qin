package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestNormalizeGlobPattern(t *testing.T) {
	tests := []struct {
		line    string
		want    string
		dirOnly bool
	}{
		{"src/*.go", "src/*.go", false},
		{"./src/./*.go", "src/*.go", false},
		{"src//*.go", "src/*.go", false},
		{"src/*/", "src/*", true},
		{"/tmp//*.go", "/tmp/*.go", false},
		{"../lib/*.go", "../lib/*.go", false},
		{`a\/b/*.go`, "a/b/*.go", false},
	}
	for _, tt := range tests {
		got, dirOnly := normalizeGlobPattern(tt.line)
		if got != tt.want || dirOnly != tt.dirOnly {
			t.Errorf("normalizeGlobPattern(%q) = (%q, %v), want (%q, %v)",
				tt.line, got, dirOnly, tt.want, tt.dirOnly)
		}
	}

	// Backslashes are separators on Windows and escapes elsewhere, so this
	// half is pinned on the platform whose rule it is.
	if runtime.GOOS == "windows" {
		if got, _ := normalizeGlobPattern(`C:\src\*.go`); got != "C:/src/*.go" {
			t.Errorf("Windows normalization = %q, want C:/src/*.go", got)
		}
	} else {
		if got, _ := normalizeGlobPattern(`a\b/*.go`); got != `a\b/*.go` {
			t.Errorf("escaped segment = %q, want the backslash kept", got)
		}
	}
}

func TestGlobSyntax(t *testing.T) {
	tests := []struct {
		p            string
		meta, escape bool
	}{
		{"plain", false, false},
		{"src/*.go", true, false},
		{"a?b", true, false},
		{"[ab]", true, false},
		{`a\*b`, false, true},
		{`a\[b`, false, true},
		{`a\b`, false, true},
		{`a\\b`, false, true},
	}
	for _, tt := range tests {
		meta, escape := globSyntax(tt.p)
		if meta != tt.meta || escape != tt.escape {
			t.Errorf("globSyntax(%q) = (%v, %v), want (%v, %v)",
				tt.p, meta, escape, tt.meta, tt.escape)
		}
	}
}

func TestExpandAtFilesExpandsGlobs(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"top.go", ".hidden.go", "src/a.go", "src/b.txt", "src/.hidden", "src/sub/b.go"} {
		writeAt(t, root, rel, rel)
	}
	listDir := t.TempDir()
	list := filepath.Join(listDir, "globs.txt")
	content := "*.go\nsrc/*.go\n**/*.go\nsrc/*/\nnomatch*.zzz\nsrc/*.go\n"
	if err := ioutil.WriteFile(list, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := expandAtFiles([]string{"@" + list}, root, root, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"top.go", "src/a.go", "src/sub/b.go", "src/sub", "nomatch*.zzz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expanded %v, want %v", got, want)
	}
}

// TestExpandAtFilesExcludeNotExpanded pins the one branch that must stay
// byte-for-byte what it was: --exclude lines are patterns for the matcher, not
// paths to look for.
func TestExpandAtFilesExcludeNotExpanded(t *testing.T) {
	root := t.TempDir()
	writeAt(t, root, "debug.log", "log")
	list := filepath.Join(t.TempDir(), "excludes.txt")
	if err := ioutil.WriteFile(list, []byte("*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := expandAtFiles([]string{"@" + list}, root, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"*.log"}) {
		t.Fatalf("expanded %v, want the pattern untouched", got)
	}
}

// TestExpandListEntryEscapedLiteral: an escaped wildcard names a file literally,
// the way the shell would hand it over. A platform that refuses "*" in a
// filename cannot host the test.
func TestExpandListEntryEscapedLiteral(t *testing.T) {
	root := t.TempDir()
	if err := ioutil.WriteFile(filepath.Join(root, "a*b"), []byte("star"), 0644); err != nil {
		t.Skipf("this platform cannot name a file with '*': %v", err)
	}

	got := expandListEntry(`a\*b`, root, root)
	if !reflect.DeepEqual(got, []string{"a*b"}) {
		t.Fatalf("expandListEntry = %v, want the literal name a*b", got)
	}
}

// TestExpandListEntrySkipsRepoMetadata: the monitor's own storage is never
// content, even when a pattern names it explicitly. Pruning it leaves the
// pattern unmatched, hence the literal fallback.
func TestExpandListEntrySkipsRepoMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".qin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(root, ".qin", "config"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	got := expandListEntry(".qin/*", root, root)
	if !reflect.DeepEqual(got, []string{".qin/*"}) {
		t.Fatalf("expandListEntry = %v, want no match under .qin", got)
	}

	// A nested .qin is ordinary content: only the repository root's metadata
	// directory is special.
	writeAt(t, root, "sub/.qin/config", "{}")
	got = expandListEntry("sub/.qin/*", root, root)
	if !reflect.DeepEqual(got, []string{"sub/.qin/config"}) {
		t.Fatalf("expandListEntry = %v, want the nested .qin treated as content", got)
	}
}
