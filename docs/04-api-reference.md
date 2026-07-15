# 04 – API Reference

All paths are prefixed with `/api`. JSON responses are produced with `writeJSON`. Error responses carry
**only** a machine-readable `error_code` (localized on the client via `translateApiError`); raw
`err.Error()` strings are never forwarded to the client for connection failures.

**Response conventions**
- Success: `200 OK` JSON (`{ "success": true, … }` for action endpoints).
- Connection-test/browse/mkdir logical failures: `200 OK` with `{ "success": false, "error_code": "…" }`
  (so the frontend can localize the message). They do **not** return `4xx`.
- Auth/validation errors: `writeError`/`writeValidationError`/`writeConflictError` with the typed
  `APIErrorCode` and the correct HTTP status.

**Protection legend**
- `public` — no auth.
- `refresh` — requires the HTTP-only refresh-token cookie.
- `JWT` — requires `Authorization: Bearer <access_token>`.
- `token/query` — the `/ws` endpoint is **not** behind `AuthMiddleware`; it authenticates via the
  `?token=<jwt>` query parameter (and validates ownership, blocking 2FA temp tokens).
- `admin` — JWT + `role == ADMIN` (enforced inside the handler).

---

## 1. Authentication

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `POST` | `/auth/register` | public | Create account (password ≥ 12 chars). |
| `POST` | `/auth/login` | public | Login → JWT access + refresh cookie. Returns `2fa_pending` token if 2FA enabled. |
| `POST` | `/auth/totp` | public | Verify TOTP code (2FA) using the 5-min temp token; returns full JWT. |
| `POST` | `/auth/refresh` | refresh | Rotate access token from refresh cookie. |
| `POST` | `/auth/logout` | refresh | Invalidate refresh token. |
| `GET` | `/auth/me` | JWT | Current user profile. |
| `PUT` | `/auth/me` | JWT | Update display name. |
| `POST` | `/auth/change-password` | JWT (allow must-change) | Change password. |
| `GET` | `/auth/2fa/setup` | JWT | Begin TOTP setup (returns secret/QR). |
| `POST` | `/auth/2fa/enable` | JWT | Enable 2FA (verify first code + backup codes). |
| `POST` | `/auth/2fa/disable` | JWT | Disable 2FA. |
| `GET` | `/auth/2fa/status` | JWT | 2FA enabled? |
| `GET` | `/auth/password-reset-available` | public | Whether system SMTP is configured. |
| `POST` | `/auth/forgot-password` | public | Send reset email (rate-limited). |
| `POST` | `/auth/reset-password` | public | Set new password via token. |
| `GET` | `/auth/email-change-available` | public | Whether email-change is available. |
| `POST` | `/auth/change-email` | JWT | Request email change (confirmation to old address). |
| `POST` | `/auth/confirm-email-change` | public | Confirm email change via token. |

---

## 2. Migrations

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `GET` | `/migration` | JWT | List the user's migrations. |
| `GET` | `/migration/stream` | JWT | SSE migration-stream (rate-limited, capped per user). |
| `POST` | `/migration/connect` | JWT | Connection test for source **and** target; returns source listing. Rate-limited. |
| `POST` | `/migration/browse` | JWT | Browse source directories/calendars/contacts. Rate-limited. |
| `POST` | `/migration/target/browse` | JWT | Browse target directories. Rate-limited. |
| `POST` | `/migration/target/mkdir` | JWT | Create a target directory. Rate-limited. |
| `POST` | `/migration/start` | JWT | Create + start a migration (optional `scheduled_time`). |
| `GET` | `/migration/{id}` | JWT (own) | Migration status + resource stats. |
| `POST` | `/migration/{id}/pause` | JWT (own) | Pause (`RUNNING`/`INDEXING` only). |
| `POST` | `/migration/{id}/resume` | JWT (own) | Resume (`PAUSED`/`PAUSED_CONNECTION_LOSS`). |
| `POST` | `/migration/{id}/cancel` | JWT (own) | Cancel; marks tasks cancelled + publishes Redis cancel event. |
| `DELETE` | `/migration/{id}` | JWT (own) | Delete migration + cascading tasks. |
| `GET` | `/migration/{id}/report` | JWT (own) | CSV report (failed tasks + skipped indexing errors). |
| `POST` | `/migration/{id}/retry-failed` | JWT (own) | Re-enqueue failed tasks (`COMPLETED`/`FAILED` only). |
| `POST` | `/migration/{id}/reindex` | JWT (own) | Re-run indexing for a `FAILED` migration. |
| `PUT` | `/migration/{id}/threads` | JWT (own) | Live thread count (1–16). |
| `PUT` | `/migration/{id}/bandwidth` | JWT (own) | Live bandwidth limit (0–1000 Mbps); publishes Redis event. |
| `GET` | `/migration/{id}/ws` | token/query | WebSocket live progress. |

