import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import { uploadFile } from '../../api/files';
import { AUTO_DISMISS_MS, useTransferCenter, type TransferCenter, type UseTransferCenterOptions } from './useTransferCenter';
import type { FileActionTransfer } from './useFileManagerActions';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../../api/files', () => ({
  uploadFile: vi.fn(),
}));

type UploadResult = Awaited<ReturnType<typeof uploadFile>>;

function makeFile(name: string): File {
  return new File(['x'], name, { type: 'text/plain' });
}

function transfer(overrides: Partial<FileActionTransfer>): FileActionTransfer {
  return {
    id: 't1',
    origin: 'same-profile',
    operation: 'copy',
    status: 'running',
    sourceLabel: 'a.txt',
    destinationLabel: 'Docs',
    itemCount: 1,
    processedCount: 0,
    failedCount: 0,
    skippedCount: 0,
    ...overrides,
  };
}

const target = { profileId: 'profile-1', parentRef: null, strategy: 'SKIP' as const };

describe('useTransferCenter', () => {
  let container: HTMLDivElement;
  let root: Root;
  let center: TransferCenter;
  const cancelTransfer = vi.fn(async () => undefined);
  const dismissTransfer = vi.fn();
  const onUploadCompleted = vi.fn();
  let transfers: FileActionTransfer[];

  function Probe(props: UseTransferCenterOptions) {
    center = useTransferCenter(props);
    return null;
  }

  async function render() {
    await act(async () => {
      root.render(
        <Probe
          apiUrl="https://api.example.test"
          token="jwt"
          transfers={transfers}
          onCancelTransfer={cancelTransfer}
          onDismissTransfer={dismissTransfer}
          onUploadCompleted={onUploadCompleted}
        />,
      );
    });
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    cancelTransfer.mockClear();
    dismissTransfer.mockReset();
    onUploadCompleted.mockReset();
    vi.mocked(uploadFile).mockReset();
    transfers = [];
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    vi.useRealTimers();
    act(() => root.unmount());
    container.remove();
  });

  it('presents uploads, same-profile and cross-profile transfers as one list', async () => {
    vi.mocked(uploadFile).mockReturnValue(new Promise(() => undefined));
    transfers = [
      transfer({ id: 'same', origin: 'same-profile' }),
      transfer({ id: 'cross', origin: 'cross-profile', itemCount: 3 }),
    ];
    await render();
    act(() => center.enqueueUploads([makeFile('up.txt')], target));
    await flush();

    expect(center.items.map((item) => [item.id === 'same' || item.id === 'cross' ? item.id : 'upload', item.origin])).toEqual([
      ['same', 'same-profile'],
      ['cross', 'cross-profile'],
      ['upload', 'upload'],
    ]);
  });

  it('cancels each origin through its own source', async () => {
    let signal: AbortSignal | undefined;
    vi.mocked(uploadFile).mockImplementation((_u, _t, _p, _f, _r, _s, _o, abortSignal) => {
      signal = abortSignal;
      return new Promise(() => undefined);
    });
    transfers = [transfer({ id: 'same' }), transfer({ id: 'cross', origin: 'cross-profile' })];
    await render();
    act(() => center.enqueueUploads([makeFile('up.txt')], target));
    await flush();
    const uploadId = center.items.find((item) => item.origin === 'upload')!.id;

    act(() => center.cancel(uploadId));
    expect(signal?.aborted).toBe(true);
    expect(cancelTransfer).not.toHaveBeenCalled();

    act(() => center.cancel('same'));
    act(() => center.cancel('cross'));
    expect(cancelTransfer).toHaveBeenCalledTimes(2);
    expect(cancelTransfer).toHaveBeenNthCalledWith(1, 'same');
    expect(cancelTransfer).toHaveBeenNthCalledWith(2, 'cross');
  });

  it('dismisses an upload without touching transfer state, and a transfer without touching the upload queue', async () => {
    vi.mocked(uploadFile).mockResolvedValue({ ok: true, status: 201, data: { status: 'uploaded', name: 'up.txt' } } as UploadResult);
    transfers = [transfer({ id: 'same', status: 'completed' })];
    await render();
    act(() => center.enqueueUploads([makeFile('up.txt')], target));
    await flush();
    const uploadId = center.items.find((item) => item.origin === 'upload')!.id;

    act(() => center.dismiss(uploadId));
    expect(dismissTransfer).not.toHaveBeenCalled();
    expect(center.items.map((item) => item.id)).toEqual(['same']);

    act(() => center.dismiss('same'));
    expect(dismissTransfer).toHaveBeenCalledWith('same');
    expect(center.items).toEqual([]);
  });

  it('keeps a dismissed transfer hidden when the server list still reports it', async () => {
    transfers = [transfer({ id: 'cross', origin: 'cross-profile', status: 'completed', itemCount: 1, processedCount: 1 })];
    await render();
    act(() => center.dismiss('cross'));
    transfers = [{ ...transfers[0] }];
    await render();
    expect(center.items).toEqual([]);
  });

  it('retries only uploads', async () => {
    vi.mocked(uploadFile)
      .mockResolvedValueOnce({ ok: false, status: 500, errorCode: 'UNKNOWN', networkError: false })
      .mockResolvedValueOnce({ ok: true, status: 201, data: { status: 'uploaded', name: 'up.txt' } });
    await render();
    act(() => center.enqueueUploads([makeFile('up.txt')], target));
    await flush();
    const failedItem = center.items[0];
    expect(failedItem).toMatchObject({ state: 'failed', retryable: true });

    act(() => center.retry(failedItem.id));
    await flush();
    expect(center.items[0].state).toBe('completed');
    expect(onUploadCompleted).toHaveBeenCalledWith('profile-1');
  });

  it.each([
    ['upload', undefined],
    ['same-profile', 'same-profile'],
    ['cross-profile', 'cross-profile'],
  ] as const)('auto-dismisses a completed %s item after four seconds', async (origin, actionOrigin) => {
    vi.useFakeTimers();
    vi.mocked(uploadFile).mockResolvedValue({ ok: true, status: 201, data: { status: 'uploaded', name: 'up.txt' } } as UploadResult);
    if (actionOrigin) {
      transfers = [transfer({ id: 'done', origin: actionOrigin, status: 'completed', itemCount: 1, processedCount: 1 })];
    }
    await render();
    if (origin === 'upload') {
      act(() => center.enqueueUploads([makeFile('up.txt')], target));
      await flush();
    }
    expect(center.items).toHaveLength(1);
    expect(center.items[0].state).toBe('completed');

    await act(async () => {
      vi.advanceTimersByTime(AUTO_DISMISS_MS - 1);
    });
    expect(center.items).toHaveLength(1);

    await act(async () => {
      vi.advanceTimersByTime(1);
    });
    expect(center.items).toEqual([]);
    if (actionOrigin) expect(dismissTransfer).toHaveBeenCalledWith('done');
  });

  it('does not auto-dismiss failed or cancelled items', async () => {
    vi.useFakeTimers();
    transfers = [transfer({ id: 'bad', status: 'failed', error: 'x' }), transfer({ id: 'stopped', status: 'cancelled' })];
    await render();
    await act(async () => {
      vi.advanceTimersByTime(AUTO_DISMISS_MS * 2);
    });
    expect(center.items.map((item) => item.id)).toEqual(['bad', 'stopped']);
  });

  it('cancels a pending auto-dismiss when the item is dismissed manually', async () => {
    vi.useFakeTimers();
    transfers = [transfer({ id: 'done', status: 'completed' })];
    await render();
    act(() => center.dismiss('done'));
    await act(async () => {
      vi.advanceTimersByTime(AUTO_DISMISS_MS * 2);
    });
    expect(dismissTransfer).toHaveBeenCalledTimes(1);
  });
});
