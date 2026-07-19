# 09 – Development

Guidance for working on Clumoove locally without Docker, plus code-quality tooling and conventions.

---

## 1. Local Setup (no Docker)

You need Go 1.25+ and a running PostgreSQL + Redis (or Docker just for those two services).

### Backend

```bash
cd backend
# Ensure PostgreSQL + Redis are reachable, then set env vars:
export DATABASE_URL="postgres://postgres:postgres@localhost:5432/cloud_migration_db?sslmode=disable"
export REDIS_URL="localhost:6379"
export REDIS_PASSWORD="your-strong-password"
export ENCRYPTION_SECRET_KEY="$(openssl rand -base64 32)"
export JWT_SECRET_KEY="$(openssl rand -base64 32)"   # must differ from ENCRYPTION_SECRET_KEY

go run ./cmd/api      # API on :8000
go run ./cmd/worker   # worker
```

`db.InitDB` creates/updates the schema automatically on first connection, so no separate migration step
is required.

### Frontend

```bash
cd frontend
npm install
npm run dev           # Vite dev server on :5173 (or as configured)
```

The frontend resolves the API via `getApiUrl()` (see
[Deployment §5](./08-deployment.md#5-dynamic-api-url-frontend)). For local dev it points at
`http://<host>:8001`.

---

## 2. Code Quality & Checks

Run these from the repo root / respective directories:

```bash
# Go (from backend/)
go vet ./backend/...
go build ./backend/...

# TypeScript typecheck (frontend/)
npx tsc --noEmit --project frontend/tsconfig.app.json

# Frontend lint
npx eslint frontend/src
```

File-scoped commands referenced in `AGENTS.md`:

| Task | Command |
| :--- | :------ |
| Go typecheck/lint | `go vet ./backend/...` |
| TS typecheck | `npx tsc --noEmit --project frontend/tsconfig.app.json` |
| JS/TS lint | `npx eslint frontend/src` |

---

## 3. Conventions

### Database
- All schema changes go into `db/schema.sql` **and** as an inline `CREATE/ALTER` statement in
  `InitDB()` for automatic startup migration.
- All queries use **parameterized statements** (`$1`, `$2`, …) — never string-interpolate user input
  into SQL.

### Storage providers
- Every provider implements `StorageProvider` (`storage/provider.go`) and is registered in
  `factory.go`.
- Valid provider values are whitelisted: `nextcloud`, `webdav`, `dropbox`, `google`, `smb`, `s3`,
  `sftp`, `magentacloud`. Never pass unvalidated provider strings to `NewProvider`.
- Resource types: `files`, `calendars`, `contacts`. Calendars/contacts are always overwritten on
  conflict.
- S3 `insecure=true` endpoints must check literal IPs / `*.local`/`localhost` without DNS resolution.

### Security
- Credentials: never pass plaintext to background goroutines; query + decrypt at the last moment
  (`crypto.Decrypt`).
- Error messages: never forward raw `err.Error()` for connection failures; log with `log.Printf` and
  return only a machine-readable `error_code`.
- `ENCRYPTION_SECRET_KEY` is used only for AES-GCM; `JWT_SECRET_KEY` only for JWT signing. API refuses
  to start if either is missing or they are equal.
- OAuth2 access/refresh tokens are stored AES-GCM encrypted; token refresh must invalidate the old
  refresh token before storing the new one.
- CORS uses a static `allowedOrigins` whitelist; unknown origins get no `Access-Control-Allow-Origin`.
- Redis requires a password; connection fails on empty/known-default passwords.

### Multi-tenancy & ownership
- All per-migration endpoints call `auth.GetUserIDFromContext(r.Context())` and compare with
  `mig.UserID` → `403` on mismatch.
- WebSocket `/migration/{id}/ws` performs the same ownership check manually via the `?token` query param
  and blocks 2FA temp tokens.
- Schedule endpoints use `db.VerifyScheduleOwnership` (EXISTS) and return `404` (not `403`) for
  non-owners.

### Indexing (BFS)
- Use a queue-based BFS with a `visited` map for recursive directory traversal (prevents symlink/circular
  DAV loops).
- Track indexed paths with a `resourceType:path` key in an `indexedPaths` map to avoid duplicate tasks.

### Scheduler
- Validate user cron via `scheduler.ValidateCronExpression` (wraps `cron.ParseStandard`) before
  persisting.
- One-shot jobs leave `cron_expression` NULL and set `run_at`/`next_run_at`; recurring jobs compute
  `next_run_at` via `NextRun(cron_expression)`.
- Multi-instance safety: claim each schedule with a Redis `SET NX` lock (`schedule:lock:{id}`, 2-min TTL).

### API responses
- Use `writeJSON(w, status, data)` for all JSON responses.
- Return machine-readable `error_code` via `writeError` / `writeValidationError` (400) /
  `writeConflictError` (409). Add every new code to the `APIErrorCode` block **and** both frontend
  locale files.
- Connection-test/browse/mkdir endpoints return `200 OK` with `{ "success": false, "error_code": "…" }`
  for logical failures (not `4xx`).

### Internationalization
- Frontend: `i18next` + `react-i18next` + `i18next-browser-languagedetector`; supported `de`, `en`
  (fallback `en`).
- `locales/de/translation.json` and `locales/en/translation.json` **must stay in key parity**.
- Error codes localized under `errors.*`; `useApiError()` maps `error_code` → translated string,
  falling back to `errors.UNKNOWN`.
- Use `utils/format.ts` (`formatBytes`, `formatDate`, `formatDateTime`, `useFormat`) for locale-aware
  formatting.

### Threads & parallelism
- `threads` per migration capped 1–16 (validated in `handleStart`). The worker respects this via the
  dequeue query (`COUNT(*) < m.threads`).
- Worker-level `MAX_THREADS` (default 16) is total parallel tasks per worker process.

### Retry & backoff
- Exponential backoff: $10 \times 3^{\text{attempt}}$ s (10, 30, 90), max 3 attempts.
- Permanent errors (expired/invalid OAuth, irrecoverable auth) skip retry and mark the task `FAILED`.

---

## 4. Project Layout (quick reference)

```
backend/cmd/{api,worker}   entrypoints
backend/internal/{auth,crypto,db,email,indexer,oauth,processor,queue,sanitize,scheduler,storage,throttle,totp2fa}
frontend/src/{components,contexts,hooks,locales,utils}
db/schema.sql
docker-compose.yml / docker-compose.dev.yml / docker-compose.prod.yml
.env.example
```

`docker-compose.yml` builds the production stack locally (`target: prod`); `docker-compose.dev.yml` builds
all images locally for development. See [Deployment §4](./08-deployment.md#4-starting-the-stack).

See [Architecture §8](./01-architecture.md#8-project-layout) for the full tree.
