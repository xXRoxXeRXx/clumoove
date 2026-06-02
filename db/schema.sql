-- Schema for Multi-Cloud Migrations-Plattform

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Table for Migrations (Main Jobs)
CREATE TABLE IF NOT EXISTS migrations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_url TEXT NOT NULL,
    source_username TEXT NOT NULL,
    source_password_encrypted TEXT NOT NULL,
    target_url TEXT NOT NULL,
    target_username TEXT NOT NULL,
    target_password_encrypted TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING', -- PENDING, INDEXING, RUNNING, PAUSED_CONNECTION_LOSS, COMPLETED, FAILED
    conflict_strategy TEXT NOT NULL DEFAULT 'SKIP', -- SKIP, OVERWRITE, RENAME
    total_files INT NOT NULL DEFAULT 0,
    total_bytes BIGINT NOT NULL DEFAULT 0,
    processed_files INT NOT NULL DEFAULT 0,
    processed_bytes BIGINT NOT NULL DEFAULT 0,
    skipped_files INT NOT NULL DEFAULT 0,
    failed_files INT NOT NULL DEFAULT 0,
    error_message TEXT,
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
