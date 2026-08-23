# Concept & Architecture: Service Comparison (Migration, Sync, Backup)

This document describes the interaction and technical differences between the three core services of the platform: **Migration**, **Synchronization (Sync)**, and **Backup & Restore**.

Although all three services transfer data via the `StorageProvider` interface (`backend/internal/storage/provider.go`), they differ fundamentally in their lifecycle, data retention, storage format, and functional purpose.

---

## 1. Overview & Comparison Table

| Criterion | Migration (One-Shot) | Synchronization (Sync) | Backup & Restore (Snapshots) |
| :--- | :--- | :--- | :--- |
| **Purpose** | One-time or deferred transfer of data from source A to target B. | Continuous, scheduled bidirectional or unidirectional synchronization between two active endpoints. | Creation of historical, point-in-time immutable snapshots with deduplication and point-in-time restore. |
| **Lifecycle** | Temporary. Terminates with completion (`COMPLETED`) or failure/cancellation. | Long-running. Executes recurring passes according to `interval_minutes` until deleted. | Scheduled & on-demand. Runs cron-scheduled or manual passes, maintaining snapshots until pruned by retention. |
| **Credential Storage** | Stored encrypted for the lifetime of the migration. Terminal migrations are retained until deleted. | **Permanent.** Stored encrypted in PostgreSQL as long as the sync job is active. | **Permanent.** Stored encrypted in `backup_jobs` or linked to reusable `connection_profiles`. |
| **Data Cleanup** | Retains task history permanently until manual deletion or user account deletion. | The job remains active; the delta baseline (`sync_state`) is updated atomically after each successful pass. | Prunes oldest snapshots when snapshot count exceeds `retention_count` (1–365). Deleting a backup job removes remote packs and catalog. |
| **Deletions Handling** | Ignored. Source deletions after start have no effect on target files. | Optional. Configurable delete propagation (`delete_propagation`, default `false`). | Immutable snapshots. Files deleted at source remain present in earlier snapshots. |
| **Versioning & Deduplication** | None (direct file-level copy with `OVERWRITE`, `SKIP`, or `RENAME`). | None (maintains single active state on both sides). | **Block-level deduplication.** 4 MiB fixed SHA-256 blocks packed into immutable 64 MiB packs in Clumoove format v1. |
| **Conflict Resolution** | File conflict strategy (`SKIP`, `OVERWRITE`, `RENAME`). | Conflict strategy (`OVERWRITE`, `SKIP`, `RENAME`) depending on direction. | Configurable on restore (`SKIP`, `OVERWRITE`, `RENAME`) with pre-execution conflict analysis in restore previews. |
| **Restoration / Recovery** | N/A (data is written directly to destination tree). | N/A (continuous synchronization). | **Two-phase restore.** Mandatory asynchronous preview for conflict/warning inspection, followed by fenced restore run. |

---

## 2. Technical Mechanisms

### 2.1. Migration (One-Shot / Deferred)
1. **Initiation:** The user selects source, target, resource types (files, calendars, contacts), and paths, then clicks "Start" (immediate) or specifies a future time (`scheduled_time`).
2. **Scheduling:** For deferred starts, an entry in `schedules` is created with `cron_expression = NULL` and `run_at = <scheduled_time>`.
3. **Execution:** The scheduler triggers the job once. Source files are indexed via BFS into `tasks`, dequeued by workers via `SELECT … FOR UPDATE SKIP LOCKED`, and streamed through RAM buffers from source to target without disk caching.
4. **Completion:** The migration transitions to `COMPLETED` or `COMPLETED_WITH_ERRORS`. A downloadable CSV report is available for error inspection.

### 2.2. Synchronization (Sync)
1. **Initiation:** The user configures a sync job (e.g. 15-minute sync between Nextcloud and Google Drive) with direction (`one_way` or `two_way`), conflict strategy, and optional delete propagation.
2. **Scheduling:** An entry in `schedules` is created without a cron expression; `next_run_at` is advanced by `interval_minutes` after each pass.
3. **Delta Engine:**
   - At each pass, the coordinator scans both endpoints (BFS inventory).
   - It compares discovered items against the baseline stored in `sync_state`.
   - It computes delta actions: copy new/modified files, propagate deletions, resolve conflicts.
4. **Execution & Baseline Commit:** Workers execute file transfers. On successful completion, the new baseline is committed atomically to `sync_state`, and the job returns to `IDLE`.

### 2.3. Backup & Restore (Point-in-Time Snapshots)
1. **Initiation & Scheduling:** A backup job uses saved connection profiles or direct credentials, selected source paths, a five-field cron expression with an IANA timezone, and a retention count between 1 and 365 snapshots. Immich endpoints are excluded.
2. **Repository Structure:** The backup target hosts an immutable repository root under `.clumoove-backup/<repository-id>` containing `format-v1.json` and a `packs/` directory.
3. **Execution (Backup Run):**
   - The scheduler queues a generation-fenced `backup_runs` entry.
   - A worker claims the run with a PostgreSQL advisory lock, scans selected source paths, and chunks files into 4 MiB fixed blocks (SHA-256).
   - Blocks already present in `backup_blocks` are deduplicated.
   - New blocks are written into immutable pack files (up to 64 MiB payload) and uploaded to the remote target.
   - A snapshot catalog is built in `backup_snapshots` and `backup_snapshot_items`.
4. **Publishing & Retention:**
   - Snapshots remain in `PUBLISHING` until pack sizes and metadata are verified, then transition atomically to `READY` (or `PARTIAL` if non-fatal item errors occurred).
   - Old snapshots exceeding `retention_count` are marked `EXPIRED` and pruned by `backup_maintenance`.
5. **Repository Verification:**
   - Operators or users can trigger `METADATA`, `BUDGETED` (64 MiB – 1 TiB), or `FULL` repository integrity checks via `POST /api/backup/{id}/verify` with live SSE progress.
6. **Restore Engine:**
   - **Phase 1 (Preview):** The user selects snapshot items and a target destination. The API queues a `restore_previews` job. The worker inspects the target tree and produces conflict counts, mergeable directory stats, type conflicts, and sample conflict items.
   - **Phase 2 (Execution):** The user consumes the ready preview. A `restore_runs` job is created. Workers fetch required blocks from packs (via HTTP Range reads or bounded pack buffers governed by `MAX_RESTORE_PACK_READERS`), reconstruct files, verify checksums/sizes, and write them to the destination.

---

## 3. Data Flow Architecture

The diagram below illustrates the data flow and storage differences between the three services:

```
[ Source Store (Endpoint A) ] ───► [ Worker RAM Stream ] ───► [ Target Store (Endpoint B) ]
                                             │
      ┌──────────────────────────────────────┼──────────────────────────────────────┐
      ▼                                      ▼                                      ▼
[ Migration ]                          [ Sync ]                               [ Backup ]
 - Direct file copy                     - Evaluates sync_state delta           - Chunks into 4 MiB blocks
 - No snapshot history                  - Updates baseline hashes              - Deduplicates via backup_blocks
 - Tasks persisted in PostgreSQL        - Recurring interval-based             - Writes immutable 64 MiB packs
 - Optional deferred schedule           - Overwrite / Skip / Rename            - Builds backup_snapshots catalog
 - Immediate destination structure      - Optional delete propagation          - FIFO retention pruning
                                                                               - Two-phase restore (Preview -> Run)
```
