import { apiFetch, apiJson, type ApiJsonResult } from '../utils/apiClient';

export type MigrationRequest = { apiUrl: string; token: string; migrationId: string };
export type MigrationAction = 'pause' | 'resume' | 'cancel';

const url = ({ apiUrl, migrationId }: MigrationRequest, suffix = '') =>
  `${apiUrl}/api/migration/${encodeURIComponent(migrationId)}${suffix}`;
const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const jsonAuth = (token: string) => ({ ...auth(token), 'Content-Type': 'application/json' });

export function getMigration<T>(request: MigrationRequest & { signal?: AbortSignal }): Promise<ApiJsonResult<T>> {
  return apiJson<T>(url(request), { headers: auth(request.token), signal: request.signal });
}

export function controlMigration(request: MigrationRequest & { action: MigrationAction }): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, `/${request.action}`), { method: 'POST', headers: auth(request.token) });
}

export function deleteMigration(request: MigrationRequest): Promise<ApiJsonResult<void>> {
  return apiJson(url(request), { method: 'DELETE', headers: auth(request.token) });
}

export function updateMigrationBandwidth(request: MigrationRequest & { limitMbps: number }): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, '/bandwidth'), { method: 'PUT', headers: jsonAuth(request.token), body: JSON.stringify({ limit_mbps: request.limitMbps }) });
}

export function updateMigrationThreads(request: MigrationRequest & { threads: number }): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, '/threads'), { method: 'PUT', headers: jsonAuth(request.token), body: JSON.stringify({ threads: request.threads }) });
}

export function retryFailedMigration(request: MigrationRequest): Promise<ApiJsonResult<{ success?: boolean; retried?: number }>> {
  return apiJson(url(request, '/retry-failed'), { method: 'POST', headers: auth(request.token) });
}

export function reauthenticateMigration(request: MigrationRequest & { role: 'source' | 'target'; accessToken: string; refreshToken?: string; expiresIn?: number }): Promise<ApiJsonResult<void>> {
  return apiJson(url(request, '/reauth'), { method: 'POST', headers: jsonAuth(request.token), body: JSON.stringify({ role: request.role, access_token: request.accessToken, refresh_token: request.refreshToken, expires_in: request.expiresIn }) });
}

export function downloadMigrationReport(request: MigrationRequest): Promise<Response> {
  return apiFetch(url(request, '/report'), { headers: auth(request.token) });
}

export function migrationStreamUrl(request: MigrationRequest): string {
  return url(request, '/stream');
}

export function migrationErrorsUrl(request: MigrationRequest): string {
  return url(request, '/errors');
}

type FeatureRequest = { apiUrl: string; token: string; payload: Record<string, unknown>; signal?: AbortSignal };

export function browseMigrationSource(request: FeatureRequest): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/migration/browse`, {
    method: 'POST', headers: { ...auth(request.token), 'Content-Type': 'application/json' },
    body: JSON.stringify(request.payload), signal: request.signal,
  });
}

export function browseMigrationTarget(request: FeatureRequest): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/migration/target/browse`, {
    method: 'POST', headers: { ...auth(request.token), 'Content-Type': 'application/json' },
    body: JSON.stringify(request.payload), signal: request.signal,
  });
}

export function createMigrationTargetDirectory(request: FeatureRequest): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/migration/target/mkdir`, {
    method: 'POST', headers: { ...auth(request.token), 'Content-Type': 'application/json' },
    body: JSON.stringify(request.payload), signal: request.signal,
  });
}

export function startMigration(request: FeatureRequest): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/migration/start`, {
    method: 'POST', headers: { ...auth(request.token), 'Content-Type': 'application/json' },
    body: JSON.stringify(request.payload), signal: request.signal,
  });
}
