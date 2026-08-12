export interface CloudFile {
  path: string;
  name: string;
  size: number;
  is_dir: boolean;
  hash: string;
  last_modified: string;
  metadata?: { custom_props?: Record<string, string> };
}

export type Provider =
  | 'nextcloud'
  | 'opencloud'
  | 'seafile'
  | 'dropbox'
  | 'webdav'
  | 'magentacloud'
  | 'google'
  | 'onedrive'
  | 'hidrive'
  | 'smb'
  | 's3'
  | 'sftp'
  | 'ftp'
  | 'local'
  | 'immich'
  | 'mega';


export type ProviderId = Provider;

export const OAUTH_PROVIDERS = ['dropbox', 'google', 'onedrive', 'hidrive'] as const satisfies readonly Provider[];

export function isOAuthProvider(provider: string): provider is (typeof OAUTH_PROVIDERS)[number] {
  return (OAUTH_PROVIDERS as readonly string[]).includes(provider);
}

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

export type UserRole = 'USER' | 'ADMIN';

export type JobStatus =
  | 'PENDING'
  | 'SCHEDULED'
  | 'INDEXING'
  | 'RUNNING'
  | 'VERIFYING'
  | 'PAUSED'
  | 'PAUSED_CONNECTION_LOSS'
  | 'COMPLETED'
  | 'COMPLETED_WITH_ERRORS'
  | 'FAILED'
  | 'CANCELLED'
  | 'IDLE';

export interface User {
  id?: string;
  email?: string;
  display_name?: string;
  language?: 'de' | 'en';
  role?: UserRole | string;
  avatar?: string;
  totp_enabled?: boolean;
  last_login_at?: string | null;
}

export interface Migration {
  id: string;
  status: JobStatus;
  source_provider: string;
  source_url: string | null;
  target_provider: string;
  target_url: string | null;
  target_dir?: string;
  selected_paths?: string[];
  processed_files: number;
  total_files: number;
  processed_bytes: number;
  live_bytes?: number;
  total_bytes: number;
  created_at: string;
}

export interface SyncJob {
  id: string;
  status: JobStatus;
  threads?: number;
  bandwidth_limit_mbps?: number;
  direction: 'one_way' | 'two_way';
  interval_minutes: number;
  delete_propagation: boolean;
  conflict_strategy: 'OVERWRITE' | 'SKIP' | 'RENAME';
  source_provider: string;
  source_url: string | null;
  source_username?: string | null;
  target_provider: string;
  target_url: string | null;
  target_username?: string | null;
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
  next_run_at?: string | null;
  last_run_status: string | null;
  error_message: string | null;
  created_at: string;
}

