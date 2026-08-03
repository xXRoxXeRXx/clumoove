# 02 – Backend

The backend is a single Go module (`backend/`) with two binary entrypoints that share the same
internal packages. It is written in Go 1.25 and uses the standard library `net/http` mux
(Go 1.22 method/pattern routing) — no external router dependencies.

Immich is a files-only, one-time-migration provider. Its API key is stored as the encrypted password, URLs use the normal SSRF-safe transport, and API handlers reject calendars, contacts, non-`SKIP` target conflicts, and sync-job creation involving Immich.

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
| `internal/email` | SMTP config + `SendMail`, localized HTML delivery rendering. |
| `internal/indexer` | BFS indexing of source paths/calendars/contacts → `PENDING` tasks. |
| `internal/oauth` | OAuth2 token refresh for Dropbox/Google/OneDrive/HiDrive; `InitConfigs`. |
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
  `instance_smtp_settings`, `notification_channels`, `notification_events`,
  `notification_deliveries`, `password_reset_tokens`, `email_change_tokens`, `indexing_errors`,
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

Migration tasks are eligible while their migration is `RUNNING` or `INDEXING`.
Sync tasks are eligible only while their sync job is `RUNNING`: `INDEXING` is
reserved for listing both sides, draining and removing leftover tasks from the
previous pass, and building the new delta. Workers are notified after the sync
job transitions to `RUNNING`, so they can claim the freshly created tasks
only then. Each claimed sync pass increments `sync_jobs.run_generation`; every
task persists that value as `pass_generation`, and worker task/status/progress
updates require the matching generation. The coordinator keeps its PostgreSQL
advisory pass lock while its generation's running workers drain cancellation,
with a bounded 30-second grace period. Engine
lifecycle updates and the running-only progress reconciler compare that token,
so a stale coordinator/reconciliation read cannot finalize a successor pass without
waiting for a fallback poll.

This guarantees at-least-once delivery and per-job thread caps.

Redis is used for:

- `RegisterActiveWorker` / `GetAbandonedWorkerQueues` — liveness heartbeats (TTL 120s).
- `TryClaimWorkerRecoveryLock` — distributed recovery lock (`worker:recovery-lock:{id}`, `SET NX`).
- `TryClaimScheduleLock` — schedule trigger lock (`schedule:lock:{id}`, `SET NX`, 2-min TTL).
- `TryClaimOrphanedSyncRecoveryLock` — orphaned sync-job recovery lock (`sync:orphaned-recovery-lock`, `SET NX`).
- `PublishCancelEvent` / `SubscribeToCancelEvents` — cancel Pub/Sub with auto-reconnect backoff.
- `PublishBandwidthChange` / `SubscribeToBandwidthChanges` — migration and sync-job bandwidth Pub/Sub with auto-reconnect.

`NewQueue` **rejects empty or known-default passwords** (`redis_secret`, `dev_redis_secure_pass_999`).

---

## 5. Processor (`internal/processor`)

`Processor.Start(ctx)`:

1. Recovers any abandoned tasks on startup.
2. Spawns background schedulers: `RunWorkerLiveness`, `RunRetryScheduler`, `RunConnectionRecoveryScheduler`,
   `RunOrphanedRunningTasksRecovery`, `RunNotifier`.
   On recovered sync connectivity, the worker atomically moves the job from
   `PAUSED_CONNECTION_LOSS` to `IDLE` and sets its active schedule's `next_run_at` to `NOW()`;
   it never starts a sync-pass coordinator itself.
3. Subscribes to cancel & bandwidth events (cancel invokes `activeTaskInfo.cancel()`; bandwidth updates
   the per-migration or per-sync-job throttler).
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

- A `io.TeeReader` computes provider-specific hashes while streaming (`SHA1` default; `MD5`, `SHA256`,
  `DROPBOX`, or `QUICKXOR` per provider). For a target using a different native algorithm, its streaming
  hash is retained: `storage.NewHiDriveHasher` computes HiDrive's hierarchical `HIDRIVE` `chash`, and
  `storage.NewQuickXorHasher` computes OneDrive's `QUICKXOR`; the verifier compares either to the
  target's server-side hash.
  The target hash is queried after upload (retried 3× against transient
  Nextcloud errors).
- When hashes can't be compared (algorithm mismatch, WebDAV, dynamic sizes), the system falls back to
  **size comparison**; a failed *size query* is treated as success because the chunked-upload commit
  already verified size. A source-hash mismatch with a verified target size is also accepted (some
  providers report unreliable legacy checksums). This avoids false "corrupted" verdicts.
