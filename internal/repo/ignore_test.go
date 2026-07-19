package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func writeIgnore(t *testing.T, dir, content string) {
	t.Helper()
	if err := ioutil.WriteFile(filepath.Join(dir, ".qinignore"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestIgnoreMatcherBasic(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	writeIgnore(t, dir, "*.log\nbuild/\n!important.log\n/config.yaml")

	m, err := repo.LoadIgnoreMatcher()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path  string
		dir   bool
		ignored bool
	}{
		{"debug.log", false, true},
		{"output.log", false, true},
		{"important.log", false, false}, // negated
		{"build", true, true},
		{"build/output.o", false, true},
		{"src/main.go", false, false},
		{"config.yaml", false, true},   // anchored
	}

	for _, tt := range tests {
		got := m.Match(tt.path, tt.dir)
		if got != tt.ignored {
			t.Errorf("Match(%q, dir=%v) = %v, want %v", tt.path, tt.dir, got, tt.ignored)
		}
	}
}

func TestIgnoreMatcherNoFile(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	m, err := repo.LoadIgnoreMatcher()
	if err != nil {
		t.Fatal(err)
	}

	if m.Match("anything.go", false) {
		t.Fatal("expected no matches with empty ignore file")
	}
}

func TestIgnoreMatcherCommentsAndBlanks(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	writeIgnore(t, dir, "# this is a comment\n\n*.txt\n")

	m, err := repo.LoadIgnoreMatcher()
	if err != nil {
		t.Fatal(err)
	}

	if !m.Match("notes.txt", false) {
		t.Fatal("expected notes.txt to be ignored")
	}
	if m.Match("main.go", false) {
		t.Fatal("expected main.go not to be ignored")
	}
}

func TestIgnoreStatusFiltering(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	writeIgnore(t, dir, "*.o\nbuild/\n")

	// Tracked file
	ioutil.WriteFile(filepath.Join(dir, "main.c"), []byte("code"), 0644)
	repo.AddFile(filepath.Join(dir, "main.c"))

	// Ignored files
	ioutil.WriteFile(filepath.Join(dir, "main.o"), []byte("obj"), 0644)
	os.Mkdir(filepath.Join(dir, "build"), 0755)
	ioutil.WriteFile(filepath.Join(dir, "build", "out.bin"), []byte("binary"), 0644)

	// Untracked non-ignored file
	ioutil.WriteFile(filepath.Join(dir, "README.md"), []byte("readme"), 0644)

	s, err := repo.WorkTreeStatus()
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Untracked) != 1 || s.Untracked[0] != "README.md" {
		t.Fatalf("expected untracked [README.md], got %v", s.Untracked)
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		s       string
		p       string
		matched bool
	}{
		{"foo.txt", "*.txt", true},
		{"foo.go", "*.txt", false},
		{"src/foo.go", "*.go", true},  // non-anchored matches suffix
		{"file.txt", "file.?xt", true},
		{"file.xxt", "file.?xt", true},
		{"file.txtt", "file.?xt", false},
		// character class
		{"file.txt", "file.[tx]xt", true},
		{"file.axt", "file.[tx]xt", false},
		// character range
		{"file.axt", "file.[a-c]xt", true},
		{"file.dxt", "file.[a-c]xt", false},
		{"log.1", "log.[0-9]", true},
		{"log.a", "log.[0-9]", false},
		// escaping
		{"file*txt", "file\\*txt", true},
		{"file.txt", "file\\*txt", false},
		{"file[1].txt", "file\\[1\\].txt", true},
		{"file.txt", "file\\[1\\].txt", false},
	}

	for _, tt := range tests {
		got := matchGlob(tt.s, tt.p, false)
		if got != tt.matched {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.s, tt.p, got, tt.matched)
		}
	}
}

func TestIgnoreNegation(t *testing.T) {
	dir, err := ioutil.TempDir("", "lo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	writeIgnore(t, dir, "*.log\n!important.log\nsecret/*\n!secret/README.md")

	m, err := repo.LoadIgnoreMatcher()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path    string
		ignored bool
	}{
		{"debug.log", true},
		{"important.log", false},  // negated
		{"secret/data.key", true},
		{"secret/README.md", false}, // negated
		{"src/main.go", false},
	}

	for _, tt := range tests {
		got := m.Match(tt.path, false)
		if got != tt.ignored {
			t.Errorf("Match(%q) = %v, want %v", tt.path, got, tt.ignored)
		}
	}
}
