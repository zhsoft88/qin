package main
import (
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"github.com/zhsoft88/lo/internal/core"
	"github.com/zhsoft88/lo/internal/repo"
)
// stringSlice is a flag.Value that collects multiple values into a slice.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type command struct {
	name    string
	desc    string
	run     func(args []string) error
}
func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println("lo version " + core.Version)
		return
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmds := map[string]command{
		"init":     {"init", "Initialize a new repository", runInit},
		"add":      {"add", "Stage file(s) for commit", runAdd},
		"rm":       {"rm", "Remove staged file(s)", runRm},
		"commit":   {"commit", "Create a commit from staged files", runCommit},
		"log":      {"log", "Show commit history [--graph]", runLog},
		"status":   {"status", "Show working tree status", runStatus},
		"cat":      {"cat", "Print an object's content", runCat},
		"ls":       {"ls", "List staged files", runLs},
		"checkout": {"checkout", "Restore files from a commit", runCheckout},
		"switch":   {"switch", "Switch to an existing branch", runSwitch},
		"branch":   {"branch", "List, create, or delete branches", runBranch},
		"tag":      {"tag", "List or create tags", runTag},
		"diff":     {"diff", "Show file-level changes", runDiff},
		"merge":    {"merge", "Merge a branch into the current branch", runMerge},
		"rebase":   {"rebase", "Rebase current branch onto another branch", runRebase},
		"cherry-pick": {"cherry-pick", "Apply changes from an existing commit", runCherryPick},
		"stash":    {"stash", "Stash or pop working tree changes", runStash},
		"remote":   {"remote", "Manage remotes", runRemote},
		"push":     {"push", "Push to remote", runPush},
		"fetch":    {"fetch", "Fetch from remote", runFetch},
		"pull":     {"pull", "Pull from remote and merge", runPull},
		"clone":     {"clone", "Clone a repository [--lazy]", runClone},
		"lfs":       {"lfs", "Manage large files (status, pull)", runLfs},
		"serve":      {"serve", "Start HTTP server for remote access [--addr] [--base-path]", runServe},
		"show":      {"show", "Show file content for the given OS [--os <os>]", runShow},
		"config":    {"config", "Get or set configuration values [--unset]", runConfig},
		"reset":     {"reset", "Reset HEAD [--soft | --mixed | --hard] [<commit>]", runReset},
		"restore":   {"restore", "Restore working tree or index files", runRestore},
		"apply":     {"apply", "Apply a patch to the working tree", runApply},
		"submodule":  {"submodule", "Manage submodules", runSubmodule},
		"lost-found": {"lost-found", "List dangling (unreachable) commits", runLostFound},
		"gc":         {"gc", "Prune dangling objects to reclaim space", runGC},
		"version":    {"version", "Show version information", runVersion},
	}
	cmd, ok := cmds[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
	if err := cmd.run(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
func usage() {
	fmt.Println(`Usage: lo <command> [options]
Commands:
  init              Initialize a new repository
  add <file>        Stage file(s) [--os | --os-match <expr>] [--exclude <glob>]
  rm <file>         Remove staged file(s)
  commit            Create a commit from staged files
  log [--graph]     Show commit history (--graph for branch visualization)
  status            Show working tree status
  cat <hash>        Print an object
  ls                List staged files
  checkout <ref>    Restore files from a commit
  switch <branch>   Switch to an existing branch
  branch [-d <name>] List, create, or delete branches
  tag [name]        List or create tags
  diff [--cached] [<ref> <ref>] Show file-level changes
  merge <branch>     Merge a branch into the current branch
  rebase <branch>    Rebase current branch onto another branch
  cherry-pick <ref>  Apply changes from an existing commit
  stash [pop|list]   Stash or pop working tree changes
  remote [add <name> <path>|remove <name>|list]  Manage remotes
  push [<remote>]    Push to remote (default: origin)
  fetch [<remote>]   Fetch from remote (default: origin)
  pull [<remote>]    Pull from remote and merge (default: origin)
  clone [--lazy] [--recursive] <url> <dir>  Clone a repository
  lfs status         Show large file status (placeholder vs. available)
  lfs pull [<file>]   Pull large file chunks on demand (--all for all)
  serve [--addr <addr>] [--base-path <path>]  Start HTTP server (default :8080; --base-path for multi-repo)
  show [--os <os>] <file>  Show file content (defaults to current OS if --os omitted)
  config [<key> [<value>]]  Get or set configuration values
  reset [--soft|--mixed|--hard] [<commit>]  Reset HEAD/index/working tree
  restore [--staged] <file>...       Restore working tree or index files
  apply [<patchfile>]            Apply a patch to the working tree (default: stdin)
  submodule add <url> <path>    Add a submodule
  submodule update [--init]     Update submodules
  submodule status              Show submodule status
  lost-found                    List dangling (unreachable) commits
  version                       Show version information
  gc                            Prune dangling objects to reclaim space

OS identifiers: win, mac, linux, freebsd, netbsd, openbsd, dragonfly, solaris, android`)
}
// ---- init ----
func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	path := fs.String("path", ".", "repository path")
	fs.Parse(args)
	r, err := repo.Init(*path)
	if err != nil {
		return err
	}
	fmt.Printf("initialized empty repository at %s\n", r.Path)
	return nil
}
// ---- add ----
func runAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	osFlag := fs.Bool("os", false, "tag file(s) with current OS")
	osMatchFlag := fs.String("os-match", "", "tag file(s) with OS expression (e.g., win, !win, win,linux)")
	var excludeFlags stringSlice
	fs.Var(&excludeFlags, "exclude", "exclude files matching glob pattern (repeatable)")
	args = reorderFlags(args)
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: lo add [--os] [--os-match <expr>] [--exclude <glob>] <file> [file...]")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	// --os-match takes priority over --os
	useOSExpr := *osMatchFlag
	if useOSExpr == "" && *osFlag {
		useOSExpr = repo.OSName(repo.CurrentOSID())
	}
	if useOSExpr != "" && useOSExpr != "*" {
		inc, exc, err := repo.ParseOSExpr(useOSExpr)
		if err != nil {
			return err
		}
		cOS := repo.CurrentOSID()
		if exc[cOS] || (len(inc) > 0 && !inc[cOS]) {
			return fmt.Errorf("--os-match %q excludes current OS (%s)", useOSExpr, repo.OSName(cOS))
		}
	}
	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}
	added := 0
	for _, f := range fs.Args() {
		if useOSExpr != "" {
			if err := addFileOrDirExpr(r, f, useOSExpr, excludeFlags, &added, idx); err != nil {
				fmt.Fprintf(os.Stderr, "  add %s: %v\n", f, err)
			}
		} else {
			if err := addFileOrDir(r, f, excludeFlags, &added, idx); err != nil {
				fmt.Fprintf(os.Stderr, "  add %s: %v\n", f, err)
			}
		}
	}
	if added > 0 {
		if err := r.SaveIndex(idx); err != nil {
			return err
		}
		fmt.Printf("added %d file(s)\n", added)
	}
	return nil
}
// addFileOrDir adds a file or directory recursively (default OS).
func addFileOrDir(r *repo.Repository, path string, excludes []string, added *int, idx *repo.Index) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		if pathExcluded(r, path, excludes) {
			return nil
		}
		// Skip if already in index with same size
		abs, _ := filepath.Abs(path)
		rel, _ := filepath.Rel(r.Path, abs)
		if rel != "" {
			k := repo.EntryKey(filepath.ToSlash(rel), 0)
			if e, ok := idx.Entries[k]; ok && e.Size == fi.Size() && e.Mode != repo.DirMode {
				return nil
			}
		}
		if err := r.AddFileToIndex(path, 0, nil, idx); err != nil {
			return err
		}
		(*added)++
		display := relPath(r, path)
		cd := 1
		for n := *added; n >= 10; n /= 10 { cd++ }
		w := repo.TermWidth()
		if w <= 0 {
			w = 80
		}
		const addPre = "\r  ["
		const addSuf = " [*]   "
		overhead := len(addPre) + len("] ") + len(addSuf)
		max := w - 2 - overhead - cd

		if max < 10 {
			max = 10
		}
		if len(display) > max {
			keep := max - 3
			if keep > 0 {
				first := (keep + 1) / 2
				last := keep / 2
				display = display[:first] + "..." + display[len(display)-last:]
			} else {
				display = display[:max]
			}
		}
		fmt.Fprintf(os.Stdout, addPre+"%d] %-*s"+addSuf, *added, max, display)
		return nil
	}
	if dirExcluded(r, path, excludes) {
		fmt.Fprintf(os.Stderr, "\nskip: %s   \n", path)
		return nil
	}
	entries, err := ioutil.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if err := r.AddFileToIndex(path, 0, nil, idx); err != nil {
			return err
		}
		(*added)++
		display := relPath(r, path) + "/"
		cd := 1
		for n := *added; n >= 10; n /= 10 { cd++ }
		w := repo.TermWidth()
		if w <= 0 {
			w = 80
		}
		const dPre = "\r  ["
		const dSuf = " [*]   "
		overhead := len(dPre) + len("] ") + len(dSuf)
		max := w - 2 - overhead - cd

		if max < 10 {
			max = 10
		}
		if len(display) > max {
			keep := max - 3
			if keep > 0 {
				first := (keep + 1) // 2
				last := keep // 2
				display = display[:first] + "..." + display[len(display)-last:]
			} else {
				display = display[:max]
			}
		}
		fmt.Fprintf(os.Stdout, dPre+"%d] %-*s"+dSuf, *added, max, display)
		return nil
	}
	for _, entry := range entries {
		if entry.Name() == ".lo" || entry.Name() == ".loignore" {
			continue
		}
		childPath := filepath.Join(path, entry.Name())
		if err := addFileOrDir(r, childPath, excludes, added, idx); err != nil {
			fmt.Fprintf(os.Stderr, "  add %s: %v\n", childPath, err)
		}
	}
	return nil
}
// addFileOrDirExpr adds a file or directory recursively with an OS expression.
func addFileOrDirExpr(r *repo.Repository, path, expr string, excludes []string, added *int, idx *repo.Index) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		if pathExcluded(r, path, excludes) {
			return nil
		}
		// Skip if already in index with same size
		abs, _ := filepath.Abs(path)
		rel, _ := filepath.Rel(r.Path, abs)
		if rel != "" {
			k := repo.EntryKey(filepath.ToSlash(rel), 0)
			if e, ok := idx.Entries[k]; ok && e.Size == fi.Size() && e.Mode != repo.DirMode {
				return nil
			}
		}
		var oss []uint8
		var osID uint8
		if expr != "" && expr != "*" {
			inc, exc, err := repo.ParseOSExpr(expr)
			if err != nil {
				return fmt.Errorf("invalid OS expression: %w", err)
			}
			if len(inc) > 0 && len(exc) == 0 {
				for id := range inc {
					oss = append(oss, id)
				}
				if len(oss) == 1 {
					osID = oss[0]
				}
			} else if len(exc) > 0 {
				for _, name := range repo.KnownOSes {
					id := repo.OSID(name)
					if !exc[id] {
						oss = append(oss, id)
					}
				}
			}
		}
		if err := r.AddFileToIndex(path, osID, oss, idx); err != nil {
			return err
		}
		(*added)++
		display := relPath(r, path)
		cd := 1
		for n := *added; n >= 10; n /= 10 { cd++ }
		w := repo.TermWidth()
		if w <= 0 {
			w = 80
		}
		const ePre = "\r  ["
		const eSuffix = "]   "
		midLen := len("] ") + len(" [")
		overhead := len(ePre) + midLen + len(expr) + len(eSuffix)
		max := w - 2 - overhead - cd

		if max < 10 {
			max = 10
		}
		if len(display) > max {
			keep := max - 3
			if keep > 0 {
				first := (keep + 1) / 2
				last := keep / 2
				display = display[:first] + "..." + display[len(display)-last:]
			} else {
				display = display[:max]
			}
		}
		fmt.Fprintf(os.Stdout, ePre+"%d] %-*s [%s"+eSuffix, *added, max, display, expr)
		return nil
	}
	if dirExcluded(r, path, excludes) {
		fmt.Fprintf(os.Stderr, "\nskip: %s   \n", path)
		return nil
	}
	entries, err := ioutil.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if err := r.AddFileToIndex(path, 0, nil, idx); err != nil {
			return err
		}
		(*added)++
		display := relPath(r, path) + "/"
		cd := 1
		for n := *added; n >= 10; n /= 10 { cd++ }
		w := repo.TermWidth()
		if w <= 0 {
			w = 80
		}
		const dPre = "\r  ["
		const dSuf = " [*]   "
		overhead := len(dPre) + len("] ") + len(dSuf)
		max := w - 2 - overhead - cd

		if max < 10 {
			max = 10
		}
		if len(display) > max {
			keep := max - 3
			if keep > 0 {
				first := (keep + 1) // 2
				last := keep // 2
				display = display[:first] + "..." + display[len(display)-last:]
			} else {
				display = display[:max]
			}
		}
		fmt.Fprintf(os.Stdout, dPre+"%d] %-*s"+dSuf, *added, max, display)
		return nil
	}
	for _, entry := range entries {
		if entry.Name() == ".lo" || entry.Name() == ".loignore" {
			continue
		}
		childPath := filepath.Join(path, entry.Name())
		if err := addFileOrDirExpr(r, childPath, expr, excludes, added, idx); err != nil {
			fmt.Fprintf(os.Stderr, "  add %s: %v\n", childPath, err)
		}
	}
	return nil
}

