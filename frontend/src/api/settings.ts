import { apiJson, type ApiJsonResult } from '../utils/apiClient';

type AuthenticatedRequest = { apiUrl: string; token: string };
export type PublicSettings = { local_storage_enabled?: boolean; oauth_providers?: Record<string, boolean> };
export type InstanceSettings = PublicSettings & { registrations_enabled?: boolean | string };
export type NotificationType = 'email' | 'gotify' | 'ntfy' | 'telegram' | 'discord';
const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const jsonAuth = (token: string) => ({ ...auth(token), 'Content-Type': 'application/json' });

export function getPublicSettings(request: { apiUrl: string }): Promise<ApiJsonResult<PublicSettings>> { return apiJson<PublicSettings>(`${request.apiUrl}/api/settings`); }
export function getInstanceSettings(request: AuthenticatedRequest): Promise<ApiJsonResult<InstanceSettings>> { return apiJson<InstanceSettings>(`${request.apiUrl}/api/settings`, { headers: auth(request.token) }); }
export function updateInstanceSetting(request: AuthenticatedRequest & { key: string; value: string }): Promise<ApiJsonResult<void>> { return apiJson(`${request.apiUrl}/api/settings`, { method: 'PUT', headers: jsonAuth(request.token), body: JSON.stringify({ key: request.key, value: request.value }) }); }
export function getNotificationSettings<T>(request: AuthenticatedRequest): Promise<ApiJsonResult<T>> { return apiJson<T>(`${request.apiUrl}/api/settings/notifications`, { headers: auth(request.token) }); }
export function updateNotificationSetting(request: AuthenticatedRequest & { type: NotificationType; enabled: boolean; config?: Record<string, unknown> }): Promise<ApiJsonResult<void>> { return apiJson(`${request.apiUrl}/api/settings/notifications`, { method: 'PUT', headers: jsonAuth(request.token), body: JSON.stringify({ type: request.type, enabled: request.enabled, config: request.config }) }); }
export function testNotificationSetting(request: AuthenticatedRequest & { type: NotificationType; enabled: boolean; config?: Record<string, unknown> }): Promise<ApiJsonResult<void>> { return apiJson(`${request.apiUrl}/api/settings/notifications/test`, { method: 'POST', headers: jsonAuth(request.token), body: JSON.stringify({ type: request.type, enabled: request.enabled, config: request.config }) }); }
