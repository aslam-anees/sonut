# internal/know — vendoring notes

`internal/know` is a scoped, in-process port of the core document/chunking
domain from [WeKnora](https://github.com/Tencent/WeKnora) (Tencent's RAG
product, checked out locally at `./WeKnora`, its own `go.mod`, excluded from
the sonut build). It gives sonut a local knowledge base — ingest text →
chunk → keyword-retrieve — with **no external services** (no postgres, no
docker, no vector DB, no embedding server).

## What was vendored

`internal/know/chunker/` — WeKnora's adaptive text chunker, copied verbatim
from `WeKnora/internal/infrastructure/chunker/`:

- `splitter.go` — legacy recursive splitter (`SplitText`) + image-ref
  extraction; the fallback tier and always-correct baseline.
- `heuristic_splitter.go`, `heading_splitter` logic in `splitter.go`,
  `heading_hierarchy.go`, `header_tracker.go` — heading-aware and
  heuristic-boundary splitting tiers.
- `patterns.go`, `tokens.go` — shared regex/token-counting helpers.
- `strategy.go` — `Split`/`SplitWithDiagnostics`/`SplitParentChild`, the
  tier-selection chain (auto-picks heading → heuristic → recursive →
  legacy based on document shape, falls back down the chain on
  validation failure).
- `validator.go` — post-split sanity checks (coverage, size bounds) used by
  the tier chain.
- `profiler.go` — lightweight document profiling (heading density, avg
  paragraph length, ...) that drives auto strategy selection.

This is ~2,260 lines, unmodified except for two things:

1. **Import path**: `github.com/Tencent/WeKnora/internal/infrastructure/chunker`
   → `github.com/aistatine/sonut/internal/know/chunker`.
2. **Two removed dependencies**, both inlined as small local
   replacements so the package is dependency-free:
   - `docparser.UnwrapLinkedImages` (one regex function) → inlined as
     `unwrapLinkedImages` in `splitter.go`, byte-for-byte the same regex.
   - `internal/logger.Debugf` (WeKnora's tracing-aware logger) → replaced
     with a trivial `debugf` in `strategy.go` that logs via `log.Printf`
     when `ATCODE_KNOW_DEBUG` is set, else is a no-op.

On top of the vendored chunker, `internal/know/` adds new (not ported)
sonut-specific glue, since WeKnora's own document/chunk/retrieval types are
gorm/postgres-coupled and not portable in-process:

- `types.go` — `Document`, `Chunk`, `Result`: minimal in-process domain
  types (no ORM tags, no DB fields).
- `retriever.go` — `Retriever` interface + `BM25Retriever`, a pure-Go,
  in-memory Okapi BM25 keyword index (k1=1.2, b=0.75). This is sonut's
  answer to "WeKnora's retrievers all need postgres/opensearch/qdrant/etc":
  none of those were vendored (see below); BM25Retriever is a from-scratch,
  dependency-free default that implements the same shape of interface.
- `store.go` — `Store` interface + `MemoryStore`, an in-memory document
  store (metadata + full content, so a retrieval hit can be traced back to
  its source document).
- `service.go` — `Service`: ties `Store` + `Retriever` + the chunker
  together into `Ingest(doc)` / `Retrieve(query, topK)` / `Delete(id)` /
  `Documents()`. `NewService()` with no options is fully in-process
  (`MemoryStore` + `BM25Retriever`); `WithStore`/`WithRetriever` let a
  caller swap in durable or embedding-based implementations later without
  touching this package.

## What was explicitly skipped

Per the task's scope (only what can run in-process without postgres/docker):

- `frontend/`, `miniprogram/`, `docs/` — web/mobile UI, no Go code relevant.
- `docker/`, `helm/`, `deploy/`, `migrations/` — deployment/DB-schema
  tooling, not applicable to an in-process library.
- `mcp-server/`, `cli/` — separate binaries/services on top of the WeKnora
  HTTP API; sonut has its own MCP/agent surface.
- `docreader/` — a separate gRPC document-extraction service (PDF/OCR/video
  parsing via Python + external models). Not vendored; `internal/know`
  only handles plain-text/markdown input (the caller is responsible for
  extracting text from other formats before calling `Ingest`).
- `internal/application/repository/retriever/{opensearch,qdrant,milvus,
  weaviate,elasticsearch,doris,tencentvectordb,postgres,sqlite,neo4j}` —
  every one of WeKnora's retriever backends requires a running external
  service. None were vendored; `BM25Retriever` (pure Go, in-memory) is the
  replacement default, reachable through the same `Retriever` interface
  shape so a real embedding-backed retriever can be dropped in later.
- `internal/database`, `internal/models/{embedding,rerank,chat,vlm,asr}`,
  `internal/agent`, `internal/mcp`, `internal/handler`, `internal/router`,
  `internal/im`, `internal/sandbox`, `internal/tracing`, etc. — WeKnora's
  service/HTTP/agent layer, gorm models, and embedder/reranker model
  clients. Not vendored; out of scope for "core domain: chunking,
  types, retrieval."
- WeKnora's own `internal/types/{chunk,knowledge,knowledgebase}.go` — these
  are gorm-tagged DB row types (see `ChunkStatus`, `ChunkFlags`, soft-delete
  fields, etc.) and don't separate cleanly from the DB layer. Replaced with
  the minimal `Document`/`Chunk`/`Result` types in `internal/know/types.go`
  instead of porting and stripping them.

## Integration point in sonut

`internal/knowledge` (sonut's existing markdown-notes-with-wikilinks
knowledge base — `Note`, `LoadNotes`, `BuildGraph`, `AddNote`) is unchanged
in its existing behavior. `internal/knowledge/know_backend.go` adds a new,
opt-in type:

```go
backend, err := knowledge.NewSearchBackend()   // loads + chunks + indexes all notes
results, err := backend.Search("goroutines channels", 5)
// results[i].NoteName / .Title / .Snippet / .Score
backend.Refresh()  // re-load notes from disk, re-index
```

`SearchBackend` wraps a `know.Service`, feeding it every note from
`knowledge.LoadNotes()` as a `know.Document` (`ID` = note slug). This gives
sonut's agent chunk-level ("which paragraph of which note") search over the
knowledge base in addition to the existing whole-note graph view — nothing
in `internal/knowledge`'s existing API (`LoadNotes`, `BuildGraph`, `AddNote`,
`Dir`, `EnsureDir`, `Slugify`) was touched, so this is purely additive.
`SearchBackend` is not wired into any command/handler yet; that's a
follow-up once the agent has a use for chunk-level retrieval (e.g. a
`/knowledge search <query>` command or an agent tool).

## New dependencies

None. The vendored chunker used only two non-stdlib WeKnora internal
packages (`docparser`, `logger`), both replaced with small local
equivalents (see above) rather than vendored. `go.mod`/`go.sum` are
unchanged by this work.

## Tests

- `internal/know/know_test.go` — `Service` round-trip: ingest two
  documents, verify BM25 retrieval ranks the topically-matching document
  first for two different queries, verify `Delete` removes a document from
  both the store and the retrieval index.
- `internal/knowledge/know_backend_test.go` — `SearchBackend` round-trip
  against real notes on disk (via `ATCODE_KNOWLEDGE_DIR`): ingest via
  `NewSearchBackend`, search, add a note, `Refresh`, search again.

Run: `go build ./... && go test ./internal/know/... ./internal/knowledge/...`
