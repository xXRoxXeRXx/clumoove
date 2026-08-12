declare global {
  interface Window {
    __CLUMOOVE_RUNTIME_CONFIG__?: {
      apiUrl?: unknown;
    };
  }
}

/** Returns a normalized, origin-only API URL or undefined for an invalid value. */
export function parseApiOrigin(value: unknown): string | undefined {
  if (typeof value !== 'string' || value.length === 0) return undefined;

  try {
    const url = new URL(value);
    if (
      (url.protocol !== 'https:' && url.protocol !== 'http:') ||
      (import.meta.env.PROD && url.protocol === 'http:') ||
      url.username ||
      url.password ||
      url.pathname !== '/' ||
      url.search ||
      url.hash
    ) {
      return undefined;
    }
    return url.origin;
  } catch {
    return undefined;
  }
}

export function configuredApiOrigin(): string | undefined {
  return parseApiOrigin(window.__CLUMOOVE_RUNTIME_CONFIG__?.apiUrl)
    ?? parseApiOrigin(import.meta.env.VITE_API_URL);
}
