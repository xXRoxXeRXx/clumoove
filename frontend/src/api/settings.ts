import { apiJson, type ApiJsonResult } from '../utils/apiClient';

type AuthenticatedRequest = { apiUrl: string; token: string };
const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const jsonAuth = (token: string) => ({ ...auth(token), 'Content-Type': 'application/json' });

export function getNotificationSettings<T>(request: AuthenticatedRequest): Promise<ApiJsonResult<T>> { return apiJson<T>(`${request.apiUrl}/api/settings/notifications`, { headers: auth(request.token) }); }
export function updateNotificationSetting(request: AuthenticatedRequest & { type: string; enabled: boolean; config?: Record<string, unknown> }): Promise<ApiJsonResult<void>> { return apiJson(`${request.apiUrl}/api/settings/notifications`, { method: 'PUT', headers: jsonAuth(request.token), body: JSON.stringify({ type: request.type, enabled: request.enabled, config: request.config }) }); }
export function testNotificationSetting(request: AuthenticatedRequest & { type: string; enabled: boolean; config?: Record<string, unknown> }): Promise<ApiJsonResult<void>> { return apiJson(`${request.apiUrl}/api/settings/notifications/test`, { method: 'POST', headers: jsonAuth(request.token), body: JSON.stringify({ type: request.type, enabled: request.enabled, config: request.config }) }); }
