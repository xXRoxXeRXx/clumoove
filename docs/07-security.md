# 07 – Security Model

Clumoove is built for handling third-party credentials and cross-service data movement, so security is
defense-in-depth. This document summarizes the controls; see linked sections for implementation detail.

---

## 1. Key Segregation

Two unrelated secrets are required and **must differ**:

- `ENCRYPTION_SECRET_KEY` — used **exclusively** for AES-256-GCM encryption/decryption of stored
  credentials (`crypto.Encrypt`/`Decrypt`). The raw secret is SHA-256-hashed inside `crypto.deriveKey`
  to produce the actual 32-byte key, so any-length secrets are accepted.
- `JWT_SECRET_KEY` — used **exclusively** for HS256 JWT signing/validation (`auth.GenerateAccessToken`).

The API server **refuses to start** if either is missing, if they are equal, or if the JWT key is < 32
bytes. This prevents key reuse and weak signing keys.

---

## 2. Credential Handling (Zero Plaintext)

- Usernames/passwords/OAuth tokens are encrypted with AES-256-GCM **before** being written to
  PostgreSQL.
- Plaintext credentials are **never** passed to background goroutines. The worker queries them from the
  DB by `MigrationID` and decrypts **at the last moment** (inside `processTask` / `indexer.Start`) using
  `crypto.Decrypt`, then constructs the provider client.
- The frontend holds secrets **in memory only** and clears them (`setCredentials(null)`) once the
  migration is created or when navigating away from selection/dashboard screens.
- Transfers are streamed through RAM buffers (zero on-disk retention of file contents).

---

## 3. Error Message Hygiene

Connection failures can embed URLs with credentials (`https://user:pass@host/…`). The backend:

- Logs the error through structured `slog` (server-side only) after applying log-field redaction.
- Returns **only** a machine-readable `error_code` to the client — never raw `err.Error()` text.
- `indexer.sanitizeError` and `db`-level sanitizers redact `user:pass@` from any error string
  **before** persisting it to `migrations.error_message` / `indexing_errors`, so the report and DB
  never leak credentials.

---

## 3.5 Refresh-Token Sessions

- Refresh tokens are stored only as hashes and rotate on each refresh; their seven-day expiry bounds a
  renewable login session.
- Each row has a random public session ID plus a capped User-Agent label. `GET /api/auth/sessions` exposes
  only the caller's active session metadata, never token hashes or token values.
- `DELETE /api/auth/sessions/{id}` scopes deletion to the caller's user ID and returns no cross-account
  existence information, allowing a user to revoke a lost-device session.

---

## 4. OAuth2 & Token Rotation

- OAuth2 access/refresh tokens are stored AES-GCM encrypted on migrations and sync jobs
  (`source_refresh_token_encrypted`, `target_refresh_token_encrypted`).
- `RunOAuthRotationDaemon` (API gateway) proactively refreshes Dropbox, Google, OneDrive, and HiDrive tokens before expiry.
- The worker also refreshes inline when a token is expired or within 2 minutes of expiry, using a
  per-migration mutex (`getOrCreateRefreshLock`) to serialize refreshes.
- **Token rotation:** the new token pair is encrypted and persisted **atomically** before the old
  refresh token is considered consumed. A single-use refresh token that fails to persist would otherwise
  cause a permanent auth failure, so encryption failure is fatal (aborts the task).
- The OAuth callback posts tokens to `window.opener` via `postMessage`; the receiver validates
  `event.origin` against the API origin.
- **OAuth client secrets (administrator-managed):** Google/OneDrive/Dropbox/HiDrive client credentials
  live in the `instance_oauth_providers` table, with `client_secret_encrypted` AES-256-GCM encrypted using
  `ENCRYPTION_SECRET_KEY`. They are **never** configured via `*_CLIENT_ID`/`*_CLIENT_SECRET` environment
  variables and are **never** serialized to the frontend (the admin endpoints return only
  `client_id` + `client_secret_set`). The process-local cache holds only ciphertext (30 s TTL in workers);
  the plaintext secret is decrypted **at the moment a token request is made** and never retained.

---

## 5. Migration Detail SSE Authentication

