# gram: vendoring notes

`internal/gram` is sonut's persistent long-term memory system: store facts
or notes, recall them later by text/project query, and have them survive
across process restarts. It was ported (in spirit, not line-for-line) from
the `engram` project checked out at `./engram` in this worktree.

## What was vendored, and how

`engram`'s core memory engine lives in `engram/internal/store/store.go`
(~7,000 lines) and is built on SQLite + FTS5 via `modernc.org/sqlite`, plus
a `Session`/`Observation` data model (`engram/internal/store/store.go` top
of file) and 20 MCP tools in `engram/internal/mcp/mcp.go` for
save/search/context/timeline/etc.

Given the task constraint to keep new dependencies minimal (stdlib
preferred) and to port only the core storage/recall logic — not engram's
SQLite schema, migrations, FTS5 indexing, or MCP/HTTP/TUI/CLI surface — gram
**re-implements the same store/recall contract engram exposes**
(save a titled note with a body and tags, scoped to a project, recall it
later by text match) as a small, dependency-free Go package, rather than
copying engram's SQLite-backed implementation verbatim. The concepts carried
over from engram's data model:

- `Memory` (`internal/gram/gram.go`) mirrors engram's `Observation`: an
  ID, a project scope, a title, a body, tags, and created/updated
  timestamps.
- `Store` (`internal/gram/gram.go`) mirrors the save/search operations of
  engram's `store.Store` (`Save`/`mem_save`, `Recall`/`mem_search`,
  `Get`/`mem_get_observation`, `Delete`/`mem_delete`), narrowed to the
  subset sonut needs.
- `FileStore` (`internal/gram/filestore.go`) is the default local
  implementation: it persists all memories as a single JSON document under
  a root directory, with atomic writes (temp file + rename) so a crash
  mid-write can't corrupt the store. This replaces engram's SQLite+FTS5
  storage engine; text search is a simple case-insensitive substring match
  over title/body/tags rather than FTS5, which is sufficient for sonut's
  current use case (a handful to low thousands of notes per project) and
  requires zero external dependencies.

Root directory defaults to `~/.atcode/memory` (`gram.DefaultRoot`), resolved
via `os.UserHomeDir()` only as a fallback — the actual root is always a
constructor parameter (`gram.NewFileStore(rootDir string)`), so callers can
point it anywhere (e.g. a temp dir in tests).

## What was skipped, and why

Everything outside the core storage/recall logic was intentionally left out
of the port, per the task's scope:

- `engram/docker/`, `docker-compose.*.yml` — containerized cloud/server
  deployment, irrelevant to an in-process Go library.
- `engram/plugin/` — per-agent (OpenCode, Pi, VS Code, ...) plugin glue;
  sonut is its own agent host and doesn't need engram's plugin shims.
- `engram/.agents/`, `.claude-plugin/` (if present), `engram/openspec/` —
  engram's own agent/plugin marketplace metadata and spec-driven-development
  scaffolding, not portable artifacts.
- `engram/docs/` — engram's own documentation tree; this WIRING.md is
  gram's documentation instead.
- `engram/internal/cloud/` (auth, cloudserver, cloudstore, dashboard,
  autosync, remote sync) — engram's opt-in cloud replication feature. This
  is exactly the kind of "external service" dependency the task asked to be
  hidden behind an interface with a local default: gram's `Store` interface
  (`internal/gram/gram.go`) plays that role, and only the local `FileStore`
  implementation was written. A future cloud-backed `gram.Store`
  implementation can be added later without touching any caller.
  Currently, no cloud/network implementation exists in gram at all.
  - Along the same lines, `engram/internal/sync/`, `engram/internal/mcp/`
    (MCP server surface), `engram/internal/server/` (HTTP API),
    `engram/internal/tui/` (terminal UI), `engram/internal/obsidian/`
    (Obsidian export), `engram/internal/llm/` (LLM-judged conflict
    detection), `engram/internal/diagnostic/` (doctor/repair CLI), and
    `engram/cmd/engram/` (CLI entrypoint) were all skipped — they are
    engram's standalone-binary surface area, not memory storage/recall
    logic, and sonut doesn't run as a separate binary with its own MCP/HTTP
    server.
- engram's SQLite + FTS5 storage backend (`modernc.org/sqlite` dependency)
  itself was not vendored — see "What was vendored, and how" above for the
  replacement.
- `engram/internal/project/` (fuzzy project-name matching/dedup) and
  `engram/internal/cloud/syncguidance/` — auxiliary features layered on top
  of the core engine, not needed for a minimal store/recall port.

## Integration point in `internal/sessions`

`internal/sessions/gram_adapter.go` adds `MemoryAdapter`, a thin,
**entirely opt-in** wrapper around a `gram.Store`:

- `NewMemoryAdapter(store gram.Store) (*MemoryAdapter, error)` — wraps a
  caller-supplied store (typically `gram.NewFileStore("")` for the default
  `~/.atcode/memory` location, or a custom root/implementation).
- `(*MemoryAdapter) SaveNote(meta Metadata, title, body string, tags ...string)`
  — persists a note scoped to the session's project (derived from
  `Metadata.Cwd`).
- `(*MemoryAdapter) Recall(meta Metadata, text string, limit int)` —
  recalls notes scoped to the session's project.
- `(*MemoryAdapter) Close() error`.

**Non-breaking by construction**: nothing in `internal/sessions/store.go`
(or any other existing file in the package) was modified, and no existing
code path constructs a `MemoryAdapter` automatically. `Store`,
`CreateInput`, `Metadata`, and every other existing sessions API are
byte-for-byte unchanged. Long-term memory is off unless a caller (e.g.
future `cmd/` wiring) explicitly does:

```go
store, err := gram.NewFileStore("") // defaults to ~/.atcode/memory
adapter, err := sessions.NewMemoryAdapter(store)
adapter.SaveNote(meta, "title", "body", "tag1", "tag2")
adapter.Recall(meta, "query text", 10)
```

## New dependencies

None. `internal/gram` uses only the Go standard library
(`crypto/rand`, `encoding/hex`, `encoding/json`, `os`, `path/filepath`,
`sort`, `strings`, `sync`, `time`). `go.mod`/`go.sum` were not modified.

## Tests

`internal/gram/filestore_test.go` has a store → close → reopen → recall
round trip (`TestFileStore_RoundTripAcrossReopen`) verifying memories
persist across a fresh `FileStore` instance pointed at the same root
directory, plus project-scoping, update-in-place, and delete behavior.

`internal/sessions/gram_adapter_test.go` covers `MemoryAdapter`'s
project-scoped save/recall and its nil-safety (no panics when
unconfigured).
