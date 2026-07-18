# Agent Instructions

## Package Managers
- Backend: **Go** (`go build ./...`, `go vet ./...`)
- Frontend: **npm** (`npm install`, `npm run dev`, `npm run build`)

## File-Scoped Commands
| Task | Command |
|------|---------|
| Go Typecheck/Lint | `go vet ./backend/...` |
| TS Typecheck | `npx tsc --noEmit --project frontend/tsconfig.app.json` |
| JS/TS Lint | `npx eslint frontend/src` |

## Architecture Overview
- **Two Go entrypoints**: `cmd/api` (HTTP API gateway) and `cmd/worker` (migration engine). Both share the same module `backend/`.
- **Queue**: PostgreSQL-native via `SELECT … FOR UPDATE SKIP LOCKED` in `queue.DequeueSQL()`. Redis is used **only** for worker-liveness heartbeats and distributed recovery locks (`SET NX`).
- **Worker background schedulers** (all started by `processor.Start()`):
  - `RunWorkerLiveness` — heartbeat every 10 s, detects dead workers and reclaims their tasks
  - `RunRetryScheduler` — re-enqueues tasks whose `next_retry_at <= NOW()` every 10 s
  - `RunConnectionRecoveryScheduler` — re-activates `PAUSED_CONNECTION_LOSS` migrations every 60 s
  - `RunOrphanedRunningTasksRecovery` — resets tasks stuck in `RUNNING` for > 10 min
- **OAuth daemon**: `RunOAuthRotationDaemon` in `cmd/api` rotates Dropbox/Google/Google Photos refresh tokens before expiry.
- **Core Scheduler Engine**: A background daemon in `cmd/api` (`scheduler.Run`) checks for due schedules every minute and triggers the linked job (migration/sync/backup). It uses `github.com/robfig/cron/v3` for cron parsing/next-run calculation. Schedules live in the `schedules` table; a Redis `SET NX` lock (`schedule:lock:{id}`, 2-min TTL) ensures only one API instance triggers a given schedule in a multi-instance deployment.
- **Indexer package**: `backend/internal/indexer` holds the shared indexing logic (`Indexer.Start`). Both the immediate `handleStart` path and the scheduler's `triggerMigration` call it, so scheduled migrations actually create PENDING tasks. Selected paths/calendars/contacts are persisted on the `migrations` row (`selected_paths`/`selected_calendars`/`selected_contacts` JSONB) and read at trigger time.
- **WebSocket auth**: The `/api/migration/{id}/ws` endpoint is **not** behind `AuthMiddleware`. It authenticates via a `?token=<jwt>` query parameter and performs ownership validation manually inside the handler. The handler **rejects 2FA temp tokens** (`2fa_pending` claim) — they cannot open a migration socket.
- **Transfer & conflict resolution**: Files use `SKIP` (with size-match short-circuit), `OVERWRITE` (upload to `.tmp` then atomic rename), or `RENAME` (up to 100 suffix attempts). Filename sanitization (`internal/sanitize`) and case-collision resolution run first for case-insensitive targets. Calendars/contacts are always overwritten (a `SKIP` would leave stale entries). Transfers are streamed through a RAM buffer (zero disk retention); files > 50 MB use chunked upload.
- **Data integrity (3-way hash check)**: An `io.TeeReader` computes the source-side hash while streaming (`SHA1` default; `MD5`/`SHA256`/`DROPBOX` per provider). The target hash is queried after upload. Where hashes cannot be compared, fall back to size comparison to avoid false "corrupted" verdicts.
- **Completion notifier & reports**: `RunCompletionNotifier` (worker) polls terminal migrations with `email_sent = FALSE` and sends a per-user SMTP report (HTML), and cleans expired reset/email-change tokens. `GET /api/migration/{id}/report` returns a CSV report (failed tasks + skipped indexing errors); spreadsheet formula-injection chars are neutralized server-side.

## Key Conventions

