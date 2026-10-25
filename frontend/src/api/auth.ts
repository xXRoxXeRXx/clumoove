import { apiFetch, apiJson, type ApiJsonResult } from '../utils/apiClient';

export type AnonymousAuthRequest = { apiUrl: string };

export type PasswordResetAvailability = { available: boolean };
export type TotpSubmission = { token?: string };
const json = { 'Content-Type': 'application/json' };

export function getPasswordResetAvailability(request: AnonymousAuthRequest): Promise<ApiJsonResult<PasswordResetAvailability>> {
  return apiJson(`${request.apiUrl}/api/auth/password-reset-available`);
}

export function getAuthSettings(request: AnonymousAuthRequest): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/settings`);
}

export function getPasswordResetAvailabilityResponse(request: AnonymousAuthRequest): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/auth/password-reset-available`);
}

export function requestPasswordResetResponse(request: AnonymousAuthRequest & { email: string }): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/auth/forgot-password`, {
    method: 'POST', headers: json, body: JSON.stringify({ email: request.email }),
  });
}

export function submitTotpResponse(request: AnonymousAuthRequest & { tempSession: string; code: string }): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/auth/totp`, {
    method: 'POST', credentials: 'include', headers: json,
    body: JSON.stringify({ temp_session: request.tempSession, code: request.code }),
  });
}

export function requestPasswordReset(request: AnonymousAuthRequest & { email: string }): Promise<ApiJsonResult<void>> {
  return apiJson(`${request.apiUrl}/api/auth/forgot-password`, {
    method: 'POST', headers: json, body: JSON.stringify({ email: request.email }),
  });
}

export function resetPassword(request: AnonymousAuthRequest & { token: string; newPassword: string }): Promise<ApiJsonResult<void>> {
  return apiJson(`${request.apiUrl}/api/auth/reset-password`, {
    method: 'POST', headers: json, body: JSON.stringify({ token: request.token, new_password: request.newPassword }),
  });
}

export function submitTotp(request: AnonymousAuthRequest & { tempSession: string; code: string }): Promise<ApiJsonResult<TotpSubmission>> {
  return apiJson(`${request.apiUrl}/api/auth/totp`, {
    method: 'POST', credentials: 'include', headers: json,
    body: JSON.stringify({ temp_session: request.tempSession, code: request.code }),
  });
}

export function updateLanguage(language: 'de' | 'en'): Promise<Response> {
  return apiFetch('/api/auth/me/language', { method: 'PUT', headers: json, body: JSON.stringify({ language }) });
}

export function getCurrentUser(apiUrl: string): Promise<Response> {
  return apiFetch(`${apiUrl}/api/auth/me`);
}

export function getSessionTarget(apiUrl: string, path: 'migration' | 'sync' | 'backup' | 'profiles', id: string): Promise<Response> {
  return apiFetch(`${apiUrl}/api/${path}/${encodeURIComponent(id)}`);
}
