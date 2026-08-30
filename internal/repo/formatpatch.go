package repo

import (
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	"github.com/zhsoft88/qin/internal/core"
)

// PatchFile is one exported commit patch: a metadata envelope wrapping an
// apply-able RenderPatch body. It is produced by FormatPatch and consumed by
// ApplyPatchFile / ApplyMailbox, mirroring git format-patch / git am.
type PatchFile struct {
	Filename string    // suggested file name, e.g. "0001-fix-typo.patch"
	Author   string    // e.g. "Name <email>"
	Date     time.Time // original commit time
	Message  string    // full commit message
	Body     string    // RenderPatch output (the diff to replay)
}

// CommitsBetween returns the commits strictly after base and at or before
// head, following the first-parent chain, oldest first. An empty slice is
// returned when base == head. A zero base walks all the way back to the root
// commit. An error is returned when base is not an ancestor of head.
func (r *Repository) CommitsBetween(base, head core.Hash) ([]core.Hash, error) {
	var commits []core.Hash
	for h := head; !h.IsZero() && h != base; {
		commits = append(commits, h)
		c, err := r.LoadCommit(h)
		if err != nil {
			return nil, fmt.Errorf("load commit %s: %w", h.Short(), err)
		}
		if len(c.Parents) == 0 {
			if !base.IsZero() {
				return nil, fmt.Errorf("base is not an ancestor of head")
			}
			break
		}
		h = c.Parents[0]
	}
	// Reverse to chronological order
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}

// FormatPatch exports the commits in (base, head] as apply-able patch files,
// oldest first. Each patch carries the commit metadata envelope so `am` can
// rebuild the original commits (author, message, date).
func (r *Repository) FormatPatch(base, head core.Hash) ([]PatchFile, error) {
	commits, err := r.CommitsBetween(base, head)
	if err != nil {
		return nil, err
	}

	files := make([]PatchFile, 0, len(commits))
	for i, h := range commits {
		commit, err := r.LoadCommit(h)
		if err != nil {
			return nil, fmt.Errorf("load commit %s: %w", h.Short(), err)
		}

		var diff *Diff
		if len(commit.Parents) == 0 {
			// Root commit: diff against the empty tree.
			newTree, err := r.commitTree(h)
			if err != nil {
				return nil, err
			}
			diff = diffTreeMap(make(map[string]TreeEntry), newTree)
		} else {
			diff, err = r.DiffCommits(commit.Parents[0], h)
			if err != nil {
				return nil, fmt.Errorf("diff commit %s: %w", h.Short(), err)
			}
		}

		body, err := r.RenderPatch(diff)
		if err != nil {
			return nil, fmt.Errorf("render patch %s: %w", h.Short(), err)
		}

		files = append(files, PatchFile{
			Filename: fmt.Sprintf("%04d-%s.patch", i+1, slugify(commit.Message)),
			Author:   commit.Author,
			Date:     commit.Time,
			Message:  commit.Message,
			Body:     body,
		})
	}
	return files, nil
}

