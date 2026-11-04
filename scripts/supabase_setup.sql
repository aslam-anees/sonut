-- @code Supabase setup — run in the Supabase SQL editor.
-- Matches internal/insights and internal/feedback.
-- Safe to re-run: everything is IF NOT EXISTS / OR REPLACE style.

-- Privacy-preserving product analytics.
--
-- One row is stored per signed-in user per UTC day. The client sends only
-- coarse categories; this RPC increments aggregate counters without accepting
-- a user ID, raw text, a session ID, a device ID, or an exact token count.
create table if not exists public.product_usage_daily (
  user_id             uuid not null references auth.users (id) on delete cascade,
  usage_date          date not null default ((timezone('utc', now()))::date),
  run_count           integer not null default 0 check (run_count >= 0),
  intent_counts       jsonb not null default '{}'::jsonb check (jsonb_typeof(intent_counts) = 'object'),
  prompt_size_counts  jsonb not null default '{}'::jsonb check (jsonb_typeof(prompt_size_counts) = 'object'),
  outcome_size_counts jsonb not null default '{}'::jsonb check (jsonb_typeof(outcome_size_counts) = 'object'),
  platform            text,
  arch                text,
  app_version         text,
  first_seen_at       timestamptz not null default now(),
  last_seen_at        timestamptz not null default now(),
  primary key (user_id, usage_date)
);

alter table public.product_usage_daily enable row level security;

-- There are intentionally no client table policies. Authenticated clients can
-- call only the narrow function below; they cannot list, edit, or delete rows.
revoke all on table public.product_usage_daily from anon, authenticated;

create index if not exists product_usage_daily_date_idx
  on public.product_usage_daily (usage_date desc);

create or replace function public.record_product_usage(
  p_intent text,
  p_prompt_size text,
  p_outcome_size text,
  p_platform text,
  p_arch text,
  p_app_version text
)
returns void
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
  v_user_id uuid := auth.uid();
  v_usage_date date := (timezone('utc', now()))::date;
begin
  if v_user_id is null then
    raise exception 'authentication required';
  end if;
  if p_intent not in ('fix', 'test', 'refactor', 'explain', 'build', 'release', 'review', 'other') then
    raise exception 'invalid intent';
  end if;
  if p_prompt_size not in ('empty', 'xs', 's', 'm', 'l', 'xl')
     or p_outcome_size not in ('empty', 'xs', 's', 'm', 'l', 'xl') then
    raise exception 'invalid size bucket';
  end if;
  if char_length(coalesce(p_platform, '')) not between 1 and 32
     or char_length(coalesce(p_arch, '')) not between 1 and 32
     or char_length(coalesce(p_app_version, '')) not between 1 and 64 then
    raise exception 'invalid client metadata';
  end if;

  insert into public.product_usage_daily (
    user_id,
    usage_date,
    run_count,
    intent_counts,
    prompt_size_counts,
    outcome_size_counts,
    platform,
    arch,
    app_version
  )
  values (
    v_user_id,
    v_usage_date,
    1,
    jsonb_build_object(p_intent, 1),
    jsonb_build_object(p_prompt_size, 1),
    jsonb_build_object(p_outcome_size, 1),
    p_platform,
    p_arch,
    p_app_version
  )
  on conflict (user_id, usage_date) do update
  set
    run_count = public.product_usage_daily.run_count + 1,
    intent_counts = jsonb_set(
      public.product_usage_daily.intent_counts,
      array[p_intent],
      to_jsonb(coalesce((public.product_usage_daily.intent_counts ->> p_intent)::integer, 0) + 1),
      true
    ),
    prompt_size_counts = jsonb_set(
      public.product_usage_daily.prompt_size_counts,
      array[p_prompt_size],
      to_jsonb(coalesce((public.product_usage_daily.prompt_size_counts ->> p_prompt_size)::integer, 0) + 1),
      true
    ),
    outcome_size_counts = jsonb_set(
      public.product_usage_daily.outcome_size_counts,
      array[p_outcome_size],
      to_jsonb(coalesce((public.product_usage_daily.outcome_size_counts ->> p_outcome_size)::integer, 0) + 1),
      true
    ),
    platform = excluded.platform,
    arch = excluded.arch,
    app_version = excluded.app_version,
    last_seen_at = now();
