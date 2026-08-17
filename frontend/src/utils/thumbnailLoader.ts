import { getFileThumbnailResult } from '../api/files';

export type ThumbnailSize = 'sm' | 'lg';

export type ThumbnailRequestConfig = {
  apiUrl: string;
  token: string;
  profileId: string;
  ref: string;
  size: ThumbnailSize;
  signal?: AbortSignal;
};

type Subscriber = {
  onComplete: (url: string | null) => void;
  signal?: AbortSignal;
};

type QueuedItem = {
  key: string;
  apiUrl: string;
  token: string;
  profileId: string;
  ref: string;
  size: ThumbnailSize;
  subscribers: Set<Subscriber>;
  retries: number;
};

type ActiveItem = QueuedItem & {
  controller: AbortController;
};

// Global in-memory cache
export const thumbnailBlobCache = new Map<string, string>();
export const failedThumbnailCache = new Set<string>();

const MAX_CONCURRENT_REQUESTS = 6;
const MAX_429_RETRIES = 3;
const BACKOFF_MS_429 = 1500;

let activeCount = 0;
const pendingQueue: QueuedItem[] = [];
const inFlightRequests = new Map<string, ActiveItem>();
let pausedUntil = 0;
let pauseTimeoutId: ReturnType<typeof setTimeout> | null = null;

export function buildCacheKey(profileId: string, ref: string, size: ThumbnailSize): string {
  return `${profileId}:${ref}:${size}`;
}

export function getCachedThumbnail(profileId: string, ref: string, preferredSize: ThumbnailSize): string | null {
  const directKey = buildCacheKey(profileId, ref, preferredSize);
  const direct = thumbnailBlobCache.get(directKey);
  if (direct) return direct;

  // Cross-size reuse: If asking for 'sm' and we have 'lg', use 'lg' directly.
  if (preferredSize === 'sm') {
    const lgKey = buildCacheKey(profileId, ref, 'lg');
    const lg = thumbnailBlobCache.get(lgKey);
    if (lg) return lg;
  }

  // If asking for 'lg' and we have 'sm', 'sm' can be used as low-res fallback
  if (preferredSize === 'lg') {
    const smKey = buildCacheKey(profileId, ref, 'sm');
    const sm = thumbnailBlobCache.get(smKey);
    if (sm) return sm;
  }

  return null;
}

export function isThumbnailPermanentlyFailed(profileId: string, ref: string, size: ThumbnailSize): boolean {
  return (
    failedThumbnailCache.has(buildCacheKey(profileId, ref, size)) ||
    failedThumbnailCache.has(`${profileId}:${ref}:all`)
  );
}

function processQueue(): void {
  if (Date.now() < pausedUntil) {
    if (!pauseTimeoutId) {
      const delay = Math.max(0, pausedUntil - Date.now());
      pauseTimeoutId = setTimeout(() => {
        pauseTimeoutId = null;
        processQueue();
      }, delay);
    }
    return;
  }

  while (activeCount < MAX_CONCURRENT_REQUESTS && pendingQueue.length > 0) {
    const item = pendingQueue.shift();
    if (!item) break;

    // Prune subscribers whose signal has already aborted
    for (const sub of Array.from(item.subscribers)) {
      if (sub.signal?.aborted) {
        item.subscribers.delete(sub);
      }
    }

    if (item.subscribers.size === 0) {
      continue;
    }

    activeCount++;
    const controller = new AbortController();
    const activeItem: ActiveItem = { ...item, controller };
    inFlightRequests.set(item.key, activeItem);

    // If all subscribers abort while in flight, abort the controller
    const checkAbort = () => {
      let anyActive = false;
      for (const sub of Array.from(activeItem.subscribers)) {
        if (!sub.signal?.aborted) {
          anyActive = true;
          break;
        }
      }
      if (!anyActive) {
        controller.abort();
      }
    };

    for (const sub of activeItem.subscribers) {
      if (sub.signal) {
        sub.signal.addEventListener('abort', checkAbort, { once: true });
      }
    }

    const width = item.size === 'sm' ? 128 : 256;
    const height = item.size === 'sm' ? 128 : 256;

    void getFileThumbnailResult(
      item.apiUrl,
      item.token,
      item.profileId,
      item.ref,
      width,
      height,
      controller.signal
    )
      .then((result) => {
        inFlightRequests.delete(item.key);
        activeCount--;

        if (controller.signal.aborted) {
          // Aborted by subscriber scroll-out / unmount, don't mark failed
          return;
        }

        if (result.status === 429) {
          // Rate limited: pause queue and retry
          pausedUntil = Date.now() + BACKOFF_MS_429;
          if (item.retries < MAX_429_RETRIES) {
            // Re-insert at the FRONT of the queue for next retry
            pendingQueue.unshift({
              ...item,
              retries: item.retries + 1,
            });
          } else {
            // Exhausted retries, notify subscribers but DO NOT mark failed permanently
            for (const sub of item.subscribers) {
              if (!sub.signal?.aborted) {
                sub.onComplete(null);
              }
            }
          }
          processQueue();
          return;
        }

        if (result.blob && result.blob.size > 0) {
          const url = URL.createObjectURL(result.blob);
          thumbnailBlobCache.set(item.key, url);
          for (const sub of item.subscribers) {
            if (!sub.signal?.aborted) {
              sub.onComplete(url);
            }
          }
        } else {
          // 404, 415, 501 or empty response
          if (result.status === 404 || result.status === 415 || result.status === 501) {
            failedThumbnailCache.add(item.key);
            if (result.status === 415 || result.status === 501) {
              failedThumbnailCache.add(`${item.profileId}:${item.ref}:all`);
            }
          }
          for (const sub of item.subscribers) {
            if (!sub.signal?.aborted) {
              sub.onComplete(null);
            }
          }
        }

        processQueue();
      })
      .catch(() => {
        inFlightRequests.delete(item.key);
        activeCount--;
        if (!controller.signal.aborted) {
          for (const sub of item.subscribers) {
            if (!sub.signal?.aborted) {
              sub.onComplete(null);
            }
          }
        }
        processQueue();
      });
  }
}

