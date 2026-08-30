# 02 – Backend

The backend is a single Go module (`backend/`) with two binary entrypoints that share the same
internal packages. It is written in Go 1.25 and uses the standard library `net/http` mux
(Go 1.22 method/pattern routing) — no external router dependencies.

Immich is a files-only, one-time-migration provider. Its API key is stored as the encrypted password, URLs use the normal SSRF-safe transport, and API handlers reject calendars, contacts, non-`SKIP` target conflicts, and sync/backup/restore job creation involving Immich.

## Cloud File Manager (Phase 1)

`cmd/api/file_profile_resolver.go` resolves an owned saved profile fail-closed, decrypts its credentials with the field-domain AAD, refreshes expiring OAuth access tokens, scopes Local access to the JWT user, and constructs the provider through `storage.NewProvider`. `file_handlers.go` seals user/profile-bound references and cursors with independent crypto domains, resolves stored quick-link paths to sealed breadcrumbs, issues Redis-backed one-time download tickets, and accepts raw `PUT` upload bodies only after metadata validation. `ExactSizeReader` rejects short or trailing bodies without buffering; an upload lease is acquired before the body is read and released on every exit path. The manager capability registry is intentionally separate from `StorageProvider`: each provider opts into browse, download, upload, directory creation, and thumbnails through its tested, dedicated manager contracts. The profile capability endpoint is the source of truth; Immich is read-only, and Local is unavailable on Windows.

---

## 1. Entrypoints

### `cmd/api` — API Gateway

Responsibilities (started in `main()`):

- Initializes PostgreSQL (`db.InitDB`) and Redis (`queue.NewQueue`).
- **Refuses to start** unless `ENCRYPTION_SECRET_KEY` and `JWT_SECRET_KEY` are set, are different, and
  the JWT key is ≥ 32 bytes.