### Database
- Schema changes must be added to [schema.sql](db/schema.sql) **and** as an inline `CREATE TABLE IF NOT EXISTS` / `ALTER TABLE … ADD COLUMN IF NOT EXISTS` statement inside `InitDB()` in [db.go](backend/internal/db/db.go) for automatic migration on startup.
- All DB queries must use parameterised statements (`$1`, `$2`, …) — never string-interpolate user input into SQL.

### Storage Providers
- Every provider must implement the `StorageProvider` interface in [provider.go](backend/internal/storage/provider.go) and be registered in [factory.go](backend/internal/storage/factory.go). The interface is required (no optional methods), so every new provider — and every test mock — must implement all methods, including `SupportsAtomicRename() bool` (return `true` for providers with an atomic "upload-to-`.tmp`-then-rename" overwrite pattern, `false` for providers that cannot rename/delete such as `googlephotos`; the processor skips the temp-file + rename step for `false`). Omitting it breaks the build with `does not implement storage.StorageProvider (missing method SupportsAtomicRename)`.
- Valid provider values: `nextcloud`, `webdav`, `dropbox`, `google`, `googlephotos`, `smb`, `s3`, `sftp`, `magentacloud`, `local`. Whitelist these explicitly — never pass unvalidated provider strings to `NewProvider`. `googlephotos` is a **distinct** provider from `google` (own OAuth client with the `photoslibrary.readonly.appcreateddata` + `photoslibrary.appendonly` scopes); albums map to directories and media items to files. `local` requires no credentials (no URL/username/password); it reads/writes inside a server-side sandbox defined by `LOCAL_STORAGE_ROOT` and supports only `files` (calendars/contacts not applicable). It is shown in the UI only when `LOCAL_STORAGE_ROOT` is configured (`local_storage_enabled` in `/api/settings`); `NewProvider("local")` errors if unset.
- Resource types: `files`, `calendars`, `contacts`. Calendars/contacts are always overwritten on conflict (dynamic data — SKIP would silently leave stale entries).
- S3 Insecure HTTP endpoints (`insecure=true`) check literal IPs or `*.local`/`localhost` directly without DNS resolution to prevent DNS-rebinding SSRF. Users must use literal loopback/private IPs or local domain names.
- `googlephotos` (Google Photos Library API): albums = directories, media items = files. Photos exposes no content hash (`GetFileHash` returns empty → processor falls back to size comparison); `InspectResource` populates `Size` via a `HEAD` on the download `baseUrl`. Uploads use `POST /v1/uploads` (raw binary, `X-Goog-Upload-Protocol: raw`, upload token returned as plain text) + `mediaItems:batchCreate` carrying `uploadToken` and `fileName` (description left empty); the album is created on demand and deduplicated via an in-memory `title ↔ ID` cache. `DeleteFile`/`RenameFile` return "not supported". Credentials env: `GOOGLE_PHOTOS_CLIENT_ID` / `GOOGLE_PHOTOS_CLIENT_SECRET` (separate client from `GOOGLE_*`, scopes `photoslibrary.readonly.appcreateddata` + `photoslibrary.appendonly`).

