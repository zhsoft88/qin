package repo

import "testing"

func TestMatchPathGlob(t *testing.T) {
	tests := []struct {
		path, pattern string
		want          bool
	}{
		// "*" and "?" stay inside one segment.
		{"a.go", "*.go", true},
		{"sub/a.go", "*.go", false},
		{"src/a.go", "src/*.go", true},
		{"src/sub/a.go", "src/*.go", false},
		{"a.go", "?.go", true},
		{"ab.go", "?.go", false},
		{"a/b", "a?b", false},

		// Anchoring: a pattern is not tried against suffixes or basenames.
		{"src/a.go", "*.go", false},
		{"src/a.go", "src/a.go", true},
		{"x/src/a.go", "src/a.go", false},

		// "**" matches zero or more whole segments.
		{"a.go", "**/*.go", true},
		{"sub/a.go", "**/*.go", true},
		{"sub/deep/a.go", "**/*.go", true},
		{"src/a.go", "src/**/a.go", true},
		{"src/x/y/a.go", "src/**/a.go", true},
		{"a", "a/**", true},
		{"a/b/c", "a/**", true},
		{"a/b", "**/b", true},
		{"b", "**/b", true},

		// Character classes and ranges; a malformed "[" is literal.
		{"t", "[tx]", true},
		{"tx", "[tx]", false},
		{"a", "[a-c]", true},
		{"d", "[a-c]", false},
		{"[x", "[x", true},

		// "\" escapes the next character.
		{"*.go", `\*.go`, true},
		{"x.go", `\*.go`, false},
		{`a\b`, `a\\b`, true},

		// Case-sensitive, byte-wise.
		{"A.go", "*.go", true},
		{"A.GO", "*.go", false},

		// Wildcards do not match hidden names unless the pattern says ".".
		{".env", "*", false},
		{".env", ".*", true},
		{".env", `\.*`, true},
		{".env", "[.]env", false},
		{".hidden/a.go", "**/*.go", false},
		{"sub/.hidden/a", "**/.hidden/*", true},
		{"sub/.hidden/a", "**/*", false},
		{"..", "*", false},
		{"a/.h/b", "a/**/b", false},

		// Absolute paths work segment by segment, Unix and Windows spellings.
		{"/tmp/a.go", "/tmp/*.go", true},
		{"/tmp/sub/a.go", "/tmp/*.go", false},
		{"/a/b", "/**", true},
		{"C:/a/b.txt", "C:/a/*.txt", true},
		{"C:/a/b.txt", "C:/**/*.txt", true},

		// Empty segments are just segments, so a leading slash is not special.
		{"/a", "a", false},
	}
	for _, tt := range tests {
		if got := MatchPathGlob(tt.path, tt.pattern); got != tt.want {
			t.Errorf("MatchPathGlob(%q, %q) = %v, want %v", tt.path, tt.pattern, got, tt.want)
		}
	}
}
