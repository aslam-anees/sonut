# Product Insights (Supabase)

When a user explicitly opts in, @code sends a tiny privacy-preserving signal
after a completed run. Supabase merges signals into **one row per signed-in
user per UTC day**, giving the team useful adoption and product-direction
numbers without storing user content or creating a large event table.

## Trust contract

What is sent to the aggregation function:

| field          | example  | why it is useful                              |
|----------------|----------|-----------------------------------------------|
| `intent`       | `fix`    | coarse feature direction (fix/build/test/…)   |
| `prompt_size`  | `s`      | bucket for context UX, never an exact length  |
| `outcome_size` | `m`      | bucket for response UX                        |
| `platform`     | `darwin` | OS compatibility priorities                   |
| `arch`         | `arm64`  | release/build compatibility priorities        |
| `app_version`  | `0.1.0`  | upgrade adoption and regression correlation   |

What is **never** uploaded: prompt text, model output, code, file paths,
repository names, model/provider names, exact token counts, session IDs,
device IDs, IP-derived location, knowledge notes, or anything else from
`/knowledge`. Supabase may still process ordinary network metadata such as an
IP address in its infrastructure logs; the application does not put it in the
analytics table.

Uploads are asynchronous and best-effort, require a signed-in account, and
require explicit consent:

```bash
export ATCODE_TELEMETRY=1
```

Without that setting, no analytics request is made. `/hide session` suppresses
knowledge capture and analytics for that session even when opted in.

Enforced by `internal/insights` (see `TestEventNeverContainsUserContent`).

## Supabase setup

Run `scripts/supabase_setup.sql` in the Supabase SQL editor. It creates
`product_usage_daily` and a validated `record_product_usage` RPC. Clients
cannot directly read or modify the analytics table, and the RPC derives the
user ID from `auth.uid()` rather than trusting a client-supplied ID.

The old `usage_insights` table is retained only for compatibility with older
released clients; current clients do not add per-run rows to it.

## Useful admin queries

Run these only in the Supabase SQL editor or another trusted backend:

```sql
-- Daily active users and completed runs.
select usage_date, count(*) as active_users, sum(run_count) as completed_runs
from public.product_usage_daily
group by usage_date
order by usage_date desc;

-- Product intent distribution.
select item.key as intent, sum(item.value::integer) as runs
from public.product_usage_daily
cross join lateral jsonb_each_text(intent_counts) as item
group by item.key
order by runs desc;

-- Optional retention for an even smaller database.
delete from public.product_usage_daily
where usage_date < current_date - 180;
```