### Security
- **Credential handling**: Never pass plaintext credentials to background goroutines. Query from database by `MigrationID` and decrypt at the last moment using `crypto.Decrypt`.
- **Error messages**: Never forward raw `err.Error()` strings for connection failures to API responses (may embed URLs with embedded credentials). Log with `log.Printf`, and return only a machine-readable `error_code` (see API Response Patterns) - never a human-readable/English message.
- **Key Segregation**: Use `ENCRYPTION_SECRET_KEY` exclusively for AES-256-GCM encryption/decryption (`crypto.Encrypt` / `crypto.Decrypt`). Use `JWT_SECRET_KEY` exclusively for JWT signing. The API server **refuses to start** if either is missing.
- **AES-256-GCM key derivation**: The raw secret is SHA-256-hashed inside `crypto.deriveKey` to produce a 32-byte key — any length secret is accepted; the hash is the actual key.
- **OAuth2 Credentials**: OAuth2 access tokens and refresh tokens are stored AES-GCM encrypted in the `migrations` table (`source_refresh_token_encrypted`, `target_refresh_token_encrypted`). The `RunOAuthRotationDaemon` in the API gateway rotates them automatically before expiry.
- **Token Rotation**: Any token refresh request must immediately invalidate the old refresh token before generating and storing a new one.
- **CORS Whitelisting**: Never reflect incoming CORS origins or allow credentials for wildcards. Use the static `allowedOrigins` map (hardcoded localhost variants + `CORS_ALLOWED_ORIGIN` env var). Unknown origins receive no `Access-Control-Allow-Origin` header.
- **Redis Security**: Redis requires password authentication (`REDIS_PASSWORD` env var). Connection string format: `redis://:$REDIS_PASSWORD@redis-queue:6379`. Redis is **not** exposed to the host network. `NewQueue` rejects empty or known-default passwords (`redis_secret`, `dev_redis_secure_pass_999`).
- **SSRF Protection**: User-supplied provider URLs are validated by `storage/ssrf.go` (`validateEgressURL` / `ValidateEgressHost`) before any egress. Loopback (`127.0.0.0/8`, `::1`) and link-local (`169.254.0.0/16`, incl. cloud metadata `169.254.169.254`) are always blocked. RFC1918/ULA private ranges are blocked only when `MIGRATION_BLOCK_PRIVATE=1` (permitted by default for self-hosted/internal servers). DNS-rebinding (TOCTOU) is mitigated by re-resolving and re-validating the address inside `egressDialer.DialContext` immediately before each connection while keeping the real hostname for TLS SNI/cert validation. Applies to `nextcloud`/`webdav`/`smb`/`sftp`.
- **Rate Limiting & Lockouts**: An in-memory fixed-window limiter (`ipRateLimiter`) keyed by client IP (honours `X-Forwarded-For` only behind a trusted proxy — see `TRUSTED_PROXY`). Limits: Login 10/min, Register 5/5min, Connect/Browse/Mkdir 30/min, TOTP 10/min, migration stream (SSE) 10/min with max 5 concurrent streams per user. 5 failed logins → 15-min lockout; 5 failed TOTP attempts → 15-min lockout (both atomic, in `db`).
- **Security Headers**: `securityHeadersMiddleware` (wrapping `corsMiddleware`) sets `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` (the OAuth callback sets its own nonce-based CSP), and `Strict-Transport-Security` over real TLS. API server timeouts: read 30s, write 60s, idle 120s.
- **Audit Logging**: A best-effort `audit_log` captures security-relevant events (logins, migration lifecycle, user management, 2FA changes, settings updates) via `db.AuditAction` constants. `ip` values are sanitized (control/CR-LF stripped, CWE-117). Writes never block the primary request.
- **2FA / TOTP**: TOTP 2FA is optional per user (`/auth/2fa/*`). Setup returns secret/QR; enable requires verifying a code + storing backup codes. Lockout after 5 failed attempts for 15 minutes (`db.IncrementTOTPFailed`). `AuthMiddlewareAllowMustChange` lets users with `must_change_password` reach the change-password endpoint. The WebSocket handler rejects 2FA temp tokens.
- **Admin Bootstrap & Roles**: Roles are `USER` (default) and `ADMIN`; admin endpoints enforce `role == ADMIN` inside the handler. `ADMIN_EMAIL`/`ADMIN_DISPLAY_NAME` create an initial ADMIN idempotently on first start; a strong random password is printed once with `must_change_password = TRUE`. An existing non-ADMIN account with the bootstrap email is **never** auto-promoted (prevents signup-based privilege escalation). Suspending a user pauses their `RUNNING`/`INDEXING` migrations and disables schedules; reactivating re-enables schedules.
- **Startup refusal contract**: The API/worker refuse to start if `ENCRYPTION_SECRET_KEY` is empty; if `JWT_SECRET_KEY` is empty, equals `ENCRYPTION_SECRET_KEY`, or is < 32 bytes; if `REDIS_PASSWORD` is empty or a known default; or if the DB DSN uses `postgres:postgres@` on a publicly reachable host.

