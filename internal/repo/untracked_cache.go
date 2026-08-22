package repo

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
)

// untrackedCache caches per-directory untracked-file lists so repeated
// status runs can skip re-reading unchanged directories (the same idea as
// git's core.untrackedCache). A directory entry is only trusted while the
// index, the ignore rules, and the directory's own mtime are all unchanged.
//
// A directory mtime only changes when entries are created or removed
// directly in it — changes deeper down bump the deeper dir's mtime but not
// this one's. So a valid entry means "this directory's untracked children
// are exactly as cached and its child directory list is known" — the walk
// must still descend into each cached child dir and validate it
// independently at its own level.
type untrackedCache struct {
	IndexMtime  int64                   `json:"index_mtime"`  // .qin/index mtime when the cache was written
	IgnoreMtime int64                   `json:"ignore_mtime"` // .qinignore mtime when the cache was written
	Dirs        map[string]*dirCacheEnt `json:"dirs"`         // key: repo-relative dir path ("" = root)
}

type dirCacheEnt struct {
	Mtime     int64    `json:"mtime"`
	Untracked []string `json:"untracked,omitempty"` // untracked direct children (files; dirs with trailing "/")
	Dirs      []string `json:"dirs,omitempty"`      // direct child dirs the walk descended into (validated at their own level)
}

const untrackedCacheFileName = "untracked-cache.json"

func (r *Repository) untrackedCachePath() string {
	return filepath.Join(r.LoDir(), untrackedCacheFileName)
}

// loadUntrackedCache reads the cache, returning an empty one when missing or corrupt.
func (r *Repository) loadUntrackedCache() *untrackedCache {
	data, err := ioutil.ReadFile(r.untrackedCachePath())
	if err != nil {
		return &untrackedCache{Dirs: make(map[string]*dirCacheEnt)}
	}
	var c untrackedCache
	if err := json.Unmarshal(data, &c); err != nil || c.Dirs == nil {
		return &untrackedCache{Dirs: make(map[string]*dirCacheEnt)}
	}
	return &c
}

// saveUntrackedCache writes the cache atomically (temp + rename).
func (r *Repository) saveUntrackedCache(c *untrackedCache) {
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	tmp := r.untrackedCachePath() + ".tmp"
	if err := ioutil.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, r.untrackedCachePath())
}
