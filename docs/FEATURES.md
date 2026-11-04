# atcode slash commands & features

Audited 2026-07-30 against the code (`internal/tui`). All public commands are wired and tested (`go build ./...` + `go test ./internal/tui` green). `/voice` opens its optional dedicated settings/setup workspace; `/vim` is implemented (2026-07-17).

| Command | Aliases | How to call | What it does | Status |
|---------|---------|-------------|-------------|--------|
| /hw | /hardware | `/hw` | Re-detect hardware (CUDA/Apple Silicon/CPU) and refresh the atcode-server tuning cache | OK |
| /feedback | — | `/feedback <message>` | Send user-attributed feedback securely to Supabase | OK |
| /mode | — | `/mode [ask\|auto]` | Show/toggle permission mode (ask=prompt, auto=auto-approve) | OK |
| /permissions | — | `/permissions` | Show active permission mode and sandbox grants | OK |
| /build | — | `/build [on\|off]` | Toggle build mode (scaffold/implement/preview web apps) | OK |
| /integrate | — | `/integrate [supabase\|oauth\|hosting] [secret]` | Attach optional integration secrets for build mode | OK |
| /share | — | `/share [on\|off]` | Mirror session to phone/device; view chat live and send prompts from browser | OK |
| /plan | — | `/plan` | Show planning mode status | OK |
| /ps | — | `/ps` | List running background terminal sessions | OK |
| /stop | — | `/stop [session_id]` | Stop running background terminal sessions | OK |
| /sandbox-setup | — | `/sandbox-setup` | Run native sandbox setup for platform | OK |
| /tools | — | `/tools` | List all registered tools | OK |
| /skills | — | `/skills` | List bundled and installed skills | OK |
| /context | — | `/context` | Show workspace and runtime context | OK |
| /sec | — | `/sec [open\|rescan\|diff\|routes\|processes\|activity\|tools\|signals\|help]` | Open the unified native security TUI with posture, prioritized risks, typed proxy/ingress routes, bounded process lineage, live egress, outbound tool surfaces, normalized agent telemetry, transparent behavior triage, controls, evidence, and a redacted-report-grounded AI advisor. `/sec diff` scans Git-changed files only (fast PR review). Writes `report.json`, `report.md`, and `findings.sarif` under `.atcode/sec/` | OK |
| /image | — | `/image <path> \| clear` | Attach image (vision) or PDF (text layer) to next message | OK |
| /add-dir | — | `/add-dir [path]` | Grant write access to directory; bare form lists current write roots | OK |
| /clear | — | `/clear` | Clear visible transcript | OK |
| /search | /find | `/search <query>` | Search local session events | OK |
| /hide | — | `/hide session \| off` | Exclude session from knowledge graph | OK |
| /knowledge | /kb | `/knowledge [view\|add <title> :: <content>\|search <q>\|web <q>\|list\|stats\|export\|open browser]` | Native Knowledge Atlas; local Markdown notes in ~/.atcode/knowledge, with explicit web research and optional graph.html export | OK |
| /enterprise | /ent | `/enterprise` | Show security layer: egress counters, audit trail, hardening | OK |
| /graph | /graphify | `/graph [path\|explain <node>\|path <a> <b>\|open]` | Map project into local knowledge graph; explain/trace/open graph.html | OK |
| /mcp | /mcp-status | `/mcp` | Open the MCP manager: inspect servers/tools/permissions, add/check/enable/disable/remove servers, and manage OAuth | OK |
| /resume | /sessions | `/resume [id]` | List recent sessions or resume one | OK |
| /retitle | — | `/retitle` | Generate concise titles for resumable sessions | OK |
| /spec | — | `/spec <task>` | Draft implementation spec for review before editing | OK |
| /init | — | `/init` | Investigate repo and generate AGENTS.md | OK |
| /compact | — | `/compact [status\|instructions]` | Summarize session context | OK |
| /transcript | — | `/transcript` | Toggle detailed transcript view | OK |
| /rewind | — | `/rewind [latest\|<sequence>]` | Restore workspace to checkpoint and truncate session | OK |
| /effort | — | `/effort` | Re-arms auto effort routing (manual picker removed) + KV warmup; Ctrl+T same | OK |
| /btw | — | `/btw <question>` | Quick side question; after the done banner, empty-composer Enter returns to main conversation | OK |
| /handoff | — | `/handoff [note]` | Write structured handoff doc for a fresh session | OK |
| /style | — | `/style [list\|balanced\|concise\|explanatory\|review\|caveman\|normal]` | Show/set response style | OK |
| /selfcorrect | /sc | `/selfcorrect [status\|on\|off\|tests\|full\|lsp]` | Show/set post-edit self-correction depth | OK |
| /turns | — | `/turns [n]` | Show/set per-run tool-turn budget; propagates to sub-agents | OK |
| /help | — | `/help` | Show all commands grouped by category | OK |
| /doctor | /health | `/doctor [fix\|--connectivity]` | Diagnostics, safe fixes, connectivity checks | OK |
| /config | — | `/config [recaps on\|off]` | Show active config; toggle settings | OK |
| /debug | /debug-mode | `/debug` | Show debug mode status | OK |
| /theme | — | `/theme [list\|auto\|name]` | Pick color theme | OK |
| /exit | /quit | `/exit` | Exit (waits for checkpoint flush) | OK |
| /commit | — | `/commit [message]` | Stage all changes and commit (AI-generated or supplied message) | OK |
| /trash | /bin | `/trash` | List soft-delete TTL bin (.atcode/ttl) with expiry times | OK |
| /review | /code-review | `/review [diff\|branch]` | Review changed files for bugs, style, security | OK |
| /memory | /mem, /ctx | `/memory [32k\|64k\|128k\|256k\|list\|show <file>]` | Show/change context window (hot-restarts atcode-server); memory files | OK |
| /export | — | `/export [path]` | Export session transcript to Markdown | OK |
| /vim | — | `/vim [on\|off]` | Vim-style composer editing: Esc→NORMAL, i/a/A/I/o→INSERT, hjkl/0/$/w/b motions, x/dd/D delete, gg/G; Enter still submits; "-- NORMAL --" indicator in status line | OK |
| /tasks | — | `/tasks` | List active task list | OK |
| /git | — | `/git [status\|log\|diff]` | Quick git info | OK |
| /signout | /logout, /sign-out | `/signout` | Sign out, clear ~/.atcode/auth.json | OK |
| /nonstop | /keep-going, /keepgoing | `/nonstop [on\|off]` | Auto-continue while plan tasks open | OK |
| /history | — | `/history` | Browse past sessions (search / Enter restore / d delete) | OK |
| /cd | /chdir | `/cd <path>` | Change directory + write access; triggers background KV re-warmup | OK |
| /browser | — | `/browser [show\|hide]` | Show/hide automation browser window | OK |

