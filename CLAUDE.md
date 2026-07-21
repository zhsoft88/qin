# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

- **Build**: `mkdir -p dist && go build -o dist/qin ./cmd/qin`
- **Install**: `go install github.com/zhsoft88/qin/cmd/qin@latest`
- **Test all**: `go test ./...`
- **Test single package**: `go test ./internal/repo/...`
- **Test single test**: `go test ./internal/repo/... -run TestStatusClean -v`
- **Vet**: `go vet ./...`
- **Run binary**: `./dist/qin <command>` (after build)

## Code Architecture

qin is a Git-inspired, content-addressed version control system in Go (1.15+). The main binary calls command handlers in `cmd/qin/main.go` that delegate to the `internal/repo` package.

### Package Layout

- **`cmd/qin/main.go`** — CLI entry point with flag parsing and ~40 command handlers (init, add, commit, status, log, diff, merge, rebase, push, pull, clone, lfs, serve, etc.). All commands are `lo`-prefixed (legacy naming) but the project is now named `qin`.

- **`internal/core/`** — Fundamental types used across the project:
  - `hash.go` — SHA256 `Hash` type (32-byte array), hex serialization, JSON marshalers
  - `object.go` — Object types (`blob=1`, `tree=2`, `commit=3`, `chunk_manifest=4`), gzip-compressed serialization format: `gzip(type_byte + varint(content_size) + content)`
  - `chunk.go` — Content-Defined Chunking (CDC) using Gear hash polynomial. Configurable min/avg/max chunk sizes. Defaults: 1MB/4MB/8MB

- **`internal/repo/`** — All core VCS logic on the `Repository` struct:
  - `repo.go` — `Repository` struct, `Init()`, `Open()` (walks up directories to find `.qin/`), `HEAD` management, ref read/write
  - `index.go` — Staging area stored at `.qin/index` as JSON. `IndexEntry` has Hash, ContentHash, Size, Mode, Lazy flag, and OSS (OS-variant list). Entries use composite keys: `"path\0<os_id>"` for OS-specific variants
  - `store.go` — Object storage/retrieval at `.qin/objects/XX/YYYYYY` (Git-style layout). `StoreObject` serializes + compresses + writes; `LoadObject` reads + decompresses + deserializes
  - `tree.go` / `commit.go` — Tree (ordered list of `TreeEntry`) and Commit (tree hash + parents + author + message + timestamp) storage
  - `branch.go` — `SwitchBranch`, `restoreCommit` (OS-aware file checkout), `ListBranches`, `CreateBranch`, `DeleteBranch`
  - `checkout.go` — `Checkout` (detached), `ResolveRef` (full hash, short prefix, branch, tag, HEAD)
  - `status.go` — `WorkTreeStatus` (compares index vs HEAD vs working tree, OS-filtered)
  - `diff.go` — `DiffCommits`, `DiffWorking`, `DiffIndex` with LCS-based line-level diff
  - `merge.go` — BFS-based `FindMergeBase`, `fastForwardMerge`, `threeWayMerge` with conflict detection
  - `rebase.go` — Collect commits from HEAD→base, replay on target
  - `cherrypick.go` — Apply single commit's diff on HEAD
  - `chunk.go` — `StoreChunkedFile` (CDC splits + chunk manifest), `LoadChunkedFile` (reassemble), `LoadFileContent` (auto-detect chunked vs plain)
  - `patch.go` — `RenderPatch` / `ApplyPatch` (base64-encoded inline format with `====` separators)
  - `stash.go` — Stash (save wip commit + reset to HEAD), StashPop, StashList
  - `reset.go` — `ResetCommit` with soft/mixed/hard modes
  - `restore.go` — `RestoreFile` (index→working tree), `RestoreStaged` (HEAD→index, unstage)
  - `remote.go` — Remote CRUD, LFS status/pull, object collection (DAG walk), `Push`, `Fetch`, `Pull`, `Clone` with lazy LFS support. Supports local path, HTTP, SSH transport
  - `http.go` — HTTP client (GET/PUT/HEAD objects, refs), server-side DAG walk for collect missing
  - `ssh.go` — SSH transport via `ssh` command subprocess (scp-style and ssh:// URLs). Object read/write, DAG walk
  - `serve.go` — HTTP server `ServeHTTP` routes: `GET /refs`, `GET|PUT /ref/<path>`, `GET|HEAD|PUT /objects/<hash>`. `RepoServer` wraps multi-repo serving
  - `config.go` — `.qin/config` JSON config with get/set/unset for chunk sizes, diff limits, user name/email
  - `osfilter.go` — Cross-platform OS variant system: 9 known OSes (win/mac/linux/freebsd/...). Entry keys encode `path\0<os_id>`. `visibleEntries` filters by current OS, OS-specific wins over default. `ParseOSExpr` supports comma-separated include/exclude syntax
  - `ignore.go` — `.qinignore` parser (gitignore-style patterns: `*`, `?`, `**`, `[...]`, negation, anchored `/`)
  - `submodule.go` — `.lomodules` JSON config, `Clone`-based submodule add/update/status
  - `gc.go` — Mark-and-sweep garbage collection (walk refs, mark reachable, prune rest)
  - `lostfound.go` — List unreachable commits
  - `graph.go` — Topological sort with ASCII graph rendering (`log --graph`)
  - `transport_test.go` — Integration test for local-path push/fetch
  - `termwidth.go` — Terminal width detection (platform-specific)

### Key Data Flows

1. **Add & Commit**: `add file` → `AddFileToIndex` (store blob/chunks + update index) → `commit` → `WriteTree` (build tree from index) → `WriteCommit` (store commit + update branch ref)
2. **Status**: `WorkTreeStatus` → load index → filter visible entries → compare against HEAD tree (staged) → walk working tree (modified/untracked) → check deleted
3. **Clone (lazy)**: `init` → `fetch` (skip chunk blobs via `lazy=true`) → create local branch → checkout. LFS files have `"lo-lfs"` placeholder content
4. **Push**: `collectObjects` (walk DAG, skip objects remote has) → `copyObject` (atomic write: temp + rename + integrity check)
5. **Merge**: `FindMergeBase` (BFS) → if fast-forward: restore target. Else: three-way compare per-file with conflict detection
6. **Cross-platform**: Index entries carry `OSS []uint8`. `visibleEntries` selects the best match for current OS. `checkout`/`restoreCommit` writes only winning OS variant to disk

### Binary Name History

The project was originally named "lo" — the binary displays `lo` in usage text and error messages. It has since been renamed to "qin." Legacy names (`lo`, `lo-lfs`, `.lo`) may appear in comments, error strings, and internal references.