### Multi-Tenancy & Ownership
- All endpoints operating on a specific migration (`GET /api/migration/{id}`, `DELETE /api/migration/{id}`, `GET /api/migration/{id}/report`) must call `auth.GetUserIDFromContext(r.Context())` and compare against `mig.UserID` — return `403 Forbidden` on mismatch.
- The WebSocket handler (`/api/migration/{id}/ws`) performs the same check manually using the `?token` query parameter.
- User ID is always sourced from the validated JWT claims injected by `AuthMiddleware` via `auth.ClaimsKey` context key.
- Schedule endpoints (`GET /api/schedule/{id}`, `DELETE /api/schedule/{id}`) verify ownership via `db.VerifyScheduleOwnership` (uses `EXISTS`, never returns `sql.ErrNoRows`). A non-owning result means the schedule either does not exist or belongs to another user — return `404 Not Found` in both cases to avoid leaking existence/ownership (do **not** return `403`).

### Indexing (BFS)
- Use queue-based Breadth-First Search with a `visited` map for recursive directory traversal to prevent infinite loops on symlink cycles or circular DAVs.
- Track indexed paths with a `resourceType:path` key in an `indexedPaths` map to prevent duplicate tasks.
- Indexing is resilient: a single folder/file error is recorded in `indexing_errors` and skipped rather than aborting the whole migration; per-folder errors surface in the final report.
- `sanitizeError` redacts `user:pass@` from any URL embedded in error strings before persisting to `migrations.error_message` / `indexing_errors` (see Security -> Error messages).

### Scheduler Engine
- **Schedule table**: `schedules` (id, user_id, task_type, task_id, cron_expression, run_at, next_run_at, is_active). One-shot jobs leave `cron_expression` NULL and set `run_at`/`next_run_at`; recurring jobs set `cron_expression` and compute `next_run_at` via `cron.ParseStandard`.
- **Trigger loop**: `scheduler.Run` ticks every 1 min, calls `GetDueSchedules` (`is_active = TRUE AND next_run_at <= NOW()`), claims each via Redis `SET NX` (`schedule:lock:{id}`, 2-min TTL), then `processSchedule`.
- **Overlap protection**: Before triggering, `isJobActive` checks the linked job's status. For migrations, `RUNNING`/`INDEXING` ⇒ skip (log + advance `next_run_at` for recurring). This satisfies the "90-min sync skipped at 60 min" acceptance criterion.
- **Lifecycle**: One-shot ⇒ `DeactivateSchedule` after trigger. Recurring ⇒ recompute `next_run_at` via `NextRun(cron_expression)`.
- **Failure handling**: If `triggerJob` errors (e.g. linked task deleted, migration not in `SCHEDULED` state), the schedule is **deactivated** to prevent an infinite retry loop. The user re-creates it via the API if needed.
- **Deferred migrations**: `handleStart` with `scheduled_time` creates the migration in `SCHEDULED` status + a one-shot schedule. The scheduler's `triggerMigration` calls `indexer.Start`, which reads persisted `selected_paths`/`calendars`/`contacts` and creates PENDING tasks — scheduled migrations actually execute (no silent stall in `SCHEDULED`/`INDEXING`).
- **Cron validation**: Validate user-supplied cron expressions with `scheduler.ValidateCronExpression` (wraps `cron.ParseStandard`) before persisting.

