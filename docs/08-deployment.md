# 08 – Deployment & Operations

This document covers running Clumoove with Docker Compose (development and production), environment
configuration, scaling, and routine operational tasks.

---

## 1. Prerequisites

- Docker and Docker Compose installed on the host.
- A `.env` file (copy from `.env.example`) with at least:
  - `ENCRYPTION_SECRET_KEY` and `JWT_SECRET_KEY` — each `openssl rand -base64 32`, **not identical**.
  - `REDIS_PASSWORD` — a strong, unique value (not a known default).
- For remote installs: open ports `3001` (web UI) and `8001` (API) in the firewall.

---

## 2. Environment Variables

| Variable | Purpose | Default |
| :------- | :------ | :------ |
| `ENCRYPTION_SECRET_KEY` | AES-256-GCM key for credentials (required). | – |
| `JWT_SECRET_KEY` | HMAC key for JWT signing (required, ≠ encryption key, ≥ 32 bytes). | – |
| `DB_USER` / `DB_PASSWORD` | PostgreSQL credentials. | `postgres` |
| `DATABASE_URL` | Full DB connection URL. If unset, defaults to `sslmode=require` localhost. | localhost fallback |
| `REDIS_URL` | Redis connection (`redis://:pw@host:6379`). | `localhost:6379` |
| `REDIS_PASSWORD` | Redis password (required; strong/unique). | – |
| `CORS_ALLOWED_ORIGIN` | Allowed CORS origin for production. | – |
| `VITE_ALLOWED_HOSTS` | Allowed hosts for the Vite dev server. | – |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | Google OAuth2 credentials. | – |
| `GOOGLE_PHOTOS_CLIENT_ID` / `GOOGLE_PHOTOS_CLIENT_SECRET` | Google **Photos** OAuth2 credentials (distinct client, scopes `photoslibrary.readonly.appcreateddata` + `photoslibrary.appendonly`). Existing users granted the old broad `photoslibrary` scope must **reconnect** their Google Photos account after deploy (consent re-requested via `prompt=consent`). | – |
| `DROPBOX_CLIENT_ID` / `DROPBOX_CLIENT_SECRET` | Dropbox OAuth2 credentials. | – |
| `OAUTH_REDIRECT_URI` | Optional OAuth redirect override (auto-detected otherwise). | auto |
| `ADMIN_EMAIL` / `ADMIN_DISPLAY_NAME` | Optional initial admin bootstrap. | – |
| `INDEXING_TIMEOUT_MINUTES` | Max duration of one indexing run. | `60` |
| `WEBDAV_LISTING_TIMEOUT_SECONDS` | Per-PROPFIND listing timeout. | `120` |
| `MAX_THREADS` | Global max parallel tasks per worker process (also sizes DB pool). | `16` |
| `MIGRATION_BLOCK_PRIVATE` | If `1`/`true`, also block RFC1918/ULA egress (SSRF). | off |
| `TRUSTED_PROXY` | Set `1`/`true` when a reverse proxy strips client `X-Forwarded-*` (enables real client IP for rate limiting). | off |
| `SMTP_HOST` / `SMTP_PORT` / `SMTP_USERNAME` / `SMTP_PASSWORD` / `SMTP_FROM_EMAIL` / `SMTP_FROM_NAME` / `SMTP_ENCRYPTION` | System SMTP (password reset, etc.). | – |
| `FRONTEND_URL` | Frontend base URL (used in reset/email-change links). | `http://localhost:5173` |

---

## 3. Compose Files

| File | Use | Build | Notes |
| :--- | :--- | :--- | :--- |
| `docker-compose.yml` | Production | Pulls GHCR images (`ghcr.io/xxroxxerxx/clumoove-*:0.8.0`) | No local build context, no source mounts. Run as-is behind a reverse proxy. |
| `docker-compose.dev.yml` | Development | Local multi-stage build (`target: dev`) + source mounts | Live-reload backend/frontend, mounts `./backend` and `./frontend`. |
| `docker-compose.prod.yml` | Production (advanced) | Pulls GHCR images | Hardened production variant with extra ops settings. |

> The default `docker-compose.yml` now runs the **prebuilt production images** from GHCR. Use
> `docker-compose.dev.yml` for local development with source mounts and live reload.

---

## 4. Port Allocation & Network Routing

