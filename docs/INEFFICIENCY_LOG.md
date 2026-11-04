# atcode inefficiency log (supermario probe)

Captured while running `build a supermario game` through local hybrid atcode
on M-series 16 GiB (2026-07-30 / 2026-07-31). Goal: fix the agent runtime, not
the game.

## Measured symptoms (before fixes)

| Signal | Observed | Cost |
|--------|----------|------|
| Engine context | `ctx=262144` with `free_ram_mb≈7k` after mlock | Metal thrash; multi-minute prefills of only ~5k tokens |
| Cache hit | `cache_hit_pct=0.0` on **168/188** inference_end lines | Hybrid re-prefill every turn |
| TTFT | 70–106 s typical; worst turn **1,311,724 ms** (~22 min) | Agent looks stuck at "↑ 0 tok" |
| Tool schemas | `tools=20`–`21` on builder turns | ~1–2k extra schema tokens ≈ 30–40 s at ~60 tok/s |
| Decode bloat | `completion_tok` 4k–6.7k on single write_file | 5–10 min streaming one file |
| Validation | many `validation_end status=replan plan_bytes=0` | Replan loops without evidence |

Sample before:

```
atcode_engine_launch model=atcode-v1.gguf ctx=262144 ... free_ram_mb=7561
inference_start tools=20 msgs=2 prompt_bytes=9566 body_bytes=19615
inference_end elapsed_ms=13189 prefill_total=5997 prefill_processed=2048
  cache_hit_pct=0.0 saw_output=false
```

## Root causes fixed

1. **262k default on ≤18 GiB** — product default ignored RAM.  
   Fix: `bonsaiRecommendedCtx` now starts every atcode launch at the 128k
   speed-first target; static model/RAM fitting may lower it only when needed.
   Escape: `SONUT_FORCE_NATIVE_CTX=1`; emergency floor: `SONUT_FORCE_64K=1`.

2. **`ApplyLocalHybridToolBudget` wiped by `CoreToolsScoped`** — exec applied the
   budget, then re-registered all core tools as non-deferred → tools=21 every turn.  
   Fix: apply budget **after** scoped re-registration (`internal/cli/exec.go`).  
   Regression test: `TestApplyLocalHybridToolBudgetSurvivesCoreReRegistration`.

3. **Full Playwright schema preload on UI tasks** — 7 MCP tools on cold turn.  
   Fix: cold path only `navigate` + `snapshot`; expand after first successful write.

4. **Hybrid request copy still too fat** — keep-last 3 × 2 KiB tool bodies.  
   Fix: keep-last 2, 1.5 KiB/body, 6 KiB batch budget, 3 recent exchanges.

5. **No visibility into thrash** — only raw inference lines.  
   Fix: `event=inefficiency` for schema tax and slow hybrid turns in `perf.log`.

## After fixes (live re-probe, max-turns 3)

```
atcode_engine_launch ... ctx=131072
inference_start tools=11 msgs=2 prompt_bytes=3873 body_bytes=9559
agent_turn turn=1 ms=175239 tools_exposed=11 tool_calls=1
inference_end ttft_ms=41784 prefill_total=2583 cache_hit_pct=0.0 tool_calls=1
```

| Metric | Before | After | Δ |
|--------|--------|-------|---|
| Context | 262144 | 131072 default | safer KV and faster cold prefill |
| tools advertised | 20–21 | **11** | −50% schema tax |
| prompt_bytes | 9566 | 3873 | −60% |
| body_bytes | 19615 | 9559 | −51% |
| prefill tokens | 5997 | 2583 | −57% |
| TTFT (healthy turn) | ~90 s | **~42 s** | ~2× faster |
| Progress | aborted mid-prefill | **partial `mario.html` written** | real work |

Still open: hybrid `cache_hit` ≈ 0% (architecture), long `write_file` decode,
planner validation with empty evidence, concurrent engine launch races.

## Path confusion after `/cd` (fixed 2026-07-31)

**Symptom (screenshot):** user `/cd` into `.../sonut/test`, model calls
`list_directory path=/test` → `Sandbox block [outside_workspace]`, then burns
thinking turns exploring until it rediscovers the same directory.

**Root cause:** models treat the cwd leaf as a host-root path (`/test`).
`SuggestScopedPath` only matched the workspace basename as an *intermediate*
segment, so `/test/mario.html` remapped but bare `/test` did not. Agent
`canonicalToolPathForCwd` also left absolute paths untouched.

**Fixes:**
1. `sandbox.SuggestScopedPath` — map single-segment `/basename` → granted root
   when basename matches a root leaf (exact + case-insensitive).
2. `sandbox.validateAgainstRoots` — allow single-segment URL-style paths
   (`/mario.html`) via `RootSlashRelative` (multi-segment host paths still deny).
3. `tools.applyRootSlashFallback` — after `/test` → `test`, apply
   `duplicateRootFallback` so the listing is `.` not nested `test/test`.
