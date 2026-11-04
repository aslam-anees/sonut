---
name: design-eng
description: Design-engineering craft for polished UI and motion (Emil Kowalski principles). Use when building, reviewing, or improving frontend animations, micro-interactions, buttons, popovers, toasts, drawers, or any interface that should feel intentional rather than AI-slop.
---

# Design Engineering (lean)

Distilled from Emil Kowalski’s design-eng skill ([emilkowalski/skills](https://github.com/emilkowalski/skills)). Load this for taste; keep changes surgical.

## Philosophy

- Unseen details compound. Users notice the aggregate, not each rule.
- Beauty is leverage — good defaults and motion are product differentiators.
- Never animate just because it “looks cool” if users will see it constantly.

## Animation decision (before code)

1. **How often?** 100×/day (command palette, shortcuts) → **no animation**. Occasional (modal, toast) → standard. Rare (onboarding) → can delight.
2. **Purpose?** Spatial consistency, state change, feedback, prevent jarring appear/disappear. If only “cool” and frequent → skip.
3. **Easing:** enter/exit → **ease-out** (never ease-in for UI). On-screen move → ease-in-out. Hover/color → ease. Constant → linear.
4. **Custom curves** (built-in CSS easings are weak):

```css
--ease-out: cubic-bezier(0.23, 1, 0.32, 1);
--ease-in-out: cubic-bezier(0.77, 0, 0.175, 1);
--ease-drawer: cubic-bezier(0.32, 0.72, 0, 1);
```

5. **Duration:** press 100–160ms · tooltip 125–200ms · dropdown 150–250ms · modal 200–300ms. UI motion under ~300ms.

## Hard rules

| Never | Do instead |
| --- | --- |
| `transition: all` | `transition: transform 200ms var(--ease-out), opacity 200ms` |
| `scale(0)` enter | `scale(0.95)` + `opacity: 0` |
| `ease-in` on UI | `ease-out` / custom curve |
| Animate keyboard actions | Instant |
| Popover from center | `transform-origin` from trigger (modals stay centered) |
| Keyframes for rapid UI | CSS transitions (interruptible) |
| Hover without media query | `@media (hover: hover) and (pointer: fine)` |

## Components

**Buttons:** `transform: scale(0.97)` on `:active`, ~160ms ease-out.

**Popovers:** origin-aware scale from trigger. **Modals:** origin center.

**Tooltips:** delay first open; subsequent neighbors instant (`transition-duration: 0`).

**Enter without JS:** `@starting-style` when supported.

**Only animate** `transform` and `opacity` (GPU). Avoid animating layout (`height`, `padding`, `width`).

## Review format

When reviewing, use a markdown table:

| Before | After | Why |
| --- | --- | --- |
| `ease-in` 300ms dropdown | `ease-out` 180ms custom curve | Instant feedback; under 300ms |

## Accessibility

```css
@media (prefers-reduced-motion: reduce) {
  /* keep opacity/color; drop transform movement */
}
@media (hover: hover) and (pointer: fine) {
  .el:hover { /* hover-only motion */ }
}
```

## Cohesion

Match motion personality to the product (playful bounce only for flicks; dashboards crisp and fast). Enter and exit on the **same path**. Exit can be faster than enter.

## Verify in browser

After motion/UI changes: load `frontend-verify` and use **headful** Playwright so the user can see interactions. Prefer `mcp_playwright_*` tools; the browser window is visible by default on desktop.