## Feature spot-checks

- **/btw return flow** — `btwPending` → run end sets `btwDone` → empty Enter prints "── back to the main conversation ──"; typed submit clears it. Covered by `internal/tui/btw_test.go`.
- **Prefill status** — `prefillProgressMsg` renders "prefilling context… N%" in the status line; cleared on first text/reasoning/tool output, run end, and new run.
- **/effort & Ctrl+T** — both re-arm auto effort and fire `warmupEffortCmd`.
- **/cd** — fires `warmupEffortCmd` (system prompt embeds cwd/git), avoiding a silent multi-minute re-prefill.

## Harness efficiency (MiMo-Code + qwen-code ports)

Ported into Go for local-model speed and correctness. Core behavior is unchanged; these layers only shrink noise, batch I/O, or soft-nudge strategy changes.

| Feature | Where | Opt-out |
|---------|-------|---------|
| **Token-efficient shell output** | `internal/tools/token_efficient.go` — strips ANSI/progress bars, elides long lines, shape-filters go test / pytest / npm / make / stacktrace / gitdiff / tsc before the model sees bash/exec output. Never-worse gate: if clean does not shrink bytes, raw is kept. | `ATCODE_BASH_RAW=1` or `# nofilter` / `# raw` in the command |
| **Try-Best loop detector** | `internal/agent/trybest.go` — near-identical edit thrash (Jaccard shingles), repeated failing bash, edit/verify streaks without progress → soft strategy-change nudge (never hard-stops; existing exact-repeat guards still halt loops) | `ATCODE_TRY_BEST=0` |
| **Multi-skill composition** | `internal/agent/skill_composition.go` — when the user names ≥2 skills via `/skill-name` tokens, injects an orchestration reminder after the user message (prefix-cache safe) | automatic; single skill = no inject |
| **/build mode** | `internal/agent/build_mode.go` + TUI `/build` — scaffold → implement → tsc → dev server → Playwright verify loop (unchanged core; still opt-in) | `/build off` |
| **Head+tail output budget** | `internal/tools/output_budget.go` + `response_budget.go` — oversized tool text keeps start and end (errors/stack tails survive); fair shared batch allocation across tool bodies on local hybrid | automatic for local hybrid |
| **Batch tool-response budget** | `internal/agent/hybrid_prefill.go` — after per-tool caps, fair-allocate an 8 KiB shared budget across all tool bodies in the request copy (qwen-code finalizer) | automatic for local hybrid |
| **read_many_files** | `internal/tools/read_many_files.go` — one tool call for up to 12 files under a 16 KiB shared budget; local-hybrid eager (fewer sequential read_file prefills) | use `read_file` for a single full file |
| **Unlimited context ladder** | `internal/agent/soft_trim.go` + `compaction.go` — MiMo-style multi-stage reclaim: (1) soft-trim large old tool dumps at 65% window (head+tail keep), (2) hard-prune to placeholders at 80%, (3) paid LLM summary only if still over. Reactive path runs the free stages first | automatic when `ContextWindow > 0` |
| **Unchanged read fast-path** | `internal/tools/file_tracker.go` + `read_file.go` — qwen FileReadCache: identical range re-read of an unchanged file (size+mtime) returns a one-line placeholder instead of re-dumping (skips disk re-read + prefill) | automatic; writes/edits invalidate |
| **Speed-first context default** | `internal/llama/bonsai_profile.go` — atcode defaults to 128k to avoid native-262k cold-prefill thrash while retaining enough room for multi-file coding. Use `SONUT_FORCE_64K=1` for emergency pressure or `SONUT_FORCE_NATIVE_CTX=1`/`/memory` for explicit larger windows. | automatic |
| **Hybrid budget after scoped tools** | `internal/cli/exec.go` — `ApplyLocalHybridToolBudget` runs **after** `CoreToolsScoped` re-registration so force-deferred wrappers are not wiped (live bug: tools=21 every exec turn) | automatic |
| **Minimal browser cold-path** | `internal/agent/loop.go` — UI tasks only eager-load navigate+snapshot (2 schemas); click/type/evaluate expand after first successful file mutation | automatic for UI tasks |
| **Inefficiency perf events** | `internal/agent/loop.go` → `~/.atcode/perf.log` — `event=inefficiency kind=tools_schema_tax\|slow_hybrid_turn` when tools>14 or turn≥45s with little output | `SONUT_PERF=0` to disable |

