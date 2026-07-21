export interface CloudFile {
  path: string;
  name: string;
  size: number;
  is_dir: boolean;
  hash: string;
  last_modified: string;
}

export type Provider =
  | 'nextcloud'
  | 'dropbox'
  | 'webdav'
  | 'magentacloud'
  | 'google'
  | 'smb'
  | 's3'
  | 'sftp'
  | 'local';

export interface MigrationConfig {
  source_url: string;
  source_username: string;
  source_password: string;
  source_refresh_token: string;
  source_token_expires_in: number;
  target_url: string;
  target_username: string;
  target_password: string;
  target_refresh_token: string;
  target_token_expires_in: number;
  source_provider: Provider;
  target_provider: Provider;
  source_profile_id?: string;
  target_profile_id?: string;
}

export interface User {
  id?: string;
  email?: string;
  display_name?: string;
  role?: string;
  avatar?: string;
  totp_enabled?: boolean;
}

export interface Migration {
  id: string;
  status: string;
  source_provider: string;
  source_url: string | null;
  target_provider: string;
  target_url: string | null;
  processed_files: number;
  total_files: number;
  processed_bytes: number;
  live_bytes?: number;
  total_bytes: number;
  created_at: string;
}

export interface SyncJob {
  id: string;
  status: string;
  threads?: number;
  direction: 'one_way' | 'two_way';
  interval_minutes: number;
  delete_propagation: boolean;
  conflict_strategy: 'OVERWRITE' | 'SKIP' | 'RENAME';
  source_provider: string;
  source_url: string | null;
  target_provider: string;
  target_url: string | null;
  target_dir?: string;
  selected_paths?: string[];
  total_files: number;
  total_bytes?: number;
  processed_files: number;
  processed_bytes?: number;
  live_bytes?: number;
  changed_files: number;
  deleted_files: number;
  failed_files: number;
  active_files?: string[];
  last_run_at: string | null;
  last_run_status: string | null;
  error_message: string | null;
  created_at: string;
}

