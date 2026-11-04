---
name: frontend-verify
description: Verify frontend/UI work in a real headful Playwright browser so the user can see and interact. Use after building or changing any web page, component, or style — before telling the user it is done.
---

# Frontend Verify (headful Playwright)

Goal: never declare UI work done based on "the code looks right". Verify what the user will actually see, in a **visible** browser window they can watch and take over when needed.

## Headful contract (critical)

- On desktop, Playwright MCP runs **headful by default** (visible Chrome). The user must be able to see navigations, clicks, and forms.
- **Let the user interact** when needed: logins, captchas, OAuth, 2FA, file pickers, or any choice you should not guess. Pause, tell them what to do in the window, then continue with snapshot after they finish.
- Do **not** pass `--headless` or fight the visible window. Only headless when the environment has no display (SSH/CI) or the user set `ATCODE_BROWSER_HEADLESS=1` / `/browser hide` for the agent-browser helper.
- For a simple "open this site for me to look at", prefer host open (`open https://…` / `xdg-open`) — not automation. Use Playwright when you need snapshot/click/type verify loops.

## 1. Get the app running

- Start the dev server as a background command (`npm run dev` or project equivalent); fix startup errors first.
- Run a fast static check when available (`npx tsc --noEmit`) before browser work.

## 2. Load browser tools

- Playwright MCP tools are often deferred; load with `tool_search` (query: `browser` or `playwright`). Names look like `mcp_playwright_browser_navigate`.
- If tools fail to load (offline `npx`), report the error — do not silently skip or loop.

## 3. Verify loop (repeat until clean)

1. `mcp_playwright_browser_navigate` to the local URL — a **visible** window should open/focus.
2. `mcp_playwright_browser_snapshot` — confirm structure and real content (not empty shell).
3. `mcp_playwright_browser_console_messages` — any red error = broken. Fix root cause; re-check.
4. Visual changes: `mcp_playwright_browser_take_screenshot` when useful; still rely on snapshot for structure.
5. Drive the **one main user flow** (click/type/submit) and snapshot the result state.
6. If a step needs a human (login, captcha): stop automation, tell the user to complete it in the open window, then snapshot and continue.

## 4. Responsive + states

- Resize to ~375px width: no horizontal scroll; nav usable.
- Hover/focus/disabled states exist; focus visible.

## 5. Done criteria

- Intended structure on screen; zero console errors; main flow works; visual change confirmed.
- After 3 failed fix rounds, report exactly what still fails — never claim success.

## Skills to pair

- `frontend-design` — layout, type, color, craft checklist.
- `design-eng` — motion, easing, micro-interactions (Emil Kowalski rules).

Anti-patterns: declaring done from code only; ignoring console errors; verifying only in headless when a display exists; blocking the user from touching the Playwright window.
