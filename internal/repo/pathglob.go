package repo

import "strings"

// MatchPathGlob reports whether path matches pattern with shell pathname
// expansion semantics: the match is anchored at the start, "*", "?" and "[...]"
// never cross a "/", a whole "**" segment matches zero or more path segments,
// a wildcard does not match a name beginning with ".", and matching is
// case-sensitive.
//
// Both arguments use "/" as the separator. A leading empty segment (Unix) or a
// volume segment (Windows) is just another segment, so absolute patterns work.
//
// This is deliberately not MatchGlob: that one is gitignore-style, matching any
// suffix or basename and letting wildcards match dotfiles. The two answer
// different questions and must not be swapped for each other.
func MatchPathGlob(path, pattern string) bool {
	return matchPathSegments(strings.Split(path, "/"), strings.Split(pattern, "/"))
}

func matchPathSegments(path, pattern []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	if pattern[0] == "**" {
		// "**" matches zero or more segments, but never a hidden one: shell
		// globstar does not descend into dot directories it was not told about.
		if matchPathSegments(path, pattern[1:]) {
			return true
		}
		for i := 0; i < len(path); i++ {
			if isHiddenSegment(path[i]) {
				return false
			}
			if matchPathSegments(path[i+1:], pattern[1:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	// A wildcard never matches a leading dot; only a pattern segment that
	// starts with a literal dot (escaped or not) may.
	if isHiddenSegment(path[0]) && !patternStartsWithDot(pattern[0]) {
		return false
	}
	if !globMatch(path[0], pattern[0]) {
		return false
	}
	return matchPathSegments(path[1:], pattern[1:])
}

// isHiddenSegment reports whether one path segment begins with ".", which is
// what a shell wildcard refuses to match (including "." and "..").
func isHiddenSegment(s string) bool {
	return strings.HasPrefix(s, ".")
}

// patternStartsWithDot reports whether the pattern segment spells a literal
// leading dot, so it is allowed to match a hidden name.
func patternStartsWithDot(p string) bool {
	return strings.HasPrefix(p, ".") || strings.HasPrefix(p, `\.`)
}
