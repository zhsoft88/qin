package repo

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestSSHListRefsKeyShape spans the producer and the consumer of an SSH ref
// listing, because the defect was exactly that they disagreed.
//
// sshListRefs derives its keys from the paths find printed under the target's
// .qin directory; they were once relative to .qin/refs, so they came out as
// "heads/main" and matched no caller's "refs/heads/" prefix. SSH fetch then
// collected no objects and SSH clone produced an empty repository, every
// command exiting 0. Reproducing that needs no SSH server — only the shape of
// the keys — so this runs everywhere.
func TestSSHListRefsKeyShape(t *testing.T) {
	sep := string(filepath.Separator)
	qinDir := filepath.Join(sep+"srv", "repo", ".qin")
	found := []string{
		filepath.Join(qinDir, "refs", "heads", "main"),
		filepath.Join(qinDir, "refs", "heads", "feature", "nested"),
		filepath.Join(qinDir, "refs", "tags", "v1"),
	}

	refs := make(map[string]string, len(found))
	for _, path := range found {
		refs[sshRefKey(qinDir, path)] = strings.Repeat("a", 64)
	}

	// The listing is keyed by full ref name, so the branches come out named
	// the way every other transport names them. Before the fix this was empty.
	want := []string{"feature/nested", "main"}
	if got := branchNamesFromRefs(refs); !reflect.DeepEqual(got, want) {
		t.Fatalf("branchNamesFromRefs = %v, want %v (listing: %v)", got, want, refs)
	}

	// fetchSSH reads each branch's hash through the same keys.
	for _, ref := range []string{"refs/heads/main", "refs/heads/feature/nested"} {
		if _, ok := refs[ref]; !ok {
			t.Errorf("no entry under %q; fetchSSH would skip it", ref)
		}
	}

	// A path find should never have printed — outside .qin — must not be
	// mistaken for a branch.
	outsider := sshRefKey(qinDir, filepath.Join(filepath.Dir(qinDir), "etc", "passwd"))
	if strings.HasPrefix(outsider, "refs/heads/") {
		t.Errorf("sshRefKey answered %q for a path outside .qin", outsider)
	}
}