func relPath(r *repo.Repository, path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(r.Path, abs)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func pathExcluded(r *repo.Repository, path string, excludes []string) bool {
	return matchExcludes(r, path, excludes, false)
}

func dirExcluded(r *repo.Repository, path string, excludes []string) bool {
	return matchExcludes(r, path, excludes, true)
}

func matchExcludes(r *repo.Repository, path string, excludes []string, dir bool) bool {
	if len(excludes) == 0 {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(r.Path, absPath)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return false
	}
	for _, pattern := range excludes {
		if repo.MatchGlob(rel, pattern) {
			return true
		}
		if strings.Contains("/"+rel+"/", "/"+pattern+"/") {
			return true
		}
	}
	return false
}

func reorderFlags(args []string) []string {
	var flags []string
	var positional []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			flags = append(flags, args[i:]...)
			break
		}
		if len(args[i]) > 1 && args[i][0] == '-' {
			flags = append(flags, args[i])
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				if args[i] == "--exclude" || args[i] == "--os-match" {
					i++
					flags = append(flags, args[i])
				}
			}
		} else {
			positional = append(positional, args[i])
		}
	}
	return append(flags, positional...)
}

func filterMap(m map[string]repo.IndexEntry, patterns []string) map[string]repo.IndexEntry {
	if len(patterns) == 0 {
		return m
	}
	r := make(map[string]repo.IndexEntry)
	for k, v := range m {
		if matchAnyPath(k, patterns) {
			r[k] = v
		}
	}
	return r
}

