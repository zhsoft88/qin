# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

`README.md` is the user-facing feature overview and command reference (in Chinese). It is the source of truth for the CLI surface; keep it in sync when commands, flags, or config keys change. This file covers what the README does not: the invariants behind the behavior.

## Commands

- **Build (Linux/Mac)**: `mkdir -p dist && go build -o dist/qin ./cmd/qin`
- **Build (Windows)**: `mkdir dist 2>nul & go build -o dist/qin.exe ./cmd/qin`
- **Install**: `go install github.com/zhsoft88/qin/cmd/qin@latest`
- **Test all**: `go test ./...`
- **Test single package**: `go test ./internal/repo/...`
- **Test single test**: `go test ./internal/repo/... -run TestStatusClean -v`
- **Integration test**: `go test ./internal/repo/... -run TestTransport -v`
- **Vet**: `go vet ./...`
- **Run binary (after build)**: `./dist/qin <command>`
- **Change monitor (dev)**: `./dist/qin config core.fsmonitor true`, then `./dist/qin fsmonitor--daemon start|run|stop|status` (`run` is the foreground daemon)
- **Platform backend tests**: each must run on its own OS — `go test ./internal/repo/ -run 'Inotify|Fsmonitor' -v` on Linux, `-run Windows` on Windows, `-run Fsevents` on macOS. Never cross-compile to check them.
- **Symlink tests on Windows**: `TestTreePathThroughSymlinkedParentCannotEscapeRepo` and `TestTreeEntryReplacingSymlinkCannotEscapeRepo` (tree_test.go) create real symlinks. Without Developer Mode or an elevated shell, Windows fails them with `A required privilege is not held by the client` — an environment limit, not a code failure. Enable Developer Mode to run the full suite.

## Code Architecture

qin is a Git-inspired, content-addressed version control system in Go 1.15+ (stdlib only — zero external dependencies). The single binary in `cmd/qin/main.go` has ~40 command handlers that delegate to methods on `repo.Repository`.

### Two-Package Design

- **`internal/core/`** — Pure types and serialization, no filesystem access:
  - `hash.go` — SHA256 `Hash` type (32 bytes), hex/JSON/text marshaling
  - `object.go` — Object type constants (`blob=1`, `tree=2`, `commit=3`, `chunk_manifest=4`), gzip-compressed serialization format: `gzip(type_byte + varint(content_size) + JSON_content)`
  - `chunk.go` — Gear hash-based Content-Defined Chunking (CDC). `Chunker` splits data at content-defined boundaries using a sliding hash against a configurable mask, so insertions/deletions only affect nearby chunks

- **`internal/repo/`** — All VCS logic, structured as methods on `*Repository`:
  - **`repo.go`** — `Repository` struct (holds Path + Config), `Init()` (creates `.qin/` layout), `Open()` (walks up directories to find `.qin/`), `openExactRoot()` (requires `<path>/.qin` exactly and never walks up — remote targets, not working directories), HEAD management
  - **`store.go`** — Object read/write at `.qin/objects/XX/YYYYYY` (Git-style 2-char subdir). `StoreObject` uses atomic temp+rename. `FindObjectByPrefix` resolves short hashes
  - **`index.go`** — Staging area at `.qin/index` (binary, "QINIDX" magic; legacy JSON migrated on load). `IndexEntry` has Hash, ContentHash, Size, Mode, Lazy, Mtime, OSS (uint8 bitmask: 1=win, 2=mac, 4=linux). Composite keys `"path\0<oss_mask>"` enable OS-specific variants
  - **`tree.go`** — `Tree` (ordered `TreeEntry` list) / `Commit` (tree hash + parents + author + message + time). `WriteTree` builds from index, `WriteCommit` creates commit + updates branch ref
  - **All other files** handle one operation each: status, diff, merge (BFS merge-base + 3-way), rebase, cherry-pick, stash, checkout/switch/branch, reset/restore, patch (`patch.go`, plus `formatpatch.go` for `format-patch`/`am`), remote push/fetch/pull/clone, serve (HTTP server), GC, submodules, config, ignore, untracked-directory cache (`untracked_cache.go`), `log --graph` (`graph.go`), dangling-commit discovery for `lost-found` (`lostfound.go`), the tree-name rule (`pathname.go`), the binary index codec (`index_binary.go`), and the change monitor (`fsmonitor*.go` — see below)

