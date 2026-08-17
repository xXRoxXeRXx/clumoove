import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as filesApi from '../api/files';
import {
  clearThumbnailCaches,
  getCachedThumbnail,
  isThumbnailPermanentlyFailed,
  requestThumbnail,
  thumbnailBlobCache,
} from './thumbnailLoader';

describe('thumbnailLoader', () => {
  beforeEach(() => {
    clearThumbnailCaches();
    vi.stubGlobal('URL', {
      createObjectURL: vi.fn((blob: Blob) => `blob:mock-uuid-${blob.size}`),
      revokeObjectURL: vi.fn(),
    });
  });

  afterEach(() => {
    clearThumbnailCaches();
    vi.restoreAllMocks();
  });

  it('returns cached thumbnail immediately without calling API', () => {
    thumbnailBlobCache.set('p1:ref-1:lg', 'blob:cached-lg-url');

    const callback = vi.fn();
    requestThumbnail(
      {
        apiUrl: 'https://api.test',
        token: 'token',
        profileId: 'p1',
        ref: 'ref-1',
        size: 'lg',
      },
      callback
    );

    expect(callback).toHaveBeenCalledWith('blob:cached-lg-url');
    expect(getCachedThumbnail('p1', 'ref-1', 'lg')).toBe('blob:cached-lg-url');
  });

  it('reuses lg thumbnail for sm requests without calling API', () => {
    thumbnailBlobCache.set('p1:ref-1:lg', 'blob:cached-lg-url');

    const callback = vi.fn();
    requestThumbnail(
      {
        apiUrl: 'https://api.test',
        token: 'token',
        profileId: 'p1',
        ref: 'ref-1',
        size: 'sm',
      },
      callback
    );

    expect(callback).toHaveBeenCalledWith('blob:cached-lg-url');
    expect(getCachedThumbnail('p1', 'ref-1', 'sm')).toBe('blob:cached-lg-url');
  });

  it('fetches thumbnail, creates object URL and caches it', async () => {
    const mockBlob = new Blob(['image-bytes-100'], { type: 'image/jpeg' });
    vi.spyOn(filesApi, 'getFileThumbnailResult').mockResolvedValue({
      blob: mockBlob,
      status: 200,
    });

    const callback = vi.fn();
    requestThumbnail(
      {
        apiUrl: 'https://api.test',
        token: 'token',
        profileId: 'p1',
        ref: 'ref-2',
        size: 'lg',
      },
      callback
    );

    await new Promise((r) => setTimeout(r, 10));

    expect(callback).toHaveBeenCalledWith('blob:mock-uuid-15');
    expect(thumbnailBlobCache.get('p1:ref-2:lg')).toBe('blob:mock-uuid-15');
  });

  it('marks permanent failure for 404/415 and does not retry', async () => {
    vi.spyOn(filesApi, 'getFileThumbnailResult').mockResolvedValue({
      blob: null,
      status: 415,
    });

    const callback = vi.fn();
    requestThumbnail(
      {
        apiUrl: 'https://api.test',
        token: 'token',
        profileId: 'p1',
        ref: 'ref-unsupported',
        size: 'lg',
      },
      callback
    );

    await new Promise((r) => setTimeout(r, 10));

    expect(callback).toHaveBeenCalledWith(null);
    expect(isThumbnailPermanentlyFailed('p1', 'ref-unsupported', 'lg')).toBe(true);
    expect(isThumbnailPermanentlyFailed('p1', 'ref-unsupported', 'sm')).toBe(true);

    const secondCallback = vi.fn();
    requestThumbnail(
      {
        apiUrl: 'https://api.test',
        token: 'token',
        profileId: 'p1',
        ref: 'ref-unsupported',
        size: 'sm',
      },
      secondCallback
    );
    expect(secondCallback).toHaveBeenCalledWith(null);
  });

  it('retries on 429 and does not mark permanently failed', async () => {
    const mockBlob = new Blob(['image-bytes-retry'], { type: 'image/jpeg' });
    const spy = vi.spyOn(filesApi, 'getFileThumbnailResult')
      .mockResolvedValueOnce({ blob: null, status: 429 })
      .mockResolvedValueOnce({ blob: mockBlob, status: 200 });

    const callback = vi.fn();
    requestThumbnail(
      {
        apiUrl: 'https://api.test',
        token: 'token',
        profileId: 'p1',
        ref: 'ref-rate-limited',
        size: 'lg',
      },
      callback
    );

    await new Promise((r) => setTimeout(r, 10));
    expect(spy).toHaveBeenCalledTimes(1);
    expect(isThumbnailPermanentlyFailed('p1', 'ref-rate-limited', 'lg')).toBe(false);

    // Advance time to allow 429 backoff timer to fire
    await new Promise((r) => setTimeout(r, 1600));

    expect(spy).toHaveBeenCalledTimes(2);
    expect(callback).toHaveBeenCalledWith('blob:mock-uuid-17');
    expect(thumbnailBlobCache.get('p1:ref-rate-limited:lg')).toBe('blob:mock-uuid-17');
  });

  it('cancels pending item when unsubscribed before execution', async () => {
    // Fill active slots to force queueing
    const unresolving = new Promise<filesApi.ThumbnailFetchResult>(() => {});
    vi.spyOn(filesApi, 'getFileThumbnailResult').mockReturnValue(unresolving);

    for (let i = 0; i < 6; i++) {
      requestThumbnail(
        {
          apiUrl: 'https://api.test',
          token: 'token',
          profileId: 'p1',
          ref: `ref-blocker-${i}`,
          size: 'lg',
        },
        () => {}
      );
    }

    const callback = vi.fn();
    const unsubscribe = requestThumbnail(
      {
        apiUrl: 'https://api.test',
        token: 'token',
        profileId: 'p1',
        ref: 'ref-queued-cancel',
        size: 'lg',
      },
      callback
    );

    unsubscribe();
    expect(callback).not.toHaveBeenCalled();
  });
});
