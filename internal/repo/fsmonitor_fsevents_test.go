package repo

import "testing"

// The FSEvents decision table, exercised on a machine that cannot run FSEvents.
// That is the point of the split: what is left in the darwin file is the stream
// plumbing, and what is here is everything that decides whether a change was
// missed — which is the part that must not be wrong and the part hardest to
// debug on a Mac.

func TestFseventsClassify(t *testing.T) {
	tests := []struct {
		name   string
		flags  uint32
		wasDir bool
		want   fseventsOutcome
	}{
		{
			name:  "a file is created",
			flags: fsFlagItemCreated | 0x00010000, // ItemIsFile
			want:  fseventsOutcome{Emit: true, Kind: watchChanged},
		},
		{
			name:  "a file's contents change",
			flags: fsFlagItemModified | 0x00010000,
			want:  fseventsOutcome{Emit: true, Kind: watchChanged},
		},
		{
			name:  "a file's metadata changes",
			flags: fsFlagItemInodeMetaMod | 0x00010000,
			want:  fseventsOutcome{Emit: true, Kind: watchChanged},
		},
		{
			name:   "a directory's contents change but it is not a directory event",
			flags:  fsFlagItemModified | 0x00010000,
			wasDir: true,
			// The path is still a file from the flags' point of view, so the
			// directory bookkeeping has nothing to say. This is what keeps an
			// ordinary edit from costing more than the edit.
			want: fseventsOutcome{Emit: true, Kind: watchChanged},
		},
		{
			name:  "a file is removed",
			flags: fsFlagItemRemoved | 0x00010000,
			want:  fseventsOutcome{Emit: true, Kind: watchRemoved},
		},
		{
			name:  "a directory appears",
			flags: fsFlagItemCreated | fsFlagItemIsDir,
			// Its contents arrived without events naming them, so they are
			// enumerated rather than assumed unchanged.
			want: fseventsOutcome{Emit: true, Kind: watchChanged, Dir: true, Action: dirActionEnumerate},
		},
		{
			name:   "a directory we knew is removed",
			flags:  fsFlagItemRemoved | fsFlagItemIsDir,
			wasDir: true,
			// The names it held are gone and no event will name them, so the
			// epoch ends and the next run scans.
			want: fseventsOutcome{Emit: true, Kind: watchRemoved, Dir: true, Action: dirActionEndEpoch},
		},
		{
			name:  "a directory that was created and removed again",
			flags: fsFlagItemRemoved | fsFlagItemIsDir,
			// New to the bookkeeping, so it is enumerated — and enumerating a
			// path that is already gone walks nothing and reports nothing, so
			// no epoch is spent and no path is invented. That is what keeps a
			// build directory created and deleted inside one callback from
			// costing a scan.
			want: fseventsOutcome{Emit: true, Kind: watchRemoved, Dir: true, Action: dirActionEnumerate},
		},
		{
			name:   "a directory is renamed: the name it left",
			flags:  fsFlagItemRenamed | fsFlagItemIsDir,
			wasDir: true,
			want:   fseventsOutcome{Emit: true, Kind: watchRemoved, Dir: true, Action: dirActionEndEpoch},
		},
		{
			name:  "a directory is renamed: the name it took",
			flags: fsFlagItemRenamed | fsFlagItemIsDir,
			want:  fseventsOutcome{Emit: true, Kind: watchRemoved, Dir: true, Action: dirActionEnumerate},
		},
		{
			name:  "a file is renamed: the name it took",
			flags: fsFlagItemRenamed | 0x00010000,
			// The kind says removed, which for a file is not read as anything:
			// the path is what the consumer acts on, and it stats it either way.
			want: fseventsOutcome{Emit: true, Kind: watchRemoved},
		},
		{
			name:  "an event that says nothing about the path",
			flags: 0,
			// Resolved by looking, never by assuming: the caller asks the
			// filesystem whether it is a directory, as the Windows backend must
			// for every record it reads.
			want: fseventsOutcome{Unknown: true, Emit: true, Kind: watchChanged},
		},
		{
			name:  "only the item's kind is set",
			flags: fsFlagItemIsDir,
			want:  fseventsOutcome{Unknown: true, Emit: true, Kind: watchChanged},
		},
		{
			name:  "the end of a replay that starts in the past",
			flags: fsFlagHistoryDone,
			want:  fseventsOutcome{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyFsevents(tt.flags, tt.wasDir)
			if got != tt.want {
				t.Fatalf("classifyFsevents(0x%x, wasDir=%v) =\n  %+v\nwant\n  %+v",
					tt.flags, tt.wasDir, got, tt.want)
			}
		})
	}
}