### API Response Patterns
- Use `writeJSON(w, status, data)` for all JSON responses — never write raw JSON manually.
- **Machine-readable error codes (i18n)**: Every error response carries only a machine-readable `error_code` (no human-readable text). Use the typed `APIErrorCode` enum with `writeError(w, status, code)`, `writeValidationError(w, code)` (400), or `writeConflictError(w, code)` (409). Add every new code to the `APIErrorCode` constant block AND to both frontend locale files; the backend never localizes.
- Connection-test endpoints (`/connect`, `/browse`, `/target/browse`, `mkdir`) return `HTTP 200` with `{"success": false, "error_code": "..."}` for logical failures (not `4xx`) so the frontend displays the localized message via `translateApiError`.
- Never return raw `err.Error()` strings or use `http.Error(w, msg, 4xx)` for client-facing errors - this leaks internals (e.g. URLs with embedded credentials). See Security -> Error messages.
- **CSV report safety**: The migration report neutralizes spreadsheet formula-trigger characters (`=`, `+`, `-`, `@`, tab, CR) by prefixing cells with a single quote, since paths/errors originate from the (attacker-influenced) source server.

### Internationalization (i18n)
- **Library**: Frontend uses `i18next` + `react-i18next` + `i18next-browser-languagedetector`, initialized in `src/i18n.ts`. Supported languages: `de`, `en` (fallback `en`).
- **Translation files**: `src/locales/{de,en}/translation.json`. Both files MUST stay in key parity - every key present in one must exist in the other (run a structural diff after editing either file).
- **Error codes**: Backend `error_code` values are localized under the `errors.*` namespace (e.g. `errors.MIGRATION_NOT_OWNED`). When adding a new `APIErrorCode`, add the matching `errors.<CODE>` entry to BOTH locale files. Unmapped codes fall back to `errors.UNKNOWN` via `translateApiError`.
- **Consuming error codes**: Components call `useApiError()` (from `src/utils/apiError.ts`) to obtain `translateApiError`, then call `translateApiError(data.error_code)`. Do NOT read raw `error`/`message` fields from API responses.
- **API error parsing**: Read `error_code` from the JSON body, e.g. `const body = await res.json().catch(() => ({})); translateApiError(body.error_code)`.
- **Formatting**: Locale-aware number/date/bytes formatting lives in `src/utils/format.ts` (`formatBytes`, `formatDate`, `formatDateTime`, `useFormat`). Never hand-format with `toFixed`/`toLocaleString` without passing the active language.
- **Language switcher**: `src/components/LanguageSwitcher.tsx` lets users switch; the choice is persisted to `localStorage` by the detector.

### Threads & Parallelism
- `threads` per migration is capped at 1–16 in `handleStart`. The worker respects this via the SQL dequeue query (`COUNT(*) < m.threads`).
- Worker-level concurrency is set by `MAX_THREADS` env var (default: 16, matching the max selectable per-migration threads slider). This is the total parallel tasks per worker process, not per migration.

### Retry & Backoff
- Exponential backoff: $10 \times 3^{\text{attempt}}$ seconds (10 s → 30 s → 90 s), max 3 attempts.
- Permanent errors (e.g. expired/invalid OAuth token, irrecoverable auth failure) skip retry immediately and mark the task `FAILED`.

## Documentation & Sync

The full reference lives in `/docs` (`01-architecture`, `02-backend`, `03-frontend`, `04-api-reference`, `05-storage-providers`, `06-database`, `07-security`, `08-deployment`, `09-development`). This file is the **agent-facing summary**.

**Keep both in sync.** When either changes, update the other:
- AGENTS.md must remain a **strict subset** of `/docs` — no contradictory statements.
- The **File-Scoped Commands** table here and the command block in `09-development.md` must match exactly (`go vet ./backend/...`, `npx tsc --noEmit --project frontend/tsconfig.app.json`, `npx eslint frontend/src`).
- The **Security** section (SSRF, rate limiting, security headers, audit, 2FA, admin bootstrap, startup refusal) and **Storage Providers** (whitelist, conflict resolution, sanitize) are the highest-risk areas for drift — cross-check against `07-security.md` and `05-storage-providers.md` after any change.
