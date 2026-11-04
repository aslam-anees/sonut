# Phase 3 — native research and clean-room Page Assist

Phase 3 keeps research local-first and auditable. The existing Knowledge Atlas
web pipeline (bounded search, explicit page fetch, HTML extraction, source URL
and domain retention, offline/error states, and preview navigation) is now
available through the concise `/research <query>` command. `/knowledge web`
remains the explicit advanced form.

The clean-room reference work is part of this phase: `/sec processes` uses the
bounded Witr-inspired process insight collector, Knowledge Atlas is the
MiniScira-inspired local research surface, and Page Assist contributes only
the bounded HTML extraction/preview boundary documented in
`docs/REFERENCE_INTEGRATION.md`.

The Page Assist clean-room boundary is preserved: page content is fetched only
after an explicit user action, browser automation stays permission-gated, and
the extracted preview never silently becomes an instruction or tool approval.

## Acceptance

- `/research` parses as a native command and opens the Knowledge Atlas search
  view with source-preserving results;
- existing HTML extraction, citation/source navigation, offline handling, and
  browser permission tests remain the regression gate;
- no new hosted service or per-user network cost is introduced.