### Cross-Platform OS Variant System

qin supports per-file OS variants. Each `IndexEntry` has an `OSS uint8` bitmask field (1=win, 2=mac, 4=linux; 0 = all OSes). Index keys are `"path\0<oss_mask>"` for OS-specific entries (the mask byte), bare path for defaults. `visibleEntries()` selects the winning variant per path: OS-specific match beats default. 3 known OSes: win, mac, linux. All checkout/status/diff operations filter through this.

**An OS tag splits the default, and a plain add follows the visible variant.** `add --os X` replaces the path's default entry (`splitDefaultVariant`). The mask-0 entry is removed because it matches *every* OS: left in place it would shadow the tagged variant on this host and apply the default's content to every other platform. Its content is not discarded — each known OS the tag does not cover receives an explicit variant holding the default's blob, so no platform loses what it saw, and every known OS now has its own entry that can be changed independently. A variant that already exists is never overwritten. `--os-match '*'` still stages a default, and a plain `add` stages under the mask this host already sees (`visibleMask` → `AddFileToIndexVisible`), never a fresh mask 0: beside an OS-specific entry a default is shadowed on this host, so the add would appear to do nothing and `status` would keep reporting the file modified. Both walks in `cmd/qin/main.go` use the visible form, and the `[linux]`/`[*]` progress suffix is read from the same `VisibleOSMask`.

The split is unconditional: identical content is copied too, so a path whose default merely duplicated its linux variant regains explicit win/mac entries the next time it is tagged. It also does not judge whether another platform can create the name — a Windows-illegal Linux name gets a `win` variant, and the Windows checkout reports it through `hostPathError`.

### Symlinks

qin follows git's model: **a symlink is tracked as the link itself, never as its target.** `add` stores the target string as the blob and a mode carrying `os.ModeSymlink`; checkout recreates the link, or — with `core.symlinks` off — writes the target text as an ordinary file while the recorded type stays a link. This holds whatever the link points at — a file, a directory, or nothing at all.

**Every walk that decides directory-vs-file uses `Lstat`.** `addFileOrDir` and `addFileOrDirExpr` in `cmd/qin/main.go` are the two that matter, and `status`'s untracked scan gets it for free (its dirents come from `Readdir`, which is lstat semantics). This is not a detail to be tidied away: `Stat` resolves a link to a directory, so the walk takes the directory branch, `ReadDir`s the target, and records its contents under the *link's* path — one tree stored twice under two names, with the link itself never recorded. `status` then reports the link untracked forever, because the index has no entry at that exact path and nothing descends through a link to look for one. That is exactly the shape `cipd` writes (`.cipd/pkgs/<n>/_current` → a sibling directory), which is where the defect surfaced.

**Consequences that other code has to keep.** The index can never hold both `X` and `X/child` — `checkParentsNotSymlinks` exists to reject the second, since a hostile tree could otherwise write through a link to somewhere outside the repository. And `status` must compare a tracked link by *reading the link* (`os.Readlink`), not by reading through it: `entry.ContentHash` is the hash of the target string, so `ioutil.ReadFile` would hash whatever the link points at, and every tracked link to a file would be reported `modified` on every run. Links are also deliberately excluded from the stat fast path — a link's own `lstat` says nothing about its target, which can change without touching it.

