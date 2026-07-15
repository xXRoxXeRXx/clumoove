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

export interface ApiResult<T = Record<string, unknown>> {
  ok: boolean;
  data?: T;
  errorCode?: string;
  status?: number;
}

export interface ListUsersResult {
  users: AdminUser[];
  total: number;
}

export interface ListMigrationsResult {
  migrations: AdminMigration[];
  total: number;
}

export interface AuditLogResult {
  entries: AuditEntry[];
  total: number;
}

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

async function call<T = Record<string, unknown>>(
  apiUrl: string,
  token: string,
  method: string,
  path: string,
  body?: unknown,
): Promise<ApiResult<T>> {
  const res = await fetch(`${apiUrl}${path}`, {
    method,
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
    },
    credentials: 'include',
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({})) as { error_code?: unknown };
  if (!res.ok) {
    const errorCode = typeof data.error_code === 'string' ? data.error_code : 'UNKNOWN';
    return { ok: false, errorCode, status: res.status };
  }
  return { ok: true, data: data as T };
}

export const adminApi = {
  listUsers: (apiUrl: string, token: string, params: Record<string, string | number | undefined>) =>
    call<ListUsersResult>(apiUrl, token, 'GET', `/api/admin/users${buildQuery(params)}`),
  createUser: (apiUrl: string, token: string, body: { email: string; display_name: string; password: string; role?: string; must_change_password?: boolean }) =>
    call<AdminUser>(apiUrl, token, 'POST', '/api/admin/users', body),
  suspendUser: (apiUrl: string, token: string, id: string) =>
    call(apiUrl, token, 'POST', `/api/admin/users/${id}/suspend`),
  reactivateUser: (apiUrl: string, token: string, id: string) =>
    call(apiUrl, token, 'POST', `/api/admin/users/${id}/reactivate`),
  deleteUser: (apiUrl: string, token: string, id: string) =>
    call(apiUrl, token, 'DELETE', `/api/admin/users/${id}`),
  updateRole: (apiUrl: string, token: string, id: string, role: string) =>
    call(apiUrl, token, 'PUT', `/api/admin/users/${id}/role`, { role }),
  stats: (apiUrl: string, token: string) => call<AdminStats>(apiUrl, token, 'GET', '/api/admin/stats'),
  listMigrations: (apiUrl: string, token: string, params: Record<string, string | number | undefined>) =>
    call<ListMigrationsResult>(apiUrl, token, 'GET', `/api/admin/migrations${buildQuery(params)}`),
  auditLog: (apiUrl: string, token: string, params: Record<string, string | number | undefined>) =>
    call<AuditLogResult>(apiUrl, token, 'GET', `/api/audit/log${buildQuery(params)}`),
};
