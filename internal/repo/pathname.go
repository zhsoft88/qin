package repo

import (
	"errors"
	"fmt"
	"path"
	"runtime"
	"strings"
)

// errUnrepresentable marks a stored name this host cannot create on disk. It is
// deliberately not errOutsideWorkTree: a name that escapes the repository has
// to fail the whole operation, while a name this platform merely cannot hold is
// skipped and reported — the same shape add uses for a file it cannot record.
var errUnrepresentable = errors.New("cannot be represented on this platform")

// hostGOOS is a variable so a test can ask the guard about a platform other
// than the one it runs on. hostPathError is parameterized by goos for the same
// reason: the decisions are testable everywhere even though the platform they
// describe is not. That is also why this lives in an untagged file — the
// project's rule is that anything worth testing belongs on the side of the
// build-tag line that runs everywhere.
var hostGOOS = func() string { return runtime.GOOS }

// hostPathError reports why name cannot be materialized on goos, or nil if it
// can. A name that would land outside the repository when read with goos's
// path syntax returns errOutsideWorkTree; one that merely cannot exist on that
// platform returns errUnrepresentable.
//
// Only Windows needs this. Linux and macOS accept every byte except "/" and
// NUL, and safeRepoPath has already refused both, so a name that reaches here
// is a legal filename there by construction.
func hostPathError(name, goos string) error {
	if goos != "windows" {
		return nil
	}

	// A backslash is a separator on Windows, so the name has to be judged by
	// Windows' rules rather than the slash rules safeRepoPath applied. That is
	// what makes "..\..\x" an escape here — on Linux it is one harmless
	// filename component. ".." is the only spelling that escapes, because the
	// write path joins this name onto the root: a volume or a rooted path does
	// not reset that join, but ".." is resolved by it.
	winName := strings.ReplaceAll(name, `\`, "/")
	clean := path.Clean(winName)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%w: %s", errOutsideWorkTree, name)
	}

	// Everything else Windows cannot create is skippable. A backslash is a
	// separator inside what is meant to be one file: Windows would create a
	// directory or a differently named file, not this one.
	if strings.ContainsRune(name, '\\') {
		return fmt.Errorf("%w: %s", errUnrepresentable, name)
	}

	for _, part := range strings.Split(winName, "/") {
		if part == "" {
			continue
		}
		if windowsComponentError(part) {
			return fmt.Errorf("%w: %s", errUnrepresentable, name)
		}
	}
	return nil
}

// windowsComponentError reports whether one path component is a name Windows
// refuses to create. The list is Win32's: reserved characters, control
// characters, trailing dot or space (Win32 strips those, silently turning the
// stored name into a different one), and reserved device names, which are
// reserved with or without an extension.
func windowsComponentError(part string) bool {
	if strings.ContainsAny(part, `<>:"|?*`) {
		return true
	}
	for i := 0; i < len(part); i++ {
		if part[i] < 0x20 {
			return true
		}
	}
	if last := part[len(part)-1]; last == '.' || last == ' ' {
		return true
	}
	base := part
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch strings.ToUpper(strings.TrimRight(base, " ")) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}