func filterList(list []string, patterns []string) []string {
	if len(patterns) == 0 {
		return list
	}
	var r []string
	for _, p := range list {
		if matchAnyPath(p, patterns) {
			r = append(r, p)
		}
	}
	return r
}

func matchAnyPath(path string, patterns []string) bool {
	for _, p := range patterns {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
func clearLine() {
	w := repo.TermWidth()
	if w <= 0 {
		fmt.Fprintf(os.Stdout, "\n")
		return
	}
	fmt.Fprintf(os.Stdout, "\n%-*s\n", w-2, " ")
}
// ---- rm ----
func runRm(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lo rm <file> [file...]")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	for _, f := range args {
		if err := r.RemoveFile(f); err != nil {
			fmt.Fprintf(os.Stderr, "  rm %s: %v\n", f, err)
			continue
		}
		fmt.Printf("  removed: %s\n", f)
	}
	return nil
}
// ---- commit ----
func runCommit(args []string) error {
	fs := flag.NewFlagSet("commit", flag.ExitOnError)
	msg := fs.String("m", "", "commit message")
	author := fs.String("author", "", "author (default: from config)")
	fs.Parse(args)
	if *msg == "" {
		return fmt.Errorf("commit message required (-m)")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	auth := *author
	if auth == "" {
		cfg, _ := repo.LoadConfig(r.Path)
		if cfg.User.Name != "" {
			auth = cfg.User.Name
			if cfg.User.Email != "" {
				auth += " <" + cfg.User.Email + ">"
			}
		} else {
			auth = "unknown <unknown>"
		}
	}
	h, err := r.WriteCommit(auth, *msg)
	if err != nil {
		return err
	}
	fmt.Printf("committed: %s\n", h.Short())
	return nil
}
// ---- log ----
func runLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	n := fs.Int("n", 10, "number of commits to show")
	graph := fs.Bool("graph", false, "show branch graph visualization")
	all := fs.Bool("all", false, "show all branches")
	fs.Parse(args)
	r, err := findRepo()
	if err != nil {
		return err
	}
	if *graph {
		var commits []repo.GraphCommit
		if *all {
			commits, err = r.WalkAllGraph(*n)
		} else {
			commits, err = r.WalkGraph(*n)
		}
		if err != nil {
			return err
		}
		if len(commits) == 0 {
			fmt.Println("no commits")
			return nil
		}
		for _, line := range repo.RenderGraph(commits) {
			fmt.Println(line)
		}
		return nil
	}
	hashStr, err := r.ResolveHEAD()
	if err != nil {
		return err
	}
	if hashStr == "" {
		fmt.Println("no commits")
		return nil
	}
	h, err := core.HashFromHex(hashStr)
	if err != nil {
		return err
	}
	count := 0
	for !h.IsZero() && count < *n {
		commit, err := r.LoadCommit(h)
		if err != nil {
			return err
		}
		fmt.Printf("commit %s\n", h)
		fmt.Printf("Author: %s\n", commit.Author)
		fmt.Printf("Date:   %s\n\n", commit.Time.Format(time.RFC1123))
		fmt.Printf("    %s\n\n", commit.Message)
		if len(commit.Parents) > 0 {
			h = commit.Parents[0]
		} else {
			break
		}
		count++
	}
	return nil
}
// ---- status ----
func runStatus(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}

	var filter []string
	for _, a := range args {
		abs, err := filepath.Abs(a)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(r.Path, abs)
		if err != nil {
			continue
		}
		filter = append(filter, filepath.ToSlash(rel))
	}

	s, err := r.WorkTreeStatus()
	if err != nil {
		return err
	}

	if len(filter) > 0 {
		s.Staged = filterMap(s.Staged, filter)
		s.Modified = filterList(s.Modified, filter)
		s.Deleted = filterList(s.Deleted, filter)
		s.Untracked = filterList(s.Untracked, filter)
	}
	if s.Branch != "" {
		fmt.Printf("On branch: %s\n", s.Branch)
	} else if s.CommitHash != "" {
		fmt.Printf("HEAD detached at %s\n", s.CommitHash[:8])
	} else {
		fmt.Println("No commits yet")
	}
	if len(s.Staged) > 0 {
		fmt.Printf("\nstaged files: (%d)\n", len(s.Staged))
		paths := make([]string, 0, len(s.Staged))
		for p := range s.Staged {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			entry := s.Staged[p]
			osTag := ""
			if len(entry.OSS) > 0 {
				names := make([]string, len(entry.OSS))
				for i, id := range entry.OSS {
					names[i] = repo.OSName(id)
				}
				osTag = " [" + strings.Join(names, ",") + "]"
			} else {
				osTag = " [*]"
			}
			fmt.Printf("  %-20s %s bytes  %s%s\n", p, humanSize(entry.Size), entry.Hash.Short(), osTag)
		}
	}
	if len(s.Modified) > 0 {
		fmt.Printf("\nmodified files: (%d)\n", len(s.Modified))
		for _, p := range s.Modified {
			fmt.Printf("  %s (needs re-staging)\n", p)
		}
	}
	if len(s.Deleted) > 0 {
		fmt.Printf("\ndeleted files: (%d)\n", len(s.Deleted))
		for _, p := range s.Deleted {
			fmt.Printf("  %s\n", p)
		}
	}
	if len(s.Untracked) > 0 {
		fmt.Printf("\nuntracked files: (%d)\n", len(s.Untracked))
		for _, p := range s.Untracked {
			fmt.Printf("  %s\n", p)
		}
	}
	if len(s.Staged) == 0 && len(s.Modified) == 0 && len(s.Deleted) == 0 && len(s.Untracked) == 0 {
		fmt.Println("\nnothing to show, working tree clean")
	}
	return nil
}
// ---- cat ----
func runCat(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lo cat <hash>")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	var h core.Hash
	if len(args[0]) == core.HashSize*2 {
		h, err = core.HashFromHex(args[0])
	} else {
		h, err = r.FindObjectByPrefix(args[0])
	}
	if err != nil {
		return err
	}
	objType, content, err := r.LoadObject(h)
	if err != nil {
		return err
	}
	fmt.Printf("type: %s\n", objType)
	fmt.Printf("size: %d bytes\n\n", len(content))
	fmt.Println(string(content))
	return nil
}
// ---- ls ----
func runLs(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	files, err := r.ListFiles()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Println("nothing staged")
		return nil
	}

	visible := repo.VisibleFiles(files, repo.CurrentOSID())
	type displayEntry struct {
		path string
		hash string
		size int64
	}
	var entries []displayEntry
	for path, entry := range visible {
		entries = append(entries, displayEntry{
			path: path,
			hash: entry.Hash.Short(),
			size: entry.Size,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].path < entries[j].path
	})
	for _, e := range entries {
		fmt.Printf("%s  %s  %s\n", e.hash, humanSize(e.size), e.path)
	}
	return nil
}
// ---- checkout ----
func runCheckout(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lo checkout <ref>")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	h, err := r.ResolveRef(args[0])
	if err != nil {
		return fmt.Errorf("resolve ref: %w", err)
	}
	if err := r.Checkout(h); err != nil {
		return fmt.Errorf("checkout: %w", err)
	}
	fmt.Printf("checked out: %s\n", h.Short())
	return nil
}
// ---- branch ----
func runBranch(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		branches, current, err := r.ListBranches()
		if err != nil {
			return err
		}
		for _, b := range branches {
			if b == current {
				fmt.Printf("* %s\n", b)
			} else {
				fmt.Printf("  %s\n", b)
			}
		}
		return nil
	}
	if args[0] == "-d" {
		if len(args) < 2 {
			return fmt.Errorf("usage: lo branch -d <name>")
		}
		if err := r.DeleteBranch(args[1]); err != nil {
			return err
		}
		fmt.Printf("deleted branch: %s\n", args[1])
		return nil
	}
	if err := r.CreateBranch(args[0]); err != nil {
		return err
	}
	fmt.Printf("created branch: %s\n", args[0])
	return nil
}
// ---- switch ----
func runSwitch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lo switch <branch>")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	if err := r.SwitchBranch(args[0]); err != nil {
		return err
	}
	fmt.Printf("switched to branch: %s\n", args[0])
	return nil
}
// ---- tag ----
func runTag(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		tagsDir := filepath.Join(r.RefsDir(), "tags")
		entries, err := ioutil.ReadDir(tagsDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			fmt.Println(entry.Name())
		}
		return nil
	}
	name := args[0]
	headHash, err := r.ResolveHEAD()
	if err != nil {
		return fmt.Errorf("resolve HEAD: %w", err)
	}
	if headHash == "" {
		return fmt.Errorf("no commits to tag")
	}
	if err := r.WriteRef("refs/tags/"+name, headHash); err != nil {
		return err
	}
	fmt.Printf("created tag: %s\n", name)
	return nil
}
// ---- diff ----
func runDiff(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	// Check for --cached flag
	cached := false
	patchMode := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--cached" {
			cached = true
		} else if a == "--patch" {
			patchMode = true
		} else {
			rest = append(rest, a)
		}
	}
	var diff *repo.Diff
	if cached {
		diff, err = r.DiffIndex()
		if err != nil {
			return err
		}
	} else {
		switch len(rest) {
		case 0:
			diff, err = r.DiffWorking()
			if err != nil {
				return err
			}
			if len(diff.Files) == 0 {
				diff, err = r.DiffIndex()
				if err != nil {
					return err
				}
			}
		case 1:
			_, err := r.ResolveRef(rest[0])
			if err != nil {
				return fmt.Errorf("resolve ref: %w", err)
			}
			diff, err = r.DiffIndex()
			if err != nil {
				return err
			}
		case 2:
			h1, err := r.ResolveRef(rest[0])
			if err != nil {
				return fmt.Errorf("resolve ref: %w", err)
			}
			h2, err := r.ResolveRef(rest[1])
			if err != nil {
				return fmt.Errorf("resolve ref: %w", err)
			}
			diff, err = r.DiffCommits(h1, h2)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("usage: lo diff [--cached] [--patch] [<ref> <ref>]")
		}
	}
	if patchMode {
		patch, err := r.RenderPatch(diff)
		if err != nil {
			return fmt.Errorf("render patch: %w", err)
		}
		fmt.Print(patch)
	} else {
		fmt.Print(diff.Render())
	}
	return nil
}
// ---- merge ----
func runMerge(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lo merge <branch>")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	result, err := r.Merge(args[0])
	if err != nil {
		if result != nil && len(result.Conflicts) > 0 {
			fmt.Fprintf(os.Stderr, "merge conflicts in %d files:\n", len(result.Conflicts))
			for _, name := range result.Conflicts {
				fmt.Fprintf(os.Stderr, "  %s (see %s.ours, %s.theirs, %s.base)\n", name, name, name, name)
			}
			fmt.Fprintln(os.Stderr, "resolve conflicts and commit")
			return nil
		}
		return err
	}
	if result.FastForward {
		fmt.Println("fast-forward merge")
	} else {
		fmt.Println("merged")
	}
	if result.Diff != nil {
		fmt.Print(result.Diff.Render())
	}
	return nil
}
// ---- rebase ----
func runRebase(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lo rebase <branch>")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	if err := r.Rebase(args[0]); err != nil {
		return fmt.Errorf("rebase: %w", err)
	}
	fmt.Printf("rebased onto %s\n", args[0])
	return nil
}
// ---- cherry-pick ----
func runCherryPick(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lo cherry-pick <ref>")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	h, err := r.ResolveRef(args[0])
	if err != nil {
		return fmt.Errorf("resolve ref: %w", err)
	}
	if err := r.CherryPick(h); err != nil {
		return fmt.Errorf("cherry-pick: %w", err)
	}
	fmt.Printf("cherry-picked: %s\n", h.Short())
	return nil
}
// ---- stash ----
func runStash(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "pop" {
		if err := r.StashPop(); err != nil {
			return err
		}
		fmt.Println("restored stash")
		return nil
	}
	if len(args) > 0 && args[0] == "list" {
		stashes, err := r.StashList()
		if err != nil {
			return err
		}
		if len(stashes) == 0 {
			fmt.Println("no stashes")
			return nil
		}
		for _, s := range stashes {
			fmt.Println(s)
		}
		return nil
	}
	if err := r.Stash(); err != nil {
		return err
	}
	fmt.Println("saved stash")
	return nil
}
// ---- remote ----
func runRemote(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		remotes, err := r.ListRemotes()
		if err != nil {
			return err
		}
		if len(remotes) == 0 {
			fmt.Println("no remotes configured")
			return nil
		}
		for _, rm := range remotes {
			fmt.Printf("%s\t%s\n", rm.Name, rm.URL)
		}
		return nil
	}
	switch args[0] {
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: lo remote add <name> <path>")
		}
		if err := r.SaveRemote(args[1], args[2]); err != nil {
			return err
		}
		fmt.Printf("added remote: %s -> %s\n", args[1], args[2])
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: lo remote remove <name>")
		}
		if err := r.RemoveRemote(args[1]); err != nil {
			return err
		}
		fmt.Printf("removed remote: %s\n", args[1])
	case "list":
		remotes, err := r.ListRemotes()
		if err != nil {
			return err
		}
		for _, rm := range remotes {
			fmt.Printf("%s\t%s\n", rm.Name, rm.URL)
		}
	default:
		return fmt.Errorf("unknown remote subcommand: %s (use add, remove, list)", args[0])
	}
	return nil
}
// ---- push ----
func runPush(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	remote := "origin"
	if len(args) > 0 {
		remote = args[0]
	}
	if err := r.Push(remote); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	fmt.Printf("pushed to %s\n", remote)
	return nil
}
// ---- fetch ----
func runFetch(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	remote := "origin"
	if len(args) > 0 {
		remote = args[0]
	}
	if err := r.Fetch(remote); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	fmt.Printf("fetched from %s\n", remote)
	return nil
}
// ---- pull ----
func runPull(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	remote := "origin"
	if len(args) > 0 {
		remote = args[0]
	}
	result, err := r.Pull(remote)
	if err != nil {
		if result != nil && len(result.Conflicts) > 0 {
			fmt.Fprintf(os.Stderr, "pull conflicts in %d files:\n", len(result.Conflicts))
			for _, name := range result.Conflicts {
				fmt.Fprintf(os.Stderr, "  %s\n", name)
			}
			return nil
		}
		return fmt.Errorf("pull: %w", err)
	}
	if result.FastForward {
		fmt.Println("fast-forward pull")
	} else {
		fmt.Println("pulled and merged")
	}
	return nil
}
// ---- clone ----
func runClone(args []string) error {
	lazy := false
	recursive := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--lazy":
			lazy = true
		case "--recursive":
			recursive = true
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: lo clone [--lazy] <url> <dir>")
	}
	r, err := repo.Clone(rest[0], rest[1], lazy)
	if err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	if lazy {
		fmt.Println("cloned with lazy large files (use lfs-pull to fetch on demand)")
	}
	if recursive {
		if err := cloneSubmodules(r); err != nil {
			return fmt.Errorf("clone submodules: %w", err)
		}
	}
	fmt.Printf("cloned into %s\n", r.Path)
	return nil
}
// ---- lfs ----
func runLfs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lo lfs status|pull [<file>]")
	}
	switch args[0] {
	case "status":
		return runLfsStatus(args[1:])
	case "pull":
		return runLfsPull(args[1:])
	default:
		return fmt.Errorf("unknown lfs subcommand: %s (use status, pull)", args[0])
	}
}

