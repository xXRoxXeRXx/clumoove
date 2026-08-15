# 09 – Development

Guidance for working on Clumoove locally without Docker, plus code-quality tooling and conventions.

## Cloud File Manager Tests

File-manager changes require handler coverage for ownership, sealed-reference/cursor replay, pagination limits, tickets, stream slots, raw-upload header/length validation, and byte-exact stream handling. Frontend coverage must include XHR upload metadata/progress/abort, queue concurrency/retry, MIME-safe Blob previews, worker cleanup, and Blob URL revocation. Run `go test ./...` and `go vet ./...` in `backend`; run `npm test`, `npx tsc --noEmit --project tsconfig.app.json`, `npx eslint src`, and `npm run build` in `frontend`.

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

Run these from the respective component directories:

```bash
# Go (from backend/)
cd backend && go test ./...
# Includes a source-level guard that checks each canonical schema column against
# that table's CREATE/ALTER statements in InitDB's inline bootstrap DDL.
cd backend && go vet ./...
cd backend && go build ./...

# TypeScript typecheck (from frontend/)
cd frontend && npx tsc --noEmit --project tsconfig.app.json

# Frontend lint (from frontend/)
cd frontend && npx eslint src
```

File-scoped commands referenced in `AGENTS.md`:

| Task | Command |
| :--- | :------ |
| Go typecheck/lint | `(cd backend && go vet ./...)` |
| TS typecheck | `(cd frontend && npx tsc --noEmit --project tsconfig.app.json)` |
| JS/TS lint | `(cd frontend && npx eslint src)` |

---


## 3. Conventions

### Commit messages
- Use Conventional Commits: `<type>(<optional scope>): <description>`.
- Allowed types: `feat` (user-visible feature), `fix` (bug fix), `refactor` (restructure without
  behaviour change), `perf` (performance-focused refactor), `style` (formatting only), `test`, `docs`,
  `build`, `ops` (infrastructure/deployment/CI), and `chore` (maintenance).
- Descriptions are mandatory, concise, lowercase, and imperative present tense; do not end them with a
  period. Example: `fix(api): validate migration ownership`.
- Scopes are optional contextual information; do not use issue identifiers as scopes. Put issue
  references in the optional footer instead.
- Mark breaking changes with `!` before the colon and document them with a `BREAKING CHANGE:` footer.
- Keep Git's default messages for merge and revert commits.

### Database
- All schema changes go into `db/schema.sql` **and** as an inline `CREATE/ALTER` statement in
  `InitDB()` for automatic startup migration. CI checks that each canonical column appears in the
  corresponding table's `CREATE` or `ALTER` DDL in `InitDB()`.
- All queries use **parameterized statements** (`$1`, `$2`, …) — never string-interpolate user input
  into SQL.

### Storage providers
- Every provider implements `StorageProvider` (`storage/provider.go`) and is registered in
  `factory.go`.
- Valid provider values are whitelisted: `nextcloud`, `opencloud`, `webdav`, `dropbox`, `google`, `onedrive`, `hidrive`, `smb`, `s3`,
  `sftp`, `ftp`, `magentacloud`, `koofr`, `local`, `immich`, `seafile`, `mega`. Never pass unvalidated provider strings to `NewProvider`.
- `ftp` is files-only FTPS. Accept only explicit `ftp://host:21?tls=explicit` or implicit `ftps://host:990`; reject
  cleartext FTP and URL userinfo. Use system-CA hostname/SNI validation only, without insecure or custom-CA options.
  Control and passive data connections must use the SSRF-safe egress dialer; prefer EPSV and pin PASV data connections
  to the validated control host, using only the server-announced port. FTPS deployments require outbound TCP access to
  port 21 or 990 and the server's configured passive range, with no inbound Docker port required.
- Resource types: `files`, `calendars`, `contacts`. Calendars/contacts are always overwritten on
  conflict.
- `mega` is a files-only personal Cloud Drive provider. It authenticates with email/password over forced HTTPS;
  reusable session ID and master-key material are stored encrypted, MFA is unsupported, same-name siblings
   are rejected as ambiguous, and verification is `size_only`.
- `koofr` is a files-only, fixed-public-endpoint provider. It authenticates with email/username plus an application password, selects only the primary mount, uses MD5 verification, is case-insensitive, and does not claim atomic rename.
- Nextcloud, OpenCloud, WebDAV, Immich, Seafile, and custom S3 endpoints must reject plaintext HTTP and require HTTPS.

### Transfer and recovery tests
- `processor/runTransferCore` owns the provider-facing download, stream hash, upload, promotion, and
  size-verification sequence. It is intentionally independent of queue and database persistence and is
  covered with in-memory provider doubles.
- Recovery backoff and the separate migration/sync round-robin cursors have unit coverage. Provider
  connection probes and state transitions remain exercised through the scheduler integration paths.
- Storage hash tests include an official HiDrive fixed vector. QuickXorHash coverage verifies the
  documented algorithm's empty-value and streaming behavior; no Microsoft-published input/output vector
  is claimed.

### FTPS test and deployment notes
- Run `(cd backend && go test -race ./internal/storage)` for storage-provider changes that affect connection lifecycle,
  passive data channels, or synchronization.
- FTPS integration tests are separate from the default suite and require a configured FTPS server. They cover explicit
  and implicit TLS, certificate validation, protected passive transfers, and rename behavior.

