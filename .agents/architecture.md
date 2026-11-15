# Architecture & System Design

## Overview
Clumoove is a decoupled monorepo with separate entrypoints for the HTTP API gateway and the background worker engine. Both share the Go module `backend/`.

## Entrypoints
- **API Gateway (`backend/cmd/api`)**: HTTP routing, JWT auth, connection testing, file browsing, manual/scheduled indexing triggers, SSE streams, OAuth callbacks & token rotation daemon, scheduler daemon, sync-pass coordination, backup/restore APIs, connection profiles, admin endpoints.
- **Worker Engine (`backend/cmd/worker`)**: Task dequeuing via PostgreSQL (`SELECT … FOR UPDATE SKIP LOCKED`), streaming transfers, integrity verification, conflict resolution, retry/backoff, backup run execution, restore execution, repository maintenance checks.

## Worker Runtime Loops
Run by the worker runtime (`cmd/worker/runtime.go`, `runWorker`) alongside `Processor.Start(ctx)`:

| Loop / Module | Interval | Responsibility |
|-----------|----------|----------------|
| `worker_liveness` (`cmd/worker/maintenance.go`) | 10 s | Sends heartbeat so other workers can detect dead instances. |
| `retry_scheduler` (`cmd/worker/maintenance.go`) | 10 s | Re-enqueues failed tasks whose `next_retry_at <= NOW()`. |
| `connection_recovery` (`cmd/worker/maintenance.go`) | 60 s | Probes up to 10 round-robin `PAUSED_CONNECTION_LOSS` migrations and 10 sync jobs (with failure backoff). Re-activates migrations; transitions sync jobs to `IDLE` making active schedule due (API scheduler exclusively starts sync passes). |
| `orphaned_task_recovery` (`cmd/worker/maintenance.go`) | 5 min | Resets tasks stuck in `RUNNING` status for > 10 minutes. |
| `notifier` (`cmd/worker/notifications.go`) | 10 s / 1 h | Drains durable per-channel notification deliveries (email, Gotify, ntfy, Telegram, Discord) and cleans expired account tokens. |
| `progress_reconciler` (`cmd/worker/maintenance.go`) | 60 s / 5 min | Reconciles migration and sync task progress against database totals and repairs missing completion notifications. |
| `checksum_verifier` (`cmd/worker/verification.go`) | 10 s | Schedules and runs post-transfer verification for `VERIFYING` jobs on shared `ProviderSlots`; PostgreSQL lease/generation fencing ensures a single-writer verification pass. |
| `abandoned_worker_cleanup` (`cmd/worker/maintenance.go`) | 60 s | Claims expired dead-worker recovery locks and reclaims orphaned tasks. |
| Control subscribers (`cmd/worker/control.go`) | Event | Migration cancel, sync cancel and bandwidth-change control channels (`migration_cancel_events`, `sync_cancel_events`, `bandwidth_events`). |

## Backup & Restore Engines
- **Backup Coordinator (`backup.Coordinator` / `backuprepo.Repository`)**:
  - Executes claimed `backup_runs` guarded by PostgreSQL advisory locks.
  - Chunks files into 4-MiB SHA-256 blocks, deduplicating against `backup_blocks`.
  - Writes immutable 64-MiB v1 packs to `.clumoove-backup/<repository-id>/packs/`.
  - Indexes `backup_snapshot_items`, transitions to `READY`/`PARTIAL` after pack validation.
  - Prunes oldest snapshots exceeding `retention_count` (1–365) via `backup_maintenance`.
- **Restore Coordinator (`restore.Coordinator`)**:
  - Two-phase restore workflow:
    1. **Preview (`POST /api/backup/{id}/snapshots/{snapshotID}/restore/previews`)**: Mandatory read-only preview with conflict counters, fingerprinting, and sample conflict items.
    2. **Execution (`POST /api/restore/previews/{previewID}/consume`)**: Streams blocks (via Range reads or `MAX_RESTORE_PACK_READERS` bounded pack buffers) to destination with case-insensitive path reservations.