func runLfsStatus(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	files, err := r.LfsStatus()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Println("no large files in index")
		return nil
	}
	fmt.Printf("%-35s %-10s %-20s %s\n", "File", "Size", "Hash", "Status")
	for _, f := range files {
		status := "placeholder"
		if f.OnDisk {
			status = "available"
		}
		displayPath := f.Path
		if f.OS != 0 {
			displayPath = f.Path + " [" + repo.OSNameOrStar(f.OS) + "]"
		} else {
			displayPath = f.Path + " [*]"
		}
		fmt.Printf("%-35s %-10s %-20s %s\n", displayPath, humanSize(f.Size), f.Hash.Short(), status)
	}
	return nil
}

// ---- lfs pull ----
func runLfsPull(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lo lfs pull [--all | <file>]")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	remote := "origin"
	all := false
	targets := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--all":
			all = true
		default:
			targets = append(targets, a)
		}
	}
	if all {
		files, err := r.LfsStatus()
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.OnDisk {
				continue
			}
			fmt.Printf("pulling %s...\n", f.Path)
			if err := r.LfsPull(remote, f.Path); err != nil {
				fmt.Fprintf(os.Stderr, "  pull %s: %v\n", f.Path, err)
			}
		}
		return nil
	}
	for _, file := range targets {
		if err := r.LfsPull(remote, file); err != nil {
			fmt.Fprintf(os.Stderr, "  pull %s: %v\n", file, err)
			continue
		}
		fmt.Printf("pulled: %s\n", file)
	}
	return nil
}
// ---- show ----
func runShow(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lo show [--os <os>] <file>")
	}
	osName := ""
	filePath := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--os" && i+1 < len(args) {
			osName = args[i+1]
			i++
		} else if filePath == "" {
			filePath = args[i]
		}
	}
	if filePath == "" {
		return fmt.Errorf("usage: lo show [--os <os>] <file>")
	}

	// Default to current OS
	if osName == "" {
		osName = repo.OSName(repo.CurrentOSID())
	}
	osID := repo.OSID(osName)

	r, err := findRepo()
	if err != nil {
		return err
	}
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return err
	}
	relPath, err := filepath.Rel(r.Path, absPath)
	if err != nil {
		return fmt.Errorf("path outside repository: %w", err)
	}
	relFormatted := filepath.ToSlash(relPath)

	idx, err := r.LoadIndex()
	if err != nil {
		return err
	}

	// Try OS-specific key first, fall back to default
	key := repo.EntryKey(relFormatted, osID)
	entry, ok := idx.Entries[key]
	if !ok && osID != 0 {
		key = relFormatted
		entry, ok = idx.Entries[key]
	}
	if !ok {
		return fmt.Errorf("file '%s' not found for OS '%s'", filePath, osName)
	}

	objType, content, err := r.LoadObject(entry.Hash)
	if err != nil {
		return fmt.Errorf("load object: %w", err)
	}
	fmt.Printf("type: %s  size: %d bytes  hash: %s  os: %s\n\n", objType, len(content), entry.Hash.Short(), osName)
	os.Stdout.Write(content)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		fmt.Println()
	}
	return nil
}
// ---- serve ----
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	basePath := fs.String("base-path", "", "serve multiple repositories from this base directory")
	fs.Parse(args)

	if *basePath != "" {
		srv := &repo.RepoServer{BasePath: *basePath}
		fmt.Printf("serving repositories from %s on %s\n", *basePath, *addr)
		return http.ListenAndServe(*addr, srv)
	}

	r, err := findRepo()
	if err != nil {
		return err
	}
	fmt.Printf("serving %s on %s\n", r.Path, *addr)
	return http.ListenAndServe(*addr, http.HandlerFunc(r.ServeHTTP))
}
// ---- config ----

