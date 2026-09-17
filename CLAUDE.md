# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

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

## Code Architecture

qin is a Git-inspired, content-addressed version control system in Go 1.15+ (stdlib only — zero external dependencies). The single binary in `cmd/qin/main.go` has ~40 command handlers that delegate to methods on `repo.Repository`.

### Two-Package Design

- **`internal/core/`** — Pure types and serialization, no filesystem access:
  - `hash.go` — SHA256 `Hash` type (32 bytes), hex/JSON/text marshaling
  - `object.go` — Object type constants (`blob=1`, `tree=2`, `commit=3`, `chunk_manifest=4`), gzip-compressed serialization format: `gzip(type_byte + varint(content_size) + JSON_content)`
  - `chunk.go` — Gear hash-based Content-Defined Chunking (CDC). `Chunker` splits data at content-defined boundaries using a sliding hash against a configurable mask, so insertions/deletions only affect nearby chunks

- **`internal/repo/`** — All VCS logic, structured as methods on `*Repository`:
  - **`repo.go`** — `Repository` struct (holds Path + Config), `Init()` (creates `.qin/` layout), `Open()` (walks up directories to find `.qin/`), HEAD management
  - **`store.go`** — Object read/write at `.qin/objects/XX/YYYYYY` (Git-style 2-char subdir). `StoreObject` uses atomic temp+rename. `FindObjectByPrefix` resolves short hashes
  - **`index.go`** — Staging area at `.qin/index` (binary, "QINIDX" magic; legacy JSON migrated on load). `IndexEntry` has Hash, ContentHash, Size, Mode, Lazy, Mtime, OSS (uint8 bitmask: 1=win, 2=mac, 4=linux). Composite keys `"path\0<oss_mask>"` enable OS-specific variants
  - **`tree.go`** — `Tree` (ordered `TreeEntry` list) / `Commit` (tree hash + parents + author + message + time). `WriteTree` builds from index, `WriteCommit` creates commit + updates branch ref
  - **All other files** handle one operation each: status, diff, merge (BFS merge-base + 3-way), rebase, stash, checkout/switch/branch, reset/restore, patch, remote push/fetch/pull/clone, serve (HTTP server), GC, submodules, config, ignore, change monitor (`fsmonitor*.go` — see below)

### Cross-Platform OS Variant System

qin supports per-file OS variants. Each `IndexEntry` has an `OSS uint8` bitmask field (1=win, 2=mac, 4=linux; 0 = all OSes). Index keys are `"path\0<oss_mask>"` for OS-specific entries (the mask byte), bare path for defaults. `visibleEntries()` selects the winning variant per path: OS-specific match beats default. 3 known OSes: win, mac, linux. All checkout/status/diff operations filter through this.

### Large File Pipeline

Files added via `add` go through `StoreChunkedFile()`:
1. `core.NewChunker` splits data at content-defined boundaries (Gear hash)
2. Single-chunk files store as plain `ObjectBlob`; multi-chunk files store each chunk as a blob and produce an `ObjectChunkManifest`
3. `LoadFileContent()` reads back: if the hash is a manifest → reassemble chunks; if a plain blob → return directly
4. LFS lazy mode: clone with `--lazy` skips chunk blobs; files on disk get `"lo-lfs"` placeholder; `lfs pull` fetches real chunks

### Remote Transport

`remote.go` and transport files (`http.go`, `ssh.go`) implement three transports:
- **Local path** — direct filesystem access to another repo's `.qin/objects/`
- **HTTP** — client talks to `serve` HTTP server via GET/PUT/HEAD for objects and refs
- **SSH** — spawns `ssh` subprocess, runs `cat`/`cat >` commands on the remote for scp-style and `ssh://` URLs
- Push uses `collectObjects` (DAG walk) + `copyObject` (atomic write + integrity check); Fetch does the reverse
- `RepoServer` wraps multi-repo serving from a base directory

### Key Data Flows

1. **Add → Commit**: `AddFileToIndex` (read file → CDC chunk → store objects → index entry) → `WriteCommit` (build tree from index → store commit → update branch ref)
2. **Status**: `WorkTreeStatus` → load index → `visibleEntries` (OS filter) → compare HEAD tree (staged) → walk working tree (modified/untracked) → check deleted. With `core.fsmonitor` on, the change monitor's paths are consulted first so unchanged index entries skip their `lstat` — it only ever subtracts work, never a conclusion
3. **Clone**: `Init` → `Fetch` (DAG walk, skip chunk blobs for lazy) → create branch → checkout
4. **Merge**: `FindMergeBase` (BFS) → fast-forward or 3-way per-file compare with conflict detection
5. **Push**: `collectObjects` from HEAD (ancestors `HasObject`-check against remote) → `copyObject` each missing object
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
  config              — JSON config (chunk sizes, diff limits, user, core.fsmonitor)
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

### Legacy Naming

The project was originally named "lo" and now calls itself `qin` everywhere it
names itself: the usage banner, every `usage: ...` error, and `--version` all
read `core.Name`, which is the single definition of the name. Do not spell it
out at a call site — a hardcoded name is how the banner and the messages drifted
apart the first time.

The legacy name survives only where it is **data rather than display**: the
`"lo-lfs"` placeholder written into files by a lazy clone and compared against
on load is a format value, and renaming it would make existing clones stop
recognising their own placeholders. The config type is `Config`, not `LoConfig`.
