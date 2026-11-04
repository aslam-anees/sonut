# Security and privacy

This is an experimental coding agent that can read files, edit code, execute
commands, and use network tools. Review permissions and use a disposable
workspace for unfamiliar tasks. `/sec` supplies bounded observations; it is
not a guarantee that a project or machine is secure.

## Configuration

No Supabase project, Google OAuth client, or application secret is bundled.
Local inference does not require these services. To enable hosted
authentication, configure your own `SUPABASE_URL`, `SUPABASE_ANON_KEY`, and,
where applicable, Google OAuth application through environment variables.
`.env.example` documents names only; the application reads process environment
variables, so use your own environment loader if you keep a private `.env`.
Never distribute a Supabase service-role key in a client.

The terminal starts locally when hosted authentication is unconfigured. If
hosted sign-in is configured, the login screen also offers `L` to continue
locally without an account. Downloads and optional network tools remain
separate from account sign-in.

Keep provider credentials, `.env` files, `.atcode/`, session exports, model
files, and engine logs out of Git. Ignore rules help prevent accidents;
they do not sanitize files already tracked by Git.

## Data boundaries

The local llama server uses a randomly generated per-user key stored at
`~/.atcode/engine-api-key` with owner-only permissions (0600 on Unix). It is
passed to the server with `--api-key-file`, not as a command-line secret.
`ATCODE_ENGINE_KEY_FILE` can select a separate key file for isolated runs.
Keep this file private; do not display it during a recording. This boundary
does not protect against another program running as the same OS user.
Existing local servers using the older shared password are restarted when
the Bonsai profile is checked. Use a compatible runtime supporting
`--api-key-file`; an early engine exit is reported with its local log path.

`at doctor` recognizes installed local runtime/model resources without a
cloud provider profile. Its health probe is optional (`--connectivity`) and
does not prove that generation or a coding task will succeed.

- Local inference runs on your machine. Initial downloads and updates need
  network access. Configured cloud providers receive the context sent to them;
  web tools, MCP servers, and plugins have their own data boundaries.
- Sessions, knowledge, audit records, and configuration can persist locally.
  Legacy Supabase sessions use JSON files with owner-only permissions, not
  application-level encryption. Other credential stores may use the OS keyring.
- Product insights require explicit opt-in (`ATCODE_TELEMETRY=1`) and a signed-in
  user. The insights payload contains derived categories and size buckets,
  not prompt text, code, or file paths. `/feedback` explicitly submits the
  message the user provides to their configured Supabase project.
- The mobile mirror exposes a local session to devices that possess its
  access URL. Treat its URL and QR code as credentials. Use a trusted network
  and avoid displaying the access token in recordings.
- The environmental display uses a fixed token-based estimate. It does not
  measure local electricity use or establish net carbon savings.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting feature if enabled. If it is not
available, open an issue requesting a private reporting channel without
including secrets, exploit payloads against live systems, or private logs.

## Previously embedded credentials

The open-source preparation removed embedded credentials and private runtime
artifacts and starts the release branch with fresh history. Repository cleanup
does not revoke credentials or erase copies in clones, forks, provider caches,
or old commit URLs. Maintainers must rotate previously embedded Google OAuth
credentials and review the Supabase project's exposed keys, sessions, and
row-level security settings in the corresponding provider consoles.