func runConfig(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}

	cfg, err := repo.LoadConfig(r.Path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if len(args) > 0 && args[0] == "--unset" {
		if len(args) != 2 {
			return fmt.Errorf("usage: lo config --unset <key>")
		}
		if err := repo.ConfigUnset(cfg, args[1]); err != nil {
			return err
		}
		if err := repo.SaveConfig(r.Path, cfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		fmt.Printf("%s reset to default\n", args[1])
		return nil
	}

	switch len(args) {
	case 0:
		// List all keys with values
		keys := repo.ConfigKeys()
		ks := make([]string, 0, len(keys))
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			v, _ := repo.ConfigGet(cfg, k)
			fmt.Printf("%s = %s  # %s\n", k, v, keys[k])
		}
		return nil

	case 1:
		// Get single key
		v, err := repo.ConfigGet(cfg, args[0])
		if err != nil {
			return err
		}
		fmt.Println(v)
		return nil

	case 2:
		// Set key to value
		if err := repo.ConfigSet(cfg, args[0], args[1]); err != nil {
			return err
		}
		if err := repo.SaveConfig(r.Path, cfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		fmt.Printf("%s = %s\n", args[0], args[1])
		return nil

	default:
		return fmt.Errorf("usage: lo config [<key> [<value>]]")
	}
}

func runReset(args []string) error {
	mode := "mixed"
	var target string
	for _, a := range args {
		switch a {
		case "--soft":
			mode = "soft"
		case "--mixed":
			mode = "mixed"
		case "--hard":
			mode = "hard"
		default:
			if target != "" {
				return fmt.Errorf("usage: lo reset [--soft | --mixed | --hard] [<commit>]")
			}
			target = a
		}
	}

	r, err := findRepo()
	if err != nil {
		return err
	}

	if target == "" {
		// Default to HEAD
		hashStr, err := r.ResolveHEAD()
		if err != nil || hashStr == "" {
			return fmt.Errorf("no commits")
		}
		target = hashStr
	}

	h, err := r.ResolveRef(target)
	if err != nil {
		return fmt.Errorf("resolve ref: %w", err)
	}

	if err := r.ResetCommit(h, mode); err != nil {
		return fmt.Errorf("reset %s: %w", mode, err)
	}

	fmt.Printf("reset (%s) to %s\n", mode, h.Short())
	return nil
}

func runRestore(args []string) error {
	staged := false
	files := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--staged" {
			staged = true
		} else {
			files = append(files, a)
		}
	}

	if len(files) == 0 {
		return fmt.Errorf("usage: lo restore [--staged] <file> [file...]")
	}

	r, err := findRepo()
	if err != nil {
		return err
	}

	for _, f := range files {
		if staged {
			if err := r.RestoreStaged(f); err != nil {
				fmt.Fprintf(os.Stderr, "  restore --staged %s: %v\n", f, err)
				continue
			}
			fmt.Printf("  unstaged: %s\n", f)
		} else {
			if err := r.RestoreFile(f); err != nil {
				fmt.Fprintf(os.Stderr, "  restore %s: %v\n", f, err)
				continue
			}
			fmt.Printf("  restored: %s\n", f)
		}
	}
	return nil
}