- The worker verifies `VERIFYING` sync passes and refreshes an expiring OAuth target token before
  constructing its target provider. The sync engine is the sole owner of final run statistics,
  `sync_state`, and the transition back to `IDLE`; after its two-minute verification deadline it
  moves the job out of `VERIFYING`, which cancels the worker verifier across processes. Verifier
  task writes are conditional on that persisted status, so an in-flight provider call cannot mutate
  tasks after the engine has aborted the verification pass.
- Migration checksum verification is single-writer across worker processes. A PostgreSQL lease and
  monotonically increasing verification generation claim each `VERIFYING` migration; every task write
  requires the active generation, an unexpired lease, and an unverified `COMPLETED` task. A stale
  verifier therefore cannot overwrite a re-copy or mutate a cancelled migration, and only the lease
  holder may reconcile/finalize the pass.

### Failure handling (`handleTaskFailure`)

- **Shutdown** (`context.Canceled`) → requeue `PENDING`.
- **Connection loss** (network errors) → migration set to `PAUSED_CONNECTION_LOSS`, task `PENDING`.
- **Auth error** (`storage.ErrAuth` or known Google strings) → migration `FAILED` immediately, task
  `FAILED`, audit log entry.
- **Permanent errors** (Google export limits, not-found, etc.) → `FAILED` immediately (no retry).
- **Transient** → exponential backoff `10, 30, 90`s, max 3 attempts; `FAILED` after exhaustion.

`RunNotifier` drains durable per-channel notification deliveries (email, Gotify, ntfy, Telegram, Discord), retries each channel independently, and cleans expired reset/email-change tokens and throttlers. Email deliveries do not snapshot SMTP credentials: the worker loads the current instance mailer immediately before sending. The legacy `email_sent` column remains for compatibility; it no longer drives delivery. It selects the recipient's persisted `users.language` value for every channel.

---

## 6. Scheduler (`internal/scheduler`)

See [Architecture §6](./01-architecture.md#6-scheduler-engine-planned--periodic). Key points:

- `Run` ticks every 1 minute (and once on startup to catch overdue schedules).
- `processDueSchedules` claims each schedule via `TryClaimScheduleLock` (multi-instance safety).
- `processSchedule` applies overlap protection (`isJobActive`: `RUNNING`/`INDEXING`/`VERIFYING`/`PAUSED_CONNECTION_LOSS`), triggers the job,
  then advances `next_run_at` (recurring) or deactivates (one-shot / trigger failure).
- `triggerMigration` verifies `SCHEDULED` state and delegates to the shared `indexer.Start` in a
  goroutine (indexing can take up to 20 min). `triggerSync` atomically claims an `IDLE`/`FAILED`
  job and starts the sync-pass coordinator; it is the exclusive starter, including after worker
  connection recovery. Backup triggers remain placeholders for future work.
- **Operations note:** connection recovery is detected by workers every 60 seconds; the API scheduler
  polls due schedules every minute. A recovered sync pass can therefore begin up to roughly one API
  scheduler interval after detection, plus normal claiming/indexing time.

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
applies SSRF egress validation for `nextcloud`/`webdav`/`smb`/`sftp`/`ftp`, and returns the concrete
implementation. `magentacloud` uses a fixed endpoint (URL ignored).

`ftp` is a files-only FTPS provider. It accepts only explicit FTPS
(`ftp://host:21?tls=explicit`) and implicit FTPS (`ftps://host:990`); cleartext FTP, URL userinfo,
certificate-validation bypasses, and custom CAs are rejected. TLS uses the system trust store with
hostname/SNI validation. Every control and passive data connection uses the SSRF-safe egress dialer;
EPSV is preferred, and PASV may supply only the data port while the validated control host remains the
data-channel destination. FTP has no portable hash API, so integrity verification uses the existing
size-comparison fallback.

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

- `SMTPConfig` + `SendMail` — sends mail through the administrator-managed instance SMTP configuration.
- Password-reset, email-change, test, and outbox paths load the singleton configuration from the database and decrypt its password only immediately before sending.
- `BuildMigrationReportEmail` — HTML migration summary used by the completion notifier.

User-facing email and notification strings are not defined in Go. They are sourced from `delivery.*` in
`frontend/src/locales/{de,en}/translation.json`; `backend/internal/i18n/translations_gen.go` is generated
from those files and is checked in for API and worker builds. The Docker build stages run
`go generate ./internal/i18n` automatically before compiling.

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
