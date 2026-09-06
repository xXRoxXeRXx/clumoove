# Scheduler & Task Queue

## Queue Architecture (PostgreSQL-Native)
- **Dequeue Engine**: Task queue operations run directly in PostgreSQL via `SELECT … FOR UPDATE SKIP LOCKED` inside `queue.DequeueSQL()`.
  - Migrations dequeue tasks in `RUNNING` or `INDEXING` status.
  - Sync jobs dequeue only in `RUNNING` status (`INDEXING` computes the delta first).
- **Generation Fencing for Sync**:
  - Each claimed sync pass increments `run_generation`; tasks store this as `pass_generation`.
  - Lifecycle and progress updates must match the current generation.
  - Final task counters, the reconciled `sync_state` baseline, and the transition to `IDLE` commit atomically in one database transaction; failure to write the baseline rolls back the pass for retry.
  - On cancellation, the coordinator retains its distributed lock while tasks drain, bounded by a 30-second grace period.
- **Role of Redis**: Redis is used exclusively for locks, rate limiting, and Pub/Sub coordination; it is never a persistent task queue.

## Scheduler Engine (`backend/cmd/api`)
- **Trigger Loop**: `scheduler.Run` ticks every 60 seconds, querying due schedules (`is_active = TRUE AND next_run_at <= NOW()`).
- **Distributed Locking**: Each due schedule is claimed using a Redis lock `SET NX` (`schedule:lock:{id}`, 2-minute TTL) so multi-replica API deployments never double-trigger.
- **Schedule Types**:
  - **One-Shot**: `cron_expression` is `NULL`, sets `run_at` and `next_run_at`. Deactivated immediately after trigger.
  - **Cron Recurring**: Uses standard 5-field cron syntax (`scheduler.ValidateCronExpression` via `cron.ParseStandard`). Recomputes `next_run_at` upon execution.
  - **Sync Recurring**: Uses interval-based timing (`interval_minutes` added to current timestamp, supporting arbitrary intervals like 90 min).
- **Overlap Protection**:
  - `isJobActive` verifies target job status before dispatch.
  - If a job is in `RUNNING`, `INDEXING`, `VERIFYING`, or `PAUSED_CONNECTION_LOSS`, the pass is skipped, logged, and `next_run_at` is advanced.
- **Failure Handling**: If triggering fails (e.g., deleted target job or illegal status), the schedule is automatically **deactivated** to avoid unbounded spinning.
- **Deferred Migrations**: A migration started with `scheduled_time` is created in `SCHEDULED` status alongside a one-shot schedule. The scheduler invokes `indexer.Start`, reading persisted selections to spawn initial `PENDING` tasks.

## Concurrency & Resource Limits
- **Per-Job Concurrency (`threads`)**: User-selectable between 1 and 16. The worker enforces this in the SQL dequeue query (`COUNT(*) < m.threads`).
- **Sync Bandwidth Limit**: Persists a 0–1000 Mbps limit (0 = unlimited), dynamically adjustable during runtime.
- **Worker Process Capacity (`MAX_THREADS`)**: Total concurrent tasks per worker process (default: 16).
- **Restore Pack Reader Cap (`MAX_RESTORE_PACK_READERS`)**: Limits concurrent full-pack memory buffers (default: 1, range: 1–4). A worker process holds at most `64 MiB * MAX_RESTORE_PACK_READERS` of pack data.

## Retry & Error Backoff
- **Exponential Backoff**:
  $$\text{Delay} = 10 \times 3^{\text{attempt}}\text{ seconds (10 s } \to \text{ 30 s } \to \text{ 90 s)}$$
  Maximum 3 retry attempts.
- **Permanent Errors**: Terminal failures (e.g. revoked OAuth token, irrecoverable authentication denial) bypass backoff and fail immediately.
