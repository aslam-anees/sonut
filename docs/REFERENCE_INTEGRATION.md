# Reference integration boundary

The `ref/` repositories are design references, not runtime dependencies. The
following clean-room mappings are now present in `@code`:

| Reference | AISTATINE-owned surface | Boundary |
| --- | --- | --- |
| MiniScira | `internal/knowledge` Knowledge Atlas, local BM25 search, notes, sources, timeline, and explicit web mode | No hosted gateway, MCP catalog, or remote memory is required |
| Page Assist | `internal/tools/web_fetch` readable HTML extraction (`htmlToText`), bounded output, redirect/SSRF checks, and untrusted-page treatment | No browser extension code or provider keys are copied |
| Witr | `internal/sec/processinsight` bounded process/port/name/file inspection | Clean-room implementation; no Witr dependency |

Page content is data, never authority: extracted text cannot invoke tools or
change policy. Network fetches remain permission-gated, public-only, bounded,
redacted, and cached only as a short-lived accelerator. The browser automation
surface remains a separate explicit capability for interactive pages.

This keeps the local-first promise while preserving the high-value interaction
patterns from the references in reviewable, testable AISTATINE code.
