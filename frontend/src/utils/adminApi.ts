import type { User } from '../types';

export interface AdminUser extends User {
  active?: boolean;
  must_change_password?: boolean;
  totp_enabled?: boolean;
  created_at?: string;
}

export interface AdminStats {
  total_users: number;
  active_users: number;
  migrations_by_status: Record<string, number>;
  tasks_by_status: Record<string, number>;
}

export interface AdminMigration {
  id: string;
  user_id: string | null;
  status: string;
  source_provider: string;
  source_url: string | null;
  target_provider: string;
  target_url: string | null;
  total_files: number;
  processed_files: number;
  total_bytes: number;
  processed_bytes: number;
  created_at: string;
  owner_email: string;
}

export interface AuditEntry {
  id: number;
  user_id: string;
  action: string;
  target: string;
  ip: string;
  details: unknown;
  created_at: string;
}

export interface Paged<T> {
  items: T[];
  total: number;
  page: number;
  limit: number;
}

export type ApiResult = {
  ok: boolean;
  data?: any;
  errorCode?: string;
  status?: number;
};

function buildQuery(params: Record<string, string | number | undefined | null>): string {
  const sp = new URLSearchParams();
  Object.entries(params).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') {
      sp.set(k, String(v));
    }
  });
  const s = sp.toString();
  return s ? `?${s}` : '';
}

async function call(
  apiUrl: string,
  token: string,
  method: string,
  path: string,
  body?: unknown,
): Promise<ApiResult> {
  const res = await fetch(`${apiUrl}${path}`, {
    method,
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
    },
    credentials: 'include',
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    return { ok: false, errorCode: (data && data.error_code) || 'UNKNOWN', status: res.status };
  }
  return { ok: true, data };
}

export const adminApi = {
  listUsers: (apiUrl: string, token: string, params: Record<string, string | number | undefined>) =>
    call(apiUrl, token, 'GET', `/api/admin/users${buildQuery(params)}`),
  createUser: (apiUrl: string, token: string, body: { email: string; display_name: string; password: string; role?: string; must_change_password?: boolean }) =>
    call(apiUrl, token, 'POST', '/api/admin/users', body),
  suspendUser: (apiUrl: string, token: string, id: string) =>
    call(apiUrl, token, 'POST', `/api/admin/users/${id}/suspend`),
  reactivateUser: (apiUrl: string, token: string, id: string) =>
    call(apiUrl, token, 'POST', `/api/admin/users/${id}/reactivate`),
  deleteUser: (apiUrl: string, token: string, id: string) =>
    call(apiUrl, token, 'DELETE', `/api/admin/users/${id}`),
  updateRole: (apiUrl: string, token: string, id: string, role: string) =>
    call(apiUrl, token, 'PUT', `/api/admin/users/${id}/role`, { role }),
  stats: (apiUrl: string, token: string) => call(apiUrl, token, 'GET', '/api/admin/stats'),
  listMigrations: (apiUrl: string, token: string, params: Record<string, string | number | undefined>) =>
    call(apiUrl, token, 'GET', `/api/admin/migrations${buildQuery(params)}`),
  auditLog: (apiUrl: string, token: string, params: Record<string, string | number | undefined>) =>
    call(apiUrl, token, 'GET', `/api/audit/log${buildQuery(params)}`),
};
