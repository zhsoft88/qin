package repo

import (
	"fmt"
	"runtime"
	"strings"
)

// KnownOSes is the list of recognized operating system short identifiers.
// The numeric ID = index + 1. ID 0 means "all OSes" (default).
var KnownOSes = []string{
	"win",       // 1
	"mac",       // 2
	"linux",     // 3
	"freebsd",   // 4
	"netbsd",    // 5
	"openbsd",   // 6
	"dragonfly", // 7
	"solaris",   // 8
	"android",   // 9
}

// osNameToID maps OS name strings to numeric IDs.
var osNameToID map[string]uint8

// osIDToName maps numeric OS IDs to name strings. ID 0 is "".
var osIDToName map[uint8]string

func init() {
	osNameToID = make(map[string]uint8, len(KnownOSes))
	osIDToName = make(map[uint8]string, len(KnownOSes))
	for i, name := range KnownOSes {
		id := uint8(i + 1)
		osNameToID[name] = id
		osIDToName[id] = name
	}
}

// IsKnownOS reports whether s is a known OS identifier.
func IsKnownOS(s string) bool {
	_, ok := osNameToID[s]
	return ok
}

// OSID returns the numeric ID for an OS name string.
// Returns 0 if the name is unknown (0 = all OSes).
func OSID(name string) uint8 {
	if id, ok := osNameToID[name]; ok {
		return id
	}
	return 0
}

// OSName returns the name string for a numeric OS ID.
// Returns "" for ID 0 (all OSes), "?" for unknown IDs.
func OSName(id uint8) string {
	if id == 0 {
		return ""
	}
	if name, ok := osIDToName[id]; ok {
		return name
	}
	return "?"
}

// goosToOSID maps runtime.GOOS values to OS numeric IDs.
var goosToOSID = map[string]uint8{
	"windows":   1, // win
	"darwin":    2, // mac
	"linux":     3,
	"freebsd":   4,
	"netbsd":    5,
	"openbsd":   6,
	"dragonfly": 7,
	"solaris":   8,
	"android":   9,
}

// entryKey builds the composite map key for an OS-tagged entry.
// When osID is 0, key == path (backward compatible).
// When osID is non-zero, key == path + "\x00" + byte(osID).
func entryKey(path string, osID uint8) string {
	if osID == 0 {
		return path
	}
	return path + "\x00" + string([]byte{osID})
}

// EntryKey is the exported version of entryKey, for use by external packages.
func EntryKey(path string, osID uint8) string {
	return entryKey(path, osID)
}

// parseKey splits a composite key into the base path and OS ID.
// If no separator is found, OS ID is 0 (default entry).
func parseKey(key string) (path string, osID uint8) {
	for i := 0; i < len(key); i++ {
		if key[i] == '\x00' {
			if i+1 < len(key) {
				osID = uint8(key[i+1])
			}
			return key[:i], osID
		}
	}
	return key, 0
}

// ParseKey is the exported version of parseKey, for use by external packages.
func ParseKey(key string) (path string, osID uint8) {
	return parseKey(key)
}

// osMatch returns true if queryOS is in oss (empty oss matches all).
func osMatch(oss []uint8, queryOS uint8) bool {
	if len(oss) == 0 {
		return true
	}
	for _, id := range oss {
		if id == queryOS {
			return true
		}
	}
	return false
}

// isOSSpecific returns true if oss has at least one OS ID (not a default entry).
func isOSSpecific(oss []uint8) bool {
	return len(oss) > 0
}

// osIDForKey returns the OS ID to use as the key discriminator.
// If oss has a single OS, that OS ID is used; otherwise returns 0.
func osIDForKey(oss []uint8) uint8 {
	if len(oss) == 1 {
		return oss[0]
	}
	return 0
}














