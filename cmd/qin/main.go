package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/zhsoft88/qin/internal/core"
	"github.com/zhsoft88/qin/internal/repo"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// stringSlice is a flag.Value that collects multiple values into a slice.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type command struct {
	name string
	desc string
	run  func(args []string) error
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
		"init":              {"init", "Initialize a new repository", runInit},
		"add":               {"add", "Stage file(s) for commit", runAdd},
		"rm":                {"rm", "Remove staged file(s)", runRm},
		"commit":            {"commit", "Create a commit from staged files", runCommit},
		"log":               {"log", "Show commit history [--graph]", runLog},
		"status":            {"status", "Show working tree status", runStatus},
		"cat":               {"cat", "Print an object's content", runCat},
		"ls":                {"ls", "List staged files", runLs},
		"checkout":          {"checkout", "Restore files from a commit", runCheckout},
		"switch":            {"switch", "Switch to an existing branch", runSwitch},
		"branch":            {"branch", "List, create, or delete branches", runBranch},
		"tag":               {"tag", "List or create tags", runTag},
		"diff":              {"diff", "Show file-level changes", runDiff},
		"merge":             {"merge", "Merge a branch into the current branch", runMerge},
		"rebase":            {"rebase", "Rebase current branch onto another branch", runRebase},
		"cherry-pick":       {"cherry-pick", "Apply changes from an existing commit", runCherryPick},
		"stash":             {"stash", "Stash or pop working tree changes", runStash},
		"remote":            {"remote", "Manage remotes", runRemote},
		"push":              {"push", "Push to remote", runPush},
		"fetch":             {"fetch", "Fetch from remote", runFetch},
		"pull":              {"pull", "Pull from remote and merge", runPull},
		"clone":             {"clone", "Clone a repository [--lazy]", runClone},
		"lfs":               {"lfs", "Manage large files (status, pull)", runLfs},
		"serve":             {"serve", "Start HTTP server for remote access [--addr] [--base-path]", runServe},
		"show":              {"show", "Show file content for the given OS [--os <os>]", runShow},
		"config":            {"config", "Get or set configuration values [--unset]", runConfig},
		"reset":             {"reset", "Reset HEAD [--soft | --mixed | --hard] [<commit>]", runReset},
		"restore":           {"restore", "Restore working tree or index files", runRestore},
		"apply":             {"apply", "Apply a patch to the working tree", runApply},
		"format-patch":      {"format-patch", "Export commits as numbered patch files", runFormatPatch},
		"am":                {"am", "Apply patch file(s) as commits", runAm},
		"submodule":         {"submodule", "Manage submodules", runSubmodule},
		"fsmonitor--daemon": {"fsmonitor--daemon", "Manage the built-in change monitor (start | run | stop | status)", runFsmonitorDaemon},
		"lost-found":        {"lost-found", "List dangling (unreachable) commits", runLostFound},
		"gc":                {"gc", "Prune dangling objects to reclaim space", runGC},
		"version":           {"version", "Show version information", runVersion},
		// Aliases
		"st": {"st", "Alias for status", runStatus},
		"co": {"co", "Alias for checkout", runCheckout},
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
  init [<path>]     Initialize a new repository (default: current dir)
  add <file>        Stage file(s) [--os | --os-match <expr>] [--exclude <glob> | --exclude @file]
  rm [--cached] [-r] <file>   Remove files (--cached keeps on disk)
  commit            Create a commit from staged files
  log [--graph]     Show commit history (--graph for branch visualization)
  status            Show working tree status (alias: st)
  cat <hash>        Print an object
  ls                List staged files
  checkout <ref>    Restore files from a commit (alias: co)
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
  format-patch <base>..<head>    Export commits as numbered patch files (-N <n> <head> for last N)
  am <patchfile>...              Apply patch file(s) as commits (default: stdin)
  submodule add <url> <path>    Add a submodule
  submodule update [--init]     Update submodules
  submodule status              Show submodule status
  lost-found                    List dangling (unreachable) commits
  version                       Show version information
  gc                            Prune dangling objects to reclaim space
  fsmonitor--daemon start|stop|status  Built-in change monitor (core.fsmonitor)
  fsmonitor--daemon run [--repo <path>] [--poll-interval <dur>]
                                Run the monitor in the foreground