// Render serializes the patch envelope to the on-disk format:
//
//	From: <author>
//	Date: <RFC1123>
//	Subject: [PATCH] <first line of message>
//
//	<remaining message lines>
//	---
//	<RenderPatch body>
func (p *PatchFile) Render() string {
	subject, body := subjectAndBody(p.Message)
	if subject == "" {
		subject = "(no subject)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\n", p.Author)
	// RFC1123Z carries a numeric zone offset, so the date round-trips
	// unambiguously across timezones (RFC1123's named-zone abbreviation
	// like "CST" is ambiguous when parsed).
	fmt.Fprintf(&b, "Date: %s\n", p.Date.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Subject: [PATCH] %s\n\n", subject)
	if body != "" {
		b.WriteString(body)
		b.WriteByte('\n')
	}
	b.WriteString("---\n")
	b.WriteString(p.Body)
	return b.String()
}

// ParsePatchFile parses the envelope written by Render. It rebuilds the full
// commit message from the Subject header plus the body lines.
func ParsePatchFile(data []byte) (*PatchFile, error) {
	lines := strings.Split(string(data), "\n")
	p := &PatchFile{}

	i := 0
	subj := ""
	// Header block: until the first blank line
	for i < len(lines) {
		line := lines[i]
		i++
		if strings.TrimSpace(line) == "" {
			break
		}
		switch {
		case strings.HasPrefix(line, "From: "):
			p.Author = strings.TrimSpace(strings.TrimPrefix(line, "From: "))
		case strings.HasPrefix(line, "Date: "):
			if t, err := parsePatchDate(strings.TrimSpace(strings.TrimPrefix(line, "Date: "))); err == nil {
				p.Date = t
			}
		case strings.HasPrefix(line, "Subject: "):
			subj = stripPatchPrefix(strings.TrimSpace(strings.TrimPrefix(line, "Subject: ")))
		}
	}

	// Message: until the "---" separator line
	sepFound := false
	var msgLines []string
	for i < len(lines) {
		line := lines[i]
		i++
		if line == "---" {
			sepFound = true
			break
		}
		msgLines = append(msgLines, line)
	}
	if !sepFound {
		return nil, fmt.Errorf("malformed patch: missing --- separator")
	}

	p.Message = joinMessage(subj, strings.Join(msgLines, "\n"))
	p.Body = strings.Join(lines[i:], "\n")
	return p, nil
}

// ApplyMailbox applies a sequence of format-patch files in order, each
// replaying its diff and creating a commit that preserves the original
// author, message and date.
func (r *Repository) ApplyMailbox(files []string) error {
	for _, f := range files {
		data, err := ioutil.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read patch %s: %w", f, err)
		}
		if err := r.ApplyPatchFile(data); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	return nil
}

// ApplyPatchFile applies a single format-patch envelope: it replays the diff
// onto the working tree and index, then creates a commit preserving the
// envelope's author, message and date.
func (r *Repository) ApplyPatchFile(data []byte) error {
	p, err := ParsePatchFile(data)
	if err != nil {
		return err
	}
	if err := r.ApplyPatch([]byte(p.Body)); err != nil {
		return fmt.Errorf("apply patch: %w", err)
	}
	author := p.Author
	if author == "" {
		author = r.defaultAuthor()
	}
	when := p.Date
	if when.IsZero() {
		when = time.Now()
	}
	if _, err := r.WriteCommitAt(author, p.Message, when); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	fmt.Printf("applied: %s\n", firstLine(p.Message))
	return nil
}

func (r *Repository) defaultAuthor() string {
	if r.Config != nil && r.Config.User.Name != "" {
		if r.Config.User.Email != "" {
			return r.Config.User.Name + " <" + r.Config.User.Email + ">"
		}
		return r.Config.User.Name
	}
	return "unknown <unknown>"
}

func parsePatchDate(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date: %s", s)
}

// subjectAndBody splits a commit message into its first line (subject) and the
// remaining body, collapsing any leading blank separator lines.
func subjectAndBody(msg string) (subject, body string) {
	lines := strings.Split(msg, "\n")
	subject = strings.TrimSpace(lines[0])
	i := 1
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	body = strings.TrimSpace(strings.Join(lines[i:], "\n"))
	return subject, body
}

// joinMessage rebuilds a commit message from a subject and body, the inverse
// of subjectAndBody.
func joinMessage(subject, body string) string {
	subject = strings.TrimSpace(subject)
	body = strings.TrimSpace(body)
	if subject == "" {
		return body
	}
	if body == "" {
		return subject
	}
	return subject + "\n\n" + body
}

// stripPatchPrefix removes a leading "[PATCH]"/"[PATCH n/m]" wrapper from a
// Subject header value.
func stripPatchPrefix(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[PATCH") {
		if idx := strings.Index(s, "]"); idx >= 0 {
			return strings.TrimSpace(s[idx+1:])
		}
	}
	return s
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

// slugify turns a commit message's first line into a safe file-name slug:
// runs of separator characters become a single dash, and the result is capped
// at 50 runes.
func slugify(s string) string {
	s = firstLine(s)
	var b strings.Builder
	prevSep := false
	flushSep := func() {
		if prevSep {
			b.WriteByte('-')
			prevSep = false
		}
	}
	for _, r := range s {
		if isSlugSep(r) {
			prevSep = true
		} else {
			flushSep()
			b.WriteRune(r)
		}
	}
	flushSep()
	slug := strings.Trim(b.String(), "-.")
	runes := []rune(slug)
	if len(runes) > 50 {
		runes = runes[:50]
		slug = string(runes)
		slug = strings.TrimRight(slug, "-.")
	}
	if slug == "" {
		slug = "patch"
	}
	return slug
}

func isSlugSep(r rune) bool {
	if r == ' ' || r == '\t' {
		return true
	}
	if r < 0x20 || r == 0x7f {
		return true
	}
	return strings.ContainsRune("<>:\"/\\|?*.,;:'()[]{}!@#$%^&+=`~", r)
}
