import { apiJson, type ApiJsonResult } from '../utils/apiClient';

export type AnonymousAuthRequest = { apiUrl: string };
const json = { 'Content-Type': 'application/json' };

export function getPasswordResetAvailability(request: AnonymousAuthRequest): Promise<ApiJsonResult<{ available: boolean }>> { return apiJson(`${request.apiUrl}/api/auth/password-reset-available`); }
export function requestPasswordReset(request: AnonymousAuthRequest & { email: string }): Promise<ApiJsonResult<void>> { return apiJson(`${request.apiUrl}/api/auth/forgot-password`, { method: 'POST', headers: json, body: JSON.stringify({ email: request.email }) }); }
export function submitTotp(request: AnonymousAuthRequest & { tempSession: string; code: string }): Promise<ApiJsonResult<{ token?: string }>> { return apiJson(`${request.apiUrl}/api/auth/totp`, { method: 'POST', credentials: 'include', headers: json, body: JSON.stringify({ temp_session: request.tempSession, code: request.code }) }); }
