---
name: frontend-design
description: Create distinctive, production-grade frontend interfaces (web pages, components, dashboards, landing pages). Use whenever building or styling any UI so the result avoids generic "AI-generated" design.
---

# Frontend Design

Goal: interfaces with a clear point of view that look intentionally designed, not template output. Follow these rules in order; when unsure, pick the bolder option.

## 1. Commit to one aesthetic direction

Before writing code, pick ONE direction and push it hard (examples: brutalist, editorial/print, retro-terminal, luxury minimal, playful geometric, industrial utilitarian, soft organic). State it in a code comment, then make every choice serve it. A design that is 100% one style beats a mix.

## 2. Typography (highest impact)

- Pick a distinctive display font for headings and a readable body font. NEVER default to Inter, Roboto, Arial, or system-ui for headings.
- Contrast sizes hard: hero text 3–6rem, body 1rem. Use tight line-height (0.95–1.1) and negative letter-spacing (-0.02em to -0.05em) on large headings.
- Body text: line-height 1.5–1.7, max width ~65ch.

## 3. Color

- Choose a small committed palette: 1 dominant background, 1 text color, 1–2 accents. Use CSS variables.
- Avoid the AI-slop look: no purple-to-blue gradients on white cards, no #6366f1 indigo defaults, no evenly-rounded 12px cards on gray-50.
- Prefer either very dark or very light backgrounds with one saturated accent; check text contrast (≥4.5:1).

## 4. Layout and space

- Use asymmetry, overlap, or a strong grid — not three equal centered cards.
- Whitespace is a feature: double the padding you think you need, or go dense on purpose (editorial/terminal styles).
- Vary section rhythm; don't stack identical full-width bands.

## 5. Details and texture

- Add 2–3 signature details that carry the theme: borders (thick, thin, or none — consistent), noise/grain overlays, grid lines, oversized numerals, monospace labels, custom list markers.
- Shadows: either none (flat/brutalist) or large and soft — never the default `box-shadow: 0 1px 3px`.
- Consistent corner radius everywhere: 0, small (4px), or full pill — pick one.

## 6. Motion (small doses + taste)

For deeper motion craft load the `design-eng` skill (Emil Kowalski rules). Minimums here:

- One or two purposeful effects: hover (color/underline/translate), button `:active { transform: scale(0.97) }`, a single hero entrance. Respect `prefers-reduced-motion`.
- Enter/exit use **ease-out** (never ease-in). Prefer custom curves: `cubic-bezier(0.23, 1, 0.32, 1)`.
- Never `scale(0)` or `transition: all`. Animate only `transform`/`opacity`. UI motion under ~300ms.
- No scroll-jacking, no long stagger chains, no animation on high-frequency keyboard actions.

## 7. Craft checklist (verify before done)

- Real content, not lorem ipsum, where possible.
- Responsive: no horizontal scroll at 375px; test flexible widths.
- Interactive elements have hover/focus/disabled states; focus is visible.
- Semantic HTML (nav, main, button, label); images have alt text.
- Dark/light: if the page adapts, both themes must be checked, not just the one you built in.
- **Verify in headful Playwright** (`frontend-verify` skill) so the user can see the result and interact if needed.

Anti-patterns (instant fail): centered-everything layouts, purple gradient hero on white, emoji as icons in professional UIs, identical card grids, default font stacks for headings, gray-on-gray low-contrast text.
