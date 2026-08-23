# 01 – Architecture

This document describes the high-level architecture of Clumoove: the deployed components, how a
migration, synchronization, or backup flows through the system, and the resilience mechanisms that keep transfers safe.

## Cloud File Manager (Phase 1)

The authenticated file manager is a direct, profile-bound API path and is not part of the migration queue. It resolves only saved `connection_profiles`, creates a provider through the normal factory, and streams downloads and raw uploads directly to the provider. It never accepts ad-hoc credentials, remote paths in URLs, or JWTs in download URLs. Uploads use explicit manager capabilities, an exact-length stream wrapper, and a Redis per-user lease; no migration upload primitive implicitly enables an interactive mutation.

---

## 1. System Topology

Clumoove is a decoupled monorepo with separate containers for the frontend, API gateway, database,
cache, and migration/backup workers. Every job is tied to a user account and isolated from others.

```
┌────────────────────┐
│  React SPA         │
│  (frontend)        │
│  http://:3001      │
└─────────┬──────────┘
          │ HTTPS / SSE / REST
          ▼
┌────────────────────┐          ┌──────────────────────────┐
│  Go API Gateway    │◀────────▶│  PostgreSQL 15           │
│  (cmd/api) :8000   │  CRUD,    │  - users, migrations,    │
│  Auth, WS, OAuth,  │  Auth,    │    sync_jobs, sync_state,│
│  Scheduler, Sync,  │  Indexing │    backup_jobs/runs/     │
│  Backup, Restore,  │          │    snapshots/packs,      │
│  Rotation Daemon   │          │    restore_jobs/runs,    │
└─────────┬──────────┘          │    profiles, tasks,      │
          │                      │    schedules, audit_log  │
          │                      │  - also the task QUEUE   │
          │ (Redis: Pub/Sub,     └──────────────────────────┘
          │  locks, heartbeats)
          ▼
┌────────────────────┐          ┌──────────────────────────┐
│  Go Worker Engine  │◀────────▶│  Redis 7 (password auth) │
│  (cmd/worker)      │  dequeue  │  - worker:active:{id}    │
│  Dequeue, Transfer,│  via SQL  │  - worker:recovery-lock  │
│  Backup, Restore,  │           │  - schedule:lock:{id}    │
│  Recovery, Retry   │           │  - migration-control:*   │
└──────┬──────┬──────┘           │  - backup-verify:*       │
       │      │                  └──────────────────────────┘
       ▼      ▼
┌──────────────┐   ┌──────────────┐
│  Source Store│   │ Target Store │  (Nextcloud, OpenCloud, Seafile, WebDAV, Dropbox, Google, OneDrive,
└──────────────┘   └──────────────┘   S3, SMB, SFTP, FTPS, MagentaCLOUD, Koofr, Local, Mega, Immich)
```

> **Important:** The task queue runs **natively in PostgreSQL** (`SELECT … FOR UPDATE SKIP LOCKED`).
> Redis is **not** a message broker. It is used exclusively for worker liveness heartbeats,
> distributed recovery locks (`SET NX`), and cancel/bandwidth Pub/Sub events.

---

## 2. Component Responsibilities

