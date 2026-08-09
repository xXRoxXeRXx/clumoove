import { afterEach, describe, expect, it, vi } from 'vitest';
import { configuredApiOrigin, parseApiOrigin } from './runtimeConfig';

afterEach(function () {
  delete window.__CLUMOOVE_RUNTIME_CONFIG__;
  vi.unstubAllEnvs();
});

describe('runtime API configuration', function () {
  it('resolves the exact configured cross-origin API origin', function () {
    window.__CLUMOOVE_RUNTIME_CONFIG__ = { apiUrl: 'https://api.example.test:8443' };

    expect(configuredApiOrigin()).toBe('https://api.example.test:8443');
  });

  it('accepts a same-origin API configuration', function () {
    window.__CLUMOOVE_RUNTIME_CONFIG__ = { apiUrl: window.location.origin };

    expect(configuredApiOrigin()).toBe(window.location.origin);
  });

  it('falls back to VITE_API_URL when runtime configuration is empty', function () {
    vi.stubEnv('VITE_API_URL', 'http://localhost:8001');
    window.__CLUMOOVE_RUNTIME_CONFIG__ = {};

    expect(configuredApiOrigin()).toBe('http://localhost:8001');
  });

  it.each([
    'https://api.example.test/api',
    'https://user:password@api.example.test',
    'https://api.example.test?next=/api',
    'javascript:alert(1)',
  ])('rejects a non-origin configuration: %s', function (value) {
    expect(parseApiOrigin(value)).toBeUndefined();
  });
});