func runApply(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}

	var data []byte
	if len(args) > 0 {
		data, err = ioutil.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("read patch file: %w", err)
		}
	} else {
		data, err = ioutil.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	}

	if err := r.ApplyPatch(data); err != nil {
		return fmt.Errorf("apply patch: %w", err)
	}
	return nil
}
func runSubmodule(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lo submodule add|update|status ...")
	}
	switch args[0] {
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: lo submodule add url path")
		}
		r, err := findRepo()
		if err != nil {
			return err
		}
		if err := repo.AddSubmodule(r, args[1], args[2]); err != nil {
			return err
		}
		fmt.Printf("added submodule: %s -> %s\n", args[2], args[1])
	case "update":
		r, err := findRepo()
		if err != nil {
			return err
		}
		initFlag := false
		rest := args[1:]
		if len(rest) > 0 && rest[0] == "--init" {
			initFlag = true
			rest = rest[1:]
		}
		mods, err := repo.LoadLoModules(r)
		if err != nil {
			return err
		}
		if len(mods.Submodules) == 0 {
			fmt.Println("no submodules configured")
			return nil
		}
		for path, def := range mods.Submodules {
			subPath := filepath.Join(r.Path, path)
			_, statErr := os.Stat(subPath)
			if os.IsNotExist(statErr) {
				if !initFlag {
					fmt.Printf("  %s: not cloned (use --init to clone)\n", path)
					continue
				}
				fmt.Printf("  init %s...\n", path)
				if err := repo.AddSubmodule(r, def.URL, path); err != nil {
					fmt.Fprintf(os.Stderr, "  init %s: %v\n", path, err)
					continue
				}
				fmt.Printf("  %s: cloned\n", path)
			} else {
				fmt.Printf("  update %s...\n", path)
			}
		}
	case "status":
		r, err := findRepo()
		if err != nil {
			return err
		}
		mods, err := repo.LoadLoModules(r)
		if err != nil {
			return err
		}
		if len(mods.Submodules) == 0 {
			fmt.Println("no submodules")
			return nil
		}
		for path, def := range mods.Submodules {
			subPath := filepath.Join(r.Path, path)
			_, statErr := os.Stat(filepath.Join(subPath, ".lo"))
			if os.IsNotExist(statErr) {
				fmt.Printf("  %s -> %s (not initialized)\n", path, def.URL)
			} else {
				fmt.Printf("  %s -> %s\n", path, def.URL)
			}
		}
	default:
		return fmt.Errorf("unknown submodule subcommand: %s (use add, update, status)", args[0])
	}
	return nil
}

