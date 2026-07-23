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

- Logs the raw error with `log.Printf` (server-side only).
- Returns **only** a machine-readable `error_code` to the client — never raw `err.Error()` text.
- `indexer.sanitizeError` and `db`-level sanitizers redact `user:pass@` from any error string
  **before** persisting it to `migrations.error_message` / `indexing_errors`, so the report and DB
  never leak credentials.

---

## 4. OAuth2 & Token Rotation

- OAuth2 access/refresh tokens are stored AES-GCM encrypted in `migrations`
  (`source_refresh_token_encrypted`, `target_refresh_token_encrypted`).
- `RunOAuthRotationDaemon` (API gateway) proactively refreshes Dropbox/Google/Google Photos tokens before expiry.
- The worker also refreshes inline when a token is expired or within 2 minutes of expiry, using a
  per-migration mutex (`getOrCreateRefreshLock`) to serialize refreshes.
- **Token rotation:** the new token pair is encrypted and persisted **atomically** before the old
  refresh token is considered consumed. A single-use refresh token that fails to persist would otherwise
  cause a permanent auth failure, so encryption failure is fatal (aborts the task).
- The OAuth callback posts tokens to `window.opener` via `postMessage`; the receiver validates
  `event.origin` against the API origin.

---

## 5. WebSocket Authentication

`GET /api/migration/{id}/ws` is **not** behind `AuthMiddleware`. It authenticates via the `?token=<jwt>`
query parameter (or `Sec-WebSocket-Protocol`), validates ownership (`mig.UserID == claims.sub`), and
**blocks 2FA temp tokens** (a `2fa_pending` claim cannot open the migration socket).

---

## 6. CORS & Cookie Security

- `allowedOrigins` is a **static whitelist** (hardcoded localhost variants + `CORS_ALLOWED_ORIGIN` env
  var). Unknown origins receive **no** `Access-Control-Allow-Origin` header.
- `corsMiddleware` reflects credentials (`Access-Control-Allow-Credentials: true`) **only** for
  whitelisted origins; it never reflects the incoming `Origin` for unknown hosts (no wildcard +
  credentials).
- The WebSocket `CheckOrigin` enforces the same whitelist and rejects requests with no `Origin` header.

---

## 7. Redis Security

- Redis requires a password (`REDIS_PASSWORD`). Connection fails fast if the password is empty or a known
  default (`redis_secret`, `dev_redis_secure_pass_999`).
- Redis is **not** exposed to the host network (internal Docker network only).
- Used only for heartbeats, `SET NX` locks, and cancel/bandwidth Pub/Sub — never as primary storage.

---

## 8. Rate Limiting & Lockouts

In-memory fixed-window limiter (`ipRateLimiter`) keyed by client IP (honoring `X-Forwarded-For` only
behind a trusted proxy — see below). Limits:

| Endpoint group | Max / window |
| :------------- | :----------- |
| Login | 10 / 1 min |
| Register | 5 / 5 min |
| Connect/browse/mkdir | 30 / 1 min |
| TOTP | 10 / 1 min |
| Migration stream (SSE) | 10 / 1 min, max 5 concurrent streams per user |

**Account lockouts** (mirror the TOTP lockout): 5 failed logins → 15-minute lockout; 5 failed TOTP
attempts → 15-minute lockout. Both are enforced in `db` with single-statement atomic increments.

---

## 9. SSRF Protection

User-supplied provider URLs are validated before any egress (`storage/ssrf.go`):

- Loopback and link-local (incl. cloud metadata `169.254.169.254`) are **always** blocked.
- RFC1918/ULA private ranges are blocked when `MIGRATION_BLOCK_PRIVATE=1` (permitted by default because
  the tool migrates internal servers).
- DNS-rebinding (TOCTOU) is closed by re-resolving and re-validating the address inside the transport's
  `DialContext` immediately before each connection, while keeping the real hostname for TLS SNI/cert
  validation.
- S3 `insecure=true` endpoints check literal IPs / `*.local`/`localhost` directly without DNS resolution.

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
- `Strict-Transport-Security` (only over real TLS)

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