`GET /api/migration/{id}/stream` is protected by `AuthMiddleware` and accepts the JWT only through the
`Authorization: Bearer` header. It validates ownership (`mig.UserID == claims.sub`) before opening the
stream. No realtime endpoint accepts access tokens in query parameters.

---

## 6. CORS & Cookie Security

- `allowedOrigins` is a **static whitelist** (hardcoded localhost variants + `CORS_ALLOWED_ORIGIN` env
  var). Unknown origins receive **no** `Access-Control-Allow-Origin` header.
- `corsMiddleware` reflects credentials (`Access-Control-Allow-Credentials: true`) **only** for
  whitelisted origins; it never reflects the incoming `Origin` for unknown hosts (no wildcard +
  credentials).

---

## 7. Redis Security

- Redis requires a password (`REDIS_PASSWORD`). Connection fails fast if the password is empty or a known
  default (`redis_secret`, `dev_redis_secure_pass_999`).
- Redis is **not** exposed to the host network (internal Docker network only).
- Used only for heartbeats, `SET NX` locks, and cancel/bandwidth Pub/Sub — never as primary storage.

---

## 8. Rate Limiting & Lockouts

Redis-backed fixed-window limiter keyed by endpoint group and client IP (honoring `X-Forwarded-For` only
behind a trusted proxy — see below). This keeps limits consistent across API instances and prevents one
endpoint group from consuming another group's request budget. Limits:

| Endpoint group | Max / window |
| :------------- | :----------- |
| Login | 10 / 1 min |
| Register | 5 / 5 min |
| Connect/browse/mkdir | 30 / 1 min |
| Session list/revoke | 30 / 1 min |
| Migration/sync create or start | 10 / 1 min |
| TOTP | 10 / 1 min |
| Migration stream (SSE) | 60 / 1 min, max 10 concurrent streams per user |

All JSON request bodies pass through `http.MaxBytesReader` before decoding. Normal API requests are limited to 1 MiB; avatar JSON is limited to 3 MiB to support its existing 2 MiB decoded-image cap; authentication and TOTP requests are limited to 64 KiB. Malformed, oversized, trailing-data, and otherwise invalid JSON bodies return `INVALID_BODY`, except password-reset requests preserve their generic success response for anti-enumeration.

The API's normal HTTP timeouts are read 30 seconds, write 60 seconds, and idle 120 seconds. Migration list/detail and sync SSE handlers explicitly clear their per-response write deadline through `http.ResponseController`, so the normal write timeout does not terminate healthy long-lived streams; 15-second SSE comment heartbeats keep intermediary proxies from treating them as idle.

**Account lockouts** (mirror the TOTP lockout): 5 failed logins → 15-minute lockout; 5 failed TOTP
attempts → 15-minute lockout. Both are enforced in `db` with single-statement atomic increments.

TOTP setup returns recovery backup codes only once and stores bcrypt hashes. They are single-use: a code presented to disable TOTP is removed before the disable flow continues.

---

## 9. SSRF Protection

User-supplied provider URLs are validated before any egress (`storage/ssrf.go`):

- Loopback and link-local (incl. cloud metadata `169.254.169.254`) are **always** blocked.
- RFC1918/ULA private ranges are blocked when `MIGRATION_BLOCK_PRIVATE=1` (permitted by default because
  the tool migrates internal servers).
- DNS-rebinding (TOCTOU) is closed by re-resolving and re-validating the address inside the transport's
  `DialContext` immediately before each connection, while keeping the real hostname for TLS SNI/cert
  validation. The transport is bound to the configured hostname or literal IP and rejects a different
  dial target.
- User-configured provider, S3, and notification clients do not follow redirects. Configure canonical
  HTTPS and S3 regional endpoints directly.
- User-configured HTTP providers and custom S3 endpoints require HTTPS; plaintext HTTP and the former S3 `insecure=true` exception are rejected.
- `providerRegistry` is the source of truth for user-configured provider URL requirements: it marks the
  HTTPS-only providers and the providers that require SSRF egress validation. Custom S3 endpoint
  validation remains in the S3 provider because its endpoint is encoded in the `s3://` URL query.

---

## 10. Multi-Tenancy & Ownership

