import { apiFetch, apiJson, type ApiJsonResult } from '../utils/apiClient';
import { authHeaders, jsonAuthHeaders, type AuthenticatedRequest } from './headers';

export type BackupRequest = AuthenticatedRequest & { backupId: string };
export type RestoreAuthRequest = AuthenticatedRequest;
export type BackupAction = 'run' | 'pause' | 'resume';
export type UpdateBackupPayload = {
  cron_expression: string;
  timezone: string;
  retention_count: number;
  threads: number;
};
export type BackupVerifyPayload = {
  mode: string;
  byte_budget?: number;
  confirm_full?: boolean;
};
export type CreateRestorePreviewPayload = {
  selected_paths: string[];
  target_root: string;
  conflict_strategy: string;
  threads: number;
  bandwidth_mbps: number;
  target_profile_id?: string;
  target_provider?: string;
  target_url?: string;
  target_username?: string;
  target_password?: string;
  target_refresh_token?: string;
};
const url = ({ apiUrl, backupId }: BackupRequest, suffix = '') => `${apiUrl}/api/backup/${encodeURIComponent(backupId)}${suffix}`;
const restoreUrl = ({ apiUrl }: RestoreAuthRequest, suffix = '') => `${apiUrl}/api/restore${suffix}`;

export function getBackup<T>(request: BackupRequest & { signal?: AbortSignal }): Promise<ApiJsonResult<T>> { return apiJson<T>(url(request), { headers: authHeaders(request.token), signal: request.signal }); }
export function listBackupRuns<T>(request: BackupRequest): Promise<ApiJsonResult<T[]>> { return apiJson<T[]>(url(request, '/runs'), { headers: authHeaders(request.token) }); }
export function controlBackup(request: BackupRequest & { action: BackupAction }): Promise<ApiJsonResult<void>> { return apiJson(url(request, `/${request.action}`), { method: 'POST', headers: authHeaders(request.token) }); }
export function deleteBackup(request: BackupRequest): Promise<ApiJsonResult<void>> { return apiJson(url(request), { method: 'DELETE', headers: authHeaders(request.token) }); }
export function backupStreamUrl(apiUrl: string): string { return `${apiUrl}/api/backup/stream`; }

export function createBackup(request: { apiUrl: string; token: string; payload: Record<string, unknown>; signal?: AbortSignal }): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/backup`, {
    method: 'POST', headers: jsonAuthHeaders(request.token),
    body: JSON.stringify(request.payload), signal: request.signal,
  });
}

export function updateBackup<T>(request: BackupRequest & { payload: UpdateBackupPayload }): Promise<ApiJsonResult<T>> {
  return apiJson<T>(url(request), { method: 'PUT', headers: jsonAuthHeaders(request.token), body: JSON.stringify(request.payload) });
}

export function listBackupSnapshots<T>(request: BackupRequest): Promise<ApiJsonResult<T[]>> {
  return apiJson<T[]>(url(request, '/snapshots'), { headers: authHeaders(request.token) });
}

export function listSnapshotItems<T>(request: BackupRequest & { snapshotId: string; path?: string }): Promise<ApiJsonResult<T[]>> {
  const query = request.path ? `?path=${encodeURIComponent(request.path)}` : '';
  return apiJson<T[]>(url(request, `/snapshots/${encodeURIComponent(request.snapshotId)}/items${query}`), { headers: authHeaders(request.token) });
}

export function listBackupVerifies<T>(request: BackupRequest): Promise<ApiJsonResult<T[]>> {
  return apiJson<T[]>(url(request, '/verify'), { headers: authHeaders(request.token) });
}

export function startBackupVerify(request: BackupRequest & { payload: BackupVerifyPayload }): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, '/verify'), { method: 'POST', headers: jsonAuthHeaders(request.token), body: JSON.stringify(request.payload) });
}

export function cancelBackupVerify(request: BackupRequest & { verifyId: string }): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, `/verify/${encodeURIComponent(request.verifyId)}/cancel`), { method: 'POST', headers: authHeaders(request.token) });
}

export function createRestorePreview(request: BackupRequest & { snapshotId: string; payload: CreateRestorePreviewPayload }): Promise<ApiJsonResult<{ id: string }>> {
  return apiJson<{ id: string }>(url(request, `/snapshots/${encodeURIComponent(request.snapshotId)}/restore/previews`), {
    method: 'POST', headers: jsonAuthHeaders(request.token), body: JSON.stringify(request.payload),
  });
}

export function listRestoreRuns<T>(request: RestoreAuthRequest): Promise<ApiJsonResult<T[]>> {
  return apiJson<T[]>(restoreUrl(request), { headers: authHeaders(request.token) });
}

export function getRestorePreview<T>(request: RestoreAuthRequest & { previewId: string }): Promise<ApiJsonResult<T>> {
  return apiJson<T>(restoreUrl(request, `/previews/${encodeURIComponent(request.previewId)}`), { headers: authHeaders(request.token) });
}

export function consumeRestorePreview(request: RestoreAuthRequest & { previewId: string }): Promise<ApiJsonResult<{ restore_run_id: string }>> {
  return apiJson<{ restore_run_id: string }>(restoreUrl(request, `/previews/${encodeURIComponent(request.previewId)}/consume`), {
    method: 'POST', headers: authHeaders(request.token),
  });
}

export function cancelRestorePreview(request: RestoreAuthRequest & { previewId: string }): Promise<ApiJsonResult<void>> {
  return apiJson(restoreUrl(request, `/previews/${encodeURIComponent(request.previewId)}/cancel`), { method: 'POST', headers: authHeaders(request.token) });
}

export function getRestoreRun<T>(request: RestoreAuthRequest & { runId: string }): Promise<ApiJsonResult<T>> {
  return apiJson<T>(restoreUrl(request, `/runs/${encodeURIComponent(request.runId)}`), { headers: authHeaders(request.token) });
}

export function listRestoreRunItems<T>(request: RestoreAuthRequest & { runId: string }): Promise<ApiJsonResult<T[]>> {
  return apiJson<T[]>(restoreUrl(request, `/runs/${encodeURIComponent(request.runId)}/items`), { headers: authHeaders(request.token) });
}

export function cancelRestoreRun(request: RestoreAuthRequest & { runId: string }): Promise<ApiJsonResult<void>> {
  return apiJson(restoreUrl(request, `/runs/${encodeURIComponent(request.runId)}/cancel`), { method: 'POST', headers: authHeaders(request.token) });
}

export function downloadRestoreReport(request: RestoreAuthRequest & { runId: string }): Promise<Response> {
  return apiFetch(restoreUrl(request, `/runs/${encodeURIComponent(request.runId)}/report`), { headers: authHeaders(request.token) });
}
