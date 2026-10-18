package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zhsoft88/qin/internal/repo"
)

// expandListEntry expands one line read from an @file list. Only list entries
// are expanded: a command-line argument has already been through the shell, and
// a --exclude entry is a pattern in its own right, not a path to find.
//
// The semantics are the shell's pathname expansion, with one deliberate
// difference in provenance: the walk uses Lstat and never follows a symlink, the
// same invariant the add walkers keep. When nothing matches, the line is kept
// literally (bash's default), so the add walker reports the missing file
// instead of the pattern silently disappearing.
func expandListEntry(line, baseDir, repoRoot string) []string {
	norm, dirOnly := normalizeGlobPattern(line)
	hasMeta, hasEscape := globSyntax(norm)
	if !hasMeta {
		if !hasEscape {
			return []string{line}
		}
		return []string{filepath.FromSlash(unescapeGlob(norm))}
	}

	segs := strings.Split(norm, "/")
	prefix := 0
	for prefix < len(segs) && !segmentHasMeta(segs[prefix]) {
		prefix++
	}
	absPattern := filepath.IsAbs(filepath.FromSlash(norm))
	root := globWalkRoot(segs[:prefix], absPattern, baseDir)

	globstar := false
	for _, s := range segs {
		if s == "**" {
			globstar = true
			break
		}
	}

	var matches []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// An unreadable directory (or a vanished file) is no match, not a
			// new way for expansion to fail: the walker still gets its say.
			return nil
		}
		isRoot := path == root

		// The repository's own metadata is never content, even when the
		// pattern names it. Only the root .qin matches; a nested one is
		// ordinary content. The walk root is checked too: a literal prefix
		// can point straight into the metadata directory.
		if rel, err := filepath.Rel(repoRoot, path); err == nil && repo.IsRepoMetadataPath(filepath.ToSlash(rel)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		candidate, ok := globCandidate(path, baseDir, absPattern)
		if !ok {
			return nil
		}
		// "." and a filesystem root have no name to emit; the shell's
		// globstar does not produce them either. A named literal-prefix root
		// still can: "src/**" matches "src" itself.
		if candidate != "." && !(absPattern && isFilesystemRoot(path)) &&
			(!dirOnly || info.IsDir()) && repo.MatchPathGlob(candidate, norm) {
			matches = append(matches, filepath.FromSlash(candidate))
		}

		// Without a "**" segment the pattern cannot match below its own
		// depth, so stop descending. The directory itself was considered
		// above; this only bounds the children. The walk root is exempt: it
		// is the zero-segment case the pattern's first segment is tried
		// against.
		if info.IsDir() && !isRoot && !globstar && globDepth(candidate) >= len(segs) {
			return filepath.SkipDir
		}
		return nil
	})

	if len(matches) == 0 {
		return []string{filepath.FromSlash(unescapeGlob(norm))}
	}
	return matches
}

// normalizeGlobPattern converts a list line to slash form and folds away the
// segments that cannot affect a match. The leading run of empty segments is
// kept: it is what makes "/a" and "//server/share" absolute. Interior empty
// segments and "." are dropped. A trailing "/" asks for directories only.
func normalizeGlobPattern(line string) (string, bool) {
	p := line
	if runtime.GOOS == "windows" {
		// Windows filenames cannot hold a backslash, so every one of them is a
		// separator. That also means wildcards cannot be escaped there.
		p = strings.ReplaceAll(p, `\`, "/")
	} else {
		// A slash cannot be part of a filename either, so an escaped slash is
		// just the separator it spells.
		p = strings.ReplaceAll(p, `\/`, "/")
	}
	dirOnly := strings.HasSuffix(p, "/")

	segs := strings.Split(p, "/")
	leading := 0
	for leading < len(segs) && segs[leading] == "" {
		leading++
	}
	out := append([]string(nil), segs[:leading]...)
	for _, s := range segs[leading:] {
		if s == "" || s == "." {
			continue
		}
		out = append(out, s)
	}
	return strings.Join(out, "/"), dirOnly
}

// globSyntax reports whether p contains an unescaped wildcard and whether it
// contains an escape at all. A line with neither is an ordinary path and is
// returned untouched; a line with only escapes is a literal whose backslashes
// the shell would have removed.
func globSyntax(p string) (hasMeta, hasEscape bool) {
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '\\':
			if i+1 < len(p) {
				hasEscape = true
				i++
			}
		case '*', '?', '[':
			hasMeta = true
		}
	}
	return hasMeta, hasEscape
}

// segmentHasMeta reports whether one pattern segment has an unescaped wildcard.
func segmentHasMeta(seg string) bool {
	for i := 0; i < len(seg); i++ {
		if seg[i] == '\\' && i+1 < len(seg) {
			i++
			continue
		}
		if seg[i] == '*' || seg[i] == '?' || seg[i] == '[' {
			return true
		}
	}
	return false
}

// unescapeGlob removes the escaping backslashes. This is what bash does to a
// pattern that matched nothing before handing it to the program as a literal.
func unescapeGlob(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' && i+1 < len(p) {
			i++
			b.WriteByte(p[i])
			continue
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

// globWalkRoot turns the literal segment prefix of a pattern into the directory
// the walk starts from. Only that prefix is travelled: the wildcard segments
// decide which of its descendants match.
func globWalkRoot(prefix []string, absPattern bool, baseDir string) string {
	if !absPattern {
		if len(prefix) == 0 {
			return baseDir
		}
		return filepath.Join(baseDir, filepath.FromSlash(unescapeGlob(strings.Join(prefix, "/"))))
	}
	native := filepath.FromSlash(unescapeGlob(strings.Join(prefix, "/")))
	if native == "" {
		// The pattern is rooted at "/" itself.
		return string(filepath.Separator)
	}
	// "C:" alone is drive-relative; the walk root has to be "C:\".
	if vol := filepath.VolumeName(native); vol != "" && len(vol) == len(native) {
		native += string(filepath.Separator)
	}
	return native
}

// globCandidate renders a walked path the way the pattern is spelled: absolute
// for an absolute pattern, otherwise relative to baseDir.
func globCandidate(path, baseDir string, absPattern bool) (string, bool) {
	if absPattern {
		return filepath.ToSlash(path), true
	}
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// globDepth is the number of segments in a candidate. Counted on the spelling
// the pattern uses, so an absolute pattern's leading empty segment counts just
// like the pattern's own.
func globDepth(candidate string) int {
	return len(strings.Split(candidate, "/"))
}

// isFilesystemRoot reports whether p is "/", "C:\" or a volume name alone.
func isFilesystemRoot(p string) bool {
	vol := filepath.VolumeName(p)
	rest := p[len(vol):]
	return rest == "" || rest == string(filepath.Separator)
}
