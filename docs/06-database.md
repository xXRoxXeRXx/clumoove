# 06 – Database Schema

Clumoove persists all metadata in **PostgreSQL 15**. There are two sources of truth for the schema:

1. `db/schema.sql` — the canonical DDL, loaded on first `docker compose` up.
2. `backend/internal/db/db.go` `InitDB()` — **inline** `CREATE TABLE IF NOT EXISTS` /
   `ALTER TABLE … ADD COLUMN IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS` statements that run on every
startup, so the schema self-heals (new columns/tables are added automatically without a manual
migration step).

`InitDB()` also performs idempotent data cleanups needed by schema semantics. For example, it clears
legacy `cron_expression` values from `sync` schedules because sync cadence is stored in the linked
job's `interval_minutes`.

Adding the `ftp` provider requires no schema migration: existing provider, endpoint, username, and
encrypted-password fields persist its FTPS connection details.

Migration and sync job rows have nullable `source_profile_id` and `target_profile_id` foreign keys to `connection_profiles(id)` with `ON DELETE SET NULL` and indexes. They are not backfilled: encrypted credential snapshots remain the execution source of truth after a profile is changed or deleted.

> **Rule:** Any schema change must be added to `db/schema.sql` **and** as an inline statement inside
> `InitDB()` for automatic migration on startup.

A shared trigger function `update_updated_at_column()` keeps `updated_at` current on several tables
(`users`, `migrations`, `schedules`, `instance_smtp_settings`).

---

## 1. Tables

### `users`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | `gen_random_uuid()` |
| `email` | TEXT UNIQUE NOT NULL | login identity |
| `language` | TEXT NOT NULL DEFAULT `en` | User-facing delivery language (`de` or `en`) for emails and notifications. |
| `password_hash` | TEXT NOT NULL | bcrypt |
| `display_name` | TEXT NOT NULL | |
| `role` | TEXT NOT NULL DEFAULT `USER` | `USER` or `ADMIN` |
| `avatar` | BYTEA | |
| `avatar_mime` | TEXT | |
| `active` | BOOLEAN NOT NULL DEFAULT TRUE | |
| `must_change_password` | BOOLEAN NOT NULL DEFAULT FALSE | |
| `totp_secret_enc` | TEXT | AES-GCM |
| `totp_enabled` | BOOLEAN NOT NULL DEFAULT FALSE | |
| `totp_backup_codes` | JSONB | hashes |
| `totp_failed_attempts` | INTEGER NOT NULL DEFAULT 0 | |
| `totp_locked_until` | TIMESTAMPTZ | |
| `login_failed_attempts` | INTEGER NOT NULL DEFAULT 0 | |
| `login_locked_until` | TIMESTAMPTZ | |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

### `refresh_tokens`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `token_hash` | TEXT PK | SHA of refresh token |
| `id` | UUID UNIQUE NOT NULL | Public session identifier; never a token value |
| `user_id` | UUID FK → `users` ON DELETE CASCADE | |
| `user_agent` | TEXT NOT NULL | Device display metadata, capped at 512 bytes when issued |
| `expires_at` | TIMESTAMPTZ NOT NULL | |
| `created_at` | TIMESTAMPTZ | |

### `migrations`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | |
| `user_id` | UUID → `users` ON DELETE CASCADE | owner (multi-tenancy) |
| `source_url` / `target_url` | TEXT | Provider endpoint only; for `ftp`, FTPS-only canonical URL (no userinfo) |
| `source_username` / `target_username` | TEXT | |
| `source_password_encrypted` / `target_password_encrypted` | TEXT | AES-GCM |
| `source_refresh_token_encrypted` / `target_refresh_token_encrypted` | TEXT | OAuth (AES-GCM) |
| `source_token_expires_at` / `target_token_expires_at` | TIMESTAMPTZ | |
| `source_provider` / `target_provider` | TEXT NOT NULL DEFAULT `nextcloud` | whitelisted, including files-only `ftp` |
| `target_dir` | TEXT NOT NULL DEFAULT `/` | |
| `status` | TEXT | `PENDING`, `SCHEDULED`, `INDEXING`, `RUNNING`, `PAUSED`, `PAUSED_CONNECTION_LOSS`, `COMPLETED`, `FAILED`, `CANCELLED` |
| `conflict_strategy` | TEXT NOT NULL DEFAULT `SKIP`, CHECK | `SKIP`, `OVERWRITE`, `RENAME` |
| `selected_paths` / `selected_calendars` / `selected_contacts` | JSONB | persisted for deferred re-index |
| `total_files` / `processed_files` / `skipped_files` / `failed_files` | INTEGER | |
| `total_bytes` / `processed_bytes` | BIGINT | |
| `error_message` | TEXT | sanitized, credential-redacted |
| `threads` | INT NOT NULL DEFAULT 8 | 1–16 |
| `bandwidth_limit_mbps` | INT NOT NULL DEFAULT 0 | 0–1000 |
| `email_sent` | BOOLEAN NOT NULL DEFAULT FALSE | legacy completion-email flag; no longer drives delivery |
| `notification_generation` | INT NOT NULL DEFAULT 0 | increments for each retry/reindex run, making terminal notifications pass-scoped |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

