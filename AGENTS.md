# Agent Instructions

## Package Managers
- Backend: **Go** (`go build ./...`, `go vet ./...`)
- Frontend: **npm** (`npm install`, `npm run dev`, `npm run build`)

## File-Scoped Commands
| Task | Command |
|------|---------|
| Go Typecheck/Lint | `go vet ./backend/...` |
| TS Typecheck | `npx tsc --noEmit --project frontend/tsconfig.app.json` |
| JS/TS Lint | `npx eslint frontend/src/components/FileBrowser.tsx` |

## Architecture Overview
- **Two Go entrypoints**: `cmd/api` (HTTP API gateway) and `cmd/worker` (migration engine). Both share the same module `backend/`.
- **Queue**: PostgreSQL-native via `SELECT … FOR UPDATE SKIP LOCKED` in `queue.DequeueSQL()`. Redis is used **only** for worker-liveness heartbeats and distributed recovery locks (`SET NX`).
- **Worker background schedulers** (all started by `processor.Start()`):
  - `RunWorkerLiveness` — heartbeat every 10 s, detects dead workers and reclaims their tasks
  - `RunRetryScheduler` — re-enqueues tasks whose `next_retry_at <= NOW()` every 10 s
  - `RunConnectionRecoveryScheduler` — re-activates `PAUSED_CONNECTION_LOSS` migrations every 60 s
  - `RunOrphanedRunningTasksRecovery` — resets tasks stuck in `RUNNING` for > 10 min
- **OAuth daemon**: `RunOAuthRotationDaemon` in `cmd/api` rotates Dropbox/Google refresh tokens before expiry.
- **Core Scheduler Engine**: A background daemon in `cmd/api` (`scheduler.Run`) checks for due schedules every minute and triggers the linked job (migration/sync/backup). It uses `github.com/robfig/cron/v3` for cron parsing/next-run calculation. Schedules live in the `schedules` table; a Redis `SET NX` lock (`schedule:lock:{id}`, 2-min TTL) ensures only one API instance triggers a given schedule in a multi-instance deployment.
- **Indexer package**: `backend/internal/indexer` holds the shared indexing logic (`Indexer.Start`). Both the immediate `handleStart` path and the scheduler's `triggerMigration` call it, so scheduled migrations actually create PENDING tasks. Selected paths/calendars/contacts are persisted on the `migrations` row (`selected_paths`/`selected_calendars`/`selected_contacts` JSONB) and read at trigger time.
- **WebSocket auth**: The `/api/migration/{id}/ws` endpoint is **not** behind `AuthMiddleware`. It authenticates via a `?token=<jwt>` query parameter and performs ownership validation manually inside the handler.

## Key Conventions

### Database
- Schema changes must be added to [schema.sql](db/schema.sql) **and** as an inline `CREATE TABLE IF NOT EXISTS` / `ALTER TABLE … ADD COLUMN IF NOT EXISTS` statement inside `InitDB()` in [db.go](backend/internal/db/db.go) for automatic migration on startup.
- All DB queries must use parameterised statements (`$1`, `$2`, …) — never string-interpolate user input into SQL.

### Storage Providers
- Every provider must implement the `StorageProvider` interface in [provider.go](backend/internal/storage/provider.go) and be registered in [factory.go](backend/internal/storage/factory.go).
- Valid provider values: `nextcloud`, `webdav`, `dropbox`, `google`, `smb`, `s3`, `sftp`. Whitelist these explicitly — never pass unvalidated provider strings to `NewProvider`.
- Resource types: `files`, `calendars`, `contacts`. Calendars/contacts are always overwritten on conflict (dynamic data — SKIP would silently leave stale entries).
- S3 Insecure HTTP endpoints (`insecure=true`) check literal IPs or `*.local`/`localhost` directly without DNS resolution to prevent DNS-rebinding SSRF. Users must use literal loopback/private IPs or local domain names.

