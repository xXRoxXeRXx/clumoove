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
- `admin` — JWT + `role == ADMIN` (enforced inside the handler).

Conflict strategies are allowlisted as `SKIP`, `OVERWRITE`, or `RENAME`. Migration starts default an omitted strategy to `SKIP`; sync creation defaults it to `OVERWRITE`. Immich validation: migration start rejects calendar/contact selections when either endpoint is `immich`, and an Immich target requires `conflict_strategy: "SKIP"`. `POST /sync` rejects either Immich endpoint with `IMMICH_SYNC_UNSUPPORTED`. Immich endpoints use a server URL and API key supplied in the password field; no username is required.

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
| `GET` | `/auth/password-reset-available` | public | Whether the database-backed instance mailer is configured. |
| `POST` | `/auth/forgot-password` | public | Send reset email (rate-limited). |
| `PUT` | `/auth/me/language` | JWT | Persist the user's notification and email language (`de` or `en`). |
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
| `GET` | `/migration/{id}/stream` | JWT (own) | SSE detail stream. Sends `migration` immediately and only when its sanitized payload changes; terminal migrations send once and close. |
| `POST` | `/migration/{id}/pause` | JWT (own) | Pause (`RUNNING`/`INDEXING` only). |
| `POST` | `/migration/{id}/resume` | JWT (own) | Resume (`PAUSED`/`PAUSED_CONNECTION_LOSS`). |
| `POST` | `/migration/{id}/cancel` | JWT (own) | Cancel; marks tasks cancelled + publishes Redis cancel event. |
| `DELETE` | `/migration/{id}` | JWT (own) | Delete migration + cascading tasks. |
| `GET` | `/migration/{id}/report` | JWT (own) | CSV report (failed tasks + skipped indexing errors). |
| `GET` | `/migration/{id}/errors` | JWT (own) | Paginated JSON error list (final transfer failures + indexing errors); accepts `limit` (max. 100) and `offset`. |
| `POST` | `/migration/{id}/retry-failed` | JWT (own) | Re-enqueue failed tasks (`COMPLETED`/`FAILED` only). |
| `POST` | `/migration/{id}/reindex` | JWT (own) | Re-run indexing for a `FAILED` migration. |
| `PUT` | `/migration/{id}/threads` | JWT (own) | Live thread count (1–16). |
| `PUT` | `/migration/{id}/bandwidth` | JWT (own) | Live bandwidth limit (0–1000 Mbps); publishes Redis event. |

> **Ownership:** endpoints operating on a specific migration compare the JWT `sub` against
> `mig.UserID` and return `403 Forbidden` on mismatch. The authenticated detail SSE endpoint uses the
> same JWT middleware, so no credential is accepted in a query parameter.

---

## 3. Sync Engine

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `GET` | `/sync` | JWT | List the user's sync jobs. |
| `GET` | `/sync/stream` | JWT | SSE sync-stream for real-time progress. |
| `POST` | `/sync` | JWT | Create a new sync job (`one_way` / `two_way`). |
| `GET` | `/sync/{id}` | JWT (own) | Sync job status + stats. |
| `POST` | `/sync/{id}/start` | JWT (own) | Manually trigger a sync run. |
| `POST` | `/sync/{id}/pause` | JWT (own) | Pause a running sync job. |
| `POST` | `/sync/{id}/resume` | JWT (own) | Resume a paused sync job. |
| `DELETE` | `/sync/{id}` | JWT (own) | Delete sync job + cascading state/tasks. |
| `GET` | `/sync/{id}/report` | JWT (own) | Download CSV report for sync errors. |
| `GET` | `/sync/{id}/errors` | JWT (own) | Paginated JSON list of final transfer failures; accepts `limit` (max. 100) and `offset`. |
| `PUT` | `/sync/{id}/threads` | JWT (own) | Live thread count adjustment. |
| `PUT` | `/sync/{id}/bandwidth` | JWT (own) | Live bandwidth limit (0–1000 Mbps); publishes Redis event. |

---

## 4. Connection Profiles

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `GET` | `/profiles` | JWT | List stored connection profiles for the current user. |
| `POST` | `/profiles` | JWT | Create a reusable connection profile. |
| `GET` | `/profiles/{id}` | JWT (own) | Profile details. |
| `PUT` | `/profiles/{id}` | JWT (own) | Update connection profile. |
| `DELETE` | `/profiles/{id}` | JWT (own) | Delete connection profile. |
| `POST` | `/profiles/{id}/test` | JWT (own) | Perform connection test using saved profile credentials. |

---

## 5. Schedules

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `GET` | `/schedule` | JWT | List the user's schedules. |
| `GET` | `/schedule/{id}` | JWT (own) | Schedule detail. Returns `404 Not Found` (not `403`) if not owned. |
| `DELETE` | `/schedule/{id}` | JWT (own) | Delete schedule (returns `404` if not owned). |

---