### Notification outbox

`notification_channels` stores one encrypted, global configuration per user and channel (`email`,
`gotify`, `ntfy`, `telegram`, `discord`). If the instance mailer is configured, email defaults to enabled until a user saves an explicit email preference; a saved disabled row is an opt-out. `notification_events` snapshots a terminal migration or
sync pass; migrations are unique by `(migration_id, run_generation)` and sync runs by
`(sync_job_id, run_at)`. `notification_deliveries` copies the encrypted channel configuration and
tracks `PENDING`, `RUNNING`, `SENT`, or `FAILED`, attempts, retry time, and a non-sensitive error
code. This keeps channel changes from altering already-created events. At delivery time, the worker joins
the event owner and uses `users.language`; message text comes from the generated backend catalog derived
from `delivery.*` locale keys. Migration terminal-state transitions write the final task-derived counters
and their event/delivery rows in one transaction. A worker repair sweep also backfills any terminal
migration whose current notification generation has no event, protecting historical or interrupted data.

### `tasks`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | |
| `migration_id` | UUID → `migrations` ON DELETE CASCADE | Nullable (either `migration_id` OR `sync_job_id` set via `chk_task_job_type`) |
| `sync_job_id` | UUID → `sync_jobs` ON DELETE CASCADE | Nullable |
| `file_path` | TEXT | |
| `file_size` | BIGINT | |
| `source_hash` / `worker_hash` / `target_hash` | TEXT | `algo:hash` or `SIZE:n` or `DYNAMIC` |
| `status` | TEXT | `PENDING`, `RUNNING`, `COMPLETED`, `FAILED`, `SKIPPED`, `CANCELLED` |
| `resource_type` | TEXT NOT NULL DEFAULT `files` | `files`, `calendars`, `contacts` |
| `metadata` | JSONB | modification time, description, … |
| `error_message` | TEXT | |
| `attempts` | INT | |
| `next_retry_at` | TIMESTAMPTZ | backoff scheduling |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

### `connection_profiles`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | `gen_random_uuid()` |
| `user_id` | UUID → `users` ON DELETE CASCADE | owner (UNIQUE with `name`) |
| `name` | TEXT NOT NULL | profile name |
| `provider` | TEXT NOT NULL | whitelisted provider |
| `url` / `username` | TEXT | URL carries the endpoint only; `ftp` stores an FTPS-only canonical URL without userinfo |
| `password_encrypted` / `refresh_token_encrypted` | TEXT | AES-GCM |
| `token_expires_at` | TIMESTAMPTZ | |
| `oauth_user` | TEXT | |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