end;
$$;

revoke all on function public.record_product_usage(text, text, text, text, text, text)
  from public, anon;
grant execute on function public.record_product_usage(text, text, text, text, text, text)
  to authenticated;

-- Legacy per-run table retained so older released clients continue to work.
-- Current clients write only to product_usage_daily through the RPC above.
create table if not exists public.usage_insights (
  id            bigint generated always as identity primary key,
  user_id       uuid not null default auth.uid() references auth.users (id) on delete cascade,
  event         text not null,
  session_hash  text not null,
  intent        text,
  prompt_size   text,
  outcome_size  text,
  platform      text,
  arch          text,
  app_version   text,
  created_at    timestamptz not null default now()
);

-- If the table already existed with an older shape, add any missing columns.
alter table public.usage_insights
  add column if not exists user_id      uuid default auth.uid(),
  add column if not exists event        text,
  add column if not exists session_hash text,
  add column if not exists intent       text,
  add column if not exists prompt_size  text,
  add column if not exists outcome_size text,
  add column if not exists platform     text,
  add column if not exists arch         text,
  add column if not exists app_version  text,
  add column if not exists created_at   timestamptz default now();

alter table public.usage_insights enable row level security;

-- Signed-in users may insert only their own rows (client sends no user_id;
-- the default auth.uid() stamps it server-side).
drop policy if exists "insert own insights" on public.usage_insights;
create policy "insert own insights" on public.usage_insights
  for insert to authenticated
  with check (user_id = auth.uid());

-- Clients never read insights back; keep select closed to end users.
drop policy if exists "no client reads" on public.usage_insights;

create index if not exists usage_insights_created_at_idx on public.usage_insights (created_at);
create index if not exists usage_insights_user_idx       on public.usage_insights (user_id);

-- Explicit feedback submitted through /feedback <message>.
-- user_id is never accepted from the client; auth.uid() stamps the signed-in
-- Supabase user who sent the message.
create table if not exists public.user_feedback (
  id          bigint generated always as identity primary key,
  user_id     uuid not null default auth.uid() references auth.users (id) on delete cascade,
  message     text not null check (char_length(btrim(message)) between 1 and 4000),
  platform    text,
  arch        text,
  app_version text,
  created_at  timestamptz not null default now()
);

-- Make re-running this script safe for databases with an older table shape.
alter table public.user_feedback
  add column if not exists user_id     uuid default auth.uid(),
  add column if not exists message     text,
  add column if not exists platform    text,
  add column if not exists arch        text,
  add column if not exists app_version text,
  add column if not exists created_at  timestamptz default now();

alter table public.user_feedback
  alter column user_id set default auth.uid(),
  alter column created_at set default now();

alter table public.user_feedback enable row level security;

drop policy if exists "insert own feedback" on public.user_feedback;
create policy "insert own feedback" on public.user_feedback
  for insert to authenticated
  with check (user_id = auth.uid());

-- Feedback remains private: authenticated clients may submit it but cannot
-- list either their own rows or another user's rows. Project admins can inspect
-- it in the Supabase dashboard or SQL editor.
drop policy if exists "no client feedback reads" on public.user_feedback;
revoke all on table public.user_feedback from anon;
revoke select, update, delete on table public.user_feedback from authenticated;
grant insert on table public.user_feedback to authenticated;
grant usage on sequence public.user_feedback_id_seq to authenticated;

create index if not exists user_feedback_created_at_idx on public.user_feedback (created_at desc);
create index if not exists user_feedback_user_idx       on public.user_feedback (user_id);