## 6. Settings

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `GET` | `/settings` | public | Read instance setting(s). |
| `PUT` | `/settings` | JWT | Update a setting. |
| `GET` | `/settings/notifications` | JWT | List notification preferences; includes `email_available` and returns the email channel only when the instance mailer is configured. |
| `PUT` | `/settings/notifications` | JWT | Update a channel. The `email` channel accepts only `{ type, enabled }`; other channels retain their user configuration. |
| `POST` | `/settings/notifications/test` | JWT | Send a test through a configured non-email notification channel. Instance-mailer tests are admin-only. |
| `POST` | `/user/avatar` | JWT | Upload avatar. |
| `DELETE` | `/user/avatar` | JWT | Remove avatar. |

---

## 7. Admin (ADMIN only)

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `POST` | `/admin/users` | admin | Create a user with role + must-change flag. |
| `POST` | `/admin/users/{id}/suspend` | admin | Deactivate user (pauses active migrations and sync jobs, cancels active sync work, disables schedules). |
| `POST` | `/admin/users/{id}/reactivate` | admin | Reactivate user (re-enables schedules). |
| `DELETE` | `/admin/users/{id}` | admin | Delete user (cascade). |
| `PUT` | `/admin/users/{id}/role` | admin | Change role (`USER`/`ADMIN`). |
| `GET` | `/admin/users` | admin | Paginated user list. |
| `GET` | `/admin/stats` | admin | Global stats (users, migrations/tasks by status). |
| `GET` | `/admin/migrations` | admin | All migrations across users (with owner email). |
| `GET` | `/admin/syncs` | admin | All sync jobs across users (with owner email). |
| `GET` | `/admin/settings/smtp` | admin | Read the instance SMTP configuration without its password; includes `configured` and `smtp_password_set`. |
| `PUT` | `/admin/settings/smtp` | admin | Create or update the instance SMTP configuration. Password may be omitted after initial setup; only `tls` and `starttls` are accepted. |
| `POST` | `/admin/settings/smtp/test` | admin | Send a localized test email to the authenticated administrator. |
| `DELETE` | `/admin/settings/smtp` | admin | Remove the instance SMTP configuration. |
| `GET` | `/audit/log` | admin | Paginated/filtered audit log. |

If a user suspension commits but a Redis sync-cancellation event cannot be published, the endpoint still returns `200` with `partial: true`; the affected sync-job IDs are recorded in the suspension audit entry for operator follow-up.

---

## 8. OAuth

| Method | Path | Protection | Description |
| :----- | :--- | :--------- | :---------- |
| `GET` | `/oauth/auth` | public | Begin OAuth2 flow (Dropbox/Google/OneDrive/HiDrive); redirects to provider. |
| `GET` | `/oauth/callback` | public | Provider callback; sets tokens, posts result to opener via `postMessage`. |

---

## 9. Start Request Shape (reference)

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
  "threads": 8,                       // 1–16
  "bandwidth_limit_mbps": 0,          // 0–1000
  "scheduled_time": null              // RFC3339; if set → SCHEDULED + one-shot schedule
}
```

Validation rules applied server-side:
- At least one of `paths`/`calendars`/`contacts` required.
- Provider values must be in the whitelist (`nextcloud`, `webdav`, `dropbox`, `google`, `onedrive`, `hidrive`, `smb`, `s3`,
  `sftp`, `ftp`, `magentacloud`, `local`, `immich`).
- `ftp`, `magentacloud`, `onedrive`, `hidrive`, `local`, and `immich` are files-only (reject calendars/contacts on source or target).
- `ftp` accepts only `ftp://host:21?tls=explicit` for explicit FTPS or `ftps://host:990` for implicit FTPS. Plain FTP,
  URL userinfo, certificate-validation bypasses, and custom CAs are not supported. Credentials belong in the encrypted
  username/password request fields, not the URL.
- An Immich target requires `conflict_strategy: "SKIP"`; Immich relies on native duplicate detection and does not support overwrite or rename. Immich can be used only for one-time migrations, not sync jobs.
- Per-user cap of `maxActiveMigrations` (10) simultaneous active migrations.
- `threads` clamped to 1–16; `bandwidth_limit_mbps` clamped to 0–1000.
- `scheduled_time`, when present, must parse as RFC3339 and be in the future.

---

## 10. Error Codes

Error codes are typed constants (`APIErrorCode`). Examples include `ErrMigrationNotOwned`,
`ErrMigrationNotFound`, `ErrMigrationInvalidState`, `ErrProviderUnsupported`, `ErrSourceConnectionFailed`,
`ErrTargetConnectionFailed`, `ErrRateLimited`, `ErrThreadsOutOfRange`, `ErrBandwidthOutOfRange`,
`ErrCorsOriginUntrusted`, `ErrInvalidBody`, `ErrNoSourcePaths`, `ErrEncryptionFailed`,
`ErrTooManyActiveMigrations`, and many more. Each must be added to **both** locale files under
`errors.*`. The frontend maps unknown codes to `errors.UNKNOWN`.