- **Runtime lifecycle (`cmd/worker/runtime.go`, `runWorker`)**: One module owns every long-running worker loop: the backup and restore coordinators plus the runtime-owned maintenance schedulers (`cmd/worker/maintenance.go`), notifier (`cmd/worker/notifications.go`), verifier (`cmd/worker/verification.go`), and Redis control subscribers (`cmd/worker/control.go`). `main.go` loads config, builds dependencies (including shared `ProviderSlots` and `Processor` which only exposes `Start(ctx)` to `runtime.go` plus transfer control methods for `control.go`), wires signals and calls `runWorker`, which runs startup abandoned-task recovery, starts all loops and the processor, and releases the lease at the end. All loops share the worker context and run under `loopSupervisor`: a loop stops cleanly only by returning `context.Canceled` (or nil) after that context is cancelled; any other return, before or during shutdown, is a terminal failure. Backup and restore retry transient poll errors with backoff, so only cancellation normally ends them. On failure the supervisor logs `coordinator_stopped` (`error_kind=coordinator_failure`) and cancels the worker context (in-flight tasks drain, the liveness heartbeat stops). After the processor drain and every loop have stopped, the worker deletes its `worker:active:<id>` lease (safe: no task is running and the heartbeat cannot re-register it) so other workers can reclaim leftovers immediately, then exits 1 on failure (platform restarts it) or 0 on graceful shutdown.
- **Repository Checks (`backup_maintenance`, `backup_verify_targets`)**:
  - Supports `METADATA`, `BUDGETED` (64 MiB–1 TiB), or `FULL` checks with pack pins and live SSE progress.

## Daemon Processes in API
- **OAuth Token Rotation (`RunOAuthRotationDaemon`)**: Rotates Dropbox, Google, OneDrive, and HiDrive refresh tokens for active migrations, sync jobs, and restore runs before expiry. (Backup runs coordinate refresh separately).
- **Garbage Collector (`runGarbageCollector`)**: Runs hourly; deletes terminal migrations and cascaded task histories after 30 days. Stops cleanly with API context.

## Public Website & Frontend Deployment
- **Public Website (`website/`)**: Independent static Vite package served separately from the authenticated SPA. In production, public host must never be permitted by application CORS or proxy authenticated API routes.
- **Frontend SPA Deployment**:
  - SPA is built before container start.
  - `CLUMOOVE_API_URL` is validated as an origin-only HTTP(S) URL at nginx startup, injected via `/runtime-config.js`, and emitted as the sole additional CSP `connect-src` source.
  - If unset, same-origin `/api` proxying is assumed. If cross-origin, it must match API `CORS_ALLOWED_ORIGIN`.

## Cloud File Manager
- **Access & Profiles**: Routes accept only saved, owned `connection_profiles` via `storage.NewProvider`.
- **Security & Identifiers**: Uses opaque AES-GCM references/cursors binding user and profile. Downloads use single-use Redis tickets (never JWTs or raw paths in URLs). Previews reuse download tickets and never leak refs into application history.
- **Capability Isolation**: `ManagerCapabilities` is independent of `StorageProvider`. Deletion requires `ManagerDeleter`, accepts only sealed item references, and strictly rejects root locators.
- **Move / Copy / Rename**:
  - Sealed same-profile source/destination references only.
  - Validates roots, no-ops, and directory cycle hazards. Requires explicit conflict retry (`SKIP`, `OVERWRITE`, `RENAME`).
  - Copy uses native provider operations where available; approved fallback providers (Local, SMB, SFTP, FTP, MEGA) stream bytes through API with zero disk retention.
  - Move **never** deletes source before destination copy is confirmed successful.
- **Directory Deletion**: Preserves native semantics; empty and recursive directory deletions are separate capabilities requiring explicit confirmation.
- **Raw Uploads**: Require exact `Content-Length`, enforce per-user Redis four-stream lease, and validate byte-exact streaming.