### Security
- Credentials: never pass plaintext to background goroutines; query + decrypt at the last moment
  (`crypto.Decrypt`).
- Error messages: never forward raw `err.Error()` for connection failures; log through structured `slog`
  after redaction and return only a machine-readable `error_code`.
- `ENCRYPTION_SECRET_KEY` is used only for AES-GCM; `JWT_SECRET_KEY` only for JWT signing. API refuses
  to start if either is missing or they are equal.
- Login-session refresh tokens are consumed and replaced inside one database transaction. OAuth token
  pairs are encrypted and conditionally persisted atomically against the previous refresh-token ciphertext.
- CORS uses a static `allowedOrigins` whitelist; unknown origins get no `Access-Control-Allow-Origin`.
- Redis requires a password; connection fails on empty/known-default passwords.

### Structured logging
- API and worker processes emit JSON `slog` records to stdout. Collect stdout as structured JSON.
- `LOG_LEVEL` sets the minimum level; valid values are `DEBUG`, `INFO`, `WARN`, and `ERROR`
  (case-insensitive), with `INFO` as the default.
- `LOG_ENVIRONMENT` is an optional operator-defined deployment label; `INSTANCE_ID` is an optional stable,
  per-replica identifier. Both are included in structured log records when configured.
- API requests have a request ID, included in request logs and returned in `X-Request-ID` for support
  correlation.
- Redact URL userinfo, `Authorization`/`Cookie` headers, passwords, API keys, access/refresh tokens,
  client secrets, and equivalents. Never log request/response bodies. File paths are personal metadata:
  omit them at normal levels; `DEBUG` path logging is a privacy risk and must be temporary.
- Log an error once at the handling boundary with operation context and request ID. Propagate or wrap it
  without logging it again; clients still receive only an `error_code`.

### Multi-tenancy & ownership
- All per-migration endpoints call `auth.GetUserIDFromContext(r.Context())` and compare with
  `mig.UserID` → `403` on mismatch.
- Detail SSE `/migration/{id}/stream` is behind `AuthMiddleware` and verifies migration ownership from
  the JWT context; it never accepts a token in a query parameter.
- Schedule endpoints use `db.VerifyScheduleOwnership` (EXISTS) and return `404` (not `403`) for
  non-owners.

### Indexing (BFS)
- Use a queue-based BFS with a `visited` map for recursive directory traversal (prevents symlink/circular
  DAV loops).
- Track indexed paths with a `resourceType:path` key in an `indexedPaths` map to avoid duplicate tasks.

### Scheduler
- Validate user cron via `scheduler.ValidateCronExpression` (wraps `cron.ParseStandard`) before
  persisting.
- One-shot jobs leave `cron_expression` NULL and set `run_at`/`next_run_at`; cron recurring jobs compute
  `next_run_at` via `NextRun(cron_expression)`. Sync jobs leave it NULL and advance by their persisted
  `interval_minutes`, which supports intervals greater than 59 minutes.
- Multi-instance safety: claim each schedule with a Redis `SET NX` lock (`schedule:lock:{id}`, 2-min TTL).
- Workers recovering a connection-loss-paused sync job only set it to `IDLE` and make its active schedule
  due; the API scheduler is the sole sync-pass starter. Recovery detection runs every 60 seconds and the
  API scheduler polls every minute, so a recovered pass may wait up to one scheduler interval to start.

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
- Email and notification strings belong under `delivery.*` in those same locale files. Docker builds run
  the generator automatically before compiling. Before a direct local Go build, run
  `(cd backend && go generate ./internal/i18n)` and commit the regenerated
  `backend/internal/i18n/translations_gen.go`.
- Error codes localized under `errors.*`; `useApiError()` maps `error_code` → translated string,
  falling back to `errors.UNKNOWN`.
- Use `utils/format.ts` (`formatBytes`, `formatDate`, `formatDateTime`, `useFormat`) for locale-aware
  formatting.

### Frontend UI
- Use the semantic `ui-*` utilities and light/dark tokens from `frontend/src/index.css` for cards, fields,
  buttons, feedback, status badges, progress, empty/loading states, and pagination. Do not reintroduce
  `portal-*`, `glass-*`, `shadow-portal*`, decorative gradients, blur, large shadows, or scale hover effects.
- Use `@heroicons/react` for application UI/action icons. `react-icons` is limited to provider-brand icons in
  `components/connect/ProviderIcon.tsx`. Icon-only actions need localized `aria-label` and `title`.
- Prefer native controls. Tabs, menus, dialogs, and tree controls must be keyboard operable; dialogs require
  a name, modal semantics, Escape handling, focus trapping, and focus restoration.
- For frontend changes, run the typecheck and lint commands above, plus `(cd frontend && npm test)` and
  `(cd frontend && npm run build)`. Confirm locale key parity whenever translation files change.

### Threads & parallelism
- `threads` per migration or sync job are capped at 1–16. The worker respects this via the
  dequeue query (`COUNT(*) < m.threads`). Sync jobs also persist a 0–1000 Mbps bandwidth limit (0 is unlimited),
  which can be adjusted while a pass is running.
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
docker-compose.yml / docker-compose.dev.yml
.env.example
```

`docker-compose.yml` builds the production stack locally (`target: prod`); `docker-compose.dev.yml` builds
all images locally for development. See [Deployment §4](./08-deployment.md#4-starting-the-stack).

See [Architecture §8](./01-architecture.md#8-project-layout) for the full tree.
