import { apiFetch, apiJson, type ApiJsonResult } from '../utils/apiClient';

export type BackupRequest = { apiUrl: string; token: string; backupId: string };
export type RestoreAuthRequest = { apiUrl: string; token: string };
export type BackupAction = 'run' | 'pause' | 'resume';
const url = ({ apiUrl, backupId }: BackupRequest, suffix = '') => `${apiUrl}/api/backup/${encodeURIComponent(backupId)}${suffix}`;
const restoreUrl = ({ apiUrl }: RestoreAuthRequest, suffix = '') => `${apiUrl}/api/restore${suffix}`;
const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const jsonAuth = (token: string) => ({ ...auth(token), 'Content-Type': 'application/json' });

export function getBackup<T>(request: BackupRequest & { signal?: AbortSignal }): Promise<ApiJsonResult<T>> { return apiJson<T>(url(request), { headers: auth(request.token), signal: request.signal }); }
export function listBackupRuns<T>(request: BackupRequest): Promise<ApiJsonResult<T[]>> { return apiJson<T[]>(url(request, '/runs'), { headers: auth(request.token) }); }
export function controlBackup(request: BackupRequest & { action: BackupAction }): Promise<ApiJsonResult<void>> { return apiJson(url(request, `/${request.action}`), { method: 'POST', headers: auth(request.token) }); }
export function deleteBackup(request: BackupRequest): Promise<ApiJsonResult<void>> { return apiJson(url(request), { method: 'DELETE', headers: auth(request.token) }); }
export function backupStreamUrl(apiUrl: string): string { return `${apiUrl}/api/backup/stream`; }

export function createBackup(request: { apiUrl: string; token: string; payload: Record<string, unknown>; signal?: AbortSignal }): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/backup`, {
    method: 'POST', headers: { Authorization: `Bearer ${request.token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(request.payload), signal: request.signal,
  });
}

export function updateBackup<T>(request: BackupRequest & { payload: Record<string, unknown> }): Promise<ApiJsonResult<T>> {
  return apiJson<T>(url(request), { method: 'PUT', headers: jsonAuth(request.token), body: JSON.stringify(request.payload) });
}

export function listBackupSnapshots<T>(request: BackupRequest): Promise<ApiJsonResult<T[]>> {
  return apiJson<T[]>(url(request, '/snapshots'), { headers: auth(request.token) });
}

export function listSnapshotItems<T>(request: BackupRequest & { snapshotId: string; path?: string }): Promise<ApiJsonResult<T[]>> {
  const query = request.path ? `?path=${encodeURIComponent(request.path)}` : '';
  return apiJson<T[]>(url(request, `/snapshots/${encodeURIComponent(request.snapshotId)}/items${query}`), { headers: auth(request.token) });
}

export function listBackupVerifies<T>(request: BackupRequest): Promise<ApiJsonResult<T[]>> {
  return apiJson<T[]>(url(request, '/verify'), { headers: auth(request.token) });
}

export function startBackupVerify(request: BackupRequest & { payload: Record<string, unknown> }): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, '/verify'), { method: 'POST', headers: jsonAuth(request.token), body: JSON.stringify(request.payload) });
}

export function cancelBackupVerify(request: BackupRequest & { verifyId: string }): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, `/verify/${encodeURIComponent(request.verifyId)}/cancel`), { method: 'POST', headers: auth(request.token) });
}

export function createRestorePreview(request: BackupRequest & { snapshotId: string; payload: Record<string, unknown> }): Promise<ApiJsonResult<{ id: string }>> {
  return apiJson<{ id: string }>(url(request, `/snapshots/${encodeURIComponent(request.snapshotId)}/restore/previews`), {
    method: 'POST', headers: jsonAuth(request.token), body: JSON.stringify(request.payload),
  });
}

export function listRestoreRuns<T>(request: RestoreAuthRequest): Promise<ApiJsonResult<T[]>> {
  return apiJson<T[]>(restoreUrl(request), { headers: auth(request.token) });
}

export function getRestorePreview<T>(request: RestoreAuthRequest & { previewId: string }): Promise<ApiJsonResult<T>> {
  return apiJson<T>(restoreUrl(request, `/previews/${encodeURIComponent(request.previewId)}`), { headers: auth(request.token) });
}

export function consumeRestorePreview(request: RestoreAuthRequest & { previewId: string }): Promise<ApiJsonResult<{ restore_run_id: string }>> {
  return apiJson<{ restore_run_id: string }>(restoreUrl(request, `/previews/${encodeURIComponent(request.previewId)}/consume`), {
    method: 'POST', headers: auth(request.token),
  });
}

export function cancelRestorePreview(request: RestoreAuthRequest & { previewId: string }): Promise<ApiJsonResult<void>> {
  return apiJson(restoreUrl(request, `/previews/${encodeURIComponent(request.previewId)}/cancel`), { method: 'POST', headers: auth(request.token) });
}

export function getRestoreRun<T>(request: RestoreAuthRequest & { runId: string }): Promise<ApiJsonResult<T>> {
  return apiJson<T>(restoreUrl(request, `/runs/${encodeURIComponent(request.runId)}`), { headers: auth(request.token) });
}

export function listRestoreRunItems<T>(request: RestoreAuthRequest & { runId: string }): Promise<ApiJsonResult<T[]>> {
  return apiJson<T[]>(restoreUrl(request, `/runs/${encodeURIComponent(request.runId)}/items`), { headers: auth(request.token) });
}

export function cancelRestoreRun(request: RestoreAuthRequest & { runId: string }): Promise<ApiJsonResult<void>> {
  return apiJson(restoreUrl(request, `/runs/${encodeURIComponent(request.runId)}/cancel`), { method: 'POST', headers: auth(request.token) });
}

export function downloadRestoreReport(request: RestoreAuthRequest & { runId: string }): Promise<Response> {
  return apiFetch(restoreUrl(request, `/runs/${encodeURIComponent(request.runId)}/report`), { headers: auth(request.token) });
}
