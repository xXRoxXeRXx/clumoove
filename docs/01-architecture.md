# 01 – Architecture

This document describes the high-level architecture of Clumoove: the deployed components, how a
migration flows through the system, and the resilience mechanisms that keep transfers safe.

---

## 1. System Topology

Clumoove is a decoupled monorepo with separate containers for the frontend, API gateway, database,
cache, and migration workers. Every migration is tied to a user account and isolated from others.

```
┌────────────────────┐
│  React SPA         │
│  (frontend)        │
│  http://:3001      │
└─────────┬──────────┘
          │ HTTPS / WebSocket / REST
          ▼
┌────────────────────┐          ┌──────────────────────────┐
│  Go API Gateway    │◀────────▶│  PostgreSQL 15           │
│  (cmd/api) :8000   │  CRUD,    │  - users, migrations,    │
│  Auth, WS, OAuth,  │  Auth,    │    sync_jobs, sync_state,│
│  Scheduler, Sync,  │  Indexing │    profiles, tasks,      │
│  Rotation Daemon   │          │    schedules, audit_log  │
└─────────┬──────────┘          │  - also the task QUEUE   │
          │                      └──────────────────────────┘
          │ (Redis: Pub/Sub, locks, heartbeats)
          ▼
┌────────────────────┐          ┌──────────────────────────┐
│  Go Worker Engine  │◀────────▶│  Redis 7 (password auth) │
│  (cmd/worker)      │  dequeue  │  - worker:active:{id}    │
│  Dequeue, Transfer,│  via SQL  │  - worker:recovery-lock  │
│  Recovery, Retry   │           │  - schedule:lock:{id}    │
└──────┬──────┬──────┘           │  - migration-control:*   │
       │      │                  └──────────────────────────┘
       ▼      ▼
┌──────────────┐   ┌──────────────┐
│  Source Store │   │ Target Store  │  (Nextcloud, WebDAV, Dropbox, Google,
└──────────────┘   └──────────────┘   S3, SMB, SFTP, MagentaCLOUD, Local)
```

> **Important:** The task queue runs **natively in PostgreSQL** (`SELECT … FOR UPDATE SKIP LOCKED`).
> Redis is **not** a message broker. It is used exclusively for worker liveness heartbeats,
> distributed recovery locks (`SET NX`), and cancel/bandwidth Pub/Sub events.

---

## 2. Component Responsibilities

| Component | Entrypoint | Responsibilities |
| :-------- | :--------- | :--------------- |
| **API Gateway** | `backend/cmd/api` | HTTP routing, JWT auth middleware, connection tests, file browsing, triggering indexing (immediate + scheduled), WebSocket upgrades, OAuth callbacks, OAuth rotation daemon, scheduler daemon, sync engine endpoints, connection profile management, admin endpoints. |
| **Worker Engine** | `backend/cmd/worker` | Dequeue tasks via SQL, stream transfer with integrity verification, conflict resolution, retry/backoff, worker liveness, connection-loss recovery, orphan recovery, completion notifier. |
| **PostgreSQL** | container `migration-postgres` | System of record **and** queue. Stores users, credentials (encrypted), migrations, sync jobs, sync state, connection profiles, tasks, schedules, audit log, OAuth/refresh tokens, settings. |
| **Redis** | container `migration-redis` | Liveness keys, recovery/schedule distributed locks, cancel & bandwidth Pub/Sub. Password-protected, not exposed to host. |
| **Frontend** | container `migration-frontend` | SPA: login, connect form, file browser, live dashboard, sync view/dashboard, connection profiles, settings, admin panel. |

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
   providers (Dropbox, Google) a separate flow via `GET /api/oauth/auth` and `/oauth/callback` exists.
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
   it over WebSocket (`GET /api/migration/{id}/ws`, token-secured) to the live dashboard.
8. **Report** — On completion a CSV report can be downloaded (`GET /api/migration/{id}/report`) that
   includes failed tasks **and** skipped indexing errors.

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
  self-pauses (`RunConnectionRecoveryScheduler`). The scheduler periodically checks whether servers are
  back, then resumes the queue from where it stopped.
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
   worker's RAM and computes the SHA-1/SHA-256/MD5 hash live.
3. **Target hash** — After upload, the hash of the written file is queried from the target server.
4. **Validation** — A task is complete only when the hashes match
   ($\text{Hash}_{\text{source}} \equiv \text{Hash}_{\text{worker}} \equiv \text{Hash}_{\text{target}}$).
   Where a provider exposes no usable hash, the system falls back to size + timestamp comparison.

See [Backend](./02-backend.md#integrity-verification) and
[Security](./07-security.md) for details on the verification fallbacks that avoid false "corrupted"
verdicts on transient provider errors.

---

## 6. Scheduler Engine (planned & periodic)

The API gateway runs a background daemon (`scheduler.Run`) that checks for due schedules every minute
and triggers the linked job. Schedules live in the `schedules` table.

- **One-shot (deferred start):** `POST /api/migration/start` with `scheduled_time` creates the
  migration in `SCHEDULED` status plus a one-shot schedule. At execution time the scheduler's
  `triggerMigration` calls `indexer.Start`, which reads the persisted `selected_paths`/`calendars`/
  `contacts` and creates `PENDING` tasks.
- **Recurring (cron):** Schedules with `cron_expression` (validated via `cron.ParseStandard`) recompute
  `next_run_at` after each run.
- **Overlap protection:** Before triggering, `isJobActive` checks the linked job's status. For
  migrations, `RUNNING`/`INDEXING` ⇒ skip (log + advance `next_run_at` for recurring).
- **Multi-instance safety:** Each schedule is claimed via a Redis `SET NX` lock (`schedule:lock:{id}`,
  2-min TTL), so in a multi-instance deployment only one API instance triggers a given schedule.
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
reports progress ─────────────▶ DB counters ──▶ WebSocket push ──────────▶ Frontend dashboard
```

---

## 8. Project Layout

```
migration/
├── backend/                 # Go module (cmd/api, cmd/worker)
│   ├── cmd/api/             # HTTP gateway, auth, websocket, OAuth, scheduler trigger
│   ├── cmd/worker/          # migration engine (processor, recovery schedulers)
│   └── internal/
│       ├── auth/            # JWT, TOTP, middleware
│       ├── crypto/          # AES-256-GCM encrypt/decrypt
│       ├── db/              # PostgreSQL access, schema migration, audit log
│       ├── email/           # SMTP sending
│       ├── indexer/         # BFS indexing
│       ├── oauth/           # OAuth2 token refresh
│       ├── processor/       # worker liveness, retry, recovery, transfer loop
│       ├── queue/           # PostgreSQL queue, Redis locks/PubSub
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
