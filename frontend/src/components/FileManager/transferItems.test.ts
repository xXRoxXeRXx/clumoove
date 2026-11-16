import { describe, expect, it } from 'vitest';
import {
  actionTransferToItem,
  summarizeTransfers,
  uploadTaskToItem,
  type UploadTask,
} from './transferItems';
import type { FileActionTransfer } from './useFileManagerActions';

function file(name: string, size: number): File {
  return new File(['x'.repeat(size)], name, { type: 'text/plain' });
}

function task(overrides: Partial<UploadTask> = {}): UploadTask {
  return {
    id: 'u1',
    file: file('report.txt', 100),
    profileId: 'p1',
    parentRef: null,
    strategy: 'SKIP',
    status: 'queued',
    loaded: 0,
    ...overrides,
  };
}

function actionTransfer(overrides: Partial<FileActionTransfer> = {}): FileActionTransfer {
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

describe('uploadTaskToItem', () => {
  it('presents an upload as a cancellable, retryable file item with byte progress', () => {
    const item = uploadTaskToItem(task({ status: 'uploading', loaded: 40 }));
    expect(item).toMatchObject({
      id: 'u1',
      origin: 'upload',
      operation: 'upload',
      state: 'active',
      statusKey: 'uploading',
      subject: { type: 'file', name: 'report.txt', mimeType: 'text/plain', size: 100 },
      progress: { mode: 'bytes', done: 40, total: 100 },
      retryable: false,
    });
  });

  it.each([
    ['queued', 'queued', 'queued'],
    ['uploaded', 'completed', 'uploaded'],
    ['skipped', 'completed', 'skipped'],
    ['renamed', 'completed', 'renamed'],
    ['failed', 'failed', 'failed'],
    ['cancelled', 'cancelled', 'cancelled'],
  ] as const)('maps upload status %s to state %s', (status, state, statusKey) => {
    const item = uploadTaskToItem(task({ status }));
    expect(item.state).toBe(state);
    expect(item.statusKey).toBe(statusKey);
  });

  it('marks failed and cancelled uploads retryable and carries the error', () => {
    expect(uploadTaskToItem(task({ status: 'failed', error: 'boom' }))).toMatchObject({ retryable: true, error: 'boom' });
    expect(uploadTaskToItem(task({ status: 'cancelled' })).retryable).toBe(true);
  });

  it('counts a finished upload as fully transferred', () => {
    expect(uploadTaskToItem(task({ status: 'uploaded', loaded: 0 })).progress).toMatchObject({ done: 100, total: 100 });
  });

  it('never reports zero-byte uploads as zero-sized work', () => {
    expect(uploadTaskToItem(task({ file: file('empty', 0), status: 'uploaded' })).progress).toMatchObject({ done: 1, total: 1 });
  });
});

describe('actionTransferToItem', () => {
  it('presents a same-profile copy as an indeterminate route item', () => {
    expect(actionTransferToItem(actionTransfer())).toMatchObject({
      id: 't1',
      origin: 'same-profile',
      operation: 'copy',
      state: 'active',
      statusKey: 'copying',
      subject: { type: 'route', source: 'a.txt', destination: 'Docs' },
      progress: { mode: 'pulse', done: 0, total: 1 },
      retryable: false,
    });
  });

  it('uses move wording for running and completed same-profile moves', () => {
    expect(actionTransferToItem(actionTransfer({ operation: 'move' })).statusKey).toBe('moving');
    const done = actionTransferToItem(actionTransfer({ operation: 'move', status: 'completed' }));
    expect(done).toMatchObject({ state: 'completed', statusKey: 'moved', progress: { done: 1, total: 1 } });
  });

  it('presents a cross-profile transfer with file count progress and generic completion wording', () => {
    const running = actionTransferToItem(actionTransfer({
      origin: 'cross-profile', status: 'running', itemCount: 4, processedCount: 1, serverStatus: 'RUNNING',
    }));
    expect(running).toMatchObject({
      origin: 'cross-profile', state: 'active', statusKey: 'running', progress: { mode: 'count', done: 1, total: 4 },
    });
    const done = actionTransferToItem(actionTransfer({ origin: 'cross-profile', status: 'completed', itemCount: 4, processedCount: 4 }));
    expect(done).toMatchObject({ state: 'completed', statusKey: 'completed' });
  });

  it.each([
    ['queued', 'queued', 'queued'],
    ['failed', 'failed', 'failed'],
    ['cancelled', 'cancelled', 'cancelled'],
  ] as const)('maps %s transfers', (status, state, statusKey) => {
    expect(actionTransferToItem(actionTransfer({ status }))).toMatchObject({ state, statusKey });
  });

  it.each([
    ['same-profile', 'failed'],
    ['same-profile', 'cancelled'],
    ['cross-profile', 'failed'],
    ['cross-profile', 'cancelled'],
  ] as const)('never offers retry for a %s transfer that %s', (origin, status) => {
    expect(actionTransferToItem(actionTransfer({ origin, status })).retryable).toBe(false);
  });

  it('carries the error of a failed same-profile transfer', () => {
    expect(actionTransferToItem(actionTransfer({ status: 'failed', error: 'denied' })).error).toBe('denied');
  });
});

describe('summarizeTransfers', () => {
  it('is empty for no items', () => {
    expect(summarizeTransfers([])).toEqual({ total: 0, completed: 0, inProgress: 0, failed: 0, percent: 0 });
  });

  it('counts states across origins and weights uploads by bytes', () => {
    const items = [
      uploadTaskToItem(task({ id: 'a', status: 'uploaded', file: file('a', 100) })),
      uploadTaskToItem(task({ id: 'b', status: 'uploading', loaded: 50, file: file('b', 100) })),
      actionTransferToItem(actionTransfer({ id: 'c', status: 'failed' })),
      actionTransferToItem(actionTransfer({ id: 'd', origin: 'cross-profile', status: 'queued', itemCount: 2 })),
    ];
    expect(summarizeTransfers(items)).toEqual({ total: 4, completed: 1, inProgress: 2, failed: 1, percent: 75 });
  });

  it('falls back to unit progress when there are no uploads', () => {
    const items = [
      actionTransferToItem(actionTransfer({ id: 'a', status: 'completed' })),
      actionTransferToItem(actionTransfer({ id: 'b', origin: 'cross-profile', itemCount: 4, processedCount: 1 })),
    ];
    // (1 + 1) done of (1 + 4) total units
    expect(summarizeTransfers(items).percent).toBe(40);
  });

  it('reports 100 percent when everything is finished but carries no measurable work', () => {
    const items = [actionTransferToItem(actionTransfer({ origin: 'cross-profile', status: 'completed', itemCount: 0 }))];
    expect(summarizeTransfers(items).percent).toBe(100);
  });
});
