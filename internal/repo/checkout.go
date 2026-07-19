package repo

import (
	"fmt"
	"io/ioutil"
	"path/filepath"

	"github.com/zhsoft88/qin/internal/core"
)

// Checkout restores all files from a commit to the working directory,
// updates the index, and sets HEAD to the commit.
func (r *Repository) Checkout(hash core.Hash) error {
	if err := r.restoreCommit(hash); err != nil {
		return err
	}

	// Update HEAD
	head, err := r.ReadHEAD()
	if err != nil {
		return fmt.Errorf("read HEAD: %w", err)
	}
	if len(head) > 5 && head[:4] == "ref:" {
		ref := head[5:]
		if err := r.WriteRef(ref, hash.String()); err != nil {
			return fmt.Errorf("update ref: %w", err)
		}
	} else {
		if err := r.SetHEAD(hash.String()); err != nil {
			return fmt.Errorf("set HEAD: %w", err)
		}
	}

	return nil
}

// ResolveRef resolves a string to a commit hash.
// Accepts: full hash, short hash (prefix), branch name, tag name.
func (r *Repository) ResolveRef(s string) (core.Hash, error) {
	// Try as full hex hash
	if len(s) == 64 {
		h, err := core.HashFromHex(s)
		if err == nil {
			if r.HasObject(h) {
				return h, nil
			}
			return core.Hash{}, fmt.Errorf("object not found: %s", s)
		}
	}

	// Try as branch or tag ref
	refsToTry := []string{
		"refs/heads/" + s,
		"refs/tags/" + s,
	}
	for _, ref := range refsToTry {
		hashStr, err := r.ReadRef(ref)
		if err == nil {
			h, err := core.HashFromHex(hashStr)
			if err == nil {
				return h, nil
			}
		}
	}

	// Try as HEAD or special ref
	switch s {
	case "HEAD":
		hashStr, err := r.ResolveHEAD()
		if err != nil {
			return core.Hash{}, err
		}
		if hashStr == "" {
			return core.Hash{}, fmt.Errorf("HEAD has no commits")
		}
		return core.HashFromHex(hashStr)
	}

	// Try short hash prefix match
	if len(s) >= 4 && len(s) <= 63 {
		objectsDir := r.ObjectsDir()
		prefix := s[:2]
		rest := s[2:]
		dir := filepath.Join(objectsDir, prefix)
		entries, err := ioutil.ReadDir(dir)
		if err != nil {
			return core.Hash{}, fmt.Errorf("cannot resolve '%s': no match found", s)
		}
		var matches []string
		for _, entry := range entries {
			if !entry.IsDir() && len(entry.Name()) >= len(rest) && entry.Name()[:len(rest)] == rest {
				matches = append(matches, prefix+entry.Name())
			}
		}
		if len(matches) == 0 {
			return core.Hash{}, fmt.Errorf("cannot resolve '%s': no match found", s)
		}
		if len(matches) > 1 {
			return core.Hash{}, fmt.Errorf("cannot resolve '%s': ambiguous (%d matches)", s, len(matches))
		}
		return core.HashFromHex(matches[0])
	}

	return core.Hash{}, fmt.Errorf("cannot resolve '%s': no match found", s)
}
