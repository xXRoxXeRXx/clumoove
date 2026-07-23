# 02 – Backend

The backend is a single Go module (`backend/`) with two binary entrypoints that share the same
internal packages. It is written in Go 1.25 and uses the standard library `net/http` mux
(Go 1.22 method/pattern routing) — no external router dependencies.

---

## 1. Entrypoints

### `cmd/api` — API Gateway

Responsibilities (started in `main()`):

- Initializes PostgreSQL (`db.InitDB`) and Redis (`queue.NewQueue`).
- **Refuses to start** unless `ENCRYPTION_SECRET_KEY` and `JWT_SECRET_KEY` are set, are different, and
  the JWT key is ≥ 32 bytes.
- Supports initial administrator creation via Web UI when no users exist (`POST /api/auth/setup-admin`, see
  [Security](./07-security.md#admin-bootstrap)).
- Registers all HTTP routes on `http.NewServeMux()` (Go 1.22 patterns, e.g. `POST /api/migration/start`,
  `GET /api/migration/{id}`).
- Wraps the mux with `securityHeadersMiddleware(corsMiddleware(mux))`.
- Starts background goroutines:
  - `server.rateLimiter.evictExpired` — rate-limit map cleanup.
  - `server.RunOAuthRotationDaemon` — proactive OAuth2 token rotation.
  - `sched.Run(ctx)` — the Core Scheduler Engine.
- Graceful shutdown on `SIGINT`/`SIGTERM` (5-second window).

Key server struct fields: `db`, `queue`, `indexer`, `encryptionKey`, `jwtSecret`, `rateLimiter`,
`activeStreams`, `trustedProxy`.

### `cmd/worker` — Migration Engine

Responsibilities:

- Initializes the same DB and Redis connections.
- Builds a `processor.Processor` and calls `proc.Start(ctx)`.
- Handles `SIGINT`/`SIGTERM` → cancels context → `Start` blocks until all in-flight tasks finish
  (graceful drain).
- Worker ID format: `worker-<hostname>-<pid>`.

---

## 2. Package Overview

| Package | Purpose |
| :------ | :------ |
| `internal/auth` | JWT generation/validation (`auth.go`), TOTP helpers, HTTP middleware (`middleware.go`). |
| `internal/crypto` | AES-256-GCM `Encrypt`/`Decrypt` with SHA-256 key derivation. |
| `internal/db` | PostgreSQL access layer, `InitDB` schema migration, audit log, users, migrations, tasks, schedules, SMTP, indexing errors, admin queries. |
| `internal/email` | SMTP config + `SendMail`, HTML report rendering. |
| `internal/indexer` | BFS indexing of source paths/calendars/contacts → `PENDING` tasks. |
| `internal/oauth` | OAuth2 token refresh for Dropbox/Google; `InitConfigs`. |
| `internal/processor` | The worker loop, transfer logic, conflict resolution, hash verification, retry/backoff, liveness & recovery schedulers, completion notifier. |
| `internal/queue` | PostgreSQL dequeue (`DequeueSQL`), Redis locks, Pub/Sub for cancel/bandwidth, liveness tracking. |
| `internal/sanitize` | Filename sanitization + case-collision detection/resolution for target providers. |
| `internal/scheduler` | Core scheduler daemon (cron, overlap protection, multi-instance lock). |
| `internal/storage` | `StorageProvider` interface, provider implementations, `NewProvider` factory, SSRF egress guards. |
| `internal/throttle` | Per-migration bandwidth `MigrationThrottler` and throttled readers. |
| `internal/totp2fa` | TOTP secret generation, code verification. |

---

## 3. Database Layer (`internal/db`)

`InitDB(connStr)` opens the connection with up to 10 startup retries and runs **inline schema
migrations** so the schema self-heals on first boot:

- `CREATE TABLE IF NOT EXISTS` for `users`, `refresh_tokens`, `settings`, `schedules`, `audit_log`,
  `user_smtp_settings`, `password_reset_tokens`, `email_change_tokens`, `indexing_errors`,
  `connection_profiles`, `sync_jobs`, `sync_state`.
- `ALTER TABLE … ADD COLUMN IF NOT EXISTS` for every new column added over time (e.g.
  `user_id`, `source_provider`/`target_provider`, `resource_type`, `threads`, OAuth token columns,
  `selected_paths`/`selected_calendars`/`selected_contacts`, `bandwidth_limit_mbps`, TOTP columns,
  `sync_job_id` on `tasks`, audit columns, etc.).
- Useful indexes: `idx_migrations_user_id`, `idx_tasks_migration_status`, `idx_tasks_sync_status`,
  `idx_tasks_retry` (partial), `idx_schedules_next_run` (partial), `idx_conn_profiles_user`,
  `idx_sync_jobs_user_id`, `idx_sync_state_job`, `idx_audit_log_*`.
- Connection pool sizing derived from `MAX_THREADS` (`val*2`, min 50).
- **Default-credential rejection:** if the DB host is publicly reachable and the DSN still contains
  `postgres:postgres@`, startup fails (local/private hosts are exempted).

Key query helpers include `CreateMigration`, `GetMigration`, `UpdateMigrationStatus`,
`UpdateMigrationStatusIfIndexing`, `IncrementMigrationProgress` (transitions to `COMPLETED`/`FAILED`),
`CreateSyncJob`, `GetSyncJob`, `ListSyncJobs`, `CreateConnectionProfile`, `ListConnectionProfiles`,
`CreateTask`, `GetTask`, `UpdateTaskStatus`, `ResetMigrationForReindex` (TOCTOU-safe),
`RecordIndexingErrors`, `WriteAuditLog`, `GetDueSchedules`, `UpdateNextRunAt`, `DeactivateSchedule`,
`VerifyMigrationOwnership`, `VerifySyncOwnership`, `IsSetupRequired`, `ListUsers`, `GetGlobalStats`,
`ListAllMigrations`, `ListAllSyncs`, `ListAuditLog`, and paginated admin views.

### `StringArray` & JSONB

`db.StringArray` (`[]string`) implements `sql.Scanner`/`driver.Valuer` for seamless JSONB ↔ Go slice
conversion (used for `selected_paths`, `selected_calendars`, `selected_contacts`,
`totp_backup_codes`).

---

## 4. Queue (`internal/queue`)

The queue is **PostgreSQL-native**. `DequeueSQL` uses a CTE with `FOR UPDATE SKIP LOCKED`:

```sql
WITH available_tasks AS (
  SELECT t.id, t.migration_id
  FROM tasks t JOIN migrations m ON t.migration_id = m.id
  WHERE t.status = 'PENDING'
    AND m.status IN ('RUNNING', 'INDEXING')
    AND (SELECT COUNT(*) FROM tasks t2
         WHERE t2.migration_id = m.id AND t2.status = 'RUNNING') < m.threads
  ORDER BY t.created_at ASC
  LIMIT 1
  FOR UPDATE SKIP LOCKED
)
UPDATE tasks SET status = 'RUNNING', worker_hash = $1
WHERE id = (SELECT id FROM available_tasks)
RETURNING id, migration_id;
```

This guarantees at-least-once delivery and per-migration thread caps.

Redis is used for:

- `RegisterActiveWorker` / `GetAbandonedWorkerQueues` — liveness heartbeats (TTL 120s).
- `TryClaimWorkerRecoveryLock` — distributed recovery lock (`worker:recovery-lock:{id}`, `SET NX`).
- `TryClaimScheduleLock` — schedule trigger lock (`schedule:lock:{id}`, `SET NX`, 2-min TTL).
- `PublishCancelEvent` / `SubscribeToCancelEvents` — cancel Pub/Sub with auto-reconnect backoff.
- `PublishBandwidthChange` / `SubscribeToBandwidthChanges` — bandwidth Pub/Sub with auto-reconnect.

`NewQueue` **rejects empty or known-default passwords** (`redis_secret`, `dev_redis_secure_pass_999`).

---

## 5. Processor (`internal/processor`)

`Processor.Start(ctx)`:

1. Recovers any abandoned tasks on startup.
2. Spawns background schedulers: `RunWorkerLiveness`, `RunRetryScheduler`, `RunConnectionRecoveryScheduler`,
   `RunOrphanedRunningTasksRecovery`, `RunCompletionNotifier`.
3. Subscribes to cancel & bandwidth events (cancel invokes `activeTaskInfo.cancel()`; bandwidth updates
   the per-migration throttler).
4. Spawns `maxThreads` worker goroutines (default 16, overridden by `MAX_THREADS`) that loop over
   `DequeueSQL` and call `processTask`.

### Transfer loop (`processTask`)

For each task:

1. Load migration + throttler (per migration, kept alive for the migration lifetime).
2. Guard against paused / terminal / cancelled / non-running states (requeue or skip accordingly).
3. Decrypt credentials **at the last moment**; refresh OAuth token inline if expired/near expiry.
4. Build source & target `StorageProvider` clients.
5. **Conflict resolution** (files): `SKIP` (with size-match short-circuit), `OVERWRITE` (upload to `.tmp`
   then atomic rename), `RENAME` (up to 100 suffix attempts). Calendars/contacts are always overwritten
   (dynamic data; a `SKIP` would silently leave stale entries). Filename **sanitization** and
   **case-collision** resolution run before conflict resolution for case-insensitive targets.
6. **Stream download → upload** through a RAM buffer (zero disk retention). Files > 50 MB use chunked
   upload. Bandwidth throttling wraps the stream.
7. **Hash & integrity verification** (see below).
8. Apply metadata (modification time, description) if the target supports `MetadataApplier`.
9. Update task → `COMPLETED` and increment migration progress.

### Integrity verification

- A `io.TeeReader` computes the source-side hash while streaming (`SHA1` default; `MD5`, `SHA256`, or
  `DROPBOX` per provider). The target hash is queried after upload (retried 3× against transient
  Nextcloud errors).
- When hashes can't be compared (algorithm mismatch, WebDAV, dynamic sizes), the system falls back to
  **size comparison**; a failed *size query* is treated as success because the chunked-upload commit
  already verified size. A source-hash mismatch with a verified target size is also accepted (some
  providers report unreliable legacy checksums). This avoids false "corrupted" verdicts.

### Failure handling (`handleTaskFailure`)

- **Shutdown** (`context.Canceled`) → requeue `PENDING`.
- **Connection loss** (network errors) → migration set to `PAUSED_CONNECTION_LOSS`, task `PENDING`.
- **Auth error** (`storage.ErrAuth` or known Google strings) → migration `FAILED` immediately, task
  `FAILED`, audit log entry.
- **Permanent errors** (Google export limits, not-found, etc.) → `FAILED` immediately (no retry).
- **Transient** → exponential backoff `10, 30, 90`s, max 3 attempts; `FAILED` after exhaustion.

`RunCompletionNotifier` polls for terminal migrations with `email_sent = FALSE` and sends a per-user
SMTP report; also cleans expired reset/email-change tokens and throttlers.

---

## 6. Scheduler (`internal/scheduler`)

See [Architecture §6](./01-architecture.md#6-scheduler-engine-planned--periodic). Key points:

- `Run` ticks every 1 minute (and once on startup to catch overdue schedules).
- `processDueSchedules` claims each schedule via `TryClaimScheduleLock` (multi-instance safety).
- `processSchedule` applies overlap protection (`isJobActive`: `RUNNING`/`INDEXING`), triggers the job,
  then advances `next_run_at` (recurring) or deactivates (one-shot / trigger failure).
- `triggerMigration` verifies `SCHEDULED` state and delegates to the shared `indexer.Start` in a
  goroutine (indexing can take up to 20 min). Sync/backup triggers are placeholders for future work.

---

## 7. Indexer (`internal/indexer`)

`Indexer.Start(serverCtx, migID)`:

1. Transitions to `INDEXING` (`UpdateMigrationStatusIfIndexing`).
2. Loads the migration (including persisted `selected_paths`/`calendars`/`contacts`),
   **decrypts source credentials at the last moment**.
3. Walks each selected path/calendar/contact with `indexFolder` (BFS, visited-map to prevent cycles).
4. **Resilient indexing:** a single folder/file error is recorded in `indexErrors` and skipped rather
   than aborting the whole migration. Per-folder errors appear in the final report.
5. Persists indexing errors, updates totals, and transitions `INDEXING → RUNNING` (or `COMPLETED` if 0
   files).
6. On any fatal error, `failMigration` marks `FAILED` (with a sanitized, credential-redacted message)
   and writes an audit log entry.

`indexingTimeout()` is configurable via `INDEXING_TIMEOUT_MINUTES` (default 60).
`sanitizeError` redacts `user:pass@` from any URL embedded in error strings before persisting.

---

## 8. Storage Providers (`internal/storage`)

See [Storage Providers](./05-storage-providers.md) for the full interface and provider list.
`NewProvider` (factory) whitelists provider types, strips credentials from WebDAV/Nextcloud URLs,
applies SSRF egress validation for `nextcloud`/`webdav`/`smb`/`sftp`, and returns the concrete
implementation. `magentacloud` uses a fixed endpoint (URL ignored).

---

## 9. Crypto (`internal/crypto`)

- `deriveKey(secret)` → SHA-256 of the secret → 32-byte AES-256 key (any-length secret accepted; the
  hash is the actual key).
- `Encrypt(plainText, secretKey)` → random 12-byte nonce + AES-GCM seal, stored as `hex(nonce+cipher)`.
- `Decrypt(cipherTextHex, secretKey)` → reverse. Empty strings round-trip to empty.
- Used **only** for credential encryption (never JWT signing). See
  [Security](./07-security.md#key-segregation).

---

## 10. Auth (`internal/auth`)

- `GenerateAccessToken` — 15-minute HS256 JWT (issuer `clumoove-api`), claims: `sub`, `email`, `name`,
  `role`, `2fa_pending`, `must_change_password`.
- `Generate2FATempToken` — 5-minute JWT with `TwoFAPending = true` returned after password check when
  2FA is enabled; must be presented to `/api/auth/totp`.
- `ValidateToken` — parses/validates, rejects non-HMAC signing methods.
- `HashPassword`/`CheckPasswordHash` — bcrypt cost 12.
- `middleware.go` — `AuthMiddleware` (reads claims via `ClaimsKey`), ownership helpers, and
  `AuthMiddlewareAllowMustChange` (allows users with `must_change_password` to reach change-password).

---

## 11. Sanitize (`internal/sanitize`)

- `SanitizeFilename` — strips/replaces characters invalid on the target filesystem (returns
  `Changed`/`SanitizedName`/`Reasons`).
- `IsCaseInsensitive(provider)` — whether the target treats `File.txt` and `file.txt` as the same.
- `CheckCaseCollision` / `ResolveCollision` — detect and resolve case collisions on such targets.

---

## 12. Throttle (`internal/throttle`)

- `MigrationThrottler` — token-bucket style limiter for a migration's bandwidth (`SetLimit` updates live).
- `NewThrottledReader` / `NewUploadThrottledReader` — wrap `io.Reader` to cap bytes/sec; used on both
  download and upload streams (throttling is applied before the `TeeReader` so it limits real network
  I/O).

---

## 13. Email (`internal/email`)

- `SMTPConfig` + `SendMail` — sends mail via the user's own per-user SMTP (or system SMTP settings).
- `BuildMigrationReportEmail` — HTML migration summary used by the completion notifier.

---

## 14. TOTP 2FA (`internal/totp2fa`)

Generates TOTP secrets/QR data and verifies codes against a `crypto`/base32 secret. Wired into the auth
flow (`/api/auth/2fa/*`). Lockout after 5 failed attempts for 15 minutes is enforced in `db`
(`IncrementTOTPFailed`).

---

## 15. Configuration Contract (hard requirements)

The API/worker **refuse to start** when:

- `ENCRYPTION_SECRET_KEY` is empty.
- `JWT_SECRET_KEY` is empty, equals `ENCRYPTION_SECRET_KEY`, or is < 32 bytes.
- `REDIS_PASSWORD` is empty or a known default.
- The database DSN uses `postgres:postgres@` on a publicly reachable host.

See [Deployment](./08-deployment.md) for the full environment-variable reference.