## Security posture (/sec + codex-security)

| Feature | Where | Notes |
|---------|-------|-------|
| **Full scan** | `internal/sec/scanner.go` + TUI `/sec` | Agentless bounded inventory; secrets redacted; toxic path correlation |
| **Diff scan** | `/sec diff` · `sec.DiffOptions()` | Git working-tree + index + untracked only (codex security-diff-scan) |
| **SECURITY.md policy** | loaded into `Report.Policy` and advisor projection | Untrusted analysis data; never instructions |
| **Priority P0–P3** | severity policy on each finding | Mirrors codex-security priority mapping |
| **Static tuple** | source / control / sink fields | codex static-finding-assessment ingredients |
| **Extra rules** | pickle, unsafe yaml, shell=True, eval, XSS HTML, JWT none, weak password hash, debug flags, CI script injection, cleartext secret URLs | High-signal sinks only |
| **Exports** | `.atcode/sec/report.json`, `report.md`, `findings.sarif` | SARIF 2.1.0 best-effort; JSON remains canonical |
| **Agent signals** | `/sec signals` · `internal/tui/sec_agent_view.go` | ADR-inspired normalized session telemetry and high-recall triage for sensitive reads, outbound intent, prompt-override language, and permission boundaries; raw prompts/output stay out of the view |
| **Typed attack surface** | `internal/sec/structured.go` | Bounded Compose, Kubernetes, Dockerfile, GitHub Actions, and conservative Terraform extraction; configured-public is not reachability |
| **Proxy routes** | `/sec routes` · `sec.Route` | GoDoxy-inspired clean-room route inventory from Compose labels, route files, and Kubernetes ingress; target/policy metadata is evidence-backed and read-only |
| **Process insight** | `/sec processes` · `internal/sec/processinsight` | Witr-inspired clean-room PID/port/name/file inspection with bounded ancestry, child summaries, socket context, command timeouts, and argument redaction |
| **Ownership and fixes** | `internal/sec/ownership.go` · Risks `x` | `.github/CODEOWNERS`, `CODEOWNERS`, then `docs/CODEOWNERS`; last match wins; unowned is explicit; fixes use the normal agent flow |
| **Threat intel** | `.atcode/security.json` + `internal/sec/threatintel.go` | Disabled by default; exact Go/npm versions only; OSV calls are bounded and cached with mode 0600 |
| **Runtime layers** | Tool Guard + sandbox + `internal/security/sensor` | Tool Guard is payload-aware, sandbox remains independent, and the Linux sensor is an explicit helper with cgroup connect enforcement where capabilities allow; unsupported hooks degrade honestly |

### Runtime sensor boundary

`/sec sensor start` is a direct human action. It launches only the bundled `atcode-sensor` helper through the platform privilege flow; the agent and sandboxed child processes do not receive BPF handles or a writable sensor control channel. The helper is scoped to the @code process cgroup when cgroup v2 is available, records sanitized destination metadata, captures no packet payloads, and does not decrypt HTTPS. Linux capability probes expose `network-enforce`, `detect-only`, or `unavailable`; macOS and Windows remain unsupported.