| Component | Entrypoint | Responsibilities |
| :-------- | :--------- | :--------------- |
| **API Gateway** | `backend/cmd/api` | HTTP routing, JWT auth middleware, connection tests, file browsing, triggering indexing (immediate + scheduled), SSE streams, OAuth callbacks, OAuth rotation daemon, scheduler daemon, sync engine endpoints, backup job management, snapshot catalog queries, restore preview creation/consumption, repository verify endpoints, connection profile management, admin endpoints. |
| **Worker Engine** | `backend/cmd/worker` | Dequeue tasks via SQL, stream transfer with integrity verification, conflict resolution, retry/backoff, worker liveness, connection-loss recovery, orphan recovery, completion notifier, backup run execution (scanning, 4 MiB chunking, deduplication, 64 MiB pack uploads, snapshot publishing), retention maintenance, repository verification (`METADATA`, `BUDGETED`, `FULL`), restore preview calculations, restore run execution. |
| **PostgreSQL** | container `migration-postgres` | System of record **and** queue. Stores users, credentials (encrypted), migrations, sync jobs, sync state, backup jobs, backup runs, backup snapshots, backup packs/blocks, restore jobs, restore runs/items, connection profiles, tasks, schedules, audit log, OAuth/refresh tokens, settings. |
| **Redis** | container `migration-redis` | Liveness keys, recovery/schedule distributed locks, cancel & bandwidth Pub/Sub, rate limiting, file manager leases and download tickets. Password-protected, not exposed to host. |
| **Frontend** | container `migration-frontend` | SPA: login, connect form, file browser, live dashboard, sync view/dashboard, backup options form, snapshot browser, restore wizard/preview modal, connection profiles, file manager, settings, admin panel. |

---

## 3. Migration Lifecycle

```
 ┌──────────┐   connect/test   ┌──────────┐  start (immediate)  ┌──────────┐
 │ SCHEDULED│ ───────────────▶ │ INDEXING │ ─── creates PENDING ─▶│ RUNNING  │
 └──────────┘  (deferred time)└──────────┘      tasks           └────┬─────┘
       ▲                                                                 │
       │ triggerMigration (scheduler)                                    │ all tasks done
       │                                                                 ▼
 ┌──────────┐  PAUSED_CONNECTION_LOSS (auto)  ┌──────────┐        ┌──────────┐
 │  PAUSED  │ ◀──────────────────────────────│ COMPLETED│        │  FAILED  │
 └────┬─────┘  resume by user / recovery       └──────────┘        └──────────┘
      │
      └───────────── cancel ───────────────▶ CANCELLED
```

1. **Registration & Login** — User registers (`POST /api/auth/register`) and authenticates
   (`POST /api/auth/login`). They receive a short-lived JWT (HS256, issuer `clumoove-api`) and a
   longer-lived refresh token in an HTTP-only cookie. Optional TOTP 2FA can be enabled. For OAuth
   providers (Dropbox, Google, OneDrive, HiDrive) a separate flow via `GET /api/oauth/auth` and `/oauth/callback` exists.
2. **Connection test** — The user enters source/target credentials; the API performs a connection test
   via the provider client (`POST /api/migration/connect`). For OAuth providers the stored token is used.
3. **File browser** — Before indexing, the user can explore source (`POST /api/migration/browse`) and
   target directories (`POST /api/migration/target/browse`) and create target directories
   (`POST /api/migration/target/mkdir`).
4. **Indexing (inventory)** — After selecting paths, the API gateway recursively scans the selected
   source paths via queue-based BFS (visited-map protected against symlink cycles). Each discovered
   entry (file, calendar, contact) becomes a single task with metadata (path, size, resource type,
   source hash) in PostgreSQL.
5. **Configuration & start** — The user chooses a conflict strategy (`SKIP`, `OVERWRITE`, `RENAME`),
   target directory, thread count, and an optional bandwidth limit. `POST /api/migration/start` begins
   processing — optionally **deferred** to a later time (`scheduled_time`).
6. **Processing** — Workers dequeue tasks via `SELECT … FOR UPDATE SKIP LOCKED`. Parallelism is bounded
   by the migration's `threads` field. Transfers are streamed (no temp files on disk). Threads and
   bandwidth can be adjusted **during** a running migration.
7. **Real-time updates** — During transfer the worker reports progress to the DB; the API gateway pushes
   it over authenticated SSE (`GET /api/migration/{id}/stream`) to the live dashboard.
8. **Report** — On completion a CSV report can be downloaded (`GET /api/migration/{id}/report`) that
   includes failed tasks **and** skipped indexing errors.

