# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

- **Build**: `mkdir -p dist && go build -o dist/qin ./cmd/qin`
- **Install**: `go install github.com/zhsoft88/qin/cmd/qin@latest`
- **Test all**: `go test ./...`
- **Test single package**: `go test ./internal/repo/...`
- **Test single test**: `go test ./internal/repo/... -run TestStatusClean -v`
- **Integration test**: `go test ./internal/repo/... -run TestTransport -v`
- **Vet**: `go vet ./...`
- **Run binary (after build)**: `./dist/qin <command>`

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
  - **`index.go`** — Staging area at `.qin/index` (JSON). `IndexEntry` has Hash, ContentHash, Size, Mode, Lazy, OSS. Composite keys `"path\0<os_id>"` enable OS-specific variants
  - **`tree.go`** — `Tree` (ordered `TreeEntry` list) / `Commit` (tree hash + parents + author + message + time). `WriteTree` builds from index, `WriteCommit` creates commit + updates branch ref
  - **All other files** handle one operation each: status, diff, merge (BFS merge-base + 3-way), rebase, stash, checkout/switch/branch, reset/restore, patch, remote push/fetch/pull/clone, serve (HTTP server), GC, submodules, config, ignore

### Cross-Platform OS Variant System

qin supports per-file OS variants. Each `IndexEntry` has an `OSS []uint8` field listing which OSes it applies to (empty = all OSes). Index keys are `"path\0<os_id>"` for OS-specific entries, bare path for defaults. `visibleEntries()` selects the winning variant per path: OS-specific match beats default. 9 known OSes: win, mac, linux, freebsd, netbsd, openbsd, dragonfly, solaris, android. All checkout/status/diff operations filter through this.

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
2. **Status**: `WorkTreeStatus` → load index → `visibleEntries` (OS filter) → compare HEAD tree (staged) → walk working tree (modified/untracked) → check deleted
3. **Clone**: `Init` → `Fetch` (DAG walk, skip chunk blobs for lazy) → create branch → checkout
4. **Merge**: `FindMergeBase` (BFS) → fast-forward or 3-way per-file compare with conflict detection
5. **Push**: `collectObjects` from HEAD (ancestors `HasObject`-check against remote) → `copyObject` each missing object
6. **Stash**: Save current index as a commit with `refs/stash` → restore HEAD to working tree. StashPop reverses it
7. **GC**: `markReachableRefsFull` (walk all refs, recurse commits→trees→entries) → enumerate all objects → prune unreachable

### .qin Directory Layout

```
.qin/
  HEAD                — "ref: refs/heads/main" or a commit hash
  config              — JSON config (chunk sizes, diff limits, user)
  index               — JSON staging area
  objects/            — Git-style XX/YYYYYY hash layout
  refs/
    heads/            — branch refs (one file per branch)
    tags/             — tag refs
  remotes/            — remote URLs (one file per remote)
```

### Legacy Naming

The project was originally named "lo" — the binary displays `lo` in usage text and error messages. Legacy names (`lo`, `lo-lfs`, `.lo`) appear in comments, error strings, and internal references. The config type is `Config`, not `LoConfig`.