func TestFseventsLossFlagsEndTheEpoch(t *testing.T) {
	for _, c := range []struct {
		name  string
		flags uint32
	}{
		{"MustScanSubDirs", fsFlagMustScanSubDirs},
		{"UserDropped", fsFlagUserDropped},
		{"KernelDropped", fsFlagKernelDropped},
		{"EventIdsWrapped", fsFlagEventIdsWrapped},
		{"RootChanged", fsFlagRootChanged},
		{"Mount", fsFlagMount},
		{"Unmount", fsFlagUnmount},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Alone, and alongside an ordinary change: a loss flag is a loss
			// however it is dressed up, and the flags that go with it are the
			// ones that would otherwise be trusted.
			for _, flags := range []uint32{
				c.flags,
				c.flags | fsFlagItemModified | 0x00010000,
				c.flags | fsFlagItemCreated | fsFlagItemIsDir,
			} {
				got := classifyFsevents(flags, false)
				if got.Lost == "" {
					t.Fatalf("0x%x did not end the epoch: %+v", flags, got)
				}
				if got.Emit || got.Action != dirActionNone {
					t.Fatalf("0x%x both ended the epoch and asked for work: %+v", flags, got)
				}
			}
		})
	}
}

// TestFseventsFlagGroupsDoNotOverlap guards the one transcription error that
// would be silent rather than loud. Two constants written with the same value
// would make one flag decode as another: an edit that also reads as a dropped
// event costs a scan, but a dropped event that reads as an edit is a change
// nobody ever reports. The check against the SDK cannot see this — both copies
// would be wrong in the same way — so the properties are asserted here.
func TestFseventsFlagGroupsDoNotOverlap(t *testing.T) {
	groups := map[string]uint32{
		"loss":   fsLossFlags,
		"action": fsActionFlags,
		"isDir":  fsFlagItemIsDir,
	}
	for name, g := range groups {
		if g == 0 {
			t.Fatalf("%s flags are empty", name)
		}
	}
	names := []string{"loss", "action", "isDir"}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if overlap := groups[names[i]] & groups[names[j]]; overlap != 0 {
				t.Errorf("%s and %s flags overlap at 0x%x", names[i], names[j], overlap)
			}
		}
	}
	// Every flag the ABI check verifies has to be a single bit: a value with
	// two bits set would be a transcription of something that is not one flag,
	// and would make two unrelated events decode identically.
	seen := make(map[uint32]string, len(fsEventFlags))
	for _, f := range fsEventFlags {
		if f == 0 || f&(f-1) != 0 {
			t.Errorf("0x%x is not a single bit", f)
		}
		if prev, dup := seen[f]; dup {
			t.Errorf("0x%x is used for both %s and this entry", f, prev)
		}
		seen[f] = "listed"
	}
	// The table the ABI check walks must cover every flag either group uses:
	// a constant left out of it is one the SDK never gets to contradict.
	for name, g := range groups {
		for _, f := range fsEventFlags {
			if g&f == 0 {
				continue
			}
			g &^= f
		}
		if g != 0 {
			t.Errorf("%s flags are not all in the ABI check table: 0x%x missing", name, g)
		}
	}
}
