import { apiJson, type ApiJsonResult } from '../utils/apiClient';

export type AnonymousAuthRequest = { apiUrl: string };

export type PasswordResetAvailability = { available: boolean };
export type TotpSubmission = { token?: string };
const json = { 'Content-Type': 'application/json' };

export function getPasswordResetAvailability(request: AnonymousAuthRequest): Promise<ApiJsonResult<PasswordResetAvailability>> {
  return apiJson(`${request.apiUrl}/api/auth/password-reset-available`);
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
