export type DiagnosticContext = {
  status?: number;
  errorCode?: string;
  route?: string;
  requestId?: string;
  response?: Response;
  [key: string]: unknown;
};

const sensitiveKeyPattern = /password|passphrase|secret|token|authorization|api[_-]?key|access[_-]?key|credential|cookie|session|oauth|state|signature|host[_-]?key/i;
const sensitiveAssignmentPattern = /((?:password|passphrase|secret|token|authorization|api[_-]?key|access[_-]?key|credential|cookie|session|oauth|state|code|signature|host[_-]?key)\s*[=:]\s*)(?:Bearer\s+)?(?:"[^"]*"|'[^']*'|[^\s,;]+)/gi;

export function redactForLogging(value: string): string {
  return value
    .replace(sensitiveAssignmentPattern, '$1[REDACTED]')
    .replace(/\bBearer\s+[^\s,;]+/gi, 'Bearer [REDACTED]')
    .replace(/https?:\/\/[^\s"'<>]+/gi, (url) => {
      try {
        const parsed = new URL(url);
        return parsed.search ? `${parsed.origin}${parsed.pathname}?[REDACTED]${parsed.hash}` : url;
      } catch {
        return url.replace(/\?.*/, '?[REDACTED]');
      }
    });
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}

function redactValue(value: unknown, seen = new WeakSet<object>()): unknown {
  if (typeof value === 'string') return redactForLogging(value);
  if (value instanceof Error) {
    return {
      name: value.name,
      message: redactForLogging(value.message),
      stack: value.stack ? redactForLogging(value.stack) : undefined,
    };
  }
  if (!isRecord(value)) return value;
  if (seen.has(value)) return '[CIRCULAR]';
  seen.add(value);

  if (Array.isArray(value)) return value.map((item) => redactValue(item, seen));

  return Object.fromEntries(
    Object.entries(value).map(([key, item]) => [key, sensitiveKeyPattern.test(key) ? '[REDACTED]' : redactValue(item, seen)]),
  );
}

function responseDetails(response: Response): DiagnosticContext {
  let route: string | undefined;
  try {
    route = new URL(response.url).pathname;
  } catch {
    route = response.url ? redactForLogging(response.url) : undefined;
  }

  return {
    status: response.status,
    route,
    requestId: response.headers.get('x-request-id') ?? response.headers.get('request-id') ?? undefined,
  };
}

function inferredApiDetails(error: unknown): DiagnosticContext {
  if (error instanceof Response) return responseDetails(error);
  if (!isRecord(error)) return {};

  const details: DiagnosticContext = {};
  if (typeof error.status === 'number') details.status = error.status;
  if (typeof error.errorCode === 'string') details.errorCode = error.errorCode;
  if (typeof error.error_code === 'string') details.errorCode = error.error_code;
  if (typeof error.route === 'string') details.route = error.route;
  if (typeof error.requestId === 'string') details.requestId = error.requestId;
  if (typeof error.request_id === 'string') details.requestId = error.request_id;
  return details;
}

function emit(level: 'error' | 'warn', message: string, error?: unknown, context: DiagnosticContext = {}): void {
  if (!import.meta.env.DEV) return;

  const { response, ...providedContext } = context;
  const details = {
    ...inferredApiDetails(error),
    ...(response ? responseDetails(response) : {}),
    ...providedContext,
  };
  const sanitizedDetails = redactValue(details) as Record<string, unknown>;
  const output = Object.keys(sanitizedDetails).length > 0 ? sanitizedDetails : undefined;

  if (level === 'error') {
    console.error(`[client] ${redactForLogging(message)}`, redactValue(error), output);
    return;
  }
  console.warn(`[client] ${redactForLogging(message)}`, output);
}

export const logger = {
  error(message: string, error?: unknown, context?: DiagnosticContext): void {
    emit('error', message, error, context);
  },
  warn(message: string, context?: DiagnosticContext): void {
    emit('warn', message, undefined, context);
  },
};
