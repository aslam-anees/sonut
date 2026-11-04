---
name: debugging
description: Systematic debugging method for any failing code, test, build, or runtime error. Use whenever the user reports a bug, an error message, a crash, unexpected behavior, or a failing test — before attempting any fix.
---

# Debugging

Goal: find the CAUSE before touching code. A fix without a reproduced, understood cause is a guess; guesses create regressions.

## 1. Reproduce first

- Run the failing thing yourself (exec_command): the failing test, the build, the command from the user's report. Capture the EXACT error output.
- If you cannot reproduce, say so and ask for the exact command/input — do not fix blind.
- Note the first error, not the last: cascading output usually stems from one root failure at the top.

## 2. Read the error, literally

- Extract: file, line, symbol, error type. Open that exact location and read the surrounding code before forming a theory.
- Unfamiliar error text: search the codebase for where the message is produced (grep the literal string) — the throw site tells you what condition failed.
- For stack traces, walk top-down until the first frame inside project code; that frame is where to look.

## 3. Triage the failure class

- **Build/type error**: fix the named file at the named line; then rebuild. Never silence with casts, ignores, or deleting the usage.
- **Test failure**: read the assertion — is the code wrong or the expectation stale? Decide explicitly; do not "fix" tests to match broken behavior.
- **Runtime error/crash**: find the failing input or state. Add a focused log/print at the suspect point if the state is not obvious, re-run, read, then REMOVE the debug output.
- **Wrong behavior, no error**: bisect the data flow — find the last point where state is correct and the first point where it is wrong; the bug lives between them.
- **Works-sometimes/flaky**: suspect ordering, timing, shared state, or environment; re-run several times before declaring fixed.
- **Tool/app unavailable in the sandbox** (command not found, GUI launch error like NSOSStatusError): do NOT retry-loop. Sandbox-first: check for an in-sandbox alternative (`command -v <tool>`, playwright MCP tools for browsers, web_fetch for pages). If none works, ask the user with ask_user, offering exactly: "1. Open/use it OUTSIDE the sandbox on my machine (host), with my approval" and "2. Try downloading/installing it INSIDE the sandbox and use it there" — then act on the answer (1: re-run via bash with sandbox_permissions "require_escalated" and a justification; 2: install in the sandbox and retry).

## 4. Check the logs before theorizing

- Server/daemon issues: read the relevant log file or background-session output first (e.g. dev-server output, ~/.atcode/*.log, console messages in browser flows). Errors already recorded there beat any hypothesis.
- For web apps, browser console messages count as logs: red console errors mean broken, even if the page renders.

## 5. Fix at the cause, iterate bounded

- Change the smallest thing that removes the cause — not the symptom, not surrounding refactors.
- Re-run the same reproduction after EVERY change. One change per iteration; if a change didn't help, revert it before trying the next theory.
- Cap the loop: after 3 failed fix attempts, stop, summarize what was ruled out and what still fails, and ask or escalate instead of thrashing.

## 6. Prove it fixed

- The original reproduction passes, AND the surrounding test suite/build still passes (run it).
- State plainly what the root cause was in one sentence. If you never found the cause but the symptom is gone, say that honestly.

Anti-patterns (instant fail): fixing without reproducing; deleting the code that raised the error; sweeping catches that swallow errors; claiming "should work now" without re-running; leaving debug prints in; changing many things in one attempt.
