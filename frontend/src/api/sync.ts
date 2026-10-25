import { apiFetch, apiJson, type ApiJsonResult } from '../utils/apiClient';

export type SyncRequest = { apiUrl: string; token: string; syncId: string };
const url = ({ apiUrl, syncId }: SyncRequest, suffix = '') => `${apiUrl}/api/sync/${encodeURIComponent(syncId)}${suffix}`;
const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const jsonAuth = (token: string) => ({ ...auth(token), 'Content-Type': 'application/json' });

export function getSync<T>(request: SyncRequest & { signal?: AbortSignal }): Promise<ApiJsonResult<T>> {
  return apiJson<T>(url(request), { headers: auth(request.token), signal: request.signal });
}

export function startSync(request: SyncRequest): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, '/start'), { method: 'POST', headers: auth(request.token) });
}

export function pauseSync(request: SyncRequest): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, '/pause'), { method: 'POST', headers: auth(request.token) });
}

export function resumeSync(request: SyncRequest): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, '/resume'), { method: 'POST', headers: auth(request.token) });
}

export function deleteSync(request: SyncRequest): Promise<ApiJsonResult<void>> {
  return apiJson(url(request), { method: 'DELETE', headers: auth(request.token) });
}

export function updateSyncBandwidth(request: SyncRequest & { limitMbps: number }): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, '/bandwidth'), {
    method: 'PUT', headers: jsonAuth(request.token), body: JSON.stringify({ limit_mbps: request.limitMbps }),
  });
}

export function updateSyncBandwidthResponse(request: SyncRequest & { limitMbps: number }): Promise<Response> {
  return apiFetch(url(request, '/bandwidth'), {
    method: 'PUT', headers: jsonAuth(request.token), body: JSON.stringify({ limit_mbps: request.limitMbps }),
  });
}

export function createSync(request: { apiUrl: string; token: string; payload: Record<string, unknown>; signal?: AbortSignal }): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/sync`, {
    method: 'POST', headers: jsonAuth(request.token), body: JSON.stringify(request.payload), signal: request.signal,
  });
}

export function startSyncResponse(request: SyncRequest & { signal?: AbortSignal }): Promise<Response> {
  return apiFetch(url(request, '/start'), { method: 'POST', headers: auth(request.token), signal: request.signal });
}

export function updateSyncThreads(request: SyncRequest & { threads: number }): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, '/threads'), {
    method: 'PUT', headers: jsonAuth(request.token), body: JSON.stringify({ threads: request.threads }),
  });
}

export function reauthenticateSync(request: SyncRequest & { role: 'source' | 'target'; accessToken: string; refreshToken?: string; expiresIn?: number }): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, '/reauth'), {
    method: 'POST', headers: jsonAuth(request.token),
    body: JSON.stringify({ role: request.role, access_token: request.accessToken, refresh_token: request.refreshToken, expires_in: request.expiresIn }),
  });
}

export function downloadSyncReport(request: SyncRequest): Promise<Response> {
  return apiFetch(url(request, '/report'), { headers: auth(request.token) });
}

export function syncStreamUrl(apiUrl: string): string {
  return `${apiUrl}/api/sync/stream`;
}

export function syncErrorsUrl(request: SyncRequest): string {
  return url(request, '/errors');
}

export function browseSyncDirectory(request: SyncRequest & { role: 'source' | 'target'; path: string }): Promise<Response> {
  const params = new URLSearchParams({ role: request.role, path: request.path });
  return apiFetch(`${url(request, '/browse')}?${params}`, { headers: auth(request.token) });
}

export function createSyncDirectory(request: SyncRequest & { role: 'target'; path: string; name: string }): Promise<Response> {
  return apiFetch(url(request, '/mkdir'), { method: 'POST', headers: jsonAuth(request.token), body: JSON.stringify({ role: request.role, path: request.path, name: request.name }) });
}

export function updateSyncScope(request: SyncRequest & { scope: Record<string, unknown> }): Promise<Response> {
  return apiFetch(url(request, '/scope'), { method: 'PUT', headers: jsonAuth(request.token), body: JSON.stringify(request.scope) });
}

export function updateSyncSchedule(request: SyncRequest & { intervalMinutes: number }): Promise<Response> {
  return apiFetch(url(request, '/schedule'), { method: 'PUT', headers: jsonAuth(request.token), body: JSON.stringify({ interval_minutes: request.intervalMinutes }) });
}
