const AUTH_FAILURE_PATTERN = /authentication failed|oauth token refresh failed/i;

export const isAuthFailureError = (message?: string | null): boolean =>
  AUTH_FAILURE_PATTERN.test(message ?? '');
