package repo

import (
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf16"
)

// Decoding a kernel's event buffer is pure byte manipulation, so it lives here
// rather than in the platform file that reads the buffer.
//
// The split is deliberate. What is hard to get right — non-obvious alignment,
// lengths that count padding, a terminator encoded as a zero offset — is also
// what is hard to debug on the machine it runs on, and exactly what can be
// tested anywhere given nothing but bytes. It also means the inotify constants
// are declared here rather than taken from syscall, which is what keeps this
// file free of a build tag. The values are the kernel ABI's, not Go's, and
// fsmonitor_watch_linux.go asserts at compile time that they still agree.
const (
	inModify     = 0x00000002
	inAttrib     = 0x00000004
	inCloseWrite = 0x00000008
	inMovedFrom  = 0x00000040
	inMovedTo    = 0x00000080
	inCreate     = 0x00000100
	inDelete     = 0x00000200
	inDeleteSelf = 0x00000400
	inMoveSelf   = 0x00000800
	inQOverflow  = 0x00004000
	inIgnored    = 0x00008000
	inIsDir      = 0x40000000
	inOnlyDir    = 0x01000000
	inExclUnlink = 0x04000000
)

// inotifyEventSize is the fixed header every queued event begins with:
// wd, mask, cookie, then the name length.
const inotifyEventSize = 16

var errInotifyMalformed = errors.New("malformed inotify event buffer")

// lostEvent describes a change the watcher was told about but cannot report
// correctly, which ends the coverage epoch. Every one of these is a case where
// silence would otherwise be mistaken for "nothing changed" — the one failure
// this feature must not have.
type lostEvent struct{ reason string }

func (e *lostEvent) Error() string { return "fsmonitor: coverage lost: " + e.reason }

func lost(reason string) error { return &lostEvent{reason: reason} }

// parseInotifyEvents decodes one read() buffer.
//
// names maps a watch descriptor to the repo-relative directory it stands for.
// It returns the events, or a non-nil error meaning the epochs's coverage has
// ended and the caller must rebuild.
//
// Each queued event is a 16-byte header followed by the name, NUL-padded to a
// 4-byte boundary. The length field counts that padding, so the name must be
// trimmed of trailing NULs before it is compared to anything: an untrimmed
// "a.txt\x00\x00\x00" matches no index entry, and every path would silently
// stop being reported.
//
// Paths built here are not re-validated against the repository: this input
// comes from the kernel, about watches this process registered, so a path that
// escapes the root cannot arise. The place where paths are untrusted is the
// journal, which is a file on disk, and validChangePath is applied there.
func parseInotifyEvents(buf []byte, names map[int32]string) ([]watchEvent, error) {
	var out []watchEvent
	for off := 0; off < len(buf); {
		if len(buf)-off < inotifyEventSize {
			return nil, errInotifyMalformed
		}
		wd := int32(binary.LittleEndian.Uint32(buf[off : off+4]))
		mask := binary.LittleEndian.Uint32(buf[off+4 : off+8])
		nameLen := int(binary.LittleEndian.Uint32(buf[off+12 : off+16]))
		off += inotifyEventSize
		if nameLen < 0 || len(buf)-off < nameLen {
			return nil, errInotifyMalformed
		}
		name := trimTrailingNUL(buf[off : off+nameLen])
		off += nameLen

		// The queue dropped events. Whatever was lost is unknowable, so the
		// only honest response is to stop claiming coverage.
		if mask&inQOverflow != 0 {
			return nil, lost("inotify queue overflow")
		}
		// A watch was removed. We remove none ourselves except at shutdown,
		// and the kernel sends this when a watched directory's inode goes
		// away; the directory's own watch is dead from here, but the parent's
		// watch still reports its re-creation, so this alone costs nothing.
		if mask&inIgnored != 0 {
			delete(names, wd)
			continue
		}

		dir, ok := names[wd]
		if !ok {
			return nil, lost("inotify event for an unknown watch descriptor")
		}
		if name == "" {
			// An event about the watched directory itself. A move takes the
			// whole subtree away and is reported only as the directory name
			// on the parent, so the files inside are never named — the index
			// paths under it would go unchecked and their deletion unseen.
			// A delete, by contrast, reports every child individually before
			// this arrives, so it is fully covered and must not end the
			// epoch: removing a build directory is routine, and ending the
			// epoch would mean a full scan every time.
			if mask&inMoveSelf != 0 {
				return nil, lost("a watched directory was moved")
			}
			continue
		}

		// A directory moving away is the one loss the events do not announce
		// file by file: the kernel reports "sub" on its parent, never
		// "sub/a.txt", so the index paths underneath it would go unchecked
		// and their disappearance unseen.
		if inotifyLostDir(mask) {
			return nil, lost("a watched subtree was moved away")
		}

		path := name
		if dir != "" {
			path = dir + "/" + name
		}
		out = append(out, watchEvent{
			Path: path,
			Kind: inotifyKind(mask),
			Dir:  mask&inIsDir != 0,
		})
	}
	return out, nil
}