- Supports initial administrator creation via Web UI when no users exist (`POST /api/auth/setup-admin`, see
  [Security](./07-security.md#11-admin-setup-wizard)).
- Registers all HTTP routes on `http.NewServeMux()` (Go 1.22 patterns, e.g. `POST /api/migration/start`,
  `GET /api/migration/{id}`, `POST /api/backup`, `GET /api/backup/{id}`).
- Wraps the mux with `securityHeadersMiddleware(corsMiddleware(mux))`.
- Applies Redis-backed rate limits with atomic `INCR` plus `PEXPIRE`; there is no process-local
  rate-limit eviction goroutine.
- Starts background goroutines:
  - `server.RunOAuthRotationDaemon` — proactive OAuth2 token rotation.
  - `sched.Run(ctx)` — the Core Scheduler Engine.
- Graceful shutdown on `SIGINT`/`SIGTERM` (5-second window).

- `server.runGarbageCollector` deletes terminal migrations and their cascaded task histories after 30 days; it runs hourly and stops with the API context.

Key server struct fields: `db`, `queue`, `indexer`, `encryptionKey`, `jwtSecret`, `rateLimiter`,
`activeStreams`, `trustedProxy`.

### `cmd/worker` — Migration, Sync, Backup & Restore Engine

Responsibilities:

- Initializes the same DB and Redis connections.
- Builds a `processor.Processor` and calls `proc.Start(ctx)`.
- Starts dedicated backup and restore coordinators in addition to the processor.
- On `SIGINT`/`SIGTERM`, cancels the shared context. `proc.Start` drains transfer workers and the
  verification dispatcher; the independently started backup and restore coordinators receive the same
  cancellation but are not joined by the processor's wait group.
- Worker ID format: `worker-<hostname>-<pid>`.

---

## 2. Package Overview

| Package | Purpose |
| :------ | :------ |
| `internal/auth` | JWT generation/validation (`auth.go`), TOTP helpers, HTTP middleware (`middleware.go`). |
| `internal/backup` | Backup coordinator, run execution, snapshot publishing, retention management, repository verification. |
| `internal/backuprepo` | Clumoove backup pack format v1 reader/writer, manifest repository abstraction, block catalog. |
| `internal/config` | Shared process configuration defaults and environment-resolution helpers. |
| `internal/crypto` | AES-256-GCM `Encrypt`/`Decrypt` with SHA-256 key derivation. |
| `internal/db` | PostgreSQL access layer, `InitDB` schema migration, audit log, users, migrations, tasks, schedules, backup jobs/runs/snapshots/blocks/packs, restore previews/jobs/runs, SMTP, indexing errors, admin queries. |
| `internal/email` | SMTP config + `SendMail`, localized HTML delivery rendering. |
| `internal/httpresp` | Shared JSON response envelope and machine-readable API error helpers. |
| `internal/i18n` | Backend view of the frontend delivery-translation catalog. |
| `internal/indexer` | BFS indexing of source paths/calendars/contacts → `PENDING` tasks. |
| `internal/megasecret` | Encryption and lifecycle helpers for reusable MEGA session material. |
| `internal/notify` | Validation and delivery for email, Gotify, ntfy, Telegram, and Discord notification channels. |
| `internal/oauth` | OAuth2 token refresh for Dropbox/Google/OneDrive/HiDrive; `Configure`/`NewDBLoader` installs a DB-backed credential loader with a 30s ciphertext cache; secrets are decrypted only at token-request time. |
| `internal/observability` | Structured JSON logging, request IDs, and privacy-preserving redaction. |
| `internal/processor` | The worker loop, transfer logic, conflict resolution, hash verification, retry/backoff, liveness & recovery schedulers, completion notifier. |
| `internal/queue` | PostgreSQL dequeue (`DequeueSQL`), Redis locks, Pub/Sub for cancel/bandwidth, liveness tracking. |
| `internal/restore` | Two-phase restore coordinator (read-only conflict preview + execute restore runs). |
| `internal/sanitize` | Filename sanitization + case-collision detection/resolution for target providers. |
| `internal/scheduler` | Core scheduler daemon (cron, overlap protection, multi-instance lock). |
| `internal/storage` | `StorageProvider` interface, provider implementations, `NewProvider` factory, SSRF egress guards. |
| `internal/sync` | API-owned, generation-fenced sync-pass coordinator: scans both sides, computes deltas, enqueues tasks, waits for workers, and commits `sync_state`. |
| `internal/throttle` | Per-migration bandwidth `MigrationThrottler` and throttled readers. |
| `internal/totp2fa` | TOTP secret generation, code verification. |

---

## 3. Database Layer (`internal/db`)

`InitDB(connStr)` opens the connection with up to 10 startup retries and runs **inline schema
migrations** so the schema self-heals on first boot:

- `CREATE TABLE IF NOT EXISTS` for `users`, `migrations`, `tasks`, `refresh_tokens`, `settings`, `schedules`, `audit_log`,
  `instance_smtp_settings`, `instance_oauth_providers`, `notification_channels`, `notification_events`,
  `notification_deliveries`, `password_reset_tokens`, `email_change_tokens`, `indexing_errors`,
  `connection_profiles`, `sync_jobs`, `sync_state`, `backup_jobs`, `backup_runs`, `backup_snapshots`,
  `backup_packs`, `backup_blocks`, `backup_snapshot_items`, `backup_snapshot_item_blocks`,
  `backup_maintenance`, `backup_verify_targets`, `restore_previews`, `restore_jobs`, `restore_runs`,
  `restore_items`, `restore_path_reservations`, `restore_item_blocks`, `restore_pack_pins`.
- `ALTER TABLE … ADD COLUMN IF NOT EXISTS` for every new column added over time (e.g.
  `user_id`, `source_provider`/`target_provider`, `resource_type`, `threads`, OAuth token columns,
  `selected_paths`/`selected_calendars`/`selected_contacts`, `bandwidth_limit_mbps`, TOTP columns,
  `sync_job_id` on `tasks`, audit columns, etc.).
- Useful indexes include `idx_migrations_user_id`, `idx_tasks_migration_status`,
  `idx_tasks_sync_gen_status`, `idx_tasks_retry` (partial), `idx_schedules_next_run` (partial),
  `idx_conn_profiles_user`, `idx_sync_jobs_user_id`, `idx_sync_state_job`,
  `idx_backup_jobs_user_created`, `idx_backup_runs_job_created`,
  `idx_backup_snapshots_job_created`, `idx_backup_blocks_job_pack`,
  `idx_restore_previews_owner`, and `idx_audit_log_*`.
- Connection pool sizing derived from `MAX_THREADS` (`val*2`, min 50).
- **Default-credential rejection:** if the DB host is publicly reachable and the DSN still contains
  `postgres:postgres@`, startup fails (local/private hosts are exempted).

Key query helpers include `CreateMigration`, `GetMigration`, `UpdateMigrationStatus`,
`UpdateMigrationStatusIfIndexing`, `IncrementMigrationProgress` (transitions to `COMPLETED`/`FAILED`),
`CreateSyncJob`, `GetSyncJob`, `ListSyncJobs`, `CreateConnectionProfile`, `ListConnectionProfiles`,
`CreateBackupJob`, `GetBackupJob`, `ListBackupJobs`, `CreateBackupRun`, `GetBackupRun`, `ListBackupSnapshots`,
`CreateRestorePreview`, `GetRestorePreview`, `CreateRestoreRun`, `GetRestoreRun`,
`CreateTask`, `GetTask`, `UpdateTaskStatus`, `ResetMigrationForReindex` (TOCTOU-safe),
`RecordIndexingErrors`, `WriteAuditLog`, `GetDueSchedules`, `UpdateNextRunAt`, `DeactivateSchedule`,
`VerifyMigrationOwnership`, `VerifySyncOwnership`, `VerifyBackupOwnership`, `IsSetupRequired`, `ListUsers`, `GetGlobalStats`,
`ListAllMigrations`, `ListAllSyncs`, `ListAuditLog`, and paginated admin views.

### `StringArray` & JSONB

`db.StringArray` (`[]string`) implements `sql.Scanner`/`driver.Valuer` for seamless JSONB ↔ Go slice
conversion (used for `selected_paths`, `selected_calendars`, `selected_contacts`,
`totp_backup_codes`).

---

## 4. Queue (`internal/queue`)

The queue is **PostgreSQL-native**. Each `DequeueSQL` call uses one transaction to:

1. Mark a pending sync upload as `SKIPPED` when its required `conflict_copy` rename prerequisite
   has already failed, been cancelled, or been skipped.
2. Select one eligible pending migration or sync task with `FOR UPDATE SKIP LOCKED`. Migration tasks
   are candidates in `RUNNING` or `INDEXING`; sync tasks are candidates only in `RUNNING` and only for
   the current `run_generation`. A dependent rename upload is excluded until its exact prerequisite
   completed.
3. Lock the selected task's parent migration or sync job with `FOR UPDATE`, then re-check the parent
   status and its running-task capacity. Sync claims also re-check the selected task's generation.
4. Atomically set the claimed task to `RUNNING`, persist `worker_hash`, and increment `claim_epoch`.

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
waiting for a fallback poll. The coordinator commits final task statistics, the
reconciled `sync_state` baseline, and the transition back to `IDLE` in one
PostgreSQL transaction; a baseline-write failure rolls back the success result
and the pass is marked failed for a recoverable retry.

This guarantees at-least-once delivery and per-job thread caps.

After inserting pending tasks, `NotifyTaskAvailable` sends PostgreSQL `NOTIFY task_available`. Idle
worker threads keep a dedicated `LISTEN` connection and wake immediately when notified. The listener is
only a latency optimization: workers continue to poll every five seconds with a healthy listener and
every two seconds if `LISTEN` cannot be established.

Redis is used for:

- `RegisterActiveWorker` / `GetAbandonedWorkerQueues` — liveness heartbeats (TTL 120s).
- `TryClaimWorkerRecoveryLock` — distributed recovery lock (`worker:recovery-lock:{id}`, `SET NX`).
- `TryClaimScheduleLock` — schedule trigger lock (`schedule:lock:{id}`, `SET NX`, 2-min TTL).
- `TryClaimOrphanedSyncRecoveryLock` — orphaned sync-job recovery lock (`sync:orphaned-recovery-lock`, `SET NX`).
- `TryClaimOAuthLock` — per-job/role OAuth refresh lock (`oauth:lock:{type}:{id}:{role}`, `SET NX`).
- `PublishCancelEvent` / `SubscribeToCancelEvents` — cancel Pub/Sub with auto-reconnect backoff.
- `PublishBandwidthChange` / `SubscribeToBandwidthChanges` — migration and sync-job bandwidth Pub/Sub with auto-reconnect.
- The API's distributed rate limiter, using atomic Redis counters with key TTLs.
- File-manager one-time download tickets and per-user upload/download stream leases.

`NewQueue` **rejects empty or known-default passwords** (`redis_secret`, `dev_redis_secure_pass_999`).

---

## 5. Processor (`internal/processor`)

`Processor.Start(ctx)`:

1. Recovers any abandoned tasks on startup.
2. Spawns background schedulers: `RunWorkerLiveness`, `RunRetryScheduler`, `RunConnectionRecoveryScheduler`,
   `RunOrphanedRunningTasksRecovery`, `RunNotifier`, `RunProgressReconciler`, and `RunChecksumVerifier`.
   On recovered sync connectivity, the worker atomically moves the job from
   `PAUSED_CONNECTION_LOSS` to `IDLE` and sets its active schedule's `next_run_at` to `NOW()`;
   it never starts a sync-pass coordinator itself. Connection recovery runs every 60 seconds and probes
   at most 10 paused migrations and 10 paused sync jobs per tick. Independent round-robin cursors
   distribute probes across each population, and any failed decrypt, provider creation, or connection
   check enters increasing recovery backoff.
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

`RunNotifier` drains durable per-channel notification deliveries (email, Gotify, ntfy, Telegram, Discord), retries each channel independently, and cleans expired reset/email-change tokens and throttlers. When an instance mailer exists, completion email is enabled by default unless a user has explicitly saved an email preference (including an opt-out). Email deliveries do not snapshot SMTP credentials: the worker loads the current instance mailer immediately before sending. The legacy `email_sent` column remains for compatibility; it no longer drives delivery. It selects the recipient's persisted `users.language` value for every channel.

### Restore and repository verification

Restore previews are worker-owned, read-only target-tree enumerations. They persist only aggregate
conflict statistics and at most 100 sanitized examples, expire after 30 minutes, and are consumed once.
A preview may use an owned profile or a direct target connection; every direct password/access token,
refresh token, and MEGA session is encrypted immediately. Consumption copies those secrets into the
active `restore_runs` row only. Terminalization clears them atomically with pack-pin release and the
restore notification event. Retry previews explicitly identify a terminal restore job and must reproduce
the server-computed configuration fingerprint.

Restore item claims carry a worker ID, epoch, and deadline. Item status/path/progress writes fence on
that epoch, stale claims are reclaimed, and path reservations serialize case-insensitive target names
across workers. Repository checks similarly persist a maintenance lease plus a per-pack target epoch,
deadline, and cursor. An expired lease is resumable: copied locator evidence and live pack pins remain
until the check reaches a terminal state.

---

## 6. Scheduler (`internal/scheduler`)

See [Architecture §6](./01-architecture.md#6-scheduler-engine-planned--periodic). Key points:

- `Run` ticks every 1 minute (and once on startup to catch overdue schedules).
- `processDueSchedules` claims each schedule via `TryClaimScheduleLock` (multi-instance safety).
- `processSchedule` applies overlap protection (`isJobActive`: `RUNNING`/`INDEXING`/`VERIFYING`/`PAUSED_CONNECTION_LOSS`), triggers the job,
  then advances `next_run_at` (recurring) or deactivates (one-shot / any trigger failure).
- `triggerMigration` verifies `SCHEDULED` state and delegates to the shared `indexer.Start` in a
  goroutine (indexing can take up to 20 min). `triggerSync` atomically claims an `IDLE`/`FAILED`
  job and starts the sync-pass coordinator; it is the exclusive starter, including after worker
  connection recovery. Backup triggers atomically create a queued backup run; credentials and repository
  access remain worker-only.
- `RunOrphanedMigrationIndexingRecovery` repairs API-crash leftovers: stale scheduled migrations are
  made due for a fresh `SCHEDULED` attempt, while stale immediate migrations become visibly `FAILED`.
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

`indexingTimeout()` is configurable via `INDEXING_TIMEOUT_MINUTES` (default 20).
`sanitizeError` redacts `user:pass@` from any URL embedded in error strings before persisting.

---

## 8. Storage Providers (`internal/storage`)

See [Storage Providers](./05-storage-providers.md) for the full interface and provider list.
`NewProvider` (factory) whitelists provider types, strips credentials from WebDAV/Nextcloud URLs,
applies SSRF egress validation for `nextcloud`/`opencloud`/`webdav`/`smb`/`sftp`/`ftp`/`immich`/`seafile`, and returns the concrete
implementation. `magentacloud` and `koofr` use fixed endpoints (URL ignored). Koofr selects its primary mount during connection, accepts only an email/username plus application password, is files-only, and uses its MD5 response hash for cryptographic verification.

`ftp` is a files-only FTPS provider. It accepts only explicit FTPS
(`ftp://host:21?tls=explicit`) and implicit FTPS (`ftps://host:990`); cleartext FTP, URL userinfo,
certificate-validation bypasses, and custom CAs are rejected. TLS uses the system trust store with
hostname/SNI validation. Every control and passive data connection uses the SSRF-safe egress dialer;
EPSV is preferred, and PASV may supply only the data port while the validated control host remains the
data-channel destination. FTP has no portable hash API, so integrity verification uses the existing
size-comparison fallback.

---

## 8.1 Backup & Restore Engines (`internal/backup`, `internal/backuprepo`, `internal/restore`)

### Backup Engine (`internal/backup` & `internal/backuprepo`)
- `backup.Coordinator` manages repository setup, run executions, and retention maintenance.
- Pack format: immutable 64 MiB binary packs (`.clumoove-backup/<repository-id>/packs/<pack-id>.pack`) with Magic Header (`CLMBKP01`), header length, footer index containing SHA-256 block hash table, block offsets, and lengths.
- Chunking: fixed 4 MiB streaming chunks with SHA-256 hashing. Identical blocks across runs/files are deduplicated against `backup_blocks`.
- Snapshots: atomic publishing of item catalog (`backup_snapshot_items`) and block mapping (`backup_snapshot_item_blocks`).
- Retention: FIFO snapshot pruning keeping `retention_count` latest snapshots. Reclaims unreferenced blocks and unlinks deleted packs.

### Restore Engine (`internal/restore`)
- `restore.Coordinator` executes two-phase restore operations.
- Preview phase: read-only evaluation of target tree conflicts (matching size/mtime, type collisions, directory merges).
- Execution phase: reads required blocks from packs (via HTTP Range requests or bounded `MAX_RESTORE_PACK_READERS` buffers), recreates file hierarchies, and sets original modification timestamps.

---

## 9. Crypto (`internal/crypto`)

- `deriveKey(secret)` → SHA-256 of the secret → 32-byte AES-256 key (any-length secret accepted; the
  hash is the actual key).
- `EncryptWithDomain(plainText, secretKey, domain)` → random 12-byte nonce + AES-GCM seal with the
  stable persisted-field domain as authenticated additional data (AAD), stored as
  `v1:hex(nonce+cipher)`. The `v1:` prefix leaves room for future key-rotation formats.
- `DecryptBytesWithDomain(cipherText, secretKey, domain)` → reverse and returns the actual GCM
  plaintext buffer for callers to clear. It can read the legacy unversioned `hex(nonce+cipher)` envelope
  (which had no AAD) so existing rows remain usable. Empty strings round-trip to empty.
- Used **only** for credential encryption (never JWT signing). See
  [Security](./07-security.md#1-key-segregation).

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

### Additional auth guarantees

JWT validation also requires issuer `clumoove-api` and allows 30 seconds of clock skew. Passwords are limited to bcrypt's 72-byte input maximum at every API creation, reset, and change boundary, and in `HashPassword` for non-HTTP callers.

## 11. Sanitize (`internal/sanitize`)

- `SanitizeFilename` — strips/replaces characters invalid on the target filesystem (returns
  `Changed`/`SanitizedName`/`Reasons`).
- `IsCaseInsensitive(provider)` — whether the target treats `File.txt` and `file.txt` as the same.
- `CheckCaseCollision` / `ResolveCollision` — detect and resolve case collisions on such targets.

---

## 12. Throttle (`internal/throttle`)

The configured limit applies independently and in full to download and upload traffic; `0` is
unlimited in both directions. Throttled readers wait before consuming source bytes and bound each
read to the limiter burst size.

- `MigrationThrottler` — token-bucket style limiter for a migration's bandwidth (`SetLimit` updates live).
- `NewThrottledReader` / `NewUploadThrottledReader` — wrap `io.Reader` to cap bytes/sec; used on both
  download and upload streams (throttling is applied before the `TeeReader` so it limits real network
  I/O).

---

## 13. Email (`internal/email`)

- `SMTPConfig` + `SendMail` — sends mail through the administrator-managed instance SMTP configuration.
- Password-reset, email-change, test, and outbox paths load the singleton configuration from the database and decrypt its password only immediately before sending.
- SMTP requires `tls` or `starttls`, rejects non-public destinations (including CGNAT and benchmarking ranges), and requires TLS 1.2 or later.

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

Both API and worker refuse to start when `ENCRYPTION_SECRET_KEY` is empty, the effective Redis password
(from `REDIS_URL` or the `REDIS_PASSWORD` fallback) is empty or a known default, or the database DSN uses
`postgres:postgres@` on a publicly reachable host. The API additionally refuses to start when
`JWT_SECRET_KEY` is empty, equals `ENCRYPTION_SECRET_KEY`, or is shorter than 32 bytes; the worker does not
use a JWT key.

See [Deployment](./08-deployment.md) for the full environment-variable reference.
