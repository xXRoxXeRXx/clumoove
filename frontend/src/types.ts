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
  | 'googlephotos'
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