// visibleEntries filters the full index map to only entries that should be
// visible on the given OS. Returns a map keyed by clean path (no OS suffix).
// For each base path: if an OS-specific match exists it wins; otherwise default.
// Uses each entry's OSS field for OS match.
func visibleEntries(entries map[string]IndexEntry, currentOS uint8) map[string]IndexEntry {
	result := make(map[string]IndexEntry)

	for key, entry := range entries {
		path, _ := parseKey(key)
		if !osMatch(entry.OSS, currentOS) {
			continue
		}
		// OS-specific entry overrides default for the same path
		if existing, ok := result[path]; ok {
			if !isOSSpecific(existing.OSS) && isOSSpecific(entry.OSS) {
				result[path] = entry
			}
			continue
		}
		result[path] = entry
	}

	return result
}

// VisibleFiles filters index entries to show only those visible on the given OS.
func VisibleFiles(entries map[string]IndexEntry, osID uint8) map[string]IndexEntry {
	return visibleEntries(entries, osID)
}

// collectPaths extracts deduplicated clean paths from a map of composite keys.
func collectPaths(entries map[string]IndexEntry) []string {
	seen := make(map[string]bool)
	var paths []string
	for key := range entries {
		path, _ := parseKey(key)
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

// CurrentOSID returns the numeric OS ID for the current runtime OS.
func CurrentOSID() uint8 {
	if id, ok := goosToOSID[runtime.GOOS]; ok {
		return id
	}
	return 0
}

// currentOS returns the numeric OS ID for the current runtime OS.
func currentOS() uint8 {
	return CurrentOSID()
}

// ParseOSExpr parses a comma-separated OS expression into include/exclude sets.
//
// Syntax:
//
//	""           — empty (caller should use CurrentOSID as default)
//	"*"          — match any OS (include=nil, exclude=nil)
//	"win"        — match only windows
//	"!win"       — match everything except windows
//	"!win,!mac"  — match everything except windows and mac
//	"win,linux"  — match windows OR linux
//
// An empty expression returns (nil, nil, nil).
// Unknown OS names return an error.
func ParseOSExpr(s string) (include, exclude map[uint8]bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil, nil
	}
	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part == "*" {
			// * means match all — reset both sets
			return nil, nil, nil
		}
		if strings.HasPrefix(part, "!") {
			name := strings.TrimPrefix(part, "!")
			name = strings.TrimSpace(name)
			if !IsKnownOS(name) {
				return nil, nil, fmt.Errorf("unknown OS: %q", name)
			}
			if exclude == nil {
				exclude = make(map[uint8]bool)
			}
			exclude[OSID(name)] = true
		} else {
			if !IsKnownOS(part) {
				return nil, nil, fmt.Errorf("unknown OS: %q", part)
			}
			if include == nil {
				include = make(map[uint8]bool)
			}
			include[OSID(part)] = true
		}
	}
	return include, exclude, nil
}



// VisibleEntriesExpr filters the full index map using an include/exclude expression.
// For each base path: if an OS-specific match exists it wins; otherwise default.
// An empty expression (both nil) returns entries visible on the current OS.
func VisibleEntriesExpr(entries map[string]IndexEntry, include, exclude map[uint8]bool) map[string]IndexEntry {
	result := make(map[string]IndexEntry)

	for key, entry := range entries {
		path, os := parseKey(key)
		if !MatchOSExpr(os, include, exclude) {
			continue
		}
		// OS-specific match overrides default for the same path
		if existing, ok := result[path]; ok {
			if !isOSSpecific(existing.OSS) && isOSSpecific(entry.OSS) {
				result[path] = entry
			}
			continue
		}
		result[path] = entry
	}

	return result
}

// MatchOSExpr checks whether a given entry OS ID matches the include/exclude filter.
// entryOS is the key discriminator OS ID (0 = default entry, matches all filters).
// When both include and exclude are nil, all entries match.
func MatchOSExpr(entryOS uint8, include, exclude map[uint8]bool) bool {
	if include == nil && exclude == nil {
		return true
	}
	if entryOS == 0 {
		return true
	}
	if exclude[entryOS] {
		return false
	}
	if len(include) > 0 && !include[entryOS] {
		return false
	}
	return true
}

// OSNameOrStar returns the display name for an OS ID.
// 0 is displayed as "*" (all OSes).
func OSNameOrStar(id uint8) string {
	if id == 0 {
		return "*"
	}
	return OSName(id)
}
