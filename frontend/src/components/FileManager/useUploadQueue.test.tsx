import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import { uploadFile } from '../../api/files';
import { useUploadQueue, type UploadQueue } from './useUploadQueue';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../../api/files', () => ({
  uploadFile: vi.fn(),
}));

type UploadResult = Awaited<ReturnType<typeof uploadFile>>;

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

function makeFile(name: string, size = 10): File {
  return new File(['x'.repeat(size)], name, { type: 'text/plain' });
}

const uploaded = (name = 'f'): UploadResult => ({ ok: true, status: 201, data: { status: 'uploaded', name } });
const failed = (errorCode: string): UploadResult => ({ ok: false, status: 429, errorCode, networkError: false });

describe('useUploadQueue', () => {
  let container: HTMLDivElement;
  let root: Root;
  let queue: UploadQueue;
  const onCompleted = vi.fn();

  function Probe() {
    queue = useUploadQueue({ apiUrl: 'https://api.example.test', token: 'jwt', onCompleted });
    return null;
  }

  const target = { profileId: 'profile-1', parentRef: 'root', strategy: 'RENAME' as const };

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    onCompleted.mockReset();
    vi.mocked(uploadFile).mockReset();
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<Probe />);
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    act(() => root.unmount());
    container.remove();
  });

  it('uploads queued files with the chosen target and reports completion for the target profile', async () => {
    vi.mocked(uploadFile).mockResolvedValue(uploaded('a.txt'));
    const file = makeFile('a.txt');
    act(() => queue.enqueue([file], target));
    await flush();

    expect(uploadFile).toHaveBeenCalledWith(
      'https://api.example.test', 'jwt', 'profile-1', file, 'root', 'RENAME', expect.any(Function), expect.any(AbortSignal),
    );
    expect(queue.tasks[0].status).toBe('uploaded');
    expect(onCompleted).toHaveBeenCalledWith('profile-1');
  });

  it('records the server outcome (skipped/renamed) as the task status', async () => {
    vi.mocked(uploadFile).mockResolvedValue({ ok: true, status: 200, data: { status: 'skipped', name: 'a.txt' } });
    act(() => queue.enqueue([makeFile('a.txt')], target));
    await flush();
    expect(queue.tasks[0].status).toBe('skipped');
  });

  it('runs at most four uploads at once and starts the next as slots free up', async () => {
    const pending = Array.from({ length: 6 }, () => deferred<UploadResult>());
    let call = 0;
    vi.mocked(uploadFile).mockImplementation(() => pending[call++].promise);

    act(() => queue.enqueue(Array.from({ length: 6 }, (_, i) => makeFile(`f${i}`)), target));
    await flush();
    expect(uploadFile).toHaveBeenCalledTimes(4);

    await act(async () => pending[0].resolve(uploaded()));
    await flush();
    expect(uploadFile).toHaveBeenCalledTimes(5);
    expect(onCompleted).toHaveBeenCalledTimes(1);
  });

  it('marks a failed upload with a translated error and does not report completion', async () => {
    vi.mocked(uploadFile).mockResolvedValue(failed('FORBIDDEN'));
    act(() => queue.enqueue([makeFile('a.txt')], target));
    await flush();
    expect(queue.tasks[0].status).toBe('failed');
    expect(queue.tasks[0].error).toBeTruthy();
    expect(onCompleted).not.toHaveBeenCalled();
  });

  it('retries a failed upload from scratch', async () => {
    vi.mocked(uploadFile).mockResolvedValueOnce(failed('FORBIDDEN')).mockResolvedValueOnce(uploaded('a.txt'));
    act(() => queue.enqueue([makeFile('a.txt')], target));
    await flush();
    const id = queue.tasks[0].id;

    act(() => queue.retry(id));
    await flush();
    expect(uploadFile).toHaveBeenCalledTimes(2);
    expect(queue.tasks[0]).toMatchObject({ id, status: 'uploaded', error: undefined });
    expect(onCompleted).toHaveBeenCalledTimes(1);
  });

  it('delays queued uploads for 1.5s after a rate-limited failure', async () => {
    vi.useFakeTimers();
    const pending = Array.from({ length: 5 }, () => deferred<UploadResult>());
    let call = 0;
    vi.mocked(uploadFile).mockImplementation(() => pending[call++]?.promise ?? new Promise(() => undefined));

    act(() => queue.enqueue(Array.from({ length: 5 }, (_, i) => makeFile(`f${i}`)), target));
    await flush();
    expect(uploadFile).toHaveBeenCalledTimes(4);

    await act(async () => pending[0].resolve(failed('RATE_LIMITED')));
    await flush();
    expect(uploadFile).toHaveBeenCalledTimes(4);

    await act(async () => {
      vi.advanceTimersByTime(1499);
    });
    expect(uploadFile).toHaveBeenCalledTimes(4);

    await act(async () => {
      vi.advanceTimersByTime(1);
    });
    await flush();
    expect(uploadFile).toHaveBeenCalledTimes(5);
  });

  it('cancels a running upload by aborting it and keeps it retryable', async () => {
    let signal: AbortSignal | undefined;
    vi.mocked(uploadFile).mockImplementation((_u, _t, _p, _f, _r, _s, _o, abortSignal) => {
      signal = abortSignal;
      return new Promise((resolve) => abortSignal?.addEventListener('abort', () => resolve(failed('ABORTED'))));
    });
    act(() => queue.enqueue([makeFile('a.txt')], target));
    await flush();

    act(() => queue.cancel(queue.tasks[0].id));
    await flush();
    expect(signal?.aborted).toBe(true);
    expect(queue.tasks[0].status).toBe('cancelled');
    expect(onCompleted).not.toHaveBeenCalled();
  });

  it('does not cancel an upload that already finished', async () => {
    vi.mocked(uploadFile).mockResolvedValue(uploaded('a.txt'));
    act(() => queue.enqueue([makeFile('a.txt')], target));
    await flush();
    act(() => queue.cancel(queue.tasks[0].id));
    expect(queue.tasks[0].status).toBe('uploaded');
  });

  it('dismisses a task from the queue', async () => {
    vi.mocked(uploadFile).mockResolvedValue(uploaded('a.txt'));
    act(() => queue.enqueue([makeFile('a.txt')], target));
    await flush();
    act(() => queue.dismiss(queue.tasks[0].id));
    expect(queue.tasks).toEqual([]);
  });

  it('aborts running uploads on unmount', async () => {
    let signal: AbortSignal | undefined;
    vi.mocked(uploadFile).mockImplementation((_u, _t, _p, _f, _r, _s, _o, abortSignal) => {
      signal = abortSignal;
      return new Promise(() => undefined);
    });
    act(() => queue.enqueue([makeFile('a.txt')], target));
    await flush();
    act(() => root.unmount());
    expect(signal?.aborted).toBe(true);
    root = createRoot(container);
  });
});
