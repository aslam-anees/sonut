---
name: evolve
description: Safely improve @code from verified recurring work without broad rewrites.
---

# Evolve

Use this skill only for an explicit evolution request.

1. Read `.atcode/memory/projects/*/MEMORY.md` and the current session checkpoint
   or notes before proposing a change.
2. Find one recurring, evidence-backed weakness. Require at least two examples
   when the change would alter behavior.
3. Choose the smallest durable improvement: a focused test, project skill,
   command, hook, or narrow implementation fix. Prefer extending an existing
   asset over creating a duplicate.
4. Keep secrets and raw conversation text out of committed files. Never modify
   `.atcode/memory` by guessing; preserve explicit user rules and decisions.
5. Verify the change with the narrowest relevant test or dry run and report any
   remaining uncertainty.

Evolution is bounded learning, not unrestricted self-modification: do not
rewrite the agent core, permissions, or safety behavior without explicit scope
and regression tests.