### `sync_jobs`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | `gen_random_uuid()` |
| `user_id` | UUID → `users` ON DELETE CASCADE | owner |
| `source_url` / `target_url` | TEXT | Provider endpoint only; for `ftp`, FTPS-only canonical URL (no userinfo) |
| `source_username` / `target_username` | TEXT | |
| `source_password_encrypted` / `target_password_encrypted` | TEXT | AES-GCM |
| `source_refresh_token_encrypted` / `target_refresh_token_encrypted` | TEXT | OAuth (AES-GCM) |
| `source_token_expires_at` / `target_token_expires_at` | TIMESTAMPTZ | |
| `source_provider` / `target_provider` | TEXT NOT NULL DEFAULT `nextcloud` | whitelisted, including files-only `ftp` |
| `direction` | TEXT NOT NULL DEFAULT `one_way` | `one_way`, `two_way` |
| `conflict_strategy` | TEXT NOT NULL DEFAULT `OVERWRITE` | `OVERWRITE`, `SKIP`, `RENAME` |
| `delete_propagation` | BOOLEAN NOT NULL DEFAULT FALSE | |
| `interval_minutes` | INT NOT NULL DEFAULT 15 | |
| `threads` | INT NOT NULL DEFAULT 8 | |
| `bandwidth_limit_mbps` | INT NOT NULL DEFAULT 0 | 0–1000; 0 is unlimited |
| `status` | TEXT NOT NULL DEFAULT `IDLE` | `IDLE`, `RUNNING`, `PAUSED`, `FAILED` |
| `run_generation` | INTEGER NOT NULL DEFAULT `0` | incremented for each claimed sync pass; guards lifecycle CAS updates and matches task `pass_generation` |
| `target_dir` | TEXT NOT NULL DEFAULT `/` | |
| `selected_paths` | JSONB | |
| `last_run_at` | TIMESTAMPTZ | |
| `last_run_status` / `error_message` | TEXT | |
| `total_files` / `processed_files` / `changed_files` / `deleted_files` / `failed_files` | INT | counters |
| `total_bytes` / `processed_bytes` / `live_bytes` | BIGINT | counters |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

### `sync_state`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | `gen_random_uuid()` |
| `sync_job_id` | UUID → `sync_jobs` ON DELETE CASCADE | UNIQUE with `(side, rel_path)` |
| `side` | TEXT NOT NULL | `source`, `target` |
| `rel_path` | TEXT NOT NULL | |
| `size` | BIGINT NOT NULL DEFAULT 0 | |
| `mtime` | TIMESTAMPTZ | |
| `source_hash` / `target_hash` / `etag` | TEXT | |

### `schedules`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | |
| `user_id` | UUID → `users` ON DELETE CASCADE | |
| `task_type` | TEXT | `migration` / `sync` / `backup` |
| `task_id` | UUID | linked job id |
| `cron_expression` | TEXT | NULL for one-shot and duration-based sync schedules |
| `run_at` | TIMESTAMPTZ | one-shot time |
| `next_run_at` | TIMESTAMPTZ | next due time |
| `is_active` | BOOLEAN NOT NULL DEFAULT TRUE | |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

### `instance_smtp_settings`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | SMALLINT PK | fixed singleton value `1` |
| `smtp_host` / `smtp_username` / `smtp_password_encrypted` / `smtp_from_email` | TEXT | password AES-GCM |
| `smtp_port` | INT NOT NULL DEFAULT 587 | |
| `smtp_from_name` | TEXT NOT NULL DEFAULT `''` | |
| `smtp_encryption` | TEXT NOT NULL DEFAULT `tls` | `tls` / `starttls` |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

This table is a singleton (`id = 1`) managed only through the administrator SMTP endpoints. The password is AES-GCM encrypted; email notification preferences remain per-user `notification_channels` rows and never contain SMTP credentials.

### `instance_oauth_providers`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `provider` | VARCHAR(32) PK | one of `google`, `onedrive`, `dropbox`, `hidrive` (whitelist enforced in Go, not by a CHECK) |
| `client_id` | TEXT NOT NULL | |
| `client_secret_encrypted` | TEXT NOT NULL | AES-GCM encrypted; never returned to the client |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

Administrator-managed OAuth2 client credentials (no environment variables). `client_secret_encrypted` is held in the process cache only as ciphertext and decrypted at the moment a token request is made. Deleting a row removes the provider from the configured set immediately after the cache is invalidated.

### `password_reset_tokens` / `email_change_tokens`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `token_hash` | TEXT PK | |
| `user_id` | UUID FK → `users` ON DELETE CASCADE | |
| (`new_email` for email_change) | TEXT | |
| `expires_at` | TIMESTAMPTZ NOT NULL | |
| `used` | BOOLEAN NOT NULL DEFAULT FALSE | |
| `created_at` | TIMESTAMPTZ | |

