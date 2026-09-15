package repo

// FSEvents hands over decoded paths and a flags word, so unlike the inotify and
// Windows backends there is no buffer to parse here. What there is instead is
// the ABI's flag values and the decision each combination of them implies, and
// both live in this file — no build tag, no system call — so that the part
// which decides whether a change was missed can be tested on a machine that
// cannot run FSEvents at all.
//
// The constants are CoreServices' values, transcribed. fsmonitor_watch_darwin.go
// checks every one of them against the SDK's own definition before it will
// start a stream: a transcription error would otherwise make events decode into
// decisions nobody intended, and there is nothing to notice that from the
// outside. A watcher that reports nothing looks exactly like a tree that never
// changes.
const (
	// The stream lost something. Each of these ends the coverage epoch.
	fsFlagMustScanSubDirs = 0x00000001
	fsFlagUserDropped     = 0x00000002
	fsFlagKernelDropped   = 0x00000004
	fsFlagEventIdsWrapped = 0x00000008
	fsFlagHistoryDone     = 0x00000010
	fsFlagRootChanged     = 0x00000020
	fsFlagMount           = 0x00000040
	fsFlagUnmount         = 0x00000080

	// What happened to the path. These are present because the stream is
	// created with kFSEventStreamCreateFlagFileEvents.
	fsFlagItemCreated       = 0x00000100
	fsFlagItemRemoved       = 0x00000200
	fsFlagItemInodeMetaMod  = 0x00000400
	fsFlagItemRenamed       = 0x00000800
	fsFlagItemModified      = 0x00001000
	fsFlagItemFinderInfoMod = 0x00002000
	fsFlagItemChangeOwner   = 0x00004000
	fsFlagItemXattrMod      = 0x00008000
	fsFlagItemIsDir         = 0x00020000
)

// fsLossFlags is every flag that ends an epoch.
const fsLossFlags = fsFlagMustScanSubDirs | fsFlagUserDropped | fsFlagKernelDropped |
	fsFlagEventIdsWrapped | fsFlagRootChanged | fsFlagMount | fsFlagUnmount

// fsActionFlags are the flags that say what happened to a path. A FileEvents
// stream sets at least one of them on every event it delivers about a path; an
// event carrying none of them names a path without saying what became of it,
// and the caller has to look rather than assume.
const fsActionFlags = fsFlagItemCreated | fsFlagItemRemoved | fsFlagItemRenamed |
	fsFlagItemModified | fsFlagItemInodeMetaMod | fsFlagItemFinderInfoMod |
	fsFlagItemChangeOwner | fsFlagItemXattrMod

// fsEventFlags is every flag this file decodes, for the ABI check to verify.
// The order must match the table in fsmonitor_darwin.h.
var fsEventFlags = []uint32{
	fsFlagMustScanSubDirs,
	fsFlagUserDropped,
	fsFlagKernelDropped,
	fsFlagEventIdsWrapped,
	fsFlagHistoryDone,
	fsFlagRootChanged,
	fsFlagMount,
	fsFlagUnmount,
	fsFlagItemCreated,
	fsFlagItemRemoved,
	fsFlagItemInodeMetaMod,
	fsFlagItemRenamed,
	fsFlagItemModified,
	fsFlagItemFinderInfoMod,
	fsFlagItemChangeOwner,
	fsFlagItemXattrMod,
	fsFlagItemIsDir,
}

// fseventsOutcome is the decision one event implies.
type fseventsOutcome struct {
	// Lost is non-empty when the epoch is over: the stream is saying that
	// something happened which it cannot describe, and a change it cannot
	// describe is one the consumer would skip rather than see.
	Lost string

	// Unknown means the event named a path without saying what happened to it.
	// The caller resolves that the way the Windows backend resolves its
	// records, which carry no such flags at all: by asking the filesystem.
	Unknown bool

	// Emit reports whether the path itself is worth reporting.
	Emit   bool
	Kind   watchKind
	Dir    bool
	Action dirAction
}

// fseventsLoss reports the flag, if any, that ended the epoch.
//
// Each of these is the stream admitting it knows something happened and cannot
// say what: events coalesced past the point of detail, a queue dropped, the
// event numbering restarted, the watched root itself renamed or unmounted, or a
// volume appearing inside the tree bringing contents that no event will name.
// None of them can be narrowed to a subtree — the flags do not carry one — and
// none may be shrugged off, so every one of them costs a full scan.
func fseventsLoss(flags uint32) string {
	switch {
	case flags&fsFlagMustScanSubDirs != 0:
		return "FSEvents asked for a subtree to be rescanned"
	case flags&fsFlagUserDropped != 0:
		return "FSEvents dropped events for this process"
	case flags&fsFlagKernelDropped != 0:
		return "FSEvents dropped events in the kernel"
	case flags&fsFlagEventIdsWrapped != 0:
		return "FSEvents event ids wrapped"
	case flags&fsFlagRootChanged != 0:
		return "the watched root changed"
	case flags&fsFlagMount != 0:
		return "a volume was mounted inside the tree"
	case flags&fsFlagUnmount != 0:
		return "a volume was unmounted inside the tree"
	}
	return ""
}

// classifyFsevents reduces one event to the action it requires, given what the
// watcher already knew about the path.
//
// The directory reasoning is not restated here: it is classifyDirEvent's, the
// same one the Linux and Windows backends reach their conclusions through. Only
// the two facts that function needs are worked out here, and on this platform
// one of them is free — FSEvents says whether the path is a directory, where
// Windows has to ask the filesystem and keep a set.
func classifyFsevents(flags uint32, wasDir bool) fseventsOutcome {
	if reason := fseventsLoss(flags); reason != "" {
		return fseventsOutcome{Lost: reason}
	}
	// HistoryDone marks the end of the replay that a sinceWhen in the past
	// would have produced. This stream starts at the present, so it does not
	// arrive; it is recognised rather than ignored so that a later change to
	// where the stream starts cannot turn a marker into a phantom path.
	if flags&fsFlagHistoryDone != 0 && flags&fsActionFlags == 0 {
		return fseventsOutcome{}
	}
	if flags&fsActionFlags == 0 {
		return fseventsOutcome{Unknown: true, Emit: true, Kind: watchChanged}
	}

	dir := flags&fsFlagItemIsDir != 0
	// A rename arrives twice — once for the name the item left, once for the
	// name it took — and neither event says which of the two it is. What they
	// have in common is that the path named is not what it was: for the name
	// that was left, the item is gone from there. Reading it that way costs an
	// enumeration of the new name and, if it was a directory we knew, an epoch:
	// the names under it went somewhere nothing will report them from.
	gone := flags&(fsFlagItemRemoved|fsFlagItemRenamed) != 0
	kind := watchChanged
	if gone {
		kind = watchRemoved
	}
	return fseventsOutcome{
		Emit: true,
		Kind: kind,
		Dir:  dir,
		Action: classifyDirEvent(dirEvent{
			Removed: gone,
			WasDir:  wasDir,
			IsDir:   dir,
		}),
	}
}
