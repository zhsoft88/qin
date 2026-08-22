package repo

import (
	"fmt"
	"runtime"
	"strings"
)

// OS bitmask constants — each bit of the OSS byte represents one OS.
const (
	OSWin   uint8 = 1 << 0 // 1 — Microsoft Windows
	OSMac   uint8 = 1 << 1 // 2 — Apple macOS
	OSLinux uint8 = 1 << 2 // 4 — Linux
)

// KnownOSes is the list of recognized operating system short identifiers,
// in bit order. An OSS bitmask (1=win, 2=mac, 4=linux) has one bit per OS;
// mask 0 means "all OSes" (default).
var KnownOSes = []string{
	"win",
	"mac",
	"linux",
}

// osNameToID maps OS name strings to their bitmask values.
var osNameToID map[string]uint8

// osIDToName maps OS bitmask values to name strings. Mask 0 is "".
var osIDToName map[uint8]string

func init() {
	osNameToID = make(map[string]uint8, len(KnownOSes))
	osIDToName = make(map[uint8]string, len(KnownOSes))
	for i, name := range KnownOSes {
		bit := uint8(1) << uint(i)
		osNameToID[name] = bit
		osIDToName[bit] = name
	}
}

// IsKnownOS reports whether s is a known OS identifier.
func IsKnownOS(s string) bool {
	_, ok := osNameToID[s]
	return ok
}

// OSID returns the bitmask value for an OS name string.
// Returns 0 if the name is unknown (0 = all OSes).
func OSID(name string) uint8 {
	if id, ok := osNameToID[name]; ok {
		return id
	}
	return 0
}

// OSName returns the name string for an OS bitmask value.
// Returns "" for mask 0 (all OSes), "?" for unknown masks.
func OSName(id uint8) string {
	if id == 0 {
		return ""
	}
	if name, ok := osIDToName[id]; ok {
		return name
	}
	return "?"
}

// OSNames returns the names of the OSes set in the mask, in KnownOSes order.
func OSNames(mask uint8) []string {
	var names []string
	for i, name := range KnownOSes {
		if mask&(1<<uint(i)) != 0 {
			names = append(names, name)
		}
	}
	return names
}

// goosToOSID maps runtime.GOOS values to OS bitmask values.
var goosToOSID = map[string]uint8{
	"windows": OSWin,
	"darwin":  OSMac,
	"linux":   OSLinux,
}

// entryKey builds the composite map key for an OS-tagged entry.
// The key discriminator is the entry's OSS bitmask: when the mask is 0,
// key == path; otherwise key == path + "\x00" + mask byte.
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

// parseKey splits a composite key into the base path and OS mask.
// If no separator is found, the OS mask is 0 (default entry).
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

// osMatch returns true if oss includes queryOS (mask 0 matches all OSes).
func osMatch(oss, queryOS uint8) bool {
	return oss == 0 || oss&queryOS != 0
}

// osExactMatch returns true if oss applies ONLY to queryOS.
func osExactMatch(oss, queryOS uint8) bool {
	return oss != 0 && oss == queryOS
}

// isOSSpecific returns true if oss is a non-zero mask (not a default entry).
func isOSSpecific(oss uint8) bool {
	return oss != 0
}

// visibleEntries filters the full index map to only entries that should be
// visible on the given OS. Returns a map keyed by clean path (no OS suffix).
// For each base path: if an OS-specific match exists it wins; otherwise default.
// Uses each entry's OSS mask for OS match.
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

// CurrentOSID returns the OS bitmask for the current runtime OS.
// Returns 0 (all OSes) for unrecognized platforms.
func CurrentOSID() uint8 {
	if id, ok := goosToOSID[runtime.GOOS]; ok {
		return id
	}
	return 0
}

// currentOS returns the OS bitmask for the current runtime OS.
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

// MaskFromOSExpr converts include/exclude sets into an OS bitmask.
// A pure include list yields the union of the included bits; an exclude
// list yields all known bits except the excluded ones. Returns 0 (all
// OSes) for an empty expression.
func MaskFromOSExpr(include, exclude map[uint8]bool) uint8 {
	var mask uint8
	switch {
	case len(include) > 0 && len(exclude) == 0:
		for id := range include {
			mask |= id
		}
	case len(exclude) > 0:
		for _, name := range KnownOSes {
			id := OSID(name)
			if !exclude[id] {
				mask |= id
			}
		}
	}
	return mask
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

// MatchOSExpr checks whether an entry OS mask matches the include/exclude filter.
// entryOS is the entry's OS bitmask (0 = default entry, matches all filters).
// A mask matches when it overlaps at least one included bit and contains no
// excluded bit. When both include and exclude are nil, all entries match.
func MatchOSExpr(entryOS uint8, include, exclude map[uint8]bool) bool {
	if include == nil && exclude == nil {
		return true
	}
	if entryOS == 0 {
		return true
	}
	for id := range exclude {
		if entryOS&id != 0 {
			return false
		}
	}
	if len(include) > 0 {
		for id := range include {
			if entryOS&id != 0 {
				return true
			}
		}
		return false
	}
	return true
}

// OSNameOrStar returns the display name for an OS mask.
// 0 is displayed as "*" (all OSes).
func OSNameOrStar(id uint8) string {
	if id == 0 {
		return "*"
	}
	return OSName(id)
}
