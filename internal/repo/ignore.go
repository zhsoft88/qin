package repo

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// IgnoreMatcher checks whether a repo-relative path should be ignored.
type IgnoreMatcher struct {
	patterns []ignorePattern
	// hasNegate is true when any "!" re-include rule exists. It forces the
	// status walk to descend into ignored directories so re-included
	// children are still found.
	hasNegate bool
}

type ignorePattern struct {
	pattern  string
	negate   bool
	dirOnly  bool
	anchored bool
	// litPrefix is the literal prefix of the pattern before the first glob
	// metacharacter. Used to skip glob matching for paths that cannot match.
	litPrefix string
}

// LoadIgnoreMatcher reads .qinignore from the repo root.
// Returns an empty matcher if the file doesn't exist.
func (r *Repository) LoadIgnoreMatcher() (*IgnoreMatcher, error) {
	m := &IgnoreMatcher{}
	path := filepath.Join(r.Path, ".qinignore")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		p := ignorePattern{}

		if strings.HasPrefix(line, "!") {
			p.negate = true
			m.hasNegate = true
			line = line[1:]
		}

		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = line[:len(line)-1]
		}

		if strings.HasPrefix(line, "/") {
			p.anchored = true
			line = line[1:]
		}

		// Convert glob to simple pattern matching
		p.pattern = line
		p.litPrefix = literalPrefix(line)
		m.patterns = append(m.patterns, p)
	}

	return m, scanner.Err()
}

// Match checks if a repo-relative path should be ignored.
// dir should be true if the path is a directory.
func (m *IgnoreMatcher) Match(path string, dir bool) bool {
	ignored := false
	base := ""
	for _, p := range m.patterns {
		// Literal-prefix pre-check: if the pattern starts with literal text
		// that cannot appear at the start of the path (or basename for
		// unanchored patterns), the glob can never match — skip it.
		if p.litPrefix != "" && !strings.HasPrefix(path, p.litPrefix) {
			if p.anchored {
				continue
			}
			if base == "" {
				base = filepath.Base(path)
			}
			if !strings.HasPrefix(base, p.litPrefix) {
				continue
			}
		}
		if p.dirOnly {
			if dir && path == p.pattern {
				ignored = !p.negate
				continue
			}
			if !dir && strings.HasPrefix(path, p.pattern+"/") {
				ignored = !p.negate
				continue
			}
		}
		if matchGlob(path, p.pattern, p.anchored) {
			ignored = !p.negate
		}
	}
	return ignored
}

// literalPrefix returns the literal prefix of a glob pattern, stopping at the
// first glob metacharacter (* ? [ \). Empty when the pattern starts with a
// metacharacter or is itself empty.
func literalPrefix(p string) string {
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '*', '?', '[', '\\':
			return p[:i]
		}
	}
	return p
}

// matchGlob checks if path matches a glob pattern.
// Supports *, ?, and ** (as git does for directory matching).
func matchGlob(path, pattern string, anchored bool) bool {
	if anchored {
		return globMatch(path, pattern)
	}
	// Non-anchored: match against any suffix
	if globMatch(path, pattern) {
		return true
	}
	// Also try matching against basename
	base := filepath.Base(path)
	return globMatch(base, pattern)
}

// globMatch checks if s matches glob pattern p (supports *, ?, **, [...], \).
func globMatch(s, p string) bool {
	if p == "" {
		return s == ""
	}

	// ** matches across directory separators
	if strings.HasPrefix(p, "**") {
		rest := p[2:]
		if rest == "" {
			return true
		}
		for i := 0; i <= len(s); i++ {
			if globMatch(s[i:], rest) {
				return true
			}
		}
		return false
	}

	// * matches any chars except /
	if p[0] == '*' {
		rest := p[1:]
		for i := 0; i <= len(s); i++ {
			if i > 0 && s[i-1] == '/' {
				break
			}
			if globMatch(s[i:], rest) {
				return true
			}
		}
		return false
	}

	// ? matches any single char except /
	if p[0] == '?' {
		if s == "" || s[0] == '/' {
			return false
		}
		return globMatch(s[1:], p[1:])
	}

	// [...] character class / range
	if p[0] == '[' {
		if s == "" {
			return false
		}
		end := strings.IndexByte(p, ']')
		if end < 0 {
			// malformed, treat as literal
			if s[0] == '[' {
				return globMatch(s[1:], p[1:])
			}
			return false
		}
		chars := p[1:end]
		matched := false
		for i := 0; i < len(chars); i++ {
			if i+2 < len(chars) && chars[i+1] == '-' {
				// range like a-z
				if s[0] >= chars[i] && s[0] <= chars[i+2] {
					matched = true
					break
				}
				i += 2
			} else if s[0] == chars[i] {
				matched = true
				break
			}
		}
		if s[0] == '/' {
			matched = false
		}
		if !matched {
			return false
		}
		return globMatch(s[1:], p[end+1:])
	}

	// \ escapes next character
	if p[0] == '\\' && len(p) > 1 {
		if s == "" || s[0] != p[1] {
			return false
		}
		return globMatch(s[1:], p[2:])
	}

	// Literal match
	if s != "" && s[0] == p[0] {
		return globMatch(s[1:], p[1:])
	}

	return false
}

// MatchGlob checks if path matches a glob pattern (non-anchored, gitignore-style).
// Supports *, ?, and **.
func MatchGlob(path, pattern string) bool {
	return matchGlob(path, pattern, false)
}