| Service | Container | Internal port | External port | Notes |
| :------ | :-------- | :----------- | :----------- | :---- |
| Frontend | `migration-frontend` | `3000` | `3001` | http://localhost:3001 |
| API | `migration-api` | `8000` | `8001` | http://localhost:8001 |
| PostgreSQL | `migration-postgres` | `5432` | *not exposed* | internal only |
| Redis | `migration-redis` | `6379` | *not exposed* | internal, password-protected |
| Worker | `migration-worker-1` | – | – | internal network |

> PostgreSQL and Redis are deliberately **not** exposed on the host to prevent external attacks
> (e.g. the 2026-07-08 SLAVEOF incident that wiped the queue).

---

## 4. Starting the Stack

### Development (local build)

```bash
cp .env.example .env   # fill ENCRYPTION_SECRET_KEY / JWT_SECRET_KEY / REDIS_PASSWORD
docker compose -f docker-compose.dev.yml up --build -d
```

This builds local images, installs dependencies, mounts `./backend` and `./frontend` for live reload,
initializes the PostgreSQL schema from `db/schema.sql`, and starts all services in the background.

### Production (prebuilt GHCR images)

```bash
docker compose up -d
```

The default `docker-compose.yml` pulls the prebuilt `ghcr.io/xxroxxerxx/clumoove-*:0.8.0` images — no
local build needed. Suitable for running behind a reverse proxy with HTTPS. The API auto-detects proxied
CORS/hosts; set `CORS_ALLOWED_ORIGIN`, `FRONTEND_URL`, and `TRUSTED_PROXY=1` as needed.

For the hardened production variant, use:

```bash
docker compose -f docker-compose.prod.yml up -d
```

---

## 5. Dynamic API URL (Frontend)

`src/App.tsx → getApiUrl()` resolves the backend URL automatically:

1. `import.meta.env.VITE_API_URL` set and not localhost/127.0.0.1 → use directly (production proxy).
2. Custom domain with no/80/443 port → same host without port (reverse-proxy routing).
3. Local → port `8001`.

This ensures correct resolution in dev (`:8001`), behind a reverse proxy (no port), and with an explicit
`VITE_API_URL`.

---

## 6. Scaling Workers

Stateless workers can be scaled horizontally at runtime:

```bash
docker compose up --scale migration-worker=4 -d        # production (docker-compose.yml)
docker compose -f docker-compose.dev.yml up --scale migration-worker=4 -d   # development
```

Pending transfers are distributed atomically across workers via the PostgreSQL `SKIP LOCKED` queue.
`MAX_THREADS` controls per-process parallelism; raise it above the per-migration max (16) only when
running several migrations concurrently.

---

## 7. Operational Tasks & Notes

- **Graceful shutdown:** API and worker catch `SIGINT`/`SIGTERM`; the worker drains in-flight tasks
  before exiting.
- **Completion emails:** the worker's `RunCompletionNotifier` sends per-user SMTP reports for terminal
  migrations and cleans expired reset/email-change tokens hourly.
- **Permanent history:** migrations persist until manually deleted (the 24h GC is disabled). Deleting a
  migration cascades to its tasks/indexing errors.
- **Manual recovery endpoints:** users can `retry-failed` (re-enqueue failed tasks) or `reindex`
  (re-run indexing for a `FAILED` migration).
- **Audit log:** accessible to ADMIN via `GET /api/audit/log` (filterable by action/user/target/time).
- **Health/availability:** if PostgreSQL or Redis is down at startup, both API and worker retry for ~10
  attempts (2s backoff) before failing.
- **Secrets in logs:** on first boot the API generates a random admin password and prints it **once** to
  stdout (the process/container logs): `BOOTSTRAP ADMIN created — email=… password=… (rotate on first
  login)`. The plaintext password is never stored and is not recoverable afterwards — capture it from the
  startup logs, log in, and change it immediately (`must_change_password=TRUE` forces this). See
  `07-security.md` §11 for the full bootstrap flow and operator checklist.

---

## 8. Reverse Proxy Recommendations

When fronting the API with a reverse proxy (e.g. nginx):

- Terminate TLS at the proxy and set `X-Forwarded-Proto: https` + strip client-supplied
  `X-Forwarded-For`, then set `TRUSTED_PROXY=1` on the API so per-IP rate limiting and lockout
  accounting use the real client IP.
- Do **not** expose PostgreSQL/Redis; keep them on the internal Docker network.
- Ensure `CORS_ALLOWED_ORIGIN` matches the public frontend origin.