For the `ftp` provider, source and target transfers are FTPS-only and files-only. The worker opens
either explicit FTPS (`ftp://host:21?tls=explicit`) or implicit FTPS (`ftps://host:990`) with normal CA,
hostname, and SNI validation. Passive data connections are egress-validated independently and remain
pinned to the validated control host; a PASV response can contribute its port but never a replacement
host address. Deployments must allow outbound TCP to the control port and the FTPS server's configured
passive data-port range; no inbound port publishing is needed.

---

## 3.1 Backup & Restore Lifecycle

### Backup Workflow

```
┌─────────────┐   trigger / cron   ┌─────────────┐   BFS scan & hash   ┌─────────────┐
│  SCHEDULED  │ ─────────────────▶ │   RUNNING   │ ──────────────────▶ │  PUBLISHING │
└─────────────┘                    └──────┬──────┘                     └──────┬──────┘
                                          │                                   │ verify packs
                                          │ cancel / connection loss          ▼
                                          ▼                             ┌─────────────┐
                                   ┌─────────────┐                      │ READY/PART. │
                                   │FAILED/PAUSED│                      └─────────────┘
                                   └─────────────┘
```

1. **Backup Definition & Schedule** — User creates a backup job (`POST /api/backup`) with source/target profile IDs, selected paths, 5-field cron expression, IANA timezone, and retention count (1–365).
2. **Run Execution** — When triggered immediately or via scheduler, an active `backup_runs` row is created. A worker claims the run via PostgreSQL advisory locks.
3. **Chunking & Deduplication** — Files are read into 4-MiB chunks, computing SHA-256 block hashes. If a block hash already exists in `backup_blocks`, the block is reused; otherwise, it is staged into a 64-MiB pack buffer.
4. **Pack Upload** — When a pack reaches 64 MiB or the run finishes, it is uploaded in immutable format v1 to `.clumoove-backup/<repo-id>/packs/<pack-id>.pack` and recorded in `backup_packs`.
5. **Snapshot Publication** — Metadata is saved to `backup_snapshots`, `backup_snapshot_items`, and `backup_snapshot_item_blocks`. The snapshot transitions to `READY` (or `PARTIAL` if non-fatal read errors occurred).
6. **Retention Pruning** — Oldest snapshots beyond `retention_count` are pruned via `backup_maintenance`, removing unreferenced blocks and unlinking orphaned pack files.

### Two-Phase Restore Workflow

```
┌──────────────────┐  POST /restore/previews   ┌──────────────────┐
│ Snapshot Browser │ ────────────────────────▶ │  Restore Preview │
└──────────────────┘                           │ (conflict check) │
                                               └────────┬─────────┘
                                                        │ POST /restore/previews/{id}/consume
                                                        ▼
┌──────────────────┐    stream blocks/packs    ┌──────────────────┐
│  Target Storage  │ ◀──────────────────────── │   Restore Run    │
└──────────────────┘                           └──────────────────┘
```

1. **Phase 1: Restore Preview (Read-Only)** — `POST /api/backup/{id}/snapshots/{snapshotID}/restore/previews` inspects the target directory, calculating conflict counts, type mismatches, directory merges, and expected skip/overwrite/rename actions. Results have a 30-minute TTL.
2. **Phase 2: Restore Run (Execution)** — `POST /api/restore/previews/{previewID}/consume` creates a `restore_runs` entry. Workers claim the restore run, fetch required blocks (using Range requests or `MAX_RESTORE_PACK_READERS` bounded pack caches), assemble target files, and apply modification timestamps.

---

## 4. Resilience & Queue Architecture

Cloud services frequently suffer connection fluctuations, so the backend is built to be extremely robust:

- **PostgreSQL-native queue (at-least-once):** Dequeue is done directly in PostgreSQL with
  `SELECT … FOR UPDATE SKIP LOCKED`. A task is atomically moved into `RUNNING`. If a worker crashes,
  `RunWorkerLiveness` resets its orphaned `RUNNING` tasks back to `PENDING` on restart.
- **Worker liveness & distributed recovery:** Each worker periodically reports its heartbeat via Redis.
  A scheduler (`RunWorkerLiveness`) detects dead workers and atomically claims their recovery lock via
  Redis `SET NX`, preventing duplicate recovery across instances.