> **Ownership:** endpoints operating on a specific migration compare the JWT `sub` against
> `mig.UserID` and return `403 Forbidden` on mismatch. The WebSocket performs the same check manually.

---

## 3. Schedules

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `GET` | `/schedule` | JWT | List the user's schedules. |
| `GET` | `/schedule/{id}` | JWT (own) | Schedule detail. Returns `404 Not Found` (not `403`) if not owned. |
| `DELETE` | `/schedule/{id}` | JWT (own) | Delete schedule (returns `404` if not owned). |

---

## 4. Settings

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `GET` | `/settings` | public | Read instance setting(s). |
| `PUT` | `/settings` | JWT | Update a setting. |
| `GET` | `/settings/smtp` | JWT | Read per-user SMTP settings. |
| `PUT` | `/settings/smtp` | JWT | Save per-user SMTP settings (password encrypted). |
| `POST` | `/settings/smtp/test` | JWT | Send a test email. |
| `POST` | `/user/avatar` | JWT | Upload avatar. |
| `DELETE` | `/user/avatar` | JWT | Remove avatar. |

---

## 5. Admin (ADMIN only)

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `POST` | `/admin/users` | admin | Create a user with role + must-change flag. |
| `POST` | `/admin/users/{id}/suspend` | admin | Deactivate user (pauses migrations, disables schedules). |
| `POST` | `/admin/users/{id}/reactivate` | admin | Reactivate user (re-enables schedules). |
| `DELETE` | `/admin/users/{id}` | admin | Delete user (cascade). |
| `PUT` | `/admin/users/{id}/role` | admin | Change role (`USER`/`ADMIN`). |
| `GET` | `/admin/users` | admin | Paginated user list. |
| `GET` | `/admin/stats` | admin | Global stats (users, migrations/tasks by status). |
| `GET` | `/admin/migrations` | admin | All migrations across users (with owner email). |
| `GET` | `/audit/log` | admin | Paginated/filtered audit log. |

---

## 6. OAuth & WebSocket

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `GET` | `/oauth/auth` | public | Begin OAuth2 flow (Dropbox/Google); redirects to provider. |
| `GET` | `/oauth/callback` | public | Provider callback; sets tokens, posts result to opener via `postMessage`. |
| `GET` | `/migration/{id}/ws` | token/query | Live progress WebSocket (see §2). |

---

## 7. Start Request Shape (reference)

`POST /api/migration/start` accepts a `StartRequest`:

```jsonc
{
  "source_url": "https://…",
  "source_username": "…",
  "source_password": "…",            // encrypted server-side
  "source_refresh_token": "…",       // OAuth (encrypted)
  "source_token_expires_in": 3600,
  "target_url": "https://…",
  "target_username": "…",
  "target_password": "…",
  "target_refresh_token": "…",
  "target_token_expires_in": 3600,
  "source_provider": "nextcloud",    // whitelisted
  "target_provider": "webdav",
  "conflict_strategy": "SKIP",        // SKIP | OVERWRITE | RENAME
  "paths": ["/Documents"],
  "calendars": [],
  "contacts": [],
  "target_dir": "/",
  "threads": 4,                       // 1–16
  "bandwidth_limit_mbps": 0,          // 0–1000
  "scheduled_time": null              // RFC3339; if set → SCHEDULED + one-shot schedule
}
```

Validation rules applied server-side:
- At least one of `paths`/`calendars`/`contacts` required.
- Provider values must be in the whitelist (`nextcloud`, `webdav`, `dropbox`, `google`, `smb`, `s3`,
  `sftp`, `magentacloud`).
- `magentacloud` is files-only (rejects calendars/contacts on source or target).
- Per-user cap of `maxActiveMigrations` (10) simultaneous active migrations.
- `threads` clamped to 1–16; `bandwidth_limit_mbps` clamped to 0–1000.
- `scheduled_time`, when present, must parse as RFC3339 and be in the future.

---

## 8. Error Codes

Error codes are typed constants (`APIErrorCode`). Examples include `ErrMigrationNotOwned`,
`ErrMigrationNotFound`, `ErrMigrationInvalidState`, `ErrProviderUnsupported`, `ErrSourceConnectionFailed`,
`ErrTargetConnectionFailed`, `ErrRateLimited`, `ErrThreadsOutOfRange`, `ErrBandwidthOutOfRange`,
`ErrCorsOriginUntrusted`, `ErrInvalidBody`, `ErrNoSourcePaths`, `ErrEncryptionFailed`,
`ErrTooManyActiveMigrations`, and many more. Each must be added to **both** locale files under
`errors.*`. The frontend maps unknown codes to `errors.UNKNOWN`.
