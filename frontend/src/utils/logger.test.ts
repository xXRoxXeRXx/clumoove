import { afterEach, describe, expect, it, vi } from 'vitest';
import { logger, redactForLogging } from './logger';

describe('logger', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllEnvs();
  });

  it('redacts credentials, bearer tokens, and URL query strings', () => {
    expect(
      redactForLogging('Authorization=Bearer token-value password=hunter2 https://example.test/api?access_token=abc&file=private'),
    ).toBe('Authorization=[REDACTED] password=[REDACTED] https://example.test/api?[REDACTED]');
  });

  it('reports sanitized API diagnostics in development', () => {
    const errorSpy = vi.spyOn(globalThis.console, 'error').mockImplementation(() => {});

    logger.error('Request failed', new Error('https://api.example.test/path?token=secret'), {
      status: 403,
      errorCode: 'FORBIDDEN',
      route: 'https://api.example.test/api/migration?id=private',
      requestId: 'req-123',
      access_token: 'secret',
    });

    expect(errorSpy).toHaveBeenCalledWith(
      '[client] Request failed',
      expect.objectContaining({ message: 'https://api.example.test/path?[REDACTED]' }),
      expect.objectContaining({
        status: 403,
        errorCode: 'FORBIDDEN',
        route: 'https://api.example.test/api/migration?[REDACTED]',
        requestId: 'req-123',
        access_token: '[REDACTED]',
      }),
    );
  });

  it('does not emit diagnostics outside development', () => {
    vi.stubEnv('DEV', false);
    const errorSpy = vi.spyOn(globalThis.console, 'error').mockImplementation(() => {});

    logger.error('Request failed', new Error('token=secret'));

    expect(errorSpy).not.toHaveBeenCalled();
  });
});
