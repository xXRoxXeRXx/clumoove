import { apiJson, type ApiJsonResult } from '../utils/apiClient';

export type BackupRequest = { apiUrl: string; token: string; backupId: string };
export type BackupAction = 'run' | 'pause' | 'resume';
const url = ({ apiUrl, backupId }: BackupRequest, suffix = '') => `${apiUrl}/api/backup/${encodeURIComponent(backupId)}${suffix}`;
const auth = (token: string) => ({ Authorization: `Bearer ${token}` });

export function getBackup<T>(request: BackupRequest & { signal?: AbortSignal }): Promise<ApiJsonResult<T>> { return apiJson<T>(url(request), { headers: auth(request.token), signal: request.signal }); }
export function listBackupRuns<T>(request: BackupRequest): Promise<ApiJsonResult<T[]>> { return apiJson<T[]>(url(request, '/runs'), { headers: auth(request.token) }); }
export function controlBackup(request: BackupRequest & { action: BackupAction }): Promise<ApiJsonResult<void>> { return apiJson(url(request, `/${request.action}`), { method: 'POST', headers: auth(request.token) }); }
export function deleteBackup(request: BackupRequest): Promise<ApiJsonResult<void>> { return apiJson(url(request), { method: 'DELETE', headers: auth(request.token) }); }
export function backupStreamUrl(apiUrl: string): string { return `${apiUrl}/api/backup/stream`; }