/**
 * Requests a thumbnail with queueing, concurrency control, 429 backoff/retries, and caching.
 * Returns an unsubscribe function to cancel if component unmounts or scrolls away.
 */
export function requestThumbnail(
  config: ThumbnailRequestConfig,
  onComplete: (url: string | null) => void
): () => void {
  const { apiUrl, token, profileId, ref, size, signal } = config;
  const key = buildCacheKey(profileId, ref, size);

  // 1. Immediate exact cache hit
  const exactUrl = thumbnailBlobCache.get(key);
  if (exactUrl) {
    onComplete(exactUrl);
    return () => {};
  }

  // 2. High-res cache hit for 'sm'
  if (size === 'sm') {
    const lgKey = buildCacheKey(profileId, ref, 'lg');
    const lgUrl = thumbnailBlobCache.get(lgKey);
    if (lgUrl) {
      onComplete(lgUrl);
      return () => {};
    }
  }

  // 3. Check permanent failure
  if (isThumbnailPermanentlyFailed(profileId, ref, size)) {
    onComplete(null);
    return () => {};
  }

  const subscriber: Subscriber = { onComplete, signal };

  // 4. In-flight request check
  const inFlight = inFlightRequests.get(key);
  if (inFlight) {
    inFlight.subscribers.add(subscriber);
    return () => {
      inFlight.subscribers.delete(subscriber);
    };
  }

  // 5. Existing pending request check
  const existingPending = pendingQueue.find((q) => q.key === key);
  if (existingPending) {
    existingPending.subscribers.add(subscriber);
    return () => {
      existingPending.subscribers.delete(subscriber);
    };
  }

  // 6. Queue new item at the FRONT of queue (LIFO priority for viewport visibility)
  const newItem: QueuedItem = {
    key,
    apiUrl,
    token,
    profileId,
    ref,
    size,
    subscribers: new Set([subscriber]),
    retries: 0,
  };

  pendingQueue.unshift(newItem);
  processQueue();

  return () => {
    newItem.subscribers.delete(subscriber);
    if (newItem.subscribers.size === 0) {
      const idx = pendingQueue.indexOf(newItem);
      if (idx !== -1) {
        pendingQueue.splice(idx, 1);
      }
    }
  };
}

/**
 * Clears caches and resets the queue state. (Used primarily in tests).
 */
export function clearThumbnailCaches(): void {
  thumbnailBlobCache.clear();
  failedThumbnailCache.clear();
  pendingQueue.length = 0;
  inFlightRequests.clear();
  activeCount = 0;
  pausedUntil = 0;
  if (pauseTimeoutId) {
    clearTimeout(pauseTimeoutId);
    pauseTimeoutId = null;
  }
}
