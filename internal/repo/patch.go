package repo

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zhsoft88/qin/internal/core"
)

const patchSeparator = "====\n"

// RenderPatch returns an apply-able patch string with embedded content.
func (r *Repository) RenderPatch(d *Diff) (string, error) {
	var b strings.Builder

	for _, f := range d.Files {
		displayName := f.Name
		if f.OS != 0 {
			displayName = f.Name + "\x00" + string(rune(f.OS))
		}

		switch f.Type {
		case DiffAdded:
			data, err := r.loadContentForPatch(f.Name, f.NewHash)
			if err != nil {
				return "", fmt.Errorf("load content for %s: %w", f.Name, err)
			}
			if data == nil {
				continue
			}
			fmt.Fprintf(&b, "+ %d %s\n", len(data), displayName)
			b.WriteString(base64Encode(data))
			b.WriteString(patchSeparator)

		case DiffModified:
			data, err := r.loadContentForPatch(f.Name, f.NewHash)
			if err != nil {
				return "", fmt.Errorf("load content for %s: %w", f.Name, err)
			}
			if data == nil {
				continue
			}
			fmt.Fprintf(&b, "~ %d %s  (%s -> %s)\n", len(data), displayName, f.OldHash.Short(), f.NewHash.Short())
			b.WriteString(base64Encode(data))
			b.WriteString(patchSeparator)

		case DiffDeleted:
			fmt.Fprintf(&b, "- %d %s\n", f.OldSize, displayName)
			b.WriteString(patchSeparator)
		}
	}

	return b.String(), nil
}

// loadContentForPatch reads a file's content for inclusion in a patch.
// It tries the object store first (by hash), then falls back to the working tree.
func (r *Repository) loadContentForPatch(name string, hash core.Hash) ([]byte, error) {
	if !hash.IsZero() {
		data, err := r.LoadFileContent(hash)
		if err == nil {
			return data, nil
		}
	}

	// Fallback: read from working tree
	fullPath := filepath.Join(r.Path, name)
	data, err := ioutil.ReadFile(fullPath)
	if err == nil {
		return data, nil
	}

	return nil, nil
}

// base64Encode returns base64-encoded content with 76-char line wrapping.
func base64Encode(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		b.WriteString(encoded[i:end])
		b.WriteByte('\n')
	}
	return b.String()
}

// base64Decode decodes a base64 string (ignoring whitespace).
func base64Decode(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' {
			return -1
		}
		return r
	}, s)
	return base64.StdEncoding.DecodeString(s)
}

// ApplyPatch applies a patch to the working tree and index.
// The patch format is produced by RenderPatch.
func (r *Repository) ApplyPatch(data []byte) error {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	visible := visibleEntries(idx.Entries, currentOS())

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if len(line) < 2 {
			continue
		}

		op := line[0]
		rest := strings.TrimSpace(line[1:])

		switch op {
		case '+', '~':
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) < 2 {
				continue
			}
			sizeStr := parts[0]
			// Modified entries carry a trailing "  (oldhash -> newhash)"
			// suffix that must be stripped before resolving the path.
			encPath := stripHashRangeSuffix(parts[1])

			size, err := strconv.ParseInt(sizeStr, 10, 64)
			if err != nil {
				continue
			}

			cleanPath, osTag := parsePatchPath(encPath)
			osID := osTag

			// A patch is untrusted input: its paths must not escape the
			// repository. Refuse outright rather than skipping, so a
			// malicious patch is reported instead of silently half-applied.
			if !safeRepoPath(cleanPath) {
				return fmt.Errorf("refusing patch path outside repository: %s", cleanPath)
			}

			// Read base64 content until separator
			var b64Buf strings.Builder
			for scanner.Scan() {
				sepLine := scanner.Text()
				if sepLine == "====" {
					break
				}
				b64Buf.WriteString(sepLine)
				b64Buf.WriteByte('\n')
			}

			content, err := base64Decode(b64Buf.String())
			if err != nil {
				return fmt.Errorf("decode content for %s: %w", cleanPath, err)
			}
			if int64(len(content)) != size && size > 0 {
				// size mismatch warning, but proceed
			}

			// Write to working tree
			fullPath, err := r.writeWorkTreeFile(cleanPath, content, 0644)
			if err != nil {
				return fmt.Errorf("write %s: %w", cleanPath, err)
			}

			if op == '+' {
				fmt.Printf("added: %s\n", cleanPath)
			} else {
				fmt.Printf("modified: %s\n", cleanPath)
			}

			// Update index
			key := entryKey(cleanPath, osID)
			contentHash := core.HashFromBytes(content)
			h, err := r.StoreObject(core.ObjectBlob, content)
			if err != nil {
				return fmt.Errorf("store object for %s: %w", cleanPath, err)
			}
			var mt int64
			if fi, err := os.Lstat(fullPath); err == nil {
				mt = fi.ModTime().UnixNano()
			}
			idx.Entries[key] = IndexEntry{
				Hash:        h,
				ContentHash: contentHash,
				Size:        size,
				Mode:        0644,
				OSS:         osID,
				Mtime:       mt,
			}

		case '-':
			encPath := strings.TrimSpace(rest)
			// Remove size prefix if present
			if idx := strings.Index(encPath, " "); idx > 0 {
				encPath = strings.TrimSpace(encPath[idx+1:])
			}
			cleanPath, osTag := parsePatchPath(encPath)
			osID := osTag

			// Same untrusted-input guard as the add/modify branch: a delete
			// must not reach outside the repository either.
			if !safeRepoPath(cleanPath) {
				return fmt.Errorf("refusing patch path outside repository: %s", cleanPath)
			}

			// Remove from working tree. Best-effort as before — an already
			// absent file is not an error — but the helper still refuses a
			// path that resolves outside the repository.
			r.removeWorkTreeFile(cleanPath)

			// Remove from index
			key := entryKey(cleanPath, osID)
			delete(idx.Entries, key)
			// Also try the visible entry
			if entry, ok := visible[cleanPath]; ok {
				for k := range idx.Entries {
					if p, o := parseKey(k); p == cleanPath && o == entry.OSS {
						delete(idx.Entries, k)
						break
					}
				}
			}

			fmt.Printf("deleted: %s\n", cleanPath)

			// Consume separator
			for scanner.Scan() {
				if scanner.Text() == "====" {
					break
				}
			}
		}
	}

	if err := r.SaveIndex(idx); err != nil {
		return fmt.Errorf("save index: %w", err)
	}

	return scanner.Err()
}

// stripHashRangeSuffix removes a trailing "  (oldhash -> newhash)" suffix
// (16-hex-char short hashes) from a RenderPatch modified-entry path.
func stripHashRangeSuffix(s string) string {
	idx := strings.LastIndex(s, "  (")
	if idx < 0 {
		return s
	}
	rest := s[idx+3:]
	if !strings.HasSuffix(rest, ")") {
		return s
	}
	parts := strings.SplitN(strings.TrimSuffix(rest, ")"), " -> ", 2)
	if len(parts) == 2 && isShortHash(parts[0]) && isShortHash(parts[1]) {
		return s[:idx]
	}
	return s
}

func isShortHash(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// parsePatchPath extracts clean path and OS tag from an encoded patch path.
// Format: "path" or "path\x00<os_byte>"
func parsePatchPath(enc string) (string, uint8) {
	if idx := strings.IndexByte(enc, '\x00'); idx >= 0 {
		return enc[:idx], uint8(enc[idx+1])
	}
	return enc, 0
}
