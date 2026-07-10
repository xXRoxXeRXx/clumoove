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
- **WebSocket auth**: The `/api/migration/{id}/ws` endpoint is **not** behind `AuthMiddleware`. It authenticates via a `?token=<jwt>` query parameter and performs ownership validation manually inside the handler.

## Key Conventions

### Database
- Schema changes must be added to [schema.sql](db/schema.sql) **and** as an inline `CREATE TABLE IF NOT EXISTS` / `ALTER TABLE … ADD COLUMN IF NOT EXISTS` statement inside `InitDB()` in [db.go](backend/internal/db/db.go) for automatic migration on startup.
- All DB queries must use parameterised statements (`$1`, `$2`, …) — never string-interpolate user input into SQL.

### Storage Providers
- Every provider must implement the `StorageProvider` interface in [provider.go](backend/internal/storage/provider.go) and be registered in [factory.go](backend/internal/storage/factory.go).
- Valid provider values: `nextcloud`, `webdav`, `dropbox`, `google`, `smb`. Whitelist these explicitly — never pass unvalidated provider strings to `NewProvider`.
- Resource types: `files`, `calendars`, `contacts`. Calendars/contacts are always overwritten on conflict (dynamic data — SKIP would silently leave stale entries).

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

### Indexing (BFS)
- Use queue-based Breadth-First Search with a `visited` map for recursive directory traversal to prevent infinite loops on symlink cycles or circular DAVs.
- Track indexed paths with a `resourceType:path` key in an `indexedPaths` map to prevent duplicate tasks.

### API Response Patterns
- Use `writeJSON(w, status, data)` for all JSON responses — never write raw JSON manually.
- Connection-test endpoints (`/connect`, `/browse`, `/target/browse`) return `HTTP 200` with `{"success": false, "error": "..."}` for logical failures (not `4xx`) so the frontend can display the error message directly.
- Structural/validation errors (missing body, invalid provider) use standard `http.Error(w, msg, 4xx)`.

### Threads & Parallelism
- `threads` per migration is capped at 1–16 in `handleStart`. The worker respects this via the SQL dequeue query (`COUNT(*) < m.threads`).
- Worker-level concurrency is set by `MAX_THREADS` env var (default: 4). This is the total parallel tasks per worker process, not per migration.

### Retry & Backoff
- Exponential backoff: $10 \times 3^{\text{attempt}}$ seconds (10 s → 30 s → 90 s), max 3 attempts.
- Permanent errors (e.g. expired/invalid OAuth token, irrecoverable auth failure) skip retry immediately and mark the task `FAILED`.
