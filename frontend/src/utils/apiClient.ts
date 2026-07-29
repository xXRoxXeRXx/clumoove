// Scoped API client with single-flight 401 refresh.
// Replaces the previous window.fetch monkey-patch so only our API origin is
// intercepted, auth endpoints never re-enter refresh, and a failed replay
// logs the user out exactly once.

export type ApiClientConfig = {
  apiUrl: string;
  getAccessToken: () => string;
  setAccessToken: (token: string) => void;
  onAuthFailure: () => void;
};

const AUTH_PATH_MARKERS = [
  '/api/auth/login',
  '/api/auth/register',
  '/api/auth/refresh',
  '/api/auth/totp',
  '/api/auth/setup-admin',
  '/api/auth/forgot-password',
  '/api/auth/reset-password',
  '/api/auth/confirm-email-change',
  '/api/auth/password-reset-available',
  '/api/auth/email-change-available',
];

let clientConfig: ApiClientConfig | null = null;
let refreshPromise: Promise<string> | null = null;

export function configureApiClient(cfg: ApiClientConfig): void {
  clientConfig = cfg;
}

export function getConfiguredApiUrl(): string {
  return clientConfig?.apiUrl ?? '';
}

function resolveUrl(input: string): string {
  if (!clientConfig) return input;
  if (input.startsWith('http://') || input.startsWith('https://')) return input;
  const base = clientConfig.apiUrl.replace(/\/$/, '');
  const path = input.startsWith('/') ? input : `/${input}`;
  return `${base}${path}`;
}

function isOurApiUrl(url: string): boolean {
  if (!clientConfig) return false;
  try {
    const base = new URL(clientConfig.apiUrl, window.location.origin);
    const u = new URL(url, window.location.origin);
    return u.origin === base.origin;
  } catch {
    return false;
  }
}

function isAuthEndpoint(url: string): boolean {
  try {
    const path = new URL(url, window.location.origin).pathname;
    return AUTH_PATH_MARKERS.some((m) => path === m || path.endsWith(m));
  } catch {
    return AUTH_PATH_MARKERS.some((m) => url.includes(m));
  }
}

async function refreshAccessToken(): Promise<string> {
  if (!clientConfig) throw new Error('api client not configured');
  if (!refreshPromise) {
    const cfg = clientConfig;
    refreshPromise = (async () => {
      const res = await fetch(`${cfg.apiUrl}/api/auth/refresh`, {
        method: 'POST',
        credentials: 'include',
      });
      if (!res.ok) {
        throw new Error('Silent refresh failed');
      }
      const data = (await res.json()) as { access_token?: string };
      if (!data.access_token) {
        throw new Error('Silent refresh failed');
      }
      cfg.setAccessToken(data.access_token);
      return data.access_token;
    })().finally(() => {
      refreshPromise = null;
    });
  }
  return refreshPromise;
}

function buildHeaders(init: RequestInit | undefined, token: string | undefined): Headers {
  const headers = new Headers(init?.headers);
  if (token && !headers.has('Authorization')) {
    headers.set('Authorization', `Bearer ${token}`);
  }
  return headers;
}

/**
 * Fetch against the configured API. On 401 (non-auth routes), performs a
 * single-flight cookie refresh and retries the request once with the new token.
 */
export async function apiFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const url = resolveUrl(input);
  const cfg = clientConfig;

  if (!cfg || !isOurApiUrl(url)) {
    return fetch(url, init);
  }

  const token = cfg.getAccessToken();
  const headers = buildHeaders(init, token || undefined);
  const baseInit: RequestInit = {
    ...init,
    headers,
    credentials: init.credentials ?? 'include',
  };

  const response = await fetch(url, baseInit);

  if (response.status !== 401 || isAuthEndpoint(url)) {
    return response;
  }

  try {
    const newToken = await refreshAccessToken();
    const retryHeaders = buildHeaders(init, newToken);
    // Force the refreshed bearer even if the caller supplied a stale one.
    retryHeaders.set('Authorization', `Bearer ${newToken}`);
    const retry = await fetch(url, {
      ...init,
      headers: retryHeaders,
      credentials: init.credentials ?? 'include',
    });
    if (retry.status === 401 && cfg.getAccessToken() === newToken) {
      cfg.onAuthFailure();
    }
    return retry;
  } catch {
    // A different request may have refreshed the session while this request
    // was in flight. Only clear the session if this request still owns its token.
    if (cfg.getAccessToken() === token) {
      cfg.onAuthFailure();
    }
    return response;
  }
}

export type ApiJsonResult<T> = {
  ok: boolean;
  status: number;
  data: T;
  errorCode?: string;
};

export async function apiJson<T = Record<string, unknown>>(
  input: string,
  init?: RequestInit,
): Promise<ApiJsonResult<T>> {
  const res = await apiFetch(input, init);
  const data = (await res.json().catch(() => ({}))) as T & { error_code?: unknown };
  if (!res.ok) {
    const errorCode = typeof data.error_code === 'string' ? data.error_code : 'UNKNOWN';
    return { ok: false, status: res.status, data, errorCode };
  }
  return { ok: true, status: res.status, data };
}