// ---- helpers ----
func findRepo() (*repo.Repository, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return repo.Open(wd)
}
func humanSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%d KB", bytes/1024)
	}
	return fmt.Sprintf("%d MB", bytes/(1024*1024))
}
func cloneSubmodules(r *repo.Repository) error {
	mods, err := repo.LoadLoModules(r)
	if err != nil {
		return err
	}
	for path, def := range mods.Submodules {
		fmt.Printf("  cloning submodule %s...\n", path)
		if err := repo.AddSubmodule(r, def.URL, path); err != nil {
			fmt.Fprintf(os.Stderr, "  clone submodule %s: %v\n", path, err)
			continue
		}
	}
	return nil
}

func runLostFound(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	commits, err := r.FindDanglingCommits()
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		fmt.Println("no dangling commits found")
		return nil
	}
	fmt.Printf("dangling commits: (%d)\n", len(commits))
	fmt.Println()
	for _, c := range commits {
		fmt.Printf("  commit %s\n", c.Hash)
		fmt.Printf("  Author: %s\n", c.Author)
		fmt.Printf("  Date:   %s\n", c.Time.Format("Mon Jan 2 15:04:05 2006"))
		if c.Parents > 0 {
			fmt.Printf("  Parents: %d\n", c.Parents)
		} else {
			fmt.Println("  Parents: 0 (root commit)")
		}
		fmt.Printf("\n      %s\n\n", c.Message)
	}
	fmt.Println("  ---")
	fmt.Println("  To recover: use lo checkout <hash> then lo branch <name>")
	return nil
}

func runVersion(args []string) error {
	fmt.Println("lo version " + core.Version)
	return nil
}

func runGC(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	report, err := r.GC()
	if err != nil {
		return err
	}
	if report.Pruned == 0 {
		fmt.Println("nothing to prune")
		return nil
	}
	fmt.Printf("pruned %d objects, freed %s\n", report.Pruned, humanSize(report.Freed))
	return nil
}