- **Exponential backoff:** On transfer failure the worker re-schedules the task with increasing wait
  ($10 \times 3^{\text{attempt}}$ seconds → 10 s, 30 s, 90 s, max 3 attempts). Permanent errors (e.g.
  invalid OAuth token) skip retry immediately.
- **Connection-loss auto-pause (`PAUSED_CONNECTION_LOSS`):** If a service stays offline, the migration
  self-pauses (`RunConnectionRecoveryScheduler`). The worker scheduler periodically checks whether servers are
  back, then resumes migration queues from where they stopped. For sync jobs it atomically returns the job to
  `IDLE` and sets the active schedule's `next_run_at` to `NOW()`; only the API scheduler starts the fresh pass.
- **Orphaned-task recovery:** `RunOrphanedRunningTasksRecovery` detects tasks stuck in `RUNNING` for too
  long (> 10 min) and resets them to `PENDING`.
- **Retry-failed & reindex:** `POST /api/migration/{id}/retry-failed` re-enqueues failed tasks;
  `POST /api/migration/{id}/reindex` re-runs the indexing phase for a `FAILED` migration (e.g. after a
  WebDAV PROPFIND timeout).

---

## 5. Data Integrity (3-Way Hash Check)

To prevent silent data corruption, every file is mathematically verified:

1. **Source hash** — Captured before transfer via WebDAV PROPFIND (`OC-Checksums` / `getcontenthash`),
   or via a direct `GetFileHash` fallback.
2. **In-memory hash** — An `io.TeeReader` intercepts the data stream during the volatile pass through the
   worker's RAM and computes the provider-specific SHA-1/SHA-256/MD5/DROPBOX/QUICKXOR hash live. For a
   HiDrive target it also computes the native hierarchical `HIDRIVE` `chash` (4096-byte blocks and
   recursive modulo-2^160 aggregation), retaining the target-native streaming hash for verification.
3. **Target hash** — After upload, the hash of the written file is queried from the target server.
4. **Validation** — A task is complete only when the hashes match
   ($\text{Hash}_{\text{source}} \equiv \text{Hash}_{\text{worker}} \equiv \text{Hash}_{\text{target}}$).
   Where a provider exposes no usable comparable hash, the system falls back to size + timestamp comparison.

