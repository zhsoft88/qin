package repo

import (
	"io/ioutil"
	"os"
	"strings"

	"github.com/zhsoft88/qin/internal/core"
)

// GraphCommit is a commit node with its hash for graph walking.
type GraphCommit struct {
	Hash   core.Hash
	Commit *Commit
}

// WalkGraph walks all reachable commits from HEAD and returns them
// in topological order (newest first), limited to max.
func (r *Repository) WalkGraph(max int) ([]GraphCommit, error) {
	hashStr, err := r.ResolveHEAD()
	if err != nil {
		return nil, err
	}
	if hashStr == "" {
		return nil, nil
	}

	head, err := core.HashFromHex(hashStr)
	if err != nil {
		return nil, err
	}
	return r.walkFromHeads(max, head)
}

// WalkAllGraph walks all reachable commits from every branch and returns them
// in topological order (newest first), limited to max.
func (r *Repository) WalkAllGraph(max int) ([]GraphCommit, error) {
	headsDir := r.RefsDir() + "/heads"
	entries, err := ioutil.ReadDir(headsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var heads []core.Hash
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := ioutil.ReadFile(headsDir + "/" + entry.Name())
		if err != nil {
			continue
		}
		h, err := core.HashFromHex(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		heads = append(heads, h)
	}

	if len(heads) == 0 {
		return nil, nil
	}

	return r.walkFromHeads(max, heads...)
}

func (r *Repository) walkFromHeads(max int, heads ...core.Hash) ([]GraphCommit, error) {
	// Collect all commits reachable from the given heads
	all := make(map[core.Hash]*Commit)
	var collect func(h core.Hash)
	collect = func(h core.Hash) {
		if all[h] != nil || h.IsZero() {
			return
		}
		commit, err := r.LoadCommit(h)
		if err != nil {
			return
		}
		all[h] = commit
		for _, p := range commit.Parents {
			collect(p)
		}
	}
	for _, head := range heads {
		collect(head)
	}

	// Topological sort: parents before children (DFS post-order)
	visited := make(map[core.Hash]bool)
	var sorted []GraphCommit

	var tsort func(h core.Hash)
	tsort = func(h core.Hash) {
		if visited[h] || h.IsZero() {
			return
		}
		visited[h] = true
		commit := all[h]
		for _, p := range commit.Parents {
			tsort(p)
		}
		sorted = append(sorted, GraphCommit{Hash: h, Commit: commit})
	}
	for _, head := range heads {
		tsort(head)
	}

	// Reverse: children before parents (newest first)
	for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
		sorted[i], sorted[j] = sorted[j], sorted[i]
	}

	if len(sorted) > max {
		sorted = sorted[:max]
	}

	return sorted, nil
}

// RenderGraph renders graph commits into ASCII graph lines.
// Each line shows the graph prefix, short hash, and commit message.
func RenderGraph(commits []GraphCommit) []string {
	type column struct {
		hash core.Hash
	}

	var columns []column
	var rows []string

	for _, c := range commits {
		// Find which column this commit belongs to
		idx := -1
		for i := range columns {
			if columns[i].hash == c.Hash {
				idx = i
				break
			}
		}
		if idx == -1 {
			idx = len(columns)
			columns = append(columns, column{c.Hash})
		}

		// Build graph prefix
		var prefix strings.Builder
		for i := 0; i < len(columns); i++ {
			if i == idx {
				prefix.WriteString("* ")
			} else if !columns[i].hash.IsZero() {
				prefix.WriteString("| ")
			} else {
				prefix.WriteString("  ")
			}
		}

		msg := c.Commit.Message
		if nl := strings.IndexByte(msg, '\n'); nl >= 0 {
			msg = msg[:nl]
		}
		graphStr := strings.TrimRight(prefix.String(), " ")
		rows = append(rows, graphStr+" "+c.Hash.Short()+"  "+msg)

		// Update columns with this commit's parents
		parents := c.Commit.Parents
		if len(parents) > 0 && !parents[0].IsZero() {
			columns[idx].hash = parents[0]
			// Insert extra merge parents after the current column
			for j := 1; j < len(parents); j++ {
				if !parents[j].IsZero() {
					columns = append(columns, column{})
					copy(columns[idx+j+1:], columns[idx+j:])
					columns[idx+j] = column{parents[j]}
				}
			}
		} else {
			columns[idx].hash = core.Hash{}
		}

		// Merge columns that point to the same hash (branches converging)
		for i := 0; i < len(columns); i++ {
			if columns[i].hash.IsZero() {
				continue
			}
			for j := i + 1; j < len(columns); j++ {
				if columns[i].hash == columns[j].hash {
					columns = append(columns[:j], columns[j+1:]...)
					j--
				}
			}
		}

		// Trim trailing zero hashes
		for len(columns) > 0 && columns[len(columns)-1].hash.IsZero() {
			columns = columns[:len(columns)-1]
		}
	}

	return rows
}
