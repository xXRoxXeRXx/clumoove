-- Schema for Multi-Cloud Migrations-Plattform

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Table for Users (Accounts)
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    display_name TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'USER', -- USER, ADMIN
    active BOOLEAN NOT NULL DEFAULT TRUE, -- soft deactivation (suspend); blocks login
    must_change_password BOOLEAN NOT NULL DEFAULT FALSE, -- forced rotation on first login
    avatar BYTEA,
    avatar_mime TEXT,
    totp_secret_encrypted TEXT,
    totp_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    totp_backup_codes JSONB,
    totp_failed_attempts INTEGER NOT NULL DEFAULT 0,
    totp_locked_until TIMESTAMP WITH TIME ZONE,
    login_failed_attempts INTEGER NOT NULL DEFAULT 0,
    login_locked_until TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Table for Refresh Tokens (Session Extension)
CREATE TABLE IF NOT EXISTS refresh_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Table for Application Settings (Key-Value Store)
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Table for Migrations (Main Jobs)
CREATE TABLE IF NOT EXISTS migrations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    source_url TEXT NOT NULL,
    source_username TEXT NOT NULL,
    source_password_encrypted TEXT NOT NULL,
    source_refresh_token_encrypted TEXT,
    source_token_expires_at TIMESTAMP WITH TIME ZONE,
    target_url TEXT NOT NULL,
    target_username TEXT NOT NULL,
    target_password_encrypted TEXT NOT NULL,
    target_refresh_token_encrypted TEXT,
    target_token_expires_at TIMESTAMP WITH TIME ZONE,
    source_provider TEXT NOT NULL DEFAULT 'nextcloud',
    target_provider TEXT NOT NULL DEFAULT 'nextcloud',
    status TEXT NOT NULL DEFAULT 'PENDING', -- PENDING, INDEXING, RUNNING, PAUSED_CONNECTION_LOSS, COMPLETED, COMPLETED_WITH_ERRORS, FAILED, SCHEDULED
    conflict_strategy TEXT NOT NULL DEFAULT 'SKIP', -- SKIP, OVERWRITE, RENAME
    target_dir TEXT NOT NULL DEFAULT '/',
    selected_paths JSONB,
    selected_calendars JSONB,
    selected_contacts JSONB,
    picker_session_id TEXT,
    total_files INT NOT NULL DEFAULT 0,
    total_bytes BIGINT NOT NULL DEFAULT 0,
    processed_files INT NOT NULL DEFAULT 0,
    processed_bytes BIGINT NOT NULL DEFAULT 0,
    live_bytes BIGINT NOT NULL DEFAULT 0,
    skipped_files INT NOT NULL DEFAULT 0,
    failed_files INT NOT NULL DEFAULT 0,
    error_message TEXT,
    threads INT NOT NULL DEFAULT 4,
    bandwidth_limit_mbps INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Table for Tasks (Individual File Transfers)