**`core.symlinks` and degraded checkout.** An ordinary Windows user cannot create a link, so qin follows git: `core.symlinks` defaults to false on Windows and true elsewhere. It is a `*bool` because the three states are distinct — nil means "platform default", and a repository's config must not freeze the machine that wrote it; explicit false has to survive `omitempty`-style Save/Load, which a plain bool cannot do. `symlinksDefault` asks `hostGOOS()`, the same testable platform hook `hostPathError` uses. When disabled, `writeFileFromEntry` writes the target text as a regular file (0666, subject to the umask) instead of calling `os.Symlink`; the tree and index entry keep `SymlinkMode`.

Everything downstream compensates for a type the filesystem no longer shows:

- `status` branches on the on-disk `Lstat` type: a real link is read with `Readlink`; the degraded regular file is hashed by its bytes, which are the recorded target; anything else at a link path is a type change and is reported `modified`, not skipped.
- `add` takes a type hint from the visible index entry and keeps `SymlinkMode` when the disk holds a regular file and symlinks are disabled. The hint is read **before** `splitDefaultVariant`, because an explicit `--os` add may inherit the type from the default entry the split removes. With symlinks enabled the hint is deliberately not used: a regular file there is a real type change.

`os.Symlink` converts `/` to `\` in link content on Windows, so `linkTargetFromDisk` normalizes `Readlink` output back to `/`; without it the same link would hash differently after every checkout and be reported modified forever. Only `Readlink` output is normalized — a degraded file's bytes are the stored target verbatim, and a backslash is an ordinary byte in a Unix filename.

Deliberately not implemented, and why: git for Windows's phantom directory-symlink retry (a link whose directory target arrives later); reading a real link's target text in `diff`; the stat fast path for link entries; and patch/format-patch mode representation. These are separate behaviors, not gaps this feature is expected to close.

### Tree Entry Names

A `TreeEntry.Name` is a repo-relative slash path, and **`safeRepoPath` is the one predicate** saying whether a name may be one. It is applied at both ends: `AddFileToIndex` checks it before a file is written to the index, and `LoadTree` checks it before a tree is read back. It refuses only names that could escape the repository root: empty, absolute, a leading `/`, a NUL byte (`entryKey`/`parseKey` use one as the name↔OS-mask separator, so a name holding one would address a different entry), or a `path.Clean` result under `../` (so `a/../../x` is caught while `a..b` is untouched).

**The filesystem is the source of truth.** Anything it holds is a legal filename and must be storable, so the rule no longer tries to be the union of every platform's filename rules. A backslash is an ordinary character in a Linux filename — systemd escapes unit names with it, so a chromium sysroot really does contain `system-systemd\x2dcryptsetup.slice` — and a name like `C:foo` is likewise ordinary there; both are stored, read back, and restored byte-for-byte.

Cross-platform intent uses the **OS variant mask** instead of a name ban. `qin add --os src/build/linux` tags content that only Linux can hold with `IndexEntry.OSS = OSLinux` and splits any default variant for the same path into explicit variants for the other OSes, and the variant filter (`visibleEntries`, `osMatch` in `restoreCommit`) selects the tagged variant on Linux and the explicit ones elsewhere — so each platform checks out the content staged for it. This is the intended mechanism, not an afterthought: a directory that belongs to one platform is tagged at `add` time. A *fresh* path tagged `--os X` gets only the `X` variant, so it does not materialize elsewhere. If a tagged path does materialize on another platform, check whether it carried a default entry when it was tagged: `splitDefaultVariant` copies that content to the other OSes by design, because a default was the content they saw.

**Materialization is judged at the write chokepoint, not at `add`.** `writeWorkTreeFile`, `makeWorkTreeDir` and `removeWorkTreeFile` all call `hostPathError(name, goos)` (`pathname.go`, a pure function parameterized by `goos` so the Windows branch is testable everywhere). Only Windows needs it; Linux and macOS accept every byte `safeRepoPath` lets through. The two outcomes are deliberately different:

- A name that escapes the repository *under Windows' syntax* (`..\..\x`, where the backslash is a separator) is `errOutsideWorkTree` and fails the whole operation — it is remote input that must not be written at all.
- A name Windows merely cannot create — a literal backslash, a drive-relative spelling, reserved characters `< > : " | ? *`, control characters, a trailing dot or space, a reserved device name such as `CON` — is `errUnrepresentable`. It is skipped, still recorded in the index, and reported.

