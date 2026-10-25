import { apiFetch, apiJson, type ApiJsonResult } from '../utils/apiClient';

export interface ConnectionProfilePublic {
  id: string;
  name: string;
  provider: string;
  url?: string;
  username?: string;
  has_password: boolean;
  token_expires_at?: string | null;
  oauth_user?: string;
  created_at: string;
  updated_at: string;
}

type ProfilesResponse = {
  profiles?: ConnectionProfilePublic[];
};

export async function listConnectionProfiles(
  apiUrl: string,
  token: string,
  signal?: AbortSignal,
): Promise<ApiJsonResult<ProfilesResponse>> {
  return apiJson<ProfilesResponse>(`${apiUrl}/api/profiles`, {
    headers: { Authorization: `Bearer ${token}` },
    signal,
  });
}

type ProfileRequest = { apiUrl: string; token: string; profileId: string; signal?: AbortSignal };
const profileUrl = ({ apiUrl, profileId }: ProfileRequest, suffix = '') => `${apiUrl}/api/profiles/${encodeURIComponent(profileId)}${suffix}`;
const auth = (token: string) => ({ Authorization: `Bearer ${token}` });

export function deleteConnectionProfile(request: ProfileRequest): Promise<Response> {
  return apiFetch(profileUrl(request), { method: 'DELETE', headers: auth(request.token), signal: request.signal });
}

export function testConnectionProfile(request: ProfileRequest): Promise<Response> {
  return apiFetch(profileUrl(request, '/test'), { method: 'POST', headers: auth(request.token), signal: request.signal });
}

export function updateConnectionProfile(request: ProfileRequest & { payload: Record<string, unknown>; includeUrlAndUsername?: boolean }): Promise<Response> {
  const query = request.includeUrlAndUsername ? '?url=1&username=1' : '';
  return apiFetch(`${profileUrl(request)}${query}`, {
    method: 'PUT', headers: { ...auth(request.token), 'Content-Type': 'application/json' },
    signal: request.signal, body: JSON.stringify(request.payload),
  });
}

export function createConnectionProfile(request: { apiUrl: string; token: string; payload: Record<string, unknown>; signal?: AbortSignal }): Promise<Response> {
  return apiFetch(`${request.apiUrl}/api/profiles`, {
    method: 'POST', headers: { ...auth(request.token), 'Content-Type': 'application/json' },
    signal: request.signal, body: JSON.stringify(request.payload),
  });
}
