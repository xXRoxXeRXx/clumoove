export type SseHandlers = {
  onEvent: (event: string, data: string) => void;
  onError?: () => void;
};

export type SseConnectOptions = {
  url: string;
  signal: AbortSignal;
  handlers: SseHandlers;
  /**
   * Auth-aware API fetch implementation. It injects the current bearer token
   * for each connection attempt so reconnects never reuse an expired token.
   */
  fetchImpl: (input: string, init?: RequestInit) => Promise<Response>;
  /** Called with the delay (ms) that will be used for the next reconnect. */
  onRetryScheduled?: (delayMs: number) => void;
};

/**
 * Parse one SSE frame (event + data lines joined by \n\n).
 */
export function parseSseFrame(frame: string): { event: string; data: string } {
  let event = 'message';
  let data = '';
  for (const line of frame.split(/\r\n|\r|\n/)) {
    if (line.startsWith('event:')) {
      event = line.slice(6).trim();
    } else if (line.startsWith('data:')) {
      data += (data ? '\n' : '') + line.slice(5).trim();
    }
  }
  return { event, data };
}

/**
 * Read an SSE response body until abort or stream end.
 * Returns whether at least one frame was successfully delivered.
 */
export async function readSseStream(
  body: ReadableStream<Uint8Array>,
  signal: AbortSignal,
  onFrame: (event: string, data: string) => void,
): Promise<boolean> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let received = false;

  try {
    while (!signal.aborted) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      let boundary: RegExpExecArray | null;
      while ((boundary = /\r\n\r\n|\n\n|\r\r/.exec(buffer)) !== null) {
        const frame = buffer.slice(0, boundary.index);
        buffer = buffer.slice(boundary.index + boundary[0].length);
        const { event, data } = parseSseFrame(frame);
        if (data || event !== 'message') {
          onFrame(event, data);
          received = true;
        }
      }
    }
  } finally {
    try {
      reader.releaseLock();
    } catch {
      /* already released */
    }
  }
  return received;
}

/**
 * Connect to an SSE endpoint with exponential backoff reconnect.
 * Uses apiFetch when available via dynamic import path — callers pass fetchImpl.
 */
export async function connectSseLoop(options: SseConnectOptions): Promise<void> {
  let retryDelay = 2000;

  while (!options.signal.aborted) {
    try {
      const response = await options.fetchImpl(options.url, {
        signal: options.signal,
        credentials: 'include',
      });

      if (response.status === 429) {
        const retryHeader = response.headers.get('Retry-After');
        const secs = retryHeader ? parseInt(retryHeader, 10) : 15;
        retryDelay = (Number.isNaN(secs) ? 15 : secs) * 1000;
        throw new Error('rate_limited');
      }
      if (!response.ok || !response.body) {
        throw new Error('stream_unavailable');
      }

      const received = await readSseStream(response.body, options.signal, (event, data) => {
        options.handlers.onEvent(event, data);
      });
      if (received) {
        retryDelay = 2000;
      }
    } catch {
      if (options.signal.aborted) return;
      options.handlers.onError?.();
    }

    if (options.signal.aborted) return;
    options.onRetryScheduled?.(retryDelay);
    await sleep(retryDelay, options.signal);
    retryDelay = Math.min(retryDelay * 2, 30000);
  }
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const t = setTimeout(() => {
      signal.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(t);
      resolve();
    };
    signal.addEventListener('abort', onAbort, { once: true });
  });
}
