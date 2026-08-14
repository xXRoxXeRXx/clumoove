import { apiJson, type ApiJsonResult } from '../utils/apiClient';

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