OS identifiers: win, mac, linux`)
}

// ---- init ----
func runInit(args []string) error {
	path := "."
	if len(args) > 0 && args[0] != "" {
		path = args[0]
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	r, err := repo.Init(abs)
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
	ignorer, err := r.LoadIgnoreMatcher()
	if err != nil {
		return err
	}
	excludeFlags, err = expandExcludeFiles(excludeFlags, r.Path)
	if err != nil {
		return err
	}
	args, err = expandExcludeFiles(fs.Args(), r.Path)
	if err != nil {
		return err
	}
	added := 0
	for _, f := range args {
		if useOSExpr != "" {
			if err := addFileOrDirExpr(r, f, useOSExpr, excludeFlags, &added, idx, ignorer); err != nil {
				fmt.Fprintf(os.Stderr, "add %s: %v\n", f, err)
			}
		} else {
			if err := addFileOrDir(r, f, excludeFlags, &added, idx, ignorer); err != nil {
				fmt.Fprintf(os.Stderr, "add %s: %v\n", f, err)
			}
		}
	}
	if added > 0 {
		if err := r.SaveIndex(idx); err != nil {
			return err
		}
		clearLine()
		fmt.Printf("added %d file(s)\n", added)
	}
	return nil
}

// addFileOrDir adds a file or directory recursively (default OS).
func addFileOrDir(r *repo.Repository, path string, excludes []string, added *int, idx *repo.Index, ignorer *repo.IgnoreMatcher) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		if pathExcluded(r, path, excludes) {
			return nil
		}
		changed, err := r.AddFileToIndex(path, 0, idx)
		if err != nil {
			return err
		}
		if !changed {
			return nil // already in the index with the same content
		}
		(*added)++
		display := relPath(r, path)
		cd := 1
		for n := *added; n >= 10; n /= 10 {
			cd++
		}
		w := repo.TermWidth()
		if w <= 0 {
			w = 80
		}
		const addPre = "\r["
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
		changed, err := r.AddFileToIndex(path, 0, idx)
		if err != nil {
			return err
		}
		if !changed {
			return nil // already in the index with the same content
		}
		(*added)++
		display := relPath(r, path) + "/"
		cd := 1
		for n := *added; n >= 10; n /= 10 {
			cd++
		}
		w := repo.TermWidth()
		if w <= 0 {
			w = 80
		}
		const dPre = "\r["
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
				last := keep        // 2
				display = display[:first] + "..." + display[len(display)-last:]
			} else {
				display = display[:max]
			}
		}
		fmt.Fprintf(os.Stdout, dPre+"%d] %-*s"+dSuf, *added, max, display)
		return nil
	}
	for _, entry := range entries {
		childPath := filepath.Join(path, entry.Name())
		if entry.IsDir() {
			absChild, _ := filepath.Abs(childPath)
			relChild, _ := filepath.Rel(r.Path, absChild)
			if relChild != "" && ignorer.Match(filepath.ToSlash(relChild), true) {
				continue
			}
		}
		if err := addFileOrDir(r, childPath, excludes, added, idx, ignorer); err != nil {
			fmt.Fprintf(os.Stderr, "add %s: %v\n", childPath, err)
		}
	}
	return nil
}

// addFileOrDirExpr adds a file or directory recursively with an OS expression.
func addFileOrDirExpr(r *repo.Repository, path, expr string, excludes []string, added *int, idx *repo.Index, ignorer *repo.IgnoreMatcher) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		if pathExcluded(r, path, excludes) {
			return nil
		}

		var mask uint8
		if expr != "" && expr != "*" {
			inc, exc, err := repo.ParseOSExpr(expr)
			if err != nil {
				return fmt.Errorf("invalid OS expression: %w", err)
			}
			mask = repo.MaskFromOSExpr(inc, exc)
		}
		changed, err := r.AddFileToIndex(path, mask, idx)
		if err != nil {
			return err
		}
		if !changed {
			return nil // already in the index with the same content
		}
		(*added)++
		display := relPath(r, path)
		cd := 1
		for n := *added; n >= 10; n /= 10 {
			cd++
		}
		w := repo.TermWidth()
		if w <= 0 {
			w = 80
		}
		const ePre = "\r["
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
		changed, err := r.AddFileToIndex(path, 0, idx)
		if err != nil {
			return err
		}
		if !changed {
			return nil // already in the index with the same content
		}
		(*added)++
		display := relPath(r, path) + "/"
		cd := 1
		for n := *added; n >= 10; n /= 10 {
			cd++
		}
		w := repo.TermWidth()
		if w <= 0 {
			w = 80
		}
		const dPre = "\r["
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
				last := keep        // 2
				display = display[:first] + "..." + display[len(display)-last:]
			} else {
				display = display[:max]
			}
		}
		fmt.Fprintf(os.Stdout, dPre+"%d] %-*s"+dSuf, *added, max, display)
		return nil
	}
	for _, entry := range entries {
		childPath := filepath.Join(path, entry.Name())
		if entry.IsDir() {
			absChild, _ := filepath.Abs(childPath)
			relChild, _ := filepath.Rel(r.Path, absChild)
			if relChild != "" && ignorer.Match(filepath.ToSlash(relChild), true) {
				continue
			}
		}
		if err := addFileOrDirExpr(r, childPath, expr, excludes, added, idx, ignorer); err != nil {
			fmt.Fprintf(os.Stderr, "add %s: %v\n", childPath, err)
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

// expandExcludeFiles reads any exclude pattern starting with '@' as a file,
// replacing it with the non-blank, non-comment lines from that file.
func expandExcludeFiles(excludes []string, repoPath string) ([]string, error) {
	var out []string
	for _, p := range excludes {
		if !strings.HasPrefix(p, "@") {
			out = append(out, p)
			continue
		}
		f := filepath.Join(repoPath, p[1:])
		data, err := ioutil.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read exclude file %s: %w", f, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				out = append(out, line)
			}
		}
	}
	return out, nil
}

// expandArgFiles is like expandExcludeFiles but silently falls back
// to the literal path when the file doesn't exist.
func expandArgFiles(args []string, repoPath string) []string {
	var out []string
	for _, p := range args {
		if !strings.HasPrefix(p, "@") {
			out = append(out, p)
			continue
		}
		f := filepath.Join(repoPath, p[1:])
		data, err := ioutil.ReadFile(f)
		if err != nil {
			// @file not found - fall back to literal path
			out = append(out, p[1:])
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				out = append(out, line)
			}
		}
	}
	return out
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
	cached := false
	recursive := false
	var files []string
	for _, a := range args {
		if a == "--cached" {
			cached = true
		} else if a == "-r" {
			recursive = true
		} else {
			files = append(files, a)
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("usage: lo rm [--cached] [-r] <file> [file...]")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	if recursive {
		idx, err := r.LoadIndex()
		if err != nil {
			return err
		}
		for _, f := range files {
			abs, _ := filepath.Abs(f)
			rel, _ := filepath.Rel(r.Path, abs)
			relFormatted := filepath.ToSlash(rel)
			for key := range idx.Entries {
				path, _ := repo.ParseKey(key)
				if path == relFormatted || strings.HasPrefix(path, relFormatted+"/") {
					delete(idx.Entries, key)
				}
			}
			if !cached {
				os.RemoveAll(filepath.Join(r.Path, relFormatted))
			}
			fmt.Printf("  removed: %s\n", f)
		}
		return r.SaveIndex(idx)
	}
	for _, f := range files {
		// Auto-detect directories — remove recursively
		if fi, err := os.Stat(f); err == nil && fi.IsDir() {
			abs, _ := filepath.Abs(f)
			rel, _ := filepath.Rel(r.Path, abs)
			relFormatted := filepath.ToSlash(rel)
			idx, err := r.LoadIndex()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  rm %s: %v\n", f, err)
				continue
			}
			for key := range idx.Entries {
				path, _ := repo.ParseKey(key)
				if path == relFormatted || strings.HasPrefix(path, relFormatted+"/") {
					delete(idx.Entries, key)
				}
			}
			if !cached {
				os.RemoveAll(filepath.Join(r.Path, relFormatted))
			}
			if err := r.SaveIndex(idx); err != nil {
				fmt.Fprintf(os.Stderr, "  rm %s: %v\n", f, err)
				continue
			}
			fmt.Printf("  removed: %s\n", f)
			continue
		}
		if cached {
			if err := r.RemoveFile(f); err != nil {
				fmt.Fprintf(os.Stderr, "  rm %s: %v\n", f, err)
				continue
			}
			fmt.Printf("  removed from index: %s\n", f)
		} else {
			if err := r.RemoveFile(f); err != nil {
				fmt.Fprintf(os.Stderr, "  rm %s: %v\n", f, err)
				continue
			}
			os.Remove(filepath.Join(r.Path, f))
			fmt.Printf("  removed: %s\n", f)
		}
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
		fmt.Printf("  %s\n\n", commit.Message)
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

	// Make sure a change monitor is running, if this repository asks for one.
	// It is started for the runs that follow this one: the daemon publishes
	// itself only after walking the tree, which is not something a status
	// command should wait for, so this run scans everything either way.
	noteFsmonitorFallback(r)

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
			osTag := " [*]"
			if entry.OSS != 0 {
				osTag = " [" + strings.Join(repo.OSNames(entry.OSS), ",") + "]"
			}
			fmt.Printf("%-20s %s bytes  %s%s\n", p, humanSize(entry.Size), entry.Hash.Short(), osTag)
		}
	}
	if len(s.Modified) > 0 {
		fmt.Printf("\nmodified files: (%d)\n", len(s.Modified))
		for _, p := range s.Modified {
			fmt.Printf("%s (needs re-staging)\n", p)
		}
	}
	if len(s.Deleted) > 0 {
		fmt.Printf("\ndeleted files: (%d)\n", len(s.Deleted))
		for _, p := range s.Deleted {
			fmt.Printf("%s\n", p)
		}
	}
	if len(s.Untracked) > 0 {
		fmt.Printf("\nuntracked files: (%d)\n", len(s.Untracked))
		for _, p := range s.Untracked {
			fmt.Printf("%s\n", p)
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

	// Show all files regardless of OS, sorted by path then OS variant
	// (default first, then win/mac/linux)
	type lsEntry struct {
		path  string
		osID  uint8
		entry repo.IndexEntry
	}
	entries := make([]lsEntry, 0, len(files))
	for key, entry := range files {
		path, osID := repo.ParseKey(key)
		entries = append(entries, lsEntry{path, osID, entry})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].path != entries[j].path {
			return entries[i].path < entries[j].path
		}
		if entries[i].osID == 0 || entries[j].osID == 0 {
			return entries[i].osID == 0
		}
		return entries[i].osID < entries[j].osID
	})
	for _, e := range entries {
		path, osID := e.path, e.osID
		entry := e.entry
		osTag := "*"
		if osID != 0 {
			osTag = strings.Join(repo.OSNames(osID), ",")
		}
		// Chunk count
		chunks := 0
		threshold := int64(r.Config.Core.ChunkThreshold)
		if entry.Size >= threshold {
			if objType, err := r.ObjectType(entry.Hash); err == nil && objType == core.ObjectChunkManifest {
				if manifest, err := r.LoadChunkManifest(entry.Hash); err == nil {
					chunks = len(manifest.Chunks)
				}
			}
		}
		chunkInfo := ""
		if chunks > 0 {
			chunkInfo = fmt.Sprintf("  [%d chunks]", chunks)
		}
		fmt.Printf("%s  %s  [%s]  %s%s\n", entry.Hash.Short(), humanSize(entry.Size), osTag, path, chunkInfo)
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
				fmt.Printf("%s\n", b)
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
				fmt.Fprintf(os.Stderr, "%s (see %s.ours, %s.theirs, %s.base)\n", name, name, name, name)
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
				fmt.Fprintf(os.Stderr, "%s\n", name)
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
				fmt.Fprintf(os.Stderr, "pull %s: %v\n", f.Path, err)
			}
		}
		return nil
	}
	for _, file := range targets {
		if err := r.LfsPull(remote, file); err != nil {
			fmt.Fprintf(os.Stderr, "pull %s: %v\n", file, err)
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
	explicitOS := osName != ""
	if osName == "" {
		osName = repo.OSName(repo.CurrentOSID())
	}
	osID := repo.OSID(osName)
	if explicitOS && osID == 0 {
		return fmt.Errorf("unknown OS: %s", osName)
	}

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

	var handler http.Handler
	if *basePath != "" {
		srv := &repo.RepoServer{BasePath: *basePath}
		fmt.Printf("serving repositories from %s on %s\n", *basePath, *addr)
		handler = srv
	} else {
		r, err := findRepo()
		if err != nil {
			return err
		}
		fmt.Printf("serving %s on %s\n", r.Path, *addr)
		handler = http.HandlerFunc(r.ServeHTTP)
	}

	server := &http.Server{Addr: *addr, Handler: handler}

	// Graceful shutdown: on SIGINT/SIGTERM, stop accepting new connections
	// and let in-flight requests finish, bounded by a timeout so a stuck
	// request cannot block exit forever.
	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Printf("\nshutting down...\n")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
		}
		close(done)
	}()

	err := server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	<-done
	return nil
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

// ---- fsmonitor--daemon ----
//
// The change monitor is built into lo: a background process watches the
// working tree through the native API of whatever platform this is, and status
// reads the changes it records instead of stat-ing every tracked file. These
// verbs manage that process's lifecycle.
//
// noteFsmonitorFallback starts that monitor on demand, and says why it could
// not when it could not.
//
// Silence is the normal case: a monitor that is running, or one that was just
// started, is not news — the daemon is an implementation detail of a fast
// status, and announcing it on every invocation would be noise. The two cases
// worth a line are the ones where the user asked for something they are not
// getting: a platform that cannot host a monitor at all, and a start that
// failed for a reason that will not fix itself. Both go to stderr, so status's
// output stays parseable.
func noteFsmonitorFallback(r *repo.Repository) {
	err := r.EnsureFsmonitorDaemon()
	if err == nil {
		return
	}
	if err == repo.ErrNoMonitor {
		fmt.Fprintln(os.Stderr,
			"note: core.fsmonitor is on, but no change-monitor backend is available here; status scans every file")
		return
	}
	fmt.Fprintf(os.Stderr, "note: change monitor not started: %v\n", err)
}

func runFsmonitorDaemon(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}

	// `run` is the process the other three verbs start, so it takes its
	// repository explicitly: it is spawned detached, and a detached process
	// has no meaningful working directory to search upwards from.
	if sub == "run" {
		return runFsmonitorDaemonForeground(args)
	}

	r, err := findRepo()
	if err != nil {
		return err
	}
	switch sub {
	case "start":
		if !r.FsmonitorEnabled() {
			return fmt.Errorf("core.fsmonitor is off (enable with: lo config core.fsmonitor true)")
		}
		if err := r.FsmonitorDaemonStart(); err != nil {
			if err == repo.ErrDaemonBusy {
				fmt.Println("a change monitor is already starting; nothing to do")
				return nil
			}
			if err == repo.ErrNoMonitor {
				return fmt.Errorf("%v: status will keep doing a full scan here", err)
			}
			return err
		}
		fmt.Println("monitor started; status will skip paths it reports unchanged")
	case "stop":
		if err := r.FsmonitorDaemonStop(); err != nil {
			return err
		}
		fmt.Println("monitor stopped for this repository")
	case "status":
		return printFsmonitorStatus(r)
	case "":
		return fmt.Errorf("usage: lo fsmonitor--daemon start|run|stop|status")
	default:
		return fmt.Errorf("unknown fsmonitor--daemon subcommand: %s (use start, run, stop, status)", sub)
	}
	return nil
}

// runFsmonitorDaemonForeground is the daemon itself, in this process.
//
// It is normally invoked by `start` as a detached child, but running it by
// hand is the way to watch it work — and the only way to try the polling
// backend, which is never selected automatically.
func runFsmonitorDaemonForeground(args []string) error {
	path := "."
	var pollInterval time.Duration
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			if i+1 >= len(args) {
				return fmt.Errorf("--repo needs a path")
			}
			i++
			path = args[i]
		case "--poll-interval":
			if i+1 >= len(args) {
				return fmt.Errorf("--poll-interval needs a duration")
			}
			i++
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return fmt.Errorf("--poll-interval: %w", err)
			}
			pollInterval = d
		default:
			return fmt.Errorf("unknown option for fsmonitor--daemon run: %s", args[i])
		}
	}
	r, err := repo.Open(path)
	if err != nil {
		return err
	}

	// A daemon that has been asked to stop should stop, and one that is killed
	// outright is handled by its successor the ordinary way. Handling the
	// signal here is what makes Ctrl-C in a terminal work on the foreground
	// case; detached children are stopped through the stop file instead, since
	// a new session has no controlling terminal to deliver a signal from.
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		close(stop)
	}()

	logf := func(format string, a ...interface{}) {
		fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
	}
	return r.FsmonitorDaemonRun(pollInterval, stop, logf)
}

func printFsmonitorStatus(r *repo.Repository) error {
	st := r.FsmonitorDaemonStatus()

	if st.Enabled {
		fmt.Println("core.fsmonitor = true")
	} else {
		cfg := st.ConfigValue
		if cfg == "" {
			cfg = "unset"
		}
		fmt.Printf("core.fsmonitor = false (%s); status always does a full scan\n", cfg)
		return nil
	}

	if st.Available {
		fmt.Println("platform support: event-driven backend available")
	} else {
		// Worth saying plainly: on this platform no monitor can ever save
		// work, so a user waiting for the fast path is waiting for something
		// that will not arrive.
		fmt.Println("platform support: none; no event-driven backend on this platform, status always does a full scan")
	}

	switch {
	case st.Record == nil:
		fmt.Println("daemon: no record (nothing has started one here)")
	case st.Alive:
		fmt.Printf("daemon: running (pid %d, heartbeat %s ago)\n", st.Record.PID, st.BeatAge.Round(time.Millisecond))
	case st.HasBeat:
		// The case a user actually meets: killed, or crashed.
		fmt.Printf("daemon: not running (stale record for pid %d; last heartbeat %s ago)\n",
			st.Record.PID, st.BeatAge.Round(time.Second))
	default:
		fmt.Printf("daemon: not running (record for pid %d, no heartbeat)\n", st.Record.PID)
	}

	if st.Record != nil {
		fmt.Printf("backend: %s (event-driven: %v)\n", st.Record.Backend, st.Record.Events)
		fmt.Printf("generation: %016x\n", st.Record.Gen)
		fmt.Printf("watching: %s\n", st.Record.Watch)
		if st.JournalOK {
			fmt.Printf("journal: %s (%d bytes)\n", filepath.Base(journalName(st.Record.Gen)), st.JournalSize)
		} else {
			fmt.Println("journal: missing (the next status will do a full scan)")
		}
	}

	switch {
	case !st.CursorOK:
		fmt.Println("cursor: none recorded")
	case st.Record == nil || st.CursorGen != st.Record.Gen:
		fmt.Println("cursor: belongs to another generation")
	case st.JournalOK && st.CursorOffset > st.JournalSize:
		fmt.Println("cursor: past the end of the journal (corrupt)")
	default:
		fmt.Printf("cursor: generation %016x, offset %d\n", st.CursorGen, st.CursorOffset)
	}
	if st.CursorBackend != "" {
		fmt.Printf("cursor backend: %s\n", st.CursorBackend)
	}
	fmt.Printf("dirty: %d path(s) known to differ from the index\n", st.Dirty)

	if st.Failure != nil {
		fmt.Printf("last failure: %s (%s)\n",
			time.Unix(0, st.Failure.At).Format("2006-01-02 15:04:05"), st.Failure.Reason)
	}
	if st.Record != nil && !st.Alive {
		fmt.Printf("log: %s\n", r.DaemonLogPath())
	}
	return nil
}

// journalName mirrors the naming the repository uses for one generation's
// journal, so the report can name the file without reaching into it.
func journalName(gen uint64) string {
	return fmt.Sprintf("journal.%016x", gen)
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
				fmt.Fprintf(os.Stderr, "restore --staged %s: %v\n", f, err)
				continue
			}
			fmt.Printf("unstaged: %s\n", f)
		} else {
			if err := r.RestoreFile(f); err != nil {
				fmt.Fprintf(os.Stderr, "restore %s: %v\n", f, err)
				continue
			}
			fmt.Printf("restored: %s\n", f)
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

// ---- format-patch / am ----
func runFormatPatch(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}

	var base, head core.Hash
	if len(args) >= 3 && args[0] == "-N" {
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 {
			return fmt.Errorf("usage: lo format-patch -N <n> <head>")
		}
		head, err = r.ResolveRef(args[2])
		if err != nil {
			return fmt.Errorf("resolve ref: %w", err)
		}
		cur := head
		for i := 0; i < n && !cur.IsZero(); i++ {
			c, err := r.LoadCommit(cur)
			if err != nil {
				return fmt.Errorf("load commit: %w", err)
			}
			if len(c.Parents) == 0 {
				cur = core.Hash{}
				break
			}
			cur = c.Parents[0]
		}
		base = cur
	} else if len(args) == 1 {
		parts := strings.SplitN(args[0], "..", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("usage: lo format-patch <base>..<head> | -N <n> <head>")
		}
		base, err = r.ResolveRef(parts[0])
		if err != nil {
			return fmt.Errorf("resolve base: %w", err)
		}
		head, err = r.ResolveRef(parts[1])
		if err != nil {
			return fmt.Errorf("resolve head: %w", err)
		}
	} else {
		return fmt.Errorf("usage: lo format-patch <base>..<head> | -N <n> <head>")
	}

	files, err := r.FormatPatch(base, head)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("nothing to export")
	}
	for _, f := range files {
		if err := ioutil.WriteFile(f.Filename, []byte(f.Render()), 0644); err != nil {
			return fmt.Errorf("write %s: %w", f.Filename, err)
		}
		fmt.Printf("wrote %s\n", f.Filename)
	}
	return nil
}

func runAm(args []string) error {
	r, err := findRepo()
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return r.ApplyMailbox(args)
	}
	data, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	return r.ApplyPatchFile(data)
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
					fmt.Printf("%s: not cloned (use --init to clone)\n", path)
					continue
				}
				fmt.Printf("init %s...\n", path)
				if err := repo.AddSubmodule(r, def.URL, path); err != nil {
					fmt.Fprintf(os.Stderr, "init %s: %v\n", path, err)
					continue
				}
				fmt.Printf("%s: cloned\n", path)
			} else {
				fmt.Printf("update %s...\n", path)
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
			_, statErr := os.Stat(filepath.Join(subPath, ".qin"))
			if os.IsNotExist(statErr) {
				fmt.Printf("%s -> %s (not initialized)\n", path, def.URL)
			} else {
				fmt.Printf("%s -> %s\n", path, def.URL)
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
		fmt.Printf("cloning submodule %s...\n", path)
		if err := repo.AddSubmodule(r, def.URL, path); err != nil {
			fmt.Fprintf(os.Stderr, "clone submodule %s: %v\n", path, err)
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
		fmt.Printf("commit %s\n", c.Hash)
		fmt.Printf("Author: %s\n", c.Author)
		fmt.Printf("Date:   %s\n", c.Time.Format("Mon Jan 2 15:04:05 2006"))
		if c.Parents > 0 {
			fmt.Printf("Parents: %d\n", c.Parents)
		} else {
			fmt.Println("Parents: 0 (root commit)")
		}
		fmt.Printf("\n      %s\n\n", c.Message)
	}
	fmt.Println("---")
	fmt.Println("To recover: use lo checkout <hash> then lo branch <name>")
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