### Security
- **Credential handling**: Never pass plaintext credentials to background goroutines. Query from database by `MigrationID` and decrypt at the last moment using `crypto.Decrypt`.
- **Error messages**: Never forward raw `err.Error()` strings for connection failures to API responses — they may embed URLs with embedded credentials. Log with `log.Printf`, return a generic message to the client.
- **Key Segregation**: Use `ENCRYPTION_SECRET_KEY` exclusively for AES-256-GCM encryption/decryption (`crypto.Encrypt` / `crypto.Decrypt`). Use `JWT_SECRET_KEY` exclusively for JWT signing. The API server **refuses to start** if either is missing.
- **AES-256-GCM key derivation**: The raw secret is SHA-256-hashed inside `crypto.deriveKey` to produce a 32-byte key — any length secret is accepted; the hash is the actual key.
- **OAuth2 Credentials**: OAuth2 access tokens and refresh tokens are stored AES-GCM encrypted in the `migrations` table (`source_refresh_token_encrypted`, `target_refresh_token_encrypted`). The `RunOAuthRotationDaemon` in the API gateway rotates them automatically before expiry.
- **Token Rotation**: Any token refresh request must immediately invalidate the old refresh token before generating and storing a new one.
- **CORS Whitelisting**: Never reflect incoming CORS origins or allow credentials for wildcards. Use the static `allowedOrigins` map (hardcoded localhost variants + `CORS_ALLOWED_ORIGIN` env var). Unknown origins receive no `Access-Control-Allow-Origin` header.
- **Redis Security**: Redis requires password authentication (`REDIS_PASSWORD` env var). Connection string format: `redis://:$REDIS_PASSWORD@redis-queue:6379`. Redis is **not** exposed to the host network.

### Multi-Tenancy & Ownership
- All endpoints operating on a specific migration (`GET /api/migration/{id}`, `DELETE /api/migration/{id}`, `GET /api/migration/{id}/report`) must call `auth.GetUserIDFromContext(r.Context())` and compare against `mig.UserID` — return `403 Forbidden` on mismatch.
- The WebSocket handler (`/api/migration/{id}/ws`) performs the same check manually using the `?token` query parameter.
- User ID is always sourced from the validated JWT claims injected by `AuthMiddleware` via `auth.ClaimsKey` context key.
- Schedule endpoints (`GET /api/schedule/{id}`, `DELETE /api/schedule/{id}`) verify ownership via `db.VerifyScheduleOwnership` (uses `EXISTS`, never returns `sql.ErrNoRows`). A non-owning result means the schedule either does not exist or belongs to another user — return `404 Not Found` in both cases to avoid leaking existence/ownership (do **not** return `403`).

### Indexing (BFS)
- Use queue-based Breadth-First Search with a `visited` map for recursive directory traversal to prevent infinite loops on symlink cycles or circular DAVs.
- Track indexed paths with a `resourceType:path` key in an `indexedPaths` map to prevent duplicate tasks.

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
- Connection-test endpoints (`/connect`, `/browse`, `/target/browse`) return `HTTP 200` with `{"success": false, "error": "..."}` for logical failures (not `4xx`) so the frontend can display the error message directly.
- Structural/validation errors (missing body, invalid provider) use standard `http.Error(w, msg, 4xx)`.

### Threads & Parallelism
- `threads` per migration is capped at 1–16 in `handleStart`. The worker respects this via the SQL dequeue query (`COUNT(*) < m.threads`).
- Worker-level concurrency is set by `MAX_THREADS` env var (default: 16, matching the max selectable per-migration threads slider). This is the total parallel tasks per worker process, not per migration.

### Retry & Backoff
- Exponential backoff: $10 \times 3^{\text{attempt}}$ seconds (10 s → 30 s → 90 s), max 3 attempts.
- Permanent errors (e.g. expired/invalid OAuth token, irrecoverable auth failure) skip retry immediately and mark the task `FAILED`.