4. `agent.canonicalToolPathForCwd` — rewrite `/test` → `.` and
   `/test/mario.html` → `mario.html` before sandbox evaluates.
5. System prompt path rule next to Working directory line.

## Broken write quality (fixed 2026-07-31)

**Symptom:** after `/cd` into `test/`, agent “wrote” `supermario.html` that was
only ~6 lines of bare JS (`const TILE = 32; … levelData = [`) — not a game.
UI showed endless `Saved partial file content; continuing with append` while
the file stayed non-playable.

**Root causes:**
1. `validateGeneratedContentPath` accepted pure JS into `.html` when the body
   lacked `{}` (constants-only dumps).
2. Partial-stream salvage latched **any** complete lines as append-only, so a
   6-line JS stub forced endless `append_file` thrash instead of a clean rewrite.
3. `append_file` happily extended a non-HTML `.html` stub forever.

**Fixes:**
1. `LooksLikeHTMLDocument` + strict `.html` validation — require DOCTYPE/`<html>`
   or a real HTML tag; reject bare JS/CSS with a clear path hint.
2. `IsSalvageablePartialContent` — discard tiny/wrong-type partials; unlatch
   append-only so the next call can `write_file` a real HTML shell.
3. `HTMLPathNeedsRewrite` on `append_file` — refuse to append onto a raw-JS
   `.html` stub; demand `write_file` + overwrite of a full shell.
4. Planner rule for single-file HTML games: first chunk = HTML shell + canvas +
   script; logic via `append_file` inside the script; avoid giant zero arrays.

## Identical edit_file hard-stop (fixed 2026-07-31)

**Symptom (live TUI):** after writing a truncated HTML shell ending in
`const levelData = [`, the model issued the same `edit_file` with
`old_string == new_string` three times:

```
No changes: new_string is identical to old_string.
Stopped: the model issued the exact same edit_file call 3 times in a row
  and ignored the block notices. Halting instead of looping.
```

Also nested path invention: cwd=`.../sonut/test` + path=`sonut/test/supermario.html`
created `test/sonut/test/supermario.html` (shadow tree).

**Fixes:**
1. `ancestryPathFallback` / `ancestryRelativePathForCwd` — strip multi-segment
   cwd ancestry restatements (`sonut/test/foo` → `foo` when cwd ends with
   `sonut/test`). Wired into tools + agent path canonicalization.
2. `identicalEditNoopResult` — no-op edit returns strategy-change text
   (append_file next chunk / write_file overwrite) + `edit_noop` meta;
   detects truncated mid-generation files.
3. Guardrails: consecutive identical `edit_file` that was a no-op gets an
   explicit append/write redirect, not a bare "previous result" cache dump.
4. Try-Best: no-op edits count as zero progress toward action_streak.
5. Planner + system path rules: never restate parent folder names; truncated
   files continue with append_file only.

## Still open (next)

- Planner validation that passes with empty evidence (`plan_bytes=0`).
- Engine launch thrash: concurrent launches briefly chose 262k then 128k.
- Hybrid models still cannot roll back recurrent state → cache_hit often 0%;
  keep request copies tiny rather than chasing prefix reuse.
- tools_exposed still ~13–18 on UI tasks after Playwright cold/expand; further
  shrink if prefill stays slow.

## Concurrent exec while TUI holds :2748 (fixed 2026-07-31)

**Symptom:** `at exec` failed immediately with
`failed to listen on 127.0.0.1:2748: address already in use` while an
interactive `@`/`at` session owned the public proxy.

**Fix:** `EnsureLocalServer` + `StartWebUIProxy` reuse a healthy public proxy
owned by another atcode process instead of binding again.

## Live re-probe (2026-07-31, max-turns 4)

```
at exec --skip-permissions-unsafe --max-turns 4 --auto high \
  -C /tmp/supermario-probe "build a real working supermario …"
```

| Check | Result |
|-------|--------|
| Concurrent with TUI on :2748 | **OK** (no bind error) |
| Path nesting `sonut/test/…` | **none** — wrote `/tmp/supermario-probe/supermario.html` |
| First write content | Real HTML shell + canvas + HUD + levels (~660 lines), not bare JS |
| Truncated mid-generation | Partial salvage → append_file instruction (not identical edit thrash) |
| Turn-1 decode | ~16 min / 982s decode (still too slow for giant single writes) |
| Turns 2–4 | Hung idle after turn 1 with single-slot engine + concurrent TUI |

Still open after this probe: second-turn hang when sharing `--parallel 1` engine
with an interactive session; prefer shorter first chunks (≤100 lines) so
append rounds stay under ~2 min decode.

## How to re-measure

```bash
go build -o ./at ./cmd/atcode && cp ./at ~/.atcode/bin/at
at exec --skip-permissions-unsafe --max-turns 4 --auto high \
  -C /tmp/probe "build a supermario game in a single HTML file"
rg 'event=(atcode_engine_launch|inference_|agent_turn|inefficiency)' ~/.atcode/perf.log | tail -40
```