### `indexing_errors`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | |
| `migration_id` | UUID → `migrations` ON DELETE CASCADE | |
| `path` | TEXT NOT NULL | |
| `resource_type` | TEXT NOT NULL DEFAULT `files` | |
| `error_message` | TEXT NOT NULL | sanitized |
| `created_at` | TIMESTAMPTZ | |

### `audit_log`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | BIGSERIAL PK | |
| `user_id` | UUID | nullable (failed logins) |
| `action` | TEXT NOT NULL | see `AuditAction` constants |
| `target` | TEXT | migration/user id |
| `ip` | TEXT | sanitized (control chars stripped) |
| `details` | JSONB | |
| `created_at` | TIMESTAMPTZ | |

### `settings`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `key` | TEXT PK | |
| `value` | TEXT NOT NULL | |
| `updated_at` | TIMESTAMPTZ | |

---

## 2. Indexes

| Index | On | Purpose |
| :---- | :-- | :------ |
| `idx_migrations_user_id` | `migrations(user_id)` | ownership lookups |
| `idx_tasks_migration_status` | `tasks(migration_id, status)` | dequeue/progress |
| `idx_tasks_sync_status` | `tasks(sync_job_id, status)` | sync dequeue/progress |
| `idx_tasks_retry` | `tasks(status, next_retry_at) WHERE status='FAILED' AND next_retry_at IS NOT NULL` | retry scanner |
| `idx_conn_profiles_user` | `connection_profiles(user_id)` | profile ownership lookups |
| `idx_sync_jobs_user_id` | `sync_jobs(user_id)` | sync job ownership lookups |
| `idx_sync_jobs_status` | `sync_jobs(status)` | active sync job queries |
| `idx_sync_state_job` | `sync_state(sync_job_id, side)` | sync delta tracking lookup |
| `idx_schedules_next_run` | `schedules(next_run_at) WHERE is_active=TRUE` | scheduler due scan |
| `idx_schedules_user_id` | `schedules(user_id)` | |
| `idx_schedules_task` | `schedules(task_type, task_id)` | |
| `idx_indexing_errors_migration_id` | `indexing_errors(migration_id)` | report query |
| `idx_audit_log_created_at` / `_action` / `_user_id` | `audit_log(...)` | admin log filtering |

---

## 3. Queue Semantics (in `tasks`)

The dequeue (`queue.DequeueSQL`) selects `PENDING` migration tasks while their
migration is `RUNNING`/`INDEXING`, and `PENDING` sync tasks only while their
sync job is `RUNNING`. A sync job remains `INDEXING` while it lists both sides,
drains/removes leftover tasks from its prior pass, and creates the new delta;
holding its tasks until `RUNNING` prevents workers from processing stale work.
For either job type, the running task count must be below the job's `threads`
limit. It uses `FOR UPDATE SKIP LOCKED`, so multiple workers (and multiple
API/worker instances) safely share the same PostgreSQL queue without a broker.

---

## 4. Cascade & Cleanup Behavior

- Deleting a `user` cascades to `refresh_tokens`, `migrations` → `tasks`, `schedules`,
  `password_reset_tokens`, `email_change_tokens`.
- Deleting a `migration` cascades to its `tasks` and `indexing_errors`.
- `DeleteOldMigrations` (historical) pruned migrations older than 24h; the GC is currently disabled in
  favor of **permanent history until manual deletion** (see `main.go` note).
- Expired `password_reset_tokens` / `email_change_tokens` are cleaned hourly by the completion notifier.

---

## 5. Audit Actions

`db.AuditAction` constants include: `LOGIN_SUCCESS`, `LOGIN_FAILED`, `REGISTRATION`, `USER_CREATED`,
`MIGRATION_CREATED`, `MIGRATION_STARTED`, `MIGRATION_COMPLETED`, `MIGRATION_FAILED`, `MIGRATION_PAUSED`,
`MIGRATION_RESUMED`, `MIGRATION_CANCELLED`, `MIGRATION_DELETED`, `SETTING_UPDATED`, `USER_SUSPENDED`,
`USER_REACTIVATED`, `USER_DELETED`, `USER_ROLE_CHANGED`, `2FA_ENABLED`, `2FA_DISABLED`, and more.
Audit writes are best-effort and never block the primary request.