- Migrations are owned by a user; `status`/`start`/`pause`/`cancel`/`delete`/report endpoints enforce a
  strict ownership check via `JWT sub` vs `mig.UserID` → `403 Forbidden` on mismatch.
- Schedule endpoints (`GET`/`DELETE /schedule/{id}`) use `db.VerifyScheduleOwnership` (EXISTS-based); a
  non-owning result returns `404 Not Found` (not `403`) to avoid leaking existence/ownership.
- **Roles:** `USER` (default) and `ADMIN`. ADMIN gains instance-wide oversight (user list, all
  migrations, audit log). There is intentionally no separate `AUDITOR` role.
- Deactivating a user pauses their `RUNNING`/`INDEXING` migrations and disables their schedules;
  reactivating re-enables schedules.

---

## 11. Admin Setup Wizard

On a fresh installation where no users exist in the database (`COUNT(*) == 0`), Clumoove automatically prompts for initial administrator setup via the Web UI.

### 11.1 Workflow

1. The Web UI checks `/api/auth/setup-status` (or `/api/settings`), receiving `needs_setup: true`.
2. The user enters their Display Name, Email, and Password directly in the Web UI.
3. Submitting sends a `POST /api/auth/setup-admin` request.
4. The API server verifies `IsSetupRequired(db) == true`, creates the account with role `ADMIN`, issues access/refresh tokens, and logs the administrator in immediately.
5. Once created, any subsequent calls to `/api/auth/setup-admin` return `403 Forbidden` (`SETUP_ALREADY_COMPLETED`).

---

## 12. Security Headers & Hardening

`securityHeadersMiddleware` sets on every response:

- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`
- `Referrer-Policy: no-referrer`
- `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` (all JSON responses; the OAuth
  callback sets its own nonce-based CSP)
- `Strict-Transport-Security` (over TLS, or over `X-Forwarded-Proto: https` when
  `TRUSTED_PROXY` is enabled for a proxy that strips client-supplied forwarding headers)

API server timeouts: read 30s, write 60s, idle 120s.

---

## 13. Audit Logging

A best-effort `audit_log` captures security-relevant events (logins, migration lifecycle, user
management, 2FA changes, settings updates). `ip` values are sanitized (control/CR-LF stripped) to
prevent log injection (CWE-117). Writes never block the primary request.

---

## 14. CSV Formula Injection

The migration report (`/migration/{id}/report`) neutralizes spreadsheet formula-trigger characters
(`=`, `+`, `-`, `@`, tab, CR) by prefixing cells with a single quote, since file paths/error messages
originate from the (attacker-influenced) source server.

---

## 15. Structured Logging & Request Correlation

API and worker processes write one JSON record per log event to stdout through `slog`. Operators should
collect stdout as structured JSON rather than parsing rendered text. Each record includes the log level,
message, timestamp, `environment` from `LOG_ENVIRONMENT`, and `instance_id` from `INSTANCE_ID` when it
is configured.

- `LOG_LEVEL` controls the minimum emitted level. Valid values are `DEBUG`, `INFO`, `WARN`, and `ERROR`
  (case-insensitive); it defaults to `INFO`.
- `LOG_ENVIRONMENT` is an optional operator-defined deployment label, such as `development`, `staging`,
  or `production`. It identifies the emitting environment and does not change the configured log level.
- `INSTANCE_ID` is an optional stable identifier for one API or worker instance. Set a distinct value for
  each replica so events can be correlated with a process/container during incident response.
- Every API request receives a request ID. The ID is included in that request's structured logs and is
  returned in the `X-Request-ID` response header so support and operators can correlate a client report
  with server-side events.

Logs must never contain plaintext credentials or session material. Redact URL userinfo, `Authorization`
and `Cookie` headers, passwords, API keys, access/refresh tokens, client secrets, and equivalent values in
structured fields or error text. Do not log request or response bodies. File paths are personal metadata;
do not emit them at normal levels. `DEBUG` may include paths needed for diagnosis, so it must be enabled
only temporarily and only where operators accept that path-privacy risk.

An error is logged once at the boundary that handles it with the available operation context and request
ID. Callers propagate or wrap errors without logging the same failure again; the client still receives only
the machine-readable `error_code` described above.