CREATE TABLE IF NOT EXISTS tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    migration_id UUID NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,
    file_size BIGINT NOT NULL,
    source_hash TEXT,
    worker_hash TEXT,
    target_hash TEXT,
    status TEXT NOT NULL DEFAULT 'PENDING', -- PENDING, RUNNING, COMPLETED, FAILED, SKIPPED
    resource_type TEXT NOT NULL DEFAULT 'files', -- files, calendars, contacts
    metadata JSONB,
    error_message TEXT,
    attempts INT NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_tasks_migration_id ON tasks(migration_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_migrations_status ON migrations(status);
CREATE INDEX IF NOT EXISTS idx_migrations_user_id ON migrations(user_id);
CREATE INDEX IF NOT EXISTS idx_tasks_migration_status ON tasks(migration_id, status);
CREATE INDEX IF NOT EXISTS idx_tasks_retry ON tasks(status, next_retry_at) WHERE status = 'FAILED' AND next_retry_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_pending ON tasks(status, created_at) WHERE status = 'PENDING';

-- Auto-update updated_at triggers
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ language 'plpgsql';

CREATE OR REPLACE TRIGGER update_migrations_updated_at
    BEFORE UPDATE ON migrations
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

CREATE OR REPLACE TRIGGER update_tasks_updated_at
    BEFORE UPDATE ON tasks
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- Central Schedules Table (Core Scheduler Engine)
CREATE TABLE IF NOT EXISTS schedules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    task_type TEXT NOT NULL CHECK (task_type IN ('migration', 'sync', 'backup')),
    task_id UUID NOT NULL,
    cron_expression TEXT,
    run_at TIMESTAMP WITH TIME ZONE,
    next_run_at TIMESTAMP WITH TIME ZONE,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Index for efficient daemon queries (only active schedules)
CREATE INDEX IF NOT EXISTS idx_schedules_next_run 
    ON schedules(next_run_at) 
    WHERE is_active = TRUE;

-- Index for user-scoped queries (multi-tenancy)
CREATE INDEX IF NOT EXISTS idx_schedules_user_id 
    ON schedules(user_id);

-- Index for task lookup
CREATE INDEX IF NOT EXISTS idx_schedules_task 
    ON schedules(task_type, task_id);

-- Auto-update trigger for schedules
CREATE OR REPLACE TRIGGER update_schedules_updated_at
    BEFORE UPDATE ON schedules
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- Email sent flag for migration completion notifications
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS email_sent BOOLEAN NOT NULL DEFAULT FALSE;

-- Per-user SMTP settings for migration completion emails
CREATE TABLE IF NOT EXISTS user_smtp_settings (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    smtp_host TEXT NOT NULL,
    smtp_port INT NOT NULL DEFAULT 587,
    smtp_username TEXT NOT NULL,
    smtp_password_encrypted TEXT NOT NULL,
    smtp_from_email TEXT NOT NULL,
    smtp_from_name TEXT NOT NULL DEFAULT '',
    smtp_encryption TEXT NOT NULL DEFAULT 'tls',
    notify_on_completion BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE OR REPLACE TRIGGER update_user_smtp_settings_updated_at
    BEFORE UPDATE ON user_smtp_settings
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- Password reset tokens
CREATE TABLE IF NOT EXISTS password_reset_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    used BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Email change tokens (confirm new email via link sent to old address)
CREATE TABLE IF NOT EXISTS email_change_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    new_email TEXT NOT NULL,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    used BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Per-folder indexing errors (resilient indexing: skipped folders are recorded, not fatal)
CREATE TABLE IF NOT EXISTS indexing_errors (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    migration_id UUID NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    resource_type TEXT NOT NULL DEFAULT 'files',
    error_message TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_indexing_errors_migration_id ON indexing_errors(migration_id);

-- Audit Log: immutable, instance-wide record of security-relevant events.
CREATE TABLE IF NOT EXISTS audit_log (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID,                  -- actor (NULL for failed logins)
    action  TEXT NOT NULL,
    target  TEXT,                  -- migration_id / user_id / email / setting key
    ip      TEXT,
    details JSONB,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_audit_log_created_at ON audit_log(created_at);
CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log(action);
CREATE INDEX IF NOT EXISTS idx_audit_log_user_id ON audit_log(user_id);

-- Reusable connection profiles (one side of a connection: source OR target)
CREATE TABLE IF NOT EXISTS connection_profiles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    provider TEXT NOT NULL,
    url TEXT,
    username TEXT,
    password_encrypted TEXT,
    refresh_token_encrypted TEXT,
    token_expires_at TIMESTAMP WITH TIME ZONE,
    oauth_user TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, name)
);

CREATE INDEX IF NOT EXISTS idx_conn_profiles_user ON connection_profiles(user_id);

CREATE OR REPLACE TRIGGER update_connection_profiles_updated_at
    BEFORE UPDATE ON connection_profiles
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- ============================================================================
-- Sync Engine — Jobs and State
-- ============================================================================

-- Table for Sync Jobs
CREATE TABLE IF NOT EXISTS sync_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_url TEXT NOT NULL,
    source_username TEXT NOT NULL,
    source_password_encrypted TEXT NOT NULL,
    source_refresh_token_encrypted TEXT,
    source_token_expires_at TIMESTAMP WITH TIME ZONE,
    target_url TEXT NOT NULL,
    target_username TEXT NOT NULL,
    target_password_encrypted TEXT NOT NULL,
    target_refresh_token_encrypted TEXT,
    target_token_expires_at TIMESTAMP WITH TIME ZONE,
    source_provider TEXT NOT NULL DEFAULT 'nextcloud',
    target_provider TEXT NOT NULL DEFAULT 'nextcloud',
    direction TEXT NOT NULL DEFAULT 'one_way'
        CHECK (direction IN ('one_way','two_way')),
    conflict_strategy TEXT NOT NULL DEFAULT 'OVERWRITE'
        CHECK (conflict_strategy IN ('OVERWRITE','SKIP','RENAME')),
    delete_propagation BOOLEAN NOT NULL DEFAULT FALSE,
    interval_minutes INT NOT NULL DEFAULT 15,
    threads INT NOT NULL DEFAULT 4,
    status TEXT NOT NULL DEFAULT 'IDLE',
    target_dir TEXT NOT NULL DEFAULT '/',
    selected_paths JSONB,
    last_run_at TIMESTAMP WITH TIME ZONE,
    last_run_status TEXT,
    error_message TEXT,
    total_files INT NOT NULL DEFAULT 0,
    total_bytes BIGINT NOT NULL DEFAULT 0,
    processed_files INT NOT NULL DEFAULT 0,
    processed_bytes BIGINT NOT NULL DEFAULT 0,
    live_bytes BIGINT NOT NULL DEFAULT 0,
    changed_files INT NOT NULL DEFAULT 0,
    deleted_files INT NOT NULL DEFAULT 0,
    failed_files INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_sync_jobs_user_id ON sync_jobs(user_id);
CREATE INDEX IF NOT EXISTS idx_sync_jobs_status ON sync_jobs(status);

CREATE OR REPLACE TRIGGER update_sync_jobs_updated_at
    BEFORE UPDATE ON sync_jobs
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- Table for Sync State (Persistent Delta Tracking)
CREATE TABLE IF NOT EXISTS sync_state (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sync_job_id UUID NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
    side TEXT NOT NULL CHECK (side IN ('source','target')),
    rel_path TEXT NOT NULL,
    size BIGINT NOT NULL DEFAULT 0,
    mtime TIMESTAMP WITH TIME ZONE,
    source_hash TEXT,
    target_hash TEXT,
    UNIQUE (sync_job_id, side, rel_path)
);

CREATE INDEX IF NOT EXISTS idx_sync_state_job ON sync_state(sync_job_id, side);

-- Modify Tasks table to support Sync Jobs
ALTER TABLE tasks ALTER COLUMN migration_id DROP NOT NULL;

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS sync_job_id UUID REFERENCES sync_jobs(id) ON DELETE CASCADE;

ALTER TABLE tasks DROP CONSTRAINT IF EXISTS chk_task_job_type;
ALTER TABLE tasks ADD CONSTRAINT chk_task_job_type CHECK (
    (migration_id IS NOT NULL AND sync_job_id IS NULL) OR
    (migration_id IS NULL AND sync_job_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_tasks_sync_status ON tasks(sync_job_id, status);

