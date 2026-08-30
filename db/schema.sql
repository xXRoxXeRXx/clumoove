-- Schema for Multi-Cloud Migrations-Plattform

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Table for Users (Accounts)
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    display_name TEXT NOT NULL,
    language TEXT NOT NULL DEFAULT 'en' CHECK (language IN ('de', 'en')),
    role TEXT NOT NULL DEFAULT 'USER', -- USER, ADMIN
    active BOOLEAN NOT NULL DEFAULT TRUE, -- soft deactivation (suspend); blocks login
    must_change_password BOOLEAN NOT NULL DEFAULT FALSE, -- forced rotation on first login
    avatar BYTEA,
    avatar_mime TEXT,
    totp_secret_enc TEXT,
    totp_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    totp_backup_codes JSONB,
    totp_failed_attempts INTEGER NOT NULL DEFAULT 0,
    totp_locked_until TIMESTAMP WITH TIME ZONE,
    login_failed_attempts INTEGER NOT NULL DEFAULT 0,
    login_locked_until TIMESTAMP WITH TIME ZONE,
    last_login_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_users_last_login_at ON users(last_login_at);

-- Table for Refresh Tokens (Session Extension)
CREATE TABLE IF NOT EXISTS refresh_tokens (
    token_hash TEXT PRIMARY KEY,
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_agent TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_expires_at ON refresh_tokens(user_id, expires_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_refresh_tokens_id ON refresh_tokens(id);

-- Table for Application Settings (Key-Value Store)
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Table for Migrations (Main Jobs)
CREATE TABLE IF NOT EXISTS migrations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_url TEXT NOT NULL DEFAULT '',
    source_username TEXT NOT NULL DEFAULT '',
    source_password_encrypted TEXT NOT NULL DEFAULT '',
    source_refresh_token_encrypted TEXT,
    source_token_expires_at TIMESTAMP WITH TIME ZONE,
	    source_mega_session_id_encrypted TEXT,
	    source_mega_master_key_encrypted TEXT,
    target_url TEXT NOT NULL,
    target_username TEXT NOT NULL,
    target_password_encrypted TEXT NOT NULL,
    target_refresh_token_encrypted TEXT,
    target_token_expires_at TIMESTAMP WITH TIME ZONE,
	    target_mega_session_id_encrypted TEXT,
	    target_mega_master_key_encrypted TEXT,
    source_provider TEXT NOT NULL DEFAULT 'nextcloud',
    target_provider TEXT NOT NULL DEFAULT 'nextcloud',
    status TEXT NOT NULL DEFAULT 'PENDING', -- PENDING, INDEXING, RUNNING, PAUSED_CONNECTION_LOSS, COMPLETED, COMPLETED_WITH_ERRORS, FAILED, SCHEDULED
    conflict_strategy TEXT NOT NULL DEFAULT 'SKIP' CONSTRAINT chk_migrations_conflict_strategy CHECK (conflict_strategy IN ('SKIP', 'OVERWRITE', 'RENAME')),
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
    threads INT NOT NULL DEFAULT 8,
    bandwidth_limit_mbps INT NOT NULL DEFAULT 0,
    verification_generation INT NOT NULL DEFAULT 0,
    verification_lease_until TIMESTAMP WITH TIME ZONE,
    failed_retry_done BOOLEAN NOT NULL DEFAULT FALSE,
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
    claim_epoch BIGINT NOT NULL DEFAULT 0,
    pass_generation INT NOT NULL DEFAULT 0,
    target_hash TEXT,
    status TEXT NOT NULL DEFAULT 'PENDING', -- PENDING, RUNNING, COMPLETED, FAILED, SKIPPED, CANCELLED
    resource_type TEXT NOT NULL DEFAULT 'files', -- files, calendars, contacts
    metadata JSONB,
    error_message TEXT,
    attempts INT NOT NULL DEFAULT 0,
    checksum_verified BOOLEAN NOT NULL DEFAULT FALSE,
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

-- Sync schedules use interval_minutes from their linked sync job, never cron.
-- NOTE: The UPDATE below is a one-time data migration. It is safe to re-apply
-- (idempotent) but should not be executed against a live database that was
-- already cleaned up. In production the equivalent runs inside InitDB().
UPDATE schedules
SET cron_expression = NULL
WHERE task_type = 'sync' AND cron_expression IS NOT NULL;

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

-- Singleton instance mailer. Credentials never leave the server decrypted.
CREATE TABLE IF NOT EXISTS instance_smtp_settings (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    smtp_host TEXT NOT NULL,
    smtp_port INT NOT NULL DEFAULT 587,
    smtp_username TEXT NOT NULL,
    smtp_password_encrypted TEXT NOT NULL,
    smtp_from_email TEXT NOT NULL,
    smtp_from_name TEXT NOT NULL DEFAULT '',
    smtp_encryption TEXT NOT NULL DEFAULT 'tls' CHECK (smtp_encryption IN ('tls', 'starttls')),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE OR REPLACE TRIGGER update_instance_smtp_settings_updated_at
    BEFORE UPDATE ON instance_smtp_settings
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- Administrator-managed OAuth2 client credentials. Secret never leaves the server decrypted.
-- No CHECK constraint on provider: the whitelist is enforced in Go via oauth.IsProvider so a
-- future provider does not require a schema migration.
CREATE TABLE IF NOT EXISTS instance_oauth_providers (
    provider VARCHAR(32) PRIMARY KEY,
    client_id TEXT NOT NULL,
    client_secret_encrypted TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE OR REPLACE TRIGGER update_instance_oauth_providers_updated_at
    BEFORE UPDATE ON instance_oauth_providers
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
    id BIGSERIAL PRIMARY KEY,
    migration_id UUID NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    resource_type TEXT NOT NULL DEFAULT 'files',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_indexing_errors_migration_id ON indexing_errors(migration_id);

-- Audit Log: immutable, instance-wide record of security-relevant events.
CREATE TABLE IF NOT EXISTS audit_log (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,  -- actor (NULL for failed logins)
    action  TEXT NOT NULL,
    target  TEXT NOT NULL DEFAULT '',  -- migration_id / user_id / email / setting key
    ip      TEXT NOT NULL DEFAULT '',
    details JSONB,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_audit_log_created ON audit_log(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log(action);
CREATE INDEX IF NOT EXISTS idx_audit_log_user_id ON audit_log(user_id);

-- Reusable connection profiles (one side of a connection: source OR target)
CREATE TABLE IF NOT EXISTS connection_profiles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    provider TEXT NOT NULL,
    url TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT '',
    password_encrypted TEXT NOT NULL DEFAULT '',
    refresh_token_encrypted TEXT NOT NULL DEFAULT '',
    token_expires_at TIMESTAMP WITH TIME ZONE,
    oauth_user TEXT NOT NULL DEFAULT '',
	    mega_session_id_encrypted TEXT,
	    mega_master_key_encrypted TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, name)
);

CREATE INDEX IF NOT EXISTS idx_conn_profiles_user ON connection_profiles(user_id);

-- Profile references are nullable so profiles can be removed without deleting
-- the jobs that originally used them. This follows connection_profiles because
-- migrations is declared earlier in this bootstrap schema.
ALTER TABLE migrations
    ADD COLUMN IF NOT EXISTS source_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS target_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_migrations_source_profile_id ON migrations(source_profile_id);
CREATE INDEX IF NOT EXISTS idx_migrations_target_profile_id ON migrations(target_profile_id);

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
    source_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL,
    target_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL,
    source_url TEXT NOT NULL,
    source_username TEXT NOT NULL,
    source_password_encrypted TEXT NOT NULL,
    source_refresh_token_encrypted TEXT,
    source_token_expires_at TIMESTAMP WITH TIME ZONE,
	    source_mega_session_id_encrypted TEXT,
	    source_mega_master_key_encrypted TEXT,
    target_url TEXT NOT NULL,
    target_username TEXT NOT NULL,
    target_password_encrypted TEXT NOT NULL,
    target_refresh_token_encrypted TEXT,
    target_token_expires_at TIMESTAMP WITH TIME ZONE,
	    target_mega_session_id_encrypted TEXT,
	    target_mega_master_key_encrypted TEXT,
    source_provider TEXT NOT NULL DEFAULT 'nextcloud',
    target_provider TEXT NOT NULL DEFAULT 'nextcloud',
    direction TEXT NOT NULL DEFAULT 'one_way'
        CHECK (direction IN ('one_way','two_way')),
    conflict_strategy TEXT NOT NULL DEFAULT 'OVERWRITE'
        CHECK (conflict_strategy IN ('OVERWRITE','SKIP','RENAME')),
    delete_propagation BOOLEAN NOT NULL DEFAULT FALSE,
    interval_minutes INT NOT NULL DEFAULT 15,
    threads INT NOT NULL DEFAULT 8,
    bandwidth_limit_mbps INT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'IDLE',
    run_generation INT NOT NULL DEFAULT 0,
    verification_generation INT NOT NULL DEFAULT 0,
    verification_lease_until TIMESTAMP WITH TIME ZONE,
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
CREATE INDEX IF NOT EXISTS idx_sync_jobs_source_profile_id ON sync_jobs(source_profile_id);
CREATE INDEX IF NOT EXISTS idx_sync_jobs_target_profile_id ON sync_jobs(target_profile_id);

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
    etag TEXT,
    UNIQUE (sync_job_id, side, rel_path)
);

CREATE INDEX IF NOT EXISTS idx_sync_state_job ON sync_state(sync_job_id, side);

-- ============================================================================
-- Backup repository catalog (Release 1 persistence foundation)
-- ============================================================================

CREATE TABLE IF NOT EXISTS backup_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    lock_id BIGSERIAL UNIQUE NOT NULL,
    source_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL,
    target_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL,
    source_url TEXT NOT NULL,
    source_username TEXT NOT NULL,
    source_password_encrypted TEXT NOT NULL,
    source_refresh_token_encrypted TEXT,
    source_token_expires_at TIMESTAMP WITH TIME ZONE,
    source_mega_session_id_encrypted TEXT,
    source_mega_master_key_encrypted TEXT,
    target_url TEXT NOT NULL,
    target_username TEXT NOT NULL,
    target_password_encrypted TEXT NOT NULL,
    target_refresh_token_encrypted TEXT,
    target_token_expires_at TIMESTAMP WITH TIME ZONE,
    target_mega_session_id_encrypted TEXT,
    target_mega_master_key_encrypted TEXT,
    source_provider TEXT NOT NULL,
    target_provider TEXT NOT NULL,
    selected_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
    target_dir TEXT NOT NULL,
    repository_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    repository_root TEXT NOT NULL,
    cron_expression TEXT NOT NULL,
    timezone TEXT NOT NULL,
    retention_count INT NOT NULL DEFAULT 30 CHECK (retention_count >= 1),
    threads INT NOT NULL DEFAULT 8 CHECK (threads BETWEEN 1 AND 16),
    status TEXT NOT NULL DEFAULT 'IDLE' CHECK (status IN ('IDLE','QUEUED','SCANNING','RUNNING','VERIFYING','PAUSED','FAILED','DELETING')),
    run_generation INT NOT NULL DEFAULT 0 CHECK (run_generation >= 0),
    last_run_at TIMESTAMP WITH TIME ZONE,
    last_run_status TEXT,
    total_files INT NOT NULL DEFAULT 0 CHECK (total_files >= 0),
    total_bytes BIGINT NOT NULL DEFAULT 0 CHECK (total_bytes >= 0),
    processed_files INT NOT NULL DEFAULT 0 CHECK (processed_files >= 0),
    processed_bytes BIGINT NOT NULL DEFAULT 0 CHECK (processed_bytes >= 0),
    deduplicated_bytes BIGINT NOT NULL DEFAULT 0 CHECK (deduplicated_bytes >= 0),
    failed_files INT NOT NULL DEFAULT 0 CHECK (failed_files >= 0),
    error_code TEXT,
    deletion_state TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (deletion_state IN ('ACTIVE','REQUESTED','DELETING','DELETED')),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_backup_jobs_user_created ON backup_jobs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_backup_jobs_status ON backup_jobs(status);

CREATE TABLE IF NOT EXISTS backup_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
    generation INT NOT NULL CHECK (generation > 0),
    trigger TEXT NOT NULL CHECK (trigger IN ('manual','schedule','catch_up')),
    scheduled_local_key TEXT,
    state TEXT NOT NULL DEFAULT 'QUEUED' CHECK (state IN ('QUEUED','SCANNING','RUNNING','VERIFYING','COMPLETED','PARTIAL','FAILED','CANCELLED')),
    total_files INT NOT NULL DEFAULT 0 CHECK (total_files >= 0),
    total_bytes BIGINT NOT NULL DEFAULT 0 CHECK (total_bytes >= 0),
    processed_files INT NOT NULL DEFAULT 0 CHECK (processed_files >= 0),
    processed_bytes BIGINT NOT NULL DEFAULT 0 CHECK (processed_bytes >= 0),
    deduplicated_bytes BIGINT NOT NULL DEFAULT 0 CHECK (deduplicated_bytes >= 0),
    failed_files INT NOT NULL DEFAULT 0 CHECK (failed_files >= 0),
    error_code TEXT,
    started_at TIMESTAMP WITH TIME ZONE,
    finished_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (backup_job_id, generation),
    UNIQUE (backup_job_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_runs_scheduled_local_key
    ON backup_runs(backup_job_id, scheduled_local_key)
    WHERE scheduled_local_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_backup_runs_queued ON backup_runs(created_at) WHERE state = 'QUEUED';
CREATE INDEX IF NOT EXISTS idx_backup_runs_job_created ON backup_runs(backup_job_id, created_at DESC);

CREATE TABLE IF NOT EXISTS backup_snapshots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
    backup_run_id UUID NOT NULL,
    state TEXT NOT NULL DEFAULT 'PUBLISHING' CHECK (state IN ('PUBLISHING','READY','PARTIAL','EXPIRED','DELETING','DAMAGED')),
    selected_roots JSONB NOT NULL DEFAULT '[]'::jsonb,
    total_files INT NOT NULL DEFAULT 0 CHECK (total_files >= 0),
    total_dirs INT NOT NULL DEFAULT 0 CHECK (total_dirs >= 0),
    total_bytes BIGINT NOT NULL DEFAULT 0 CHECK (total_bytes >= 0),
    omitted_unstable_count INT NOT NULL DEFAULT 0 CHECK (omitted_unstable_count >= 0),
    omitted_error_count INT NOT NULL DEFAULT 0 CHECK (omitted_error_count >= 0),
    integrity_state TEXT NOT NULL DEFAULT 'UNKNOWN' CHECK (integrity_state IN ('UNKNOWN','VALID','DAMAGED')),
    expires_at TIMESTAMP WITH TIME ZONE,
    deletion_started_at TIMESTAMP WITH TIME ZONE,
    deleted_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (backup_job_id, backup_run_id),
    FOREIGN KEY (backup_job_id, backup_run_id) REFERENCES backup_runs(backup_job_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_backup_snapshots_job_created ON backup_snapshots(backup_job_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_backup_snapshots_visible ON backup_snapshots(backup_job_id, created_at DESC) WHERE state IN ('READY','PARTIAL','DAMAGED');

CREATE TABLE IF NOT EXISTS backup_packs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
    remote_rel_path TEXT NOT NULL,
    sha256 BYTEA NOT NULL CHECK (octet_length(sha256) = 32),
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    state TEXT NOT NULL DEFAULT 'UPLOADING' CHECK (state IN ('UPLOADING','READY','REPLACING','DELETE_PENDING','DELETED','DAMAGED')),
    generation INT NOT NULL DEFAULT 0 CHECK (generation >= 0),
    last_checked_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (backup_job_id, id),
    UNIQUE (backup_job_id, remote_rel_path),
    UNIQUE (backup_job_id, sha256)
);
CREATE INDEX IF NOT EXISTS idx_backup_packs_job_state ON backup_packs(backup_job_id, state);

CREATE TABLE IF NOT EXISTS backup_blocks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_job_id UUID NOT NULL,
    sha256 BYTEA NOT NULL CHECK (octet_length(sha256) = 32),
    plaintext_size INT NOT NULL CHECK (plaintext_size BETWEEN 1 AND 4194304),
    backup_pack_id UUID NOT NULL,
    payload_offset BIGINT NOT NULL CHECK (payload_offset >= 0),
    payload_length INT NOT NULL CHECK (payload_length BETWEEN 1 AND 4194304),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (backup_job_id, sha256),
    UNIQUE (backup_job_id, id),
    FOREIGN KEY (backup_job_id, backup_pack_id) REFERENCES backup_packs(backup_job_id, id) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS idx_backup_blocks_job_pack ON backup_blocks(backup_job_id, backup_pack_id);

CREATE TABLE IF NOT EXISTS backup_snapshot_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_snapshot_id UUID NOT NULL REFERENCES backup_snapshots(id) ON DELETE CASCADE,
    relative_path TEXT NOT NULL,
    is_dir BOOLEAN NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    mtime TIMESTAMP WITH TIME ZONE,
    metadata JSONB,
    file_sha256 BYTEA CHECK (file_sha256 IS NULL OR octet_length(file_sha256) = 32),
    state TEXT NOT NULL DEFAULT 'AVAILABLE' CHECK (state IN ('AVAILABLE','UNSTABLE','ERROR')),
    error_code TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (backup_snapshot_id, relative_path),
    CHECK ((is_dir AND size_bytes = 0 AND file_sha256 IS NULL) OR (NOT is_dir AND file_sha256 IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_backup_snapshot_items_snapshot ON backup_snapshot_items(backup_snapshot_id, relative_path);

CREATE TABLE IF NOT EXISTS backup_snapshot_item_blocks (
    backup_snapshot_item_id UUID NOT NULL REFERENCES backup_snapshot_items(id) ON DELETE CASCADE,
    ordinal INT NOT NULL CHECK (ordinal >= 0),
    backup_block_id UUID NOT NULL REFERENCES backup_blocks(id) ON DELETE RESTRICT,
    PRIMARY KEY (backup_snapshot_item_id, ordinal)
);
CREATE INDEX IF NOT EXISTS idx_backup_snapshot_item_blocks_block ON backup_snapshot_item_blocks(backup_block_id);

CREATE TABLE IF NOT EXISTS backup_maintenance (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('RETENTION','COMPACTION','DELETE_REPOSITORY','VERIFY')),
    state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING','RUNNING','RETRY_WAIT','CANCELLING','CANCELLED','COMPLETED','FAILED')),
    byte_budget BIGINT CHECK (byte_budget IS NULL OR byte_budget > 0),
    claim_deadline TIMESTAMP WITH TIME ZONE,
    cursor JSONB NOT NULL DEFAULT '{}'::jsonb,
    attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_retry_at TIMESTAMP WITH TIME ZONE,
    error_code TEXT,
    verify_mode TEXT CHECK (verify_mode IS NULL OR verify_mode IN ('METADATA','BUDGETED','FULL')),
    processed_bytes BIGINT NOT NULL DEFAULT 0 CHECK (processed_bytes >= 0),
    total_packs INT NOT NULL DEFAULT 0 CHECK (total_packs >= 0),
    checked_packs INT NOT NULL DEFAULT 0 CHECK (checked_packs >= 0),
    missing_packs INT NOT NULL DEFAULT 0 CHECK (missing_packs >= 0),
    damaged_packs INT NOT NULL DEFAULT 0 CHECK (damaged_packs >= 0),
    coordinator_generation INT NOT NULL DEFAULT 0 CHECK (coordinator_generation >= 0),
    coordinator_lease_until TIMESTAMP WITH TIME ZONE,
    worker_hash TEXT,
    CONSTRAINT chk_backup_maintenance_verify_mode CHECK (
        (kind = 'VERIFY' AND (
            (verify_mode IN ('METADATA', 'FULL') AND byte_budget IS NULL) OR
            (verify_mode = 'BUDGETED' AND byte_budget BETWEEN 67108864 AND 1099511627776)
        )) OR
        (kind <> 'VERIFY' AND verify_mode IS NULL)
    ),
    started_at TIMESTAMP WITH TIME ZONE,
    finished_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_backup_maintenance_pending
    ON backup_maintenance(next_retry_at, created_at)
    WHERE state IN ('PENDING','RETRY_WAIT') AND kind <> 'VERIFY';
CREATE INDEX IF NOT EXISTS idx_backup_maintenance_job ON backup_maintenance(backup_job_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_maintenance_active_verify
    ON backup_maintenance(backup_job_id) WHERE kind = 'VERIFY' AND state IN ('PENDING','RUNNING','RETRY_WAIT','CANCELLING');

CREATE TABLE IF NOT EXISTS backup_verify_targets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_maintenance_id UUID NOT NULL REFERENCES backup_maintenance(id) ON DELETE CASCADE,
    backup_pack_id UUID CONSTRAINT fk_backup_verify_targets_live_pack REFERENCES backup_packs(id) ON DELETE RESTRICT,
    pack_remote_path TEXT NOT NULL,
    pack_sha256 BYTEA NOT NULL CHECK (octet_length(pack_sha256) = 32),
    pack_size_bytes BIGINT NOT NULL CHECK (pack_size_bytes > 0),
    state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING','RUNNING','COMPLETED','MISSING','DAMAGED','FAILED','CANCELLED')),
    claim_epoch BIGINT NOT NULL DEFAULT 0 CHECK (claim_epoch >= 0),
    claim_deadline TIMESTAMP WITH TIME ZONE,
    worker_hash TEXT,
    cursor JSONB NOT NULL DEFAULT '{}'::jsonb,
    bytes_read BIGINT NOT NULL DEFAULT 0 CHECK (bytes_read >= 0),
    error_code TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (backup_maintenance_id, pack_remote_path)
);
CREATE INDEX IF NOT EXISTS idx_backup_verify_targets_claim ON backup_verify_targets(backup_maintenance_id, state);

CREATE TABLE IF NOT EXISTS restore_previews (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
    backup_snapshot_id UUID NOT NULL REFERENCES backup_snapshots(id) ON DELETE CASCADE,
    retry_restore_job_id UUID,
    target_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL,
    selected_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
    target_provider TEXT NOT NULL,
    target_url TEXT NOT NULL DEFAULT '',
    target_username TEXT NOT NULL DEFAULT '',
    target_password_encrypted TEXT,
    target_refresh_token_encrypted TEXT,
    target_token_expires_at TIMESTAMP WITH TIME ZONE,
    target_mega_session_id_encrypted TEXT,
    target_mega_master_key_encrypted TEXT,
    target_connection_identity TEXT NOT NULL DEFAULT '',
    target_root TEXT NOT NULL,
    conflict_strategy TEXT NOT NULL DEFAULT 'RENAME' CHECK (conflict_strategy IN ('SKIP','OVERWRITE','RENAME')),
    threads INT NOT NULL DEFAULT 8 CHECK (threads BETWEEN 1 AND 16),
    bandwidth_mbps INT NOT NULL DEFAULT 0 CHECK (bandwidth_mbps BETWEEN 0 AND 1000),
    config_fingerprint BYTEA NOT NULL CHECK (octet_length(config_fingerprint) = 32),
    status TEXT NOT NULL DEFAULT 'QUEUED' CHECK (status IN ('QUEUED','RUNNING','READY','FAILED','EXPIRED','CONSUMED','CANCELLED')),
    total_files INT NOT NULL DEFAULT 0 CHECK (total_files >= 0),
    total_directories INT NOT NULL DEFAULT 0 CHECK (total_directories >= 0),
    total_bytes BIGINT NOT NULL DEFAULT 0 CHECK (total_bytes >= 0),
    existing_file_conflicts INT NOT NULL DEFAULT 0 CHECK (existing_file_conflicts >= 0),
    mergeable_directories INT NOT NULL DEFAULT 0 CHECK (mergeable_directories >= 0),
    type_conflicts INT NOT NULL DEFAULT 0 CHECK (type_conflicts >= 0),
    unavailable_items INT NOT NULL DEFAULT 0 CHECK (unavailable_items >= 0),
    expected_skips INT NOT NULL DEFAULT 0 CHECK (expected_skips >= 0),
    expected_renames INT NOT NULL DEFAULT 0 CHECK (expected_renames >= 0),
    metadata_warnings INT NOT NULL DEFAULT 0 CHECK (metadata_warnings >= 0),
    conflict_examples JSONB NOT NULL DEFAULT '[]'::jsonb,
    error_code TEXT,
    coordinator_generation INT NOT NULL DEFAULT 0 CHECK (coordinator_generation >= 0),
    coordinator_lease_until TIMESTAMP WITH TIME ZONE,
    worker_hash TEXT,
    ready_at TIMESTAMP WITH TIME ZONE,
    expires_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_restore_previews_claim ON restore_previews(created_at) WHERE status = 'QUEUED';
CREATE INDEX IF NOT EXISTS idx_restore_previews_owner ON restore_previews(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS restore_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    backup_job_id UUID REFERENCES backup_jobs(id) ON DELETE SET NULL,
    backup_snapshot_id UUID REFERENCES backup_snapshots(id) ON DELETE SET NULL,
    source_backup_ref UUID NOT NULL,
    source_snapshot_ref UUID NOT NULL,
    target_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL,
    selected_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
    target_provider TEXT NOT NULL,
    target_url TEXT NOT NULL DEFAULT '',
    target_username TEXT NOT NULL DEFAULT '',
    target_connection_identity TEXT NOT NULL DEFAULT '',
    target_root TEXT NOT NULL,
    conflict_strategy TEXT NOT NULL CHECK (conflict_strategy IN ('SKIP','OVERWRITE','RENAME')),
    threads INT NOT NULL DEFAULT 8 CHECK (threads BETWEEN 1 AND 16),
    bandwidth_mbps INT NOT NULL DEFAULT 0 CHECK (bandwidth_mbps BETWEEN 0 AND 1000),
    config_fingerprint BYTEA NOT NULL CHECK (octet_length(config_fingerprint) = 32),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_restore_jobs_owner ON restore_jobs(user_id, created_at DESC);
ALTER TABLE restore_previews DROP CONSTRAINT IF EXISTS fk_restore_previews_retry_job;
ALTER TABLE restore_previews ADD CONSTRAINT fk_restore_previews_retry_job
    FOREIGN KEY (retry_restore_job_id) REFERENCES restore_jobs(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS restore_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    restore_job_id UUID NOT NULL REFERENCES restore_jobs(id) ON DELETE CASCADE,
    generation INT NOT NULL CHECK (generation > 0),
    status TEXT NOT NULL DEFAULT 'QUEUED' CHECK (status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING','COMPLETED','PARTIAL','FAILED','CANCELLED')),
    threads INT NOT NULL DEFAULT 8 CHECK (threads BETWEEN 1 AND 16),
    bandwidth_mbps INT NOT NULL DEFAULT 0 CHECK (bandwidth_mbps BETWEEN 0 AND 1000),
    target_password_encrypted TEXT,
    target_refresh_token_encrypted TEXT,
    target_token_expires_at TIMESTAMP WITH TIME ZONE,
    target_mega_session_id_encrypted TEXT,
    target_mega_master_key_encrypted TEXT,
    coordinator_generation INT NOT NULL DEFAULT 0 CHECK (coordinator_generation >= 0),
    coordinator_lease_until TIMESTAMP WITH TIME ZONE,
    worker_hash TEXT,
    total_files INT NOT NULL DEFAULT 0 CHECK (total_files >= 0),
    total_bytes BIGINT NOT NULL DEFAULT 0 CHECK (total_bytes >= 0),
    processed_files INT NOT NULL DEFAULT 0 CHECK (processed_files >= 0),
    processed_bytes BIGINT NOT NULL DEFAULT 0 CHECK (processed_bytes >= 0),
    failed_files INT NOT NULL DEFAULT 0 CHECK (failed_files >= 0),
    error_code TEXT,
    started_at TIMESTAMP WITH TIME ZONE,
    finished_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (restore_job_id, generation)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_restore_runs_active ON restore_runs(restore_job_id) WHERE status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING');
CREATE INDEX IF NOT EXISTS idx_restore_runs_claim ON restore_runs(created_at) WHERE status = 'QUEUED';

CREATE TABLE IF NOT EXISTS restore_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    restore_run_id UUID NOT NULL REFERENCES restore_runs(id) ON DELETE CASCADE,
    parent_item_id UUID,
    snapshot_relative_path TEXT NOT NULL,
    is_dir BOOLEAN NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    file_sha256 BYTEA CHECK (file_sha256 IS NULL OR octet_length(file_sha256) = 32),
    source_mtime TIMESTAMP WITH TIME ZONE,
    source_metadata JSONB,
    target_path TEXT NOT NULL,
    resolved_target_path TEXT,
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','RUNNING','COMPLETED','SKIPPED','WARNING','FAILED','CANCELLED')),
    verification_kind TEXT CHECK (verification_kind IS NULL OR verification_kind IN ('HASH_VERIFIED','SIZE_VERIFIED')),
    outcome_code TEXT,
    attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_retry_at TIMESTAMP WITH TIME ZONE,
    claim_epoch BIGINT NOT NULL DEFAULT 0 CHECK (claim_epoch >= 0),
    worker_hash TEXT,
    claim_deadline TIMESTAMP WITH TIME ZONE,
    error_code TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (restore_run_id, snapshot_relative_path),
    UNIQUE (restore_run_id, id),
    FOREIGN KEY (restore_run_id, parent_item_id) REFERENCES restore_items(restore_run_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_restore_items_run_status ON restore_items(restore_run_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_restore_items_retry ON restore_items(next_retry_at) WHERE status = 'PENDING' AND next_retry_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS restore_path_reservations (
    restore_run_id UUID NOT NULL REFERENCES restore_runs(id) ON DELETE CASCADE,
    canonical_path TEXT NOT NULL,
    restore_item_id UUID NOT NULL REFERENCES restore_items(id) ON DELETE CASCADE,
    PRIMARY KEY (restore_run_id, canonical_path),
    UNIQUE (restore_run_id, restore_item_id)
);

CREATE TABLE IF NOT EXISTS restore_item_blocks (
    restore_item_id UUID NOT NULL REFERENCES restore_items(id) ON DELETE CASCADE,
    ordinal INT NOT NULL CHECK (ordinal >= 0),
    pack_remote_path TEXT NOT NULL,
    pack_sha256 BYTEA NOT NULL CHECK (octet_length(pack_sha256) = 32),
    pack_size_bytes BIGINT NOT NULL CHECK (pack_size_bytes > 0),
    payload_offset BIGINT NOT NULL CHECK (payload_offset >= 0),
    payload_length INT NOT NULL CHECK (payload_length BETWEEN 1 AND 4194304),
    block_sha256 BYTEA NOT NULL CHECK (octet_length(block_sha256) = 32),
    plaintext_size INT NOT NULL CHECK (plaintext_size BETWEEN 1 AND 4194304),
    PRIMARY KEY (restore_item_id, ordinal)
);

CREATE TABLE IF NOT EXISTS restore_pack_pins (
    restore_run_id UUID NOT NULL REFERENCES restore_runs(id) ON DELETE CASCADE,
    backup_pack_id UUID NOT NULL REFERENCES backup_packs(id) ON DELETE RESTRICT,
    PRIMARY KEY (restore_run_id, backup_pack_id)
);

CREATE OR REPLACE TRIGGER update_backup_jobs_updated_at
    BEFORE UPDATE ON backup_jobs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE OR REPLACE TRIGGER update_backup_runs_updated_at
    BEFORE UPDATE ON backup_runs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE OR REPLACE TRIGGER update_backup_snapshots_updated_at
    BEFORE UPDATE ON backup_snapshots FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE OR REPLACE TRIGGER update_backup_packs_updated_at
    BEFORE UPDATE ON backup_packs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE OR REPLACE TRIGGER update_backup_blocks_updated_at
    BEFORE UPDATE ON backup_blocks FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE OR REPLACE TRIGGER update_backup_snapshot_items_updated_at
    BEFORE UPDATE ON backup_snapshot_items FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE OR REPLACE TRIGGER update_backup_maintenance_updated_at
    BEFORE UPDATE ON backup_maintenance FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE OR REPLACE TRIGGER update_restore_previews_updated_at
    BEFORE UPDATE ON restore_previews FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE OR REPLACE TRIGGER update_restore_jobs_updated_at
    BEFORE UPDATE ON restore_jobs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE OR REPLACE TRIGGER update_restore_runs_updated_at
    BEFORE UPDATE ON restore_runs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE OR REPLACE TRIGGER update_restore_items_updated_at
    BEFORE UPDATE ON restore_items FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- Modify Tasks table to support Sync Jobs
ALTER TABLE tasks ALTER COLUMN migration_id DROP NOT NULL;

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS sync_job_id UUID REFERENCES sync_jobs(id) ON DELETE CASCADE;
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS run_generation INT NOT NULL DEFAULT 0;
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS verification_generation INT NOT NULL DEFAULT 0;
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS verification_lease_until TIMESTAMP WITH TIME ZONE;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS pass_generation INT NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'chk_task_job_type'
    ) THEN
        ALTER TABLE tasks ADD CONSTRAINT chk_task_job_type CHECK (
            (migration_id IS NOT NULL AND sync_job_id IS NULL) OR
            (migration_id IS NULL AND sync_job_id IS NOT NULL)
        );
    END IF;
END $$;

DROP INDEX IF EXISTS idx_tasks_sync_status;
CREATE INDEX IF NOT EXISTS idx_tasks_sync_gen_status ON tasks(sync_job_id, pass_generation, status);
CREATE INDEX IF NOT EXISTS idx_tasks_wait_conflict_copy
    ON tasks ((metadata->>'wait_for_conflict_copy'))
    WHERE status = 'PENDING' AND metadata->>'wait_for_conflict_copy' = 'true';

-- Multi-channel completion notification outbox (after migrations and sync_jobs).
CREATE TABLE IF NOT EXISTS notification_channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type TEXT NOT NULL CHECK (type IN ('email','gotify','ntfy','telegram','discord')),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    config_encrypted TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, type)
);
CREATE INDEX IF NOT EXISTS idx_notification_channels_user ON notification_channels(user_id);

-- Preserve only the old email preference while the legacy table is present.
-- The guard makes this destructive cleanup a one-time upgrade migration.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'user_smtp_settings') THEN
        UPDATE notification_channels SET config_encrypted = '' WHERE type = 'email';
        DROP TABLE user_smtp_settings;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS notification_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('migration','sync','restore','backup')),
    migration_id UUID REFERENCES migrations(id) ON DELETE CASCADE,
    run_generation INT NOT NULL DEFAULT 0,
    sync_job_id UUID REFERENCES sync_jobs(id) ON DELETE CASCADE,
    restore_run_id UUID REFERENCES restore_runs(id) ON DELETE CASCADE,
    backup_run_id UUID REFERENCES backup_runs(id) ON DELETE CASCADE,
    run_at TIMESTAMP WITH TIME ZONE NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- XOR: exactly one parent must be set.
    CHECK (
        (kind = 'migration' AND migration_id IS NOT NULL AND sync_job_id IS NULL AND restore_run_id IS NULL AND backup_run_id IS NULL) OR
        (kind = 'sync'      AND sync_job_id  IS NOT NULL AND migration_id IS NULL AND restore_run_id IS NULL AND backup_run_id IS NULL) OR
        (kind = 'restore'   AND restore_run_id IS NOT NULL AND migration_id IS NULL AND sync_job_id IS NULL AND backup_run_id IS NULL) OR
        (kind = 'backup'    AND backup_run_id IS NOT NULL AND migration_id IS NULL AND sync_job_id IS NULL AND restore_run_id IS NULL)
    )
);

ALTER TABLE migrations ADD COLUMN IF NOT EXISTS notification_generation INT NOT NULL DEFAULT 0;
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS verification_generation INT NOT NULL DEFAULT 0;
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS verification_lease_until TIMESTAMP WITH TIME ZONE;
ALTER TABLE notification_events ADD COLUMN IF NOT EXISTS run_generation INT NOT NULL DEFAULT 0;
ALTER TABLE notification_events ADD COLUMN IF NOT EXISTS restore_run_id UUID REFERENCES restore_runs(id) ON DELETE CASCADE;
ALTER TABLE notification_events ADD COLUMN IF NOT EXISTS backup_run_id UUID REFERENCES backup_runs(id) ON DELETE CASCADE;
ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS notification_events_migration_id_key;
ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS notification_events_kind_check;
ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS notification_events_check;
ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS chk_notification_events_kind;
ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS chk_notification_events_parent;
ALTER TABLE notification_events ADD CONSTRAINT chk_notification_events_kind CHECK (kind IN ('migration','sync','restore','backup'));
ALTER TABLE notification_events ADD CONSTRAINT chk_notification_events_parent CHECK (
    num_nonnulls(migration_id, sync_job_id, restore_run_id, backup_run_id) = 1 AND
    ((kind = 'migration' AND migration_id IS NOT NULL) OR (kind = 'sync' AND sync_job_id IS NOT NULL) OR (kind = 'restore' AND restore_run_id IS NOT NULL) OR (kind = 'backup' AND backup_run_id IS NOT NULL))
);

-- Partial unique indexes replace the old table-level UNIQUE (sync_job_id, run_at).
-- NULL != NULL in SQL unique constraints, so the table constraint was toothless
-- for migration-kind rows where sync_job_id IS NULL. Partial indexes are precise.
CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_events_migration_uniq
    ON notification_events(migration_id, run_generation)
    WHERE migration_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_events_sync_uniq
    ON notification_events(sync_job_id, run_at)
    WHERE sync_job_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_events_restore_uniq
    ON notification_events(restore_run_id)
    WHERE restore_run_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_events_backup_uniq
    ON notification_events(backup_run_id)
    WHERE backup_run_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS notification_deliveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id UUID NOT NULL REFERENCES notification_events(id) ON DELETE CASCADE,
    channel_type TEXT NOT NULL CHECK (channel_type IN ('email','gotify','ntfy','telegram','discord')),
    config_encrypted TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING','RUNNING','SENT','FAILED')),
    attempts INT NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_error_code TEXT,
    sent_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (event_id, channel_type)
);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_pending ON notification_deliveries(state, next_retry_at);

-- notification_deliveries has an updated_at column but no trigger was originally
-- added. Add it here so every UPDATE reflects the actual modification time.
CREATE OR REPLACE TRIGGER update_notification_deliveries_updated_at
    BEFORE UPDATE ON notification_deliveries
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();
