import { apiFetch, apiJson, type ApiJsonResult } from '../utils/apiClient';
import { authHeaders, jsonAuthHeaders, type AuthenticatedRequest } from './headers';

export type AnonymousAuthRequest = { apiUrl: string };
export type AuthenticatedAuthRequest = AuthenticatedRequest;

export type PasswordResetAvailability = { available: boolean };
export type EmailChangeAvailability = { available: boolean };
export type TotpStatus = { totp_enabled: boolean };
export type TotpSetupData = { otpauth_uri: string; qr_png: string; secret: string };
export type TotpEnableResponse = { backup_codes?: string[] };
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

export function changePasswordWithSession(request: AnonymousAuthRequest & { session: string; newPassword: string; confirmPassword: string }): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/auth/change-password`, {
    method: 'POST', credentials: 'include', headers: { ...json, Authorization: `Bearer ${request.session}` },
    body: JSON.stringify({ new_password: request.newPassword, confirm_password: request.confirmPassword }),
  });
}

export function setupAdmin(request: AnonymousAuthRequest & { email: string; password: string; displayName: string; language: 'de' | 'en' }): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/auth/setup-admin`, {
    method: 'POST', credentials: 'include', headers: json,
    body: JSON.stringify({ email: request.email, password: request.password, display_name: request.displayName, language: request.language }),
  });
}

export function authenticate(request: AnonymousAuthRequest & { mode: 'login' | 'register'; payload: Record<string, string> }): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/auth/${request.mode}`, {
    method: 'POST', credentials: 'include', headers: json, body: JSON.stringify(request.payload),
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

export function getTwoFactorStatus(request: AuthenticatedAuthRequest): Promise<ApiJsonResult<TotpStatus>> {
  return apiJson(`${request.apiUrl}/api/auth/2fa/status`, { headers: authHeaders(request.token) });
}

export function setupTwoFactor(request: AuthenticatedAuthRequest): Promise<ApiJsonResult<TotpSetupData>> {
  return apiJson(`${request.apiUrl}/api/auth/2fa/setup`, { method: 'GET', headers: authHeaders(request.token) });
}

export function enableTwoFactor(request: AuthenticatedAuthRequest & { code: string }): Promise<ApiJsonResult<TotpEnableResponse>> {
  return apiJson(`${request.apiUrl}/api/auth/2fa/enable`, {
    method: 'POST', headers: jsonAuthHeaders(request.token), body: JSON.stringify({ code: request.code }),
  });
}

export function disableTwoFactor(request: AuthenticatedAuthRequest & { code: string }): Promise<ApiJsonResult<void>> {
  return apiJson(`${request.apiUrl}/api/auth/2fa/disable`, {
    method: 'POST', headers: jsonAuthHeaders(request.token), body: JSON.stringify({ code: request.code }),
  });
}

export function getEmailChangeAvailability(request: AnonymousAuthRequest): Promise<ApiJsonResult<EmailChangeAvailability>> {
  return apiJson(`${request.apiUrl}/api/auth/email-change-available`);
}

export function updateCurrentUserProfile(request: AuthenticatedAuthRequest & { displayName: string }): Promise<ApiJsonResult<{ display_name: string }>> {
  return apiJson(`${request.apiUrl}/api/auth/me`, {
    method: 'PUT', headers: jsonAuthHeaders(request.token), body: JSON.stringify({ display_name: request.displayName }),
  });
}

export function uploadAvatar(request: AuthenticatedAuthRequest & { avatar: string }): Promise<ApiJsonResult<{ avatar: string }>> {
  return apiJson(`${request.apiUrl}/api/user/avatar`, {
    method: 'POST', headers: jsonAuthHeaders(request.token), body: JSON.stringify({ avatar: request.avatar }),
  });
}

export function deleteAvatar(request: AuthenticatedAuthRequest): Promise<ApiJsonResult<void>> {
  return apiJson(`${request.apiUrl}/api/user/avatar`, { method: 'DELETE', headers: authHeaders(request.token) });
}

export function changePassword(request: AuthenticatedAuthRequest & { currentPassword: string; newPassword: string; confirmPassword: string }): Promise<ApiJsonResult<void>> {
  return apiJson(`${request.apiUrl}/api/auth/change-password`, {
    method: 'POST',
    headers: jsonAuthHeaders(request.token),
    body: JSON.stringify({
      current_password: request.currentPassword,
      new_password: request.newPassword,
      confirm_password: request.confirmPassword,
    }),
  });
}

export function requestEmailChange(request: AuthenticatedAuthRequest & { newEmail: string }): Promise<ApiJsonResult<void>> {
  return apiJson(`${request.apiUrl}/api/auth/change-email`, {
    method: 'POST', headers: jsonAuthHeaders(request.token), body: JSON.stringify({ new_email: request.newEmail }),
  });
}

export function confirmEmailChange(request: AnonymousAuthRequest & { token: string }): Promise<ApiJsonResult<void>> {
  return apiJson(`${request.apiUrl}/api/auth/confirm-email-change`, {
    method: 'POST', headers: json, body: JSON.stringify({ token: request.token }),
  });
}
