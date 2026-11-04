# internal/enterprise

Vendored enterprise security layer, sourced from `DEEIX-Chat/backend`
(module `github.com/DEEIX-AI/DEEIX-Chat/backend`, a separate product, excluded
from the sonut build). This package renames and adapts a subset of that
backend's security-relevant code to run standalone inside sonut, with no
database, no HTTP framework, and no external services required.

## Vendored scope

| Package | Source in DEEIX-Chat/backend | What it does |
|---|---|---|
| `internal/enterprise/security` | `internal/shared/security` | Sensitive-header redaction (`RedactHeadersJSON`, `IsSensitiveHeaderName`) and SSRF-hardened outbound HTTP (`ValidateOutboundHTTPURL`, `NewOutboundHTTPClient`, `NewOutboundDialContext` — resolves DNS first, rejects loopback/private/link-local/metadata IPs, dials the validated IP directly to prevent TOCTOU rebinding). Copied near-verbatim; stdlib only. |
| `internal/enterprise/secretbox` | `internal/pkg/secretbox` | AES-GCM string encryption for secrets at rest (API keys, tokens). Copied verbatim; stdlib only (`crypto/aes`, `crypto/cipher`). |
| `internal/enterprise/token` | `internal/pkg/token` | HMAC-signed JWT session tokens (`Generate`, `GenerateWithClaims`, `Parse`), carrying user/role/session-id/token-type claims. Copied verbatim. New dependency: `github.com/golang-jwt/jwt/v5`. |
| `internal/enterprise/audit` | `internal/domain/audit` + `internal/application/audit` | Append-only audit trail (`Service.Write`, `Service.List`) for security-relevant actions. **Adapted**: the original persisted via a Postgres-backed `repository.AuditRepository` and logged via `zap.Logger`. Here persistence is abstracted behind a `Store` interface (`Create`/`List`) with an in-memory `MemoryStore` default, and structured logging is abstracted behind an optional `Sink` callback. No DB, no zap. |
| `internal/enterprise/authz` | `internal/transport/http/middleware.AdminOnly` + `internal/domain/user.IsAdminRole` | Role-based permission checks (`Policy.Grant`, `Policy.Allow`, `Policy.Check`). **Generalized**: the original hard-coded a single admin/non-admin split; this package makes the role → permission mapping data-driven so any set of roles/permissions can be defined by the embedder. |
| `internal/enterprise/ratelimit` | `internal/transport/http/middleware.RateLimiter` interface (consumer: `RateLimit`/`PublicAuthRateLimit` middleware) | Sliding-window and fixed-window request throttling. **Adapted**: the original implementation was Redis-backed; `MemoryLimiter` here is an in-memory, single-process, thread-safe implementation of the same `Limiter` interface. |

## Skipped scope

- All frontend, Docker, and deployment code.
- Chat product features: conversations, channels, billing, memory, RAG,
  skills, MCP, uploads, extraction, embeddings, admin UI, etc.
- Postgres/SQLite persistence layers (`internal/infra/persistence/**`) and the
  `repository` package — DB-coupled; replaced by the `Store`/`Limiter`
  interfaces described above, which default to in-memory implementations.
- gin-gonic HTTP middleware wiring (`internal/transport/http/middleware/{auth,cors,ratelimit,security_headers,https_redirect,request_id,request_audit,logging,trusted_proxy}.go`)
  and the full `internal/application/auth` service (login, registration,
  2FA, Turnstile, session metadata). sonut is a CLI/TUI agent, not a gin web
  server, so no HTTP-framework-specific glue was vendored — only the
  framework-agnostic primitives (JWT issuance/parsing, RBAC policy,
  rate-limit accounting, header redaction, SSRF-safe outbound dialing) that
  those middlewares were built on. An embedder wiring an HTTP server can
  compose these primitives into their own middleware.
- `internal/pkg/traceid` (trivial context helper, not security-relevant on
  its own) and zap-based structured logging (replaced by the `audit.Sink`
  callback so callers can hook in whatever logger sonut uses).

## Integration points

`internal/security` (sonut's existing egress-visibility `Guard`) gained one
new file, `internal/security/enterprise.go`:

- `EnterpriseLayer` bundles `audit.Service` and `authz.Policy`.
- `NewEnterpriseLayer(EnterpriseOptions{...})` constructs one; zero-value
  options give an in-memory audit trail and an empty (deny-all) policy.
- `Guard.AttachEnterprise(layer *EnterpriseLayer)` attaches a layer to an
  existing `Guard`. **Off by default** — a `Guard` with no attached layer
  behaves exactly as it did before this change (verified by
  `TestGuardWithoutEnterpriseLayerUnaffected`).
- When attached, every `Guard.CheckOutbound` event is additionally recorded
  as an `audit.Log` entry (action `egress.allowed` / `egress.allowed_once` /
  `egress.blocked`), independent of and in addition to the existing
  `.atcode/audit/*.jsonl` `AuditLog`.
- `security.RedactHeadersJSON` and `security.ValidateOutboundHTTPURL` are
  re-exported thin wrappers over `internal/enterprise/security`, so callers
  already importing `internal/security` can reach the enterprise redaction
  and SSRF-validation helpers without a second import.

No existing exported API in `internal/security` was changed or removed; the
integration is purely additive.

## New dependencies

- `github.com/golang-jwt/jwt/v5` (direct, used only by
  `internal/enterprise/token`).

Everything else (`secretbox`, `authz`, `audit`, `ratelimit`,
`enterprise/security`) uses only the Go standard library.

## Tests

One test file per vendored capability, all passing:

- `internal/enterprise/security/security_test.go` — header redaction; SSRF
  blocking of loopback/private IPs in production; allowing public hosts;
  skipping enforcement outside production.
- `internal/enterprise/secretbox/secretbox_test.go` — encrypt/decrypt round
  trip, wrong-secret failure, empty-plaintext handling.
- `internal/enterprise/token/token_test.go` — generate/parse round trip,
  wrong-secret rejection, expired-token rejection.
- `internal/enterprise/audit/audit_test.go` — write + list round trip
  (newest-first), filter by action, sink callback observes every write.
- `internal/enterprise/authz/authz_test.go` — policy allow/deny per role,
  `Check` returning a typed `*ErrForbidden`.
- `internal/enterprise/ratelimit/ratelimit_test.go` — sliding window blocks
  after the limit, fixed window resets after TTL.
- `internal/security/enterprise_test.go` — attaching an `EnterpriseLayer` to
  a `Guard` produces an audit record on `CheckOutbound`; a `Guard` with no
  attached layer is unaffected.

Verified: `go build ./...` and
`go test ./internal/enterprise/... ./internal/security/...` both pass.