See [Backend](./02-backend.md#integrity-verification) and
[Security](./07-security.md) for details on the verification fallbacks that avoid false "corrupted"
verdicts on transient provider errors.

---

## 6. Scheduler Engine (planned & periodic)

The API gateway runs a background daemon (`scheduler.Run`) that checks for due schedules every minute
and triggers linked migration, sync, and backup jobs. Backup schedules use a five-field cron expression in
the job's IANA timezone; the API atomically queues a fenced backup run and worker replicas execute it.

- **One-shot (deferred start):** `POST /api/migration/start` with `scheduled_time` creates the
  migration in `SCHEDULED` status plus a one-shot schedule. At execution time the scheduler's
  `triggerMigration` calls `indexer.Start`, which reads the persisted `selected_paths`/`calendars`/
  `contacts` and creates `PENDING` tasks.
- **Recurring (cron):** Schedules with `cron_expression` (validated via `cron.ParseStandard`) recompute
  `next_run_at` after each run. Sync schedules instead use the linked job's persisted
  `interval_minutes` and advance relative to the current time; they leave `cron_expression` NULL so
  intervals such as 90 minutes are supported without an invalid cron minute field.
- **Overlap protection:** Before triggering, `isJobActive` checks the linked job's status. For
  migrations, `RUNNING`/`INDEXING` ⇒ skip (log + advance `next_run_at` for recurring).
- **Multi-instance safety:** Each schedule is claimed via a Redis `SET NX` lock (`schedule:lock:{id}`,
  2-min TTL), so in a multi-instance deployment only one API instance triggers a given schedule.
- **Orphaned sync-job recovery:** `scheduler.RunOrphanedSyncJobRecovery` frees sync jobs left in
  `INDEXING`/`RUNNING` after the API coordinator goroutine dies (restart/crash). Eligibility:
  `INDEXING` with `updated_at` older than 30 minutes, or `RUNNING` with stale `updated_at` **and** no
  non-terminal task updated in the last 10 minutes (avoids killing a live transfer). Resets to `IDLE`,
  sets a recovery `error_message`, advances the active schedule, and claims
  `sync:orphaned-recovery-lock` (`SET NX`) so only one API replica runs recovery per tick.
- **Orphaned migration-indexing recovery:** `scheduler.RunOrphanedMigrationIndexingRecovery` handles
  API crashes during migration indexing. After 30 minutes of staleness, migrations with a linked
  schedule return to `SCHEDULED` and reactivate that schedule for immediate retry; migrations without
  a safe scheduler trigger move to `FAILED` with a recovery message. The
  `migration:orphaned-indexing-recovery-lock` Redis lock makes the pass single-writer.
- **Failure handling:** If `triggerJob` errors (e.g. linked task deleted, migration not in `SCHEDULED`
  state), the schedule is **deactivated** to prevent an infinite retry loop.

---

## 7. Request Flow: A Deferred Migration

```
Frontend                         API                    Worker / Scheduler       PostgreSQL
─────────                       ────                   ──────────────────       ───────────
POST /migration/start ─────────▶ creates migration
  {scheduled_time}              (SCHEDULED) + schedule ──────────────────────▶ schedules row

                                                            scheduler.Run (every 1 min)
                                                            claims schedule:lock:{id}
                                                            triggerMigration ─────────▶ read migration (SCHEDULED)
                                                                 │
                                                                 ▼ indexer.Start (goroutine)
                                                                   sets INDEXING, walks paths
                                                                   creates PENDING tasks ─────▶ tasks rows
                                                                   sets RUNNING
                                                                         │
Worker dequeue (SKIP LOCKED) ◀─────────────────────────────────────────── PENDING tasks
stream source → target (hash verify)
reports progress ─────────────▶ DB counters ──▶ SSE push ────────────────▶ Frontend dashboard
```

---

## 8. Project Layout

The frontend locale files are the source of truth for UI and delivery text. The checked-in Go catalog
under `backend/internal/i18n` is generated from their `delivery.*` keys so API and worker processes use
the account's persisted language without depending on a frontend runtime.

```
clumoove/
├── backend/                 # Go module (cmd/api, cmd/worker)
│   ├── cmd/api/             # HTTP gateway, auth, SSE, OAuth, scheduler trigger, backup/restore
│   ├── cmd/worker/          # Engine (processor, recovery, backup, restore, verifiers)
│   └── internal/
│       ├── auth/            # JWT, TOTP, middleware
│       ├── backup/          # Backup coordinator, run execution, retention
│       ├── backuprepo/      # Pack writer/reader format v1, manifest catalog
│       ├── crypto/          # AES-256-GCM encrypt/decrypt
│       ├── db/              # PostgreSQL access, schema migration, audit log
│       ├── email/           # SMTP sending
│       ├── indexer/         # BFS indexing
│       ├── oauth/           # OAuth2 token refresh
│       ├── processor/       # worker liveness, retry, recovery, transfer loop
│       ├── queue/           # PostgreSQL queue, Redis locks/PubSub
│       ├── restore/         # Two-phase restore preview and run executor
│       ├── sanitize/        # filename sanitization, collision resolution
│       ├── scheduler/       # schedule engine (cron, overlap protection)
│       ├── storage/         # StorageProvider implementations + factory
│       ├── throttle/        # bandwidth throttler
│       └── totp2fa/         # TOTP generation/verification
├── frontend/                # React 19 SPA (Vite, Tailwind v4, i18n)
├── db/schema.sql            # DDL (also inline in db.go for auto-migration)
├── docker-compose.yml       # production stack (local prod build)
├── docker-compose.dev.yml   # development stack (local build)
└── .env.example             # environment variable template
```