`restoreCommit` collects every skipped name, finishes the checkout, saves the index, and then returns one error listing **all** of them: byte-sorted, one per line under a `could not restore these paths on <goos>:` header. The list is never truncated — no `... and N more` — and a name containing a control byte is Go-quoted, because a Unix filename may legally contain a newline that would otherwise split the report. Names are reported without per-file reasons. The CLI prints the error and exits non-zero (clone's path is `clone: checkout branch: ...`). Staging what can be restored and claiming success is the same shape as a push that moves nothing and exits 0. The other writers (`reset`, `merge`, `rebase`, `stash`) let the error propagate and fail the operation; on Linux/macOS `errUnrepresentable` cannot arise, and genuinely platform-specific content should have carried an `--os` mask and never been selected on the other platform.

`AddFileToIndex` still consults `.qinignore` **before** the name rule, though the reason is now simply that an ignored path should not be considered at all: `qin add .` must not fail over a file the user has already declared out of scope. The ignore refusal is `ErrIgnored`, an error so that `add <path>` names it but one the walks subtract with `errors.Is` — a file a walk passes over because it is ignored has not failed, and counting it as one would turn every repository holding a `.qinignore` into a failing `qin add .`.

### add File Lists (@file)

`runAdd` expands every positional argument and every `--exclude` value that starts with `@`
through one helper, `expandAtFiles`, **before** any walk. `@file` names a file whose
non-blank, non-`#` lines replace the item. A missing or unreadable list is a hard error —
there is no literal-path fallback; stage a file actually named `@name` as `./@name`. Only
command-line items are expanded: a line starting with `@` inside a list is a literal name,
not another list.

**Both the list path and its entries are CWD-relative**, like every other path argument.
The list file may live anywhere, including outside the repository, but a positional entry
must resolve inside it. No join onto `r.Path` and no second containment check happens in
the expander, so `addFileToIndex`'s
`filepath.Abs → filepath.Rel → isOutsideRepo` remains the one place that refuses an escape.
An absolute list path must be recognized with `filepath.IsAbs` and read as-is; resolving it
with `filepath.Join(baseDir, name)` would be wrong, because Go's `Join` keeps the base
(`Join("/repo", "/tmp/x")` is `/repo/tmp/x`).

**List entries are shell-expanded; command-line arguments and `--exclude` lines are not.**
`expandAtFiles` takes `expandEntries`, true only for positional arguments: a command-line
argument has already been through the shell, and an exclude line is a pattern for the
matcher, so expanding it would replace the pattern with what it currently matches. Each
positional list line goes through `cmd/qin/glob.go`'s `expandListEntry`; a line with no
wildcard and no escape is returned untouched, so ordinary paths keep their exact spelling.

The expansion is shell pathname expansion, anchored at the start of the path: `*`, `?` and
`[]` stay inside one segment, a whole `**` segment matches zero or more segments, matching
is case-sensitive, and a wildcard never matches a leading dot (`**` does not consume hidden
segments either). `internal/repo/pathglob.go`'s `MatchPathGlob` is the matcher. It reuses
`globMatch` per segment but is **not** `MatchGlob`: that one is gitignore-style,
non-anchored, also tries the basename, and lets wildcards match dotfiles. The two answer
different questions and must not be interchanged. `globMatch` has no `[!...]`/`[^...]`
negation, so `[!a]` is a class containing `!` and `a` rather than a negation.

An unmatched pattern is kept literally (bash's default), minus escape backslashes, so the
add walker reports no-such-file and `runAdd` exits non-zero. It is never silently dropped:
staging part of what was asked and claiming success is the failure shape this repository
keeps fixing.

The walk starts at the pattern's literal segment prefix. It uses `filepath.Walk`, hence
`Lstat` and no symlink traversal, matching the add walkers' invariant; it prunes the
repository's root `.qin` through `IsRepoMetadataPath` (a nested `.qin` is ordinary
content), and, when the pattern has no `**`, stops descending once a directory has reached
the pattern's depth. Walk errors are ignored: an unreadable directory is no match, not a
new failure. On Windows `\` normalizes to `/` before matching, so wildcards cannot be
escaped there; on Unix `\/` is the separator it spells. Expansion results are deduplicated
by resolved path across the lists, then enter the same walkers, `.qinignore` handling and
containment check as literal arguments.

This deliberately changes `--exclude @file`: it used to resolve against the repository root
and now follows CWD. Running at the root is unchanged; running from a subdirectory is not.

### Large File Pipeline

Files added via `add` go through `StoreChunkedFile()`:
1. `core.NewChunker` splits data at content-defined boundaries (Gear hash)
2. Single-chunk files store as plain `ObjectBlob`; multi-chunk files store each chunk as a blob and produce an `ObjectChunkManifest`
3. `LoadFileContent()` reads back: if the hash is a manifest → reassemble chunks; if a plain blob → return directly
4. LFS lazy mode: clone with `--lazy` skips chunk blobs; files on disk get `"qin-lfs"` placeholder; `lfs pull` fetches real chunks

### Remote Transport

`remote.go` and transport files (`http.go`, `ssh.go`) implement three transports:
- **Local path** — direct filesystem access to another repo's `.qin/objects/`
- **HTTP** — client talks to `serve` HTTP server via GET/PUT/HEAD for objects and refs
- **SSH** — spawns `ssh` subprocess, runs `cat`/`cat >` commands on the remote for scp-style and `ssh://` URLs
- Push uses `collectObjects` (DAG walk) + `copyObject` (atomic write + integrity check); Fetch does the reverse
- `RepoServer` wraps multi-repo serving from a base directory

**A local remote names one repository, at exactly that path.** `SaveRemote` is the only door a remote URL enters by, and it canonicalises a local path to absolute while the process is still in the directory the user typed it in — otherwise the same spelling would resolve to a different repository depending on where a later command happens to run. Network URLs are stored byte-identical. Values written before this rule (relative paths) are resolved at use against the repository root (`localRemotePath`), never the process CWD. Every local call site then opens the target with `openExactRoot`, which requires `<path>/.qin` and never searches parents: `Open`'s upward walk answers "which repository contains this working directory", but a remote target is not a working directory. A stale relative path used to be walked up into the pusher's own checkout, which was then reported as a non-bare target with main checked out — the intended path was never opened at all. `isHTTPURL` / `isSSHURL` / `isRemoteTransport` is the one transport rule (a Windows drive path has `:` but no `@`, so it stays local); do not reintroduce copies of the `@`/`:` test at call sites.

**An object a walk cannot read is an error, never a skip.** `collectObjects`, `httpCollectMissingWithBoundary` and `sshCollectMissing` — and their tree walkers — all return on an unreadable commit, tree or entry, rather than `continue`. Skipping is what made a push move the ref while the objects it named never travelled: `warning: tree … missing, skipping`, then `pushed`, exit 0, and a remote whose `HEAD` pointed at a commit whose tree it did not have. Collection finishes before any object is copied and before any ref is written, so a collection error leaves zero partial state — the push fails and nothing has moved. The one deliberate skip is the `lazy` chunk skip, which is a mode decision signalled by a flag, not an error being swallowed.

**Ref listings are keyed by full ref name**, whichever transport produced them: `refs/heads/main`, never `heads/main` or bare `main`. `branchNamesFromRefs` is the one extractor and every listing funnels through it, because a mismatch here fails silently in the worst way — the SSH listing once keyed its map relative to `.qin/refs`, so both callers' `refs/heads/` filter matched nothing, and SSH fetch took no objects while SSH clone produced an empty repository, every command exiting 0. `sshRefKey` is the SSH producer's half of that contract, and `ssh_test.go` pins it without needing an SSH server.

**Push preflight.** Every push decides whether it may write each ref *before* any object moves: gather the target's state, sort the branch names, run `checkPushRef` on each, and abort the whole push on the first refusal — so a refused push leaves zero partial state. The sort is not cosmetic: aborting on the first refusal would otherwise make map iteration order user-visible.

Fast-forward is checkable **without any remote objects**: it holds iff the target's current value is an ancestor of the local tip, and every ancestor of the local tip is in the local store — so a target tip that is absent locally is decisively *not* an ancestor, not an unknown. That is what lets all three transports run the same check on the pushing side, including SSH, which has no receiving-side code at all. `IsAncestor` deliberately returns no error: every way it can fail means "not an ancestor", and a caller able to tell a load error from a real answer would be able to let the error read as a pass.

`serveRefPut` re-checks server-side (it has the target repo, and clients PUT objects before refs, so the ancestry walk has everything it needs). `?force=1` is the server's only force signal; a refusal is **409**, a real write failure stays 500. Its `validRefName` also closes an arbitrary-file-write hole: the ref from the URL path is joined onto `.qin`, so `PUT /ref/refs/../../evil` used to escape the repository.

**Push target vs checkout.** Writing the branch a target has checked out desynchronises its index and worktree from HEAD, and the damage outlives the push: the next routine `commit` there builds a tree that does not mention what was pushed. So the checked-out branch is refused too, and **`--force` does not override it** — being checked out is a property of the target, not of the update, so the pusher has no signal that could mean "I consent". This is qin's `receive.denyCurrentBranch`.

The discriminator is `core.bare`. A bare repository is a push target and protects nothing; a non-bare one is a checkout and protects exactly one ref, the one HEAD points at (detached HEAD protects nothing, and a target on some other branch is an ordinary target for every branch it is not on). `qin init` produces a checkout and `qin init --bare` produces a target; `core.bare` is also a config key so that an already-created repository can be converted in place — `Init` refuses a directory that already has a `.qin`, so that key is the only remedy. The field is `omitempty`, so a non-bare config is byte-identical to what it was before the field existed.

`--bare` deliberately does nothing else: no layout change, no `clone --bare`, no refusing `add`/`commit` in a bare repo. A bare repository is layout-identical to a checkout, which is what keeps `serve`, `clone` and pushing out of one working unchanged; gating ten CLI entry points to improve an error message is not worth it. The honest consequence: `qin config core.bare true` inside a live working repository turns off its own protection, the same escape hatch git's `core.bare` has — the config key's description says "no working tree" for that reason.

The rule is checked **before** the fast-forward rule, so a push that trips both reports the one the user has to act on. Per transport: the local path and the HTTP server read `Config.Core.Bare` + `CurrentBranch()` off the target they already have open; an HTTP client reads the `core.bare` and `HEAD` keys `serveRefs` publishes (an old server that omits `core.bare` reads as non-bare, which refuses conservatively — the direction that fails safe); SSH reads `.qin/config` and `.qin/HEAD` and parses them with `bareFromConfigJSON` / `headBranchFromHEAD`. **SSH targets are protected by the pushing client alone** — the receiving side there is a shell `mkdir -p && cat >` with no qin in it, so there is nowhere else to stand.

### Key Data Flows

1. **Add → Commit**: `AddFileToIndex` (read file → CDC chunk → store objects → index entry) → `WriteCommit` (build tree from index → store commit → update branch ref)
2. **Status**: `WorkTreeStatus` → load index → `visibleEntries` (OS filter) → compare HEAD tree (staged, and `StagedDeleted`) → walk working tree (modified/untracked) → check deleted. With `core.fsmonitor` on, the change monitor's paths are consulted first so unchanged index entries skip their `lstat` — it only ever subtracts work, never a conclusion

   `StagedDeleted` exists because every other list is index-derived: `Staged` is built from index keys, and `Modified`/`Deleted` iterate `allVisible`, so a path HEAD has and the index does not (`qin rm`, `rm --cached`, `reset --soft` onto a tree that has it) was invisible to all of them — `status` called the tree clean while `diff --cached` reported the very same deletion. Membership is judged **by name**, not by composite key `entryKey(name, oss)`: HEAD holding the default variant and the index holding only an OS-specific one is still the same tracked path. It is also deliberately **not** passed to `mon.save` — a deviation is a filesystem fact re-checked until `add` reconciles it, whereas a staged deletion is a HEAD-vs-index fact no event clears, and the path is not in the index for `mustCheck` to gate anyway.
3. **Clone**: `Init` → `SaveRemote("origin", url)` → `Fetch` (DAG walk, skip chunk blobs for lazy) → create branch → checkout. The remote is written before the first network call, so a clone records where it came from even when the fetch later fails. `Clone` then reads the stored URL back and uses that value for its own `HEAD`/branch lookups: a relative argument was resolved against the invocation directory by `SaveRemote`, so the raw argument is no longer the target's address.
4. **Merge**: `FindMergeBase` (BFS) → fast-forward or 3-way per-file compare with conflict detection
5. **Push**: preflight (`checkPushRef` for every branch, sorted, aborting before anything is transferred — checked-out first, then fast-forward) → `collectObjects` from HEAD (ancestors `HasObject`-check against remote) → `copyObject` each missing object → write the refs in the same order
6. **Stash**: Save current index as a commit with `refs/stash` → restore HEAD to working tree. StashPop reverses it
7. **GC**: `markReachableRefsFull` (walk all refs, recurse commits→trees→entries) → enumerate all objects → prune unreachable

### Change Monitor (core.fsmonitor)

With `core.fsmonitor = true`, `status` can skip the `lstat` for paths a background daemon proves unchanged. The monitor is built into qin — the platform's own event API, no external program to install — and the CLI starts the daemon on demand from `status`.

The invariant every other decision serves: **the monitor may only remove work, never change a conclusion.** Any error, absence or ambiguity degrades to a full scan. A path missing from the change set is a path `status` skips without stat-ing it, so partial coverage would not be slow — it would be wrong.

- **Delta vs state.** The monitor reports deltas ("this changed since cursor C"); `status` must report state ("this is still modified"). The sidecar therefore also carries a `dirty` set of paths known to differ from the index, re-checked on every run until `add` reconciles them.
- **Generations and cursors.** A cursor is a `(gen, offset)` position in `.qin/fsmonitor/journal.<gen>`. Generations are minted monotonically and never reused, so a cursor left by a dead daemon cannot be mistaken for a current one — it simply fails to match and the run scans everything. A daemon writes READY only once every watch is registered, and a cursor below READY is never trusted: that is what makes the window between "the daemon published itself" and "the kernel is delivering events" harmless instead of merely short.
- **The SYNC handshake.** An append-only journal has no notion of "now", so a client writes a nonce into the control directory and polls until the daemon echoes it back. The echo is the synchronous proof that everything written before it has been flushed; the position stored is that echo's end, and nothing earlier. Removing the handshake would make the feature subtly wrong under load, so it is not an optimisation that can be dropped.
- **`status.go` is deliberately unchanged.** Its only contact with the monitor is `fsmonitorBackend` and `mon.save(...)`; a monitor with nothing to say returns a nil change set, which is byte-for-byte what no monitor does. Do not "fix" it.
- **Platform files are thin by design.** They cannot be tested anywhere but their own OS, so everything testable lives in files with no build tag — the kernel-buffer decoding in `fsmonitor_parse.go`, the FSEvents decision table in `fsmonitor_fsevents.go` — and a tagged file holds only the syscall plumbing (allocating a handle or a stream, translating events into `watchEvent`). Anything you would want to test belongs on the untagged side of that line. The macOS watcher's C half is `fsmonitor_darwin.h`, a header rather than a `.c` file because cgo copies a `//export` file's preamble into more than one translation unit.
- **Build tags are written twice** — `//go:build` and `// +build`. The legacy form is what the `go` directive in `go.mod` names and the modern one is what today's toolchain reads; a file carrying only one of them is silently ignored by half of them. `gofmt -w` adds the modern line.
- **Windows and macOS backends are verified on their own machines, never by cross-compiling.** A `GOOS=darwin` build proves nothing about a backend that has never run; each has its own test file and counts as done only when that passes there.

### .qin Directory Layout

```
.qin/
  HEAD                — "ref: refs/heads/main" or a commit hash
  config              — JSON config: core.chunk_min_size, core.chunk_threshold,
                        core.chunk_max_size, core.fsmonitor, core.bare,
                        core.symlinks, diff.max_size, diff.max_lines,
                        user.name, user.email
                        (the key list and defaults live in `configKeys`, config.go)
  index               — binary staging area (QINIDX v1; JSON auto-migrated)
  untracked-cache.json— per-directory untracked lists for fast status (git core.untrackedCache style)
  objects/            — Git-style XX/YYYYYY hash layout
  refs/
    heads/            — branch refs (one file per branch)
    tags/             — tag refs
  remotes/            — remote URLs (one file per remote)
  fsmonitor/          — change-monitor state, only while core.fsmonitor is on
    state.json        — the client's cursor: generation, offset, dirty set (v2)
    journal.<gen>     — one append-only change log per coverage epoch
    daemon.json       — the running daemon: pid, generation, backend, events, watch
    heartbeat         — touched every 2s; its mtime is the liveness signal
    ctl/              — sync request files, written by clients, answered by the daemon
    stop              — written by `fsmonitor--daemon stop`
    spawn.lock        — O_EXCL lock so concurrent status runs spawn one daemon
    last-failure      — why a daemon could not start; backs off respawning
    daemon.log        — the detached daemon's stdout and stderr
```

**The metadata directory is never content.** `IsRepoMetadataPath` is the one predicate for that — a repo-relative path in slash form equal to `.qin` or beneath it — and every rule about it goes through that function, because the three callers each learned it separately once already. `AddFileToIndex` refuses such a path, so no caller can put qin's storage into the index being written. The `add` walks test it before descending **and before the leaf is reached**, which is the half that matters for more than tidiness: `qin add .` used to walk into `.qin/objects` and index what it found there — including the objects it had stored seconds earlier for the files it was reading. `status`'s untracked scan skips the same path at the root (where an entry's name is its whole repo-relative path), and `validChangePath` refuses a journal record that names it, so a "skip the stat" hint can never point at the monitor's own state.

Only the root `.qin` counts. A nested directory that happens to be called `.qin` is ordinary content, and so is a submodule's own metadata — reached through its configured path, never by name here. `.qinignore` is deliberately **not** in this class: it is a file the user may track, like `.gitignore`, and both `add` and `status` treat it as one — `status` used to hide it from the untracked list by name, which made it clean in status and present in the committed tree at the same time.

### Naming

Everything the project names after itself is `qin`: the usage banner, every
`usage: ...` error, and `--version` all read `core.Name`, which is the single
definition of the name. Do not spell it out at a call site — a hardcoded name is
how the banner and the messages drifted apart the first time.

The names that outlive a process follow the same rule: the metadata directory is
`.qin` (the `repo.QinDir` constant), the submodule manifest is `.qinmodules`, and
the lazy-clone placeholder is `"qin-lfs"`. Changing any of those is a data-format
change, not a display change: existing repositories and clones hold the old
spelling.