// inotifyKind reduces a raw mask to the only distinction the consumer makes.
func inotifyKind(mask uint32) watchKind {
	if mask&(inDelete|inMovedFrom) != 0 {
		return watchRemoved
	}
	return watchChanged
}

// inotifyLostDir reports whether an event about a named directory means the
// epoch is over: a directory that moves away takes its contents' names with it.
func inotifyLostDir(mask uint32) bool {
	return mask&inIsDir != 0 && mask&inMovedFrom != 0
}

// trimTrailingNUL returns b without its NUL padding. The result is a string
// sharing b's bytes only until b is reused, which it is.
func trimTrailingNUL(b []byte) string {
	n := len(b)
	for n > 0 && b[n-1] == 0 {
		n--
	}
	return string(b[:n])
}

// normalizeWatchRel converts a path reported relative to the watched root into
// the canonical repo-relative slash form, reporting false for anything the
// index could never name.
func normalizeWatchRel(name string) (string, bool) {
	p := strings.ReplaceAll(name, `\`, "/")
	return p, validChangePath(p)
}

// ---- Windows: FILE_NOTIFY_INFORMATION ----

// The record layout ReadDirectoryChangesW writes, and the five actions it can
// report. These are the platform ABI's values, declared here for the same
// reason the inotify constants are: so that the decoding can be tested
// anywhere, including on a machine that cannot run it. Nothing about this
// decoding needs Windows — only the call that fills the buffer does.
const (
	fniHeaderSize = 12 // NextEntryOffset, Action, FileNameLength

	fniActionAdded          = 1
	fniActionRemoved        = 2
	fniActionModified       = 3
	fniActionRenamedOldName = 4
	fniActionRenamedNewName = 5
)

var errWindowsNotifyMalformed = errors.New("malformed FILE_NOTIFY_INFORMATION buffer")

// parseWindowsNotify decodes one buffer of change records.
//
// buf must already be trimmed to the byte count the call reported; the region
// beyond it is untouched and may hold anything.
//
// Every field is read with encoding/binary rather than by overlaying a struct.
// The struct the standard library defines for this record declares FileName as
// a single uint16 rather than an array, so its size is 16 bytes where the real
// record's is 12, and the name — whose length is a byte count, not a character
// count — would land at the wrong offset. Offsets here are only guaranteed to
// be even, so on a platform with stricter alignment a struct overlay faults
// rather than merely misreads.
//
// Records are chained by NextEntryOffset, which is relative to the start of
// the record it appears in and is zero for the last one. That zero is the only
// terminator: a name length is not a reliable end, because the trailing record
// may have been truncated by the buffer.
func parseWindowsNotify(buf []byte) ([]watchEvent, error) {
	var out []watchEvent
	for off := 0; ; {
		if len(buf)-off < fniHeaderSize {
			return nil, errWindowsNotifyMalformed
		}
		next := int(int32(binary.LittleEndian.Uint32(buf[off : off+4])))
		action := binary.LittleEndian.Uint32(buf[off+4 : off+8])
		nameLen := int(binary.LittleEndian.Uint32(buf[off+8 : off+12]))

		// The name is UTF-16, so its byte count is even; an odd one cannot be
		// produced by the call and means the record is not what it claims.
		if nameLen < 0 || nameLen%2 != 0 || fniHeaderSize+nameLen > len(buf)-off {
			return nil, errWindowsNotifyMalformed
		}
		// A NUL cannot occur inside a Windows name, so a trailing one is
		// padding that some implementation counted into the length. Left in
		// place it would make the path match no index entry, and every event
		// for that file would be reported for a name that does not exist.
		name := strings.TrimRight(decodeUTF16LE(buf[off+fniHeaderSize:off+fniHeaderSize+nameLen]), "\x00")

		kind, ok := windowsNotifyKind(action)
		if !ok {
			// An action outside the documented set means this is not a record
			// this code understands, and guessing what it meant would be
			// guessing whether a path changed.
			return nil, errWindowsNotifyMalformed
		}
		// A path the index can never name — inside the object store, or not
		// repo-relative at all — is dropped rather than treated as an error:
		// the object store is written constantly, and refusing the batch over
		// it would mean a full scan after every commit.
		if p, ok := normalizeWatchRel(name); ok {
			out = append(out, watchEvent{Path: p, Kind: kind})
		}

		if next == 0 {
			break
		}
		if next < fniHeaderSize {
			return nil, errWindowsNotifyMalformed
		}
		off += next
	}
	return out, nil
}

func windowsNotifyKind(action uint32) (watchKind, bool) {
	switch action {
	case fniActionAdded, fniActionModified, fniActionRenamedNewName:
		return watchChanged, true
	case fniActionRemoved, fniActionRenamedOldName:
		return watchRemoved, true
	}
	return 0, false
}

// decodeUTF16LE converts UTF-16 little-endian bytes to a string, surrounding
// unpaired surrogates with the replacement character rather than dropping
// them: a name is used to look an index entry up, and silently turning one
// name into a different one is exactly the kind of quiet substitution this
// whole design exists to avoid.
func decodeUTF16LE(b []byte) string {
	n := len(b) / 2
	u := make([]uint16, n)
	for i := 0; i < n; i++ {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

// ---- directory accounting ----

// A directory is the awkward case for every backend, for one reason: the
// events name it, never the files inside it. A path that appears brings files
// that were never announced; a path that disappears takes file names away that
// nothing will ever announce. Since the change set is looked up by exact
// index path, "sub" standing in for "sub/a.txt" is not an approximation — it
// is a miss.
type dirAction int

const (
	// dirActionNone: an ordinary path event.
	dirActionNone dirAction = iota
	// dirActionEnumerate: a directory that is new to us. Report every path
	// inside it, because they arrived without being announced.
	dirActionEnumerate
	// dirActionEndEpoch: a directory we knew about is gone. The paths it held
	// cannot be recovered from the events, so the caller must stop claiming
	// coverage and let a full scan establish the truth.
	dirActionEndEpoch
)

// dirEvent is what a backend knows about one event when it asks what to do.
type dirEvent struct {
	Removed bool // the path is gone: a delete, or the old half of a rename
	WasDir  bool // this path was known to be a directory
	IsDir   bool // this path is a directory now
}

// classifyDirEvent decides how a directory-affecting event must be handled.
//
// It is shared, and tested on one platform, rather than being written into
// each backend, because the reasoning is about what the events can express —
// not about any one API. Linux can answer WasDir from the kernel; Windows has
// to keep a set of known directories and ask the filesystem, because its
// records carry no such flag. The conclusion is the same either way.
func classifyDirEvent(e dirEvent) dirAction {
	switch {
	case e.WasDir && e.Removed:
		// A directory that was here and is not. Removing a directory is
		// reported as the directory alone, so the files it held are never
		// named; their index entries would go unchecked and their
		// disappearance unseen.
		return dirActionEndEpoch
	case !e.WasDir && e.IsDir:
		// A directory that is new to us. Whatever is inside it arrived
		// without an event of its own, so it has to be enumerated rather than
		// assumed unchanged.
		return dirActionEnumerate
	}
	return dirActionNone
}
