export type AuthenticatedRequest = { apiUrl: string; token: string };

export const authHeaders = (token: string): Record<string, string> => ({
  Authorization: `Bearer ${token}`,
});

export const jsonAuthHeaders = (token: string): Record<string, string> => ({
  ...authHeaders(token),
  'Content-Type': 'application/json',
});
