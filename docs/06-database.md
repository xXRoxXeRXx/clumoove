# 06 – Database Schema

Clumoove persists all metadata in **PostgreSQL 15**. There are two sources of truth for the schema:

1. `db/schema.sql` — the canonical DDL, loaded on first `docker compose` up.
2. `backend/internal/db/db.go` `InitDB()` — **inline** `CREATE TABLE IF NOT EXISTS` /
   `ALTER TABLE … ADD COLUMN IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS` statements that run on every
   startup, so the schema self-heals (new columns/tables are added automatically without a manual
   migration step).

> **Rule:** Any schema change must be added to `db/schema.sql` **and** as an inline statement inside
> `InitDB()` for automatic migration on startup.

A shared trigger function `update_updated_at_column()` keeps `updated_at` current on several tables
(`users`, `migrations`, `schedules`, `user_smtp_settings`).

---

## 1. Tables

### `users`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | `gen_random_uuid()` |
| `email` | TEXT UNIQUE NOT NULL | login identity |
| `password_hash` | TEXT NOT NULL | bcrypt |
| `display_name` | TEXT NOT NULL | |
| `role` | TEXT NOT NULL DEFAULT `USER` | `USER` or `ADMIN` |
| `avatar` | BYTEA | |
| `avatar_mime` | TEXT | |
| `active` | BOOLEAN NOT NULL DEFAULT TRUE | |
| `must_change_password` | BOOLEAN NOT NULL DEFAULT FALSE | |
| `totp_secret_encrypted` | TEXT | AES-GCM |
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
| `user_id` | UUID FK → `users` ON DELETE CASCADE | |
| `expires_at` | TIMESTAMPTZ NOT NULL | |
| `created_at` | TIMESTAMPTZ | |

### `migrations`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | |
| `user_id` | UUID → `users` ON DELETE CASCADE | owner (multi-tenancy) |
| `source_url` / `target_url` | TEXT | |
| `source_username` / `target_username` | TEXT | |
| `source_password_encrypted` / `target_password_encrypted` | TEXT | AES-GCM |
| `source_refresh_token_encrypted` / `target_refresh_token_encrypted` | TEXT | OAuth (AES-GCM) |
| `source_token_expires_at` / `target_token_expires_at` | TIMESTAMPTZ | |
| `source_provider` / `target_provider` | TEXT NOT NULL DEFAULT `nextcloud` | whitelisted |
| `target_dir` | TEXT NOT NULL DEFAULT `/` | |
| `status` | TEXT | `PENDING`, `SCHEDULED`, `INDEXING`, `RUNNING`, `PAUSED`, `PAUSED_CONNECTION_LOSS`, `COMPLETED`, `FAILED`, `CANCELLED` |
| `conflict_strategy` | TEXT | `SKIP`, `OVERWRITE`, `RENAME` |
| `selected_paths` / `selected_calendars` / `selected_contacts` | JSONB | persisted for deferred re-index |
| `total_files` / `processed_files` / `skipped_files` / `failed_files` | INTEGER | |
| `total_bytes` / `processed_bytes` | BIGINT | |
| `error_message` | TEXT | sanitized, credential-redacted |
| `threads` | INT NOT NULL DEFAULT 4 | 1–16 |
| `bandwidth_limit_mbps` | INT NOT NULL DEFAULT 0 | 0–1000 |
| `email_sent` | BOOLEAN NOT NULL DEFAULT FALSE | completion email flag |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

### `tasks`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | |
| `migration_id` | UUID → `migrations` ON DELETE CASCADE | |
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

### `schedules`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `id` | UUID PK | |
| `user_id` | UUID → `users` ON DELETE CASCADE | |
| `task_type` | TEXT | `migration` / `sync` / `backup` |
| `task_id` | UUID | linked job id |
| `cron_expression` | TEXT | NULL for one-shot |
| `run_at` | TIMESTAMPTZ | one-shot time |
| `next_run_at` | TIMESTAMPTZ | next due time |
| `is_active` | BOOLEAN NOT NULL DEFAULT TRUE | |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

### `user_smtp_settings`
| Column | Type | Notes |
| :----- | :--- | :---- |
| `user_id` | UUID PK → `users` ON DELETE CASCADE | |
| `smtp_host` / `smtp_username` / `smtp_password_encrypted` / `smtp_from_email` | TEXT | password AES-GCM |
| `smtp_port` | INT NOT NULL DEFAULT 587 | |
| `smtp_from_name` | TEXT NOT NULL DEFAULT `''` | |
| `smtp_encryption` | TEXT NOT NULL DEFAULT `tls` | `tls` / `starttls` |
| `notify_on_completion` | BOOLEAN NOT NULL DEFAULT TRUE | |
| `updated_at` | TIMESTAMPTZ | |

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
| `idx_tasks_retry` | `tasks(status, next_retry_at) WHERE status='FAILED' AND next_retry_at IS NOT NULL` | retry scanner |
| `idx_schedules_next_run` | `schedules(next_run_at) WHERE is_active=TRUE` | scheduler due scan |
| `idx_schedules_user_id` | `schedules(user_id)` | |
| `idx_schedules_task` | `schedules(task_type, task_id)` | |
| `idx_indexing_errors_migration_id` | `indexing_errors(migration_id)` | report query |
| `idx_audit_log_created_at` / `_action` / `_user_id` | `audit_log(...)` | admin log filtering |

---

## 3. Queue Semantics (in `tasks`)

The dequeue (`queue.DequeueSQL`) selects `PENDING` tasks whose migration is `RUNNING`/`INDEXING` and
where the running count for that migration is below `migration.threads`, using
`FOR UPDATE SKIP LOCKED`. Because locking is at the row level, multiple workers (and multiple API/worker
instances) safely share the same PostgreSQL queue without a broker.

---

## 4. Cascade & Cleanup Behavior

- Deleting a `user` cascades to `refresh_tokens`, `migrations` → `tasks`, `schedules`,
  `user_smtp_settings`, `password_reset_tokens`, `email_change_tokens`.
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
