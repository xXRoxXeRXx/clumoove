import type { UploadConflictStrategy } from '../../api/files';
import type { FileActionTransfer } from './useFileManagerActions';

/**
 * Single presentation model for everything that appears in the transfer panel:
 * browser uploads, same-profile copy/move and cross-profile copy/move.
 */
export type TransferOrigin = 'upload' | 'same-profile' | 'cross-profile';

export type TransferState = 'queued' | 'active' | 'completed' | 'failed' | 'cancelled';

/** Suffix of the `files.uploadStatus.*` translation key describing the item. */
export type TransferStatusKey =
  | 'queued'
  | 'uploading'
  | 'uploaded'
  | 'skipped'
  | 'renamed'
  | 'copying'
  | 'moving'
  | 'copied'
  | 'moved'
  | 'running'
  | 'completed'
  | 'failed'
  | 'cancelled';

export type TransferSubject =
  | { type: 'file'; name: string; mimeType: string; size: number }
  | { type: 'route'; source: string; destination: string };

export type TransferProgress = {
  /**
   * `bytes` and `count` are determinate; `pulse` is work whose progress is unknown until it finishes
   * (done is 0 or 1 of a single unit).
   */
  mode: 'bytes' | 'count' | 'pulse';
  done: number;
  total: number;
};

export type TransferItem = {
  id: string;
  origin: TransferOrigin;
  operation: 'upload' | 'copy' | 'move';
  state: TransferState;
  statusKey: TransferStatusKey;
  subject: TransferSubject;
  progress: TransferProgress;
  error?: string;
  retryable: boolean;
};

export type UploadTaskStatus = 'queued' | 'uploading' | 'uploaded' | 'skipped' | 'renamed' | 'failed' | 'cancelled';

export type UploadTask = {
  id: string;
  file: File;
  profileId: string;
  parentRef: string | null;
  strategy: UploadConflictStrategy;
  status: UploadTaskStatus;
  loaded: number;
  error?: string;
};

export function isUploadTaskCompleted(status: UploadTaskStatus): boolean {
  return status === 'uploaded' || status === 'skipped' || status === 'renamed';
}

function uploadTaskState(status: UploadTaskStatus): TransferState {
  if (status === 'uploading') return 'active';
  if (status === 'queued' || status === 'failed' || status === 'cancelled') return status;
  return 'completed';
}

export function uploadTaskToItem(task: UploadTask): TransferItem {
  const total = Math.max(task.file.size, 1);
  const completed = isUploadTaskCompleted(task.status);
  const state: TransferState = uploadTaskState(task.status);
  const done = completed ? total : task.status === 'uploading' ? Math.min(task.loaded, task.file.size) : 0;
  return {
    id: task.id,
    origin: 'upload',
    operation: 'upload',
    state,
    statusKey: task.status,
    subject: { type: 'file', name: task.file.name, mimeType: task.file.type, size: task.file.size },
    progress: { mode: 'bytes', done, total },
    error: task.error,
    retryable: task.status === 'failed' || task.status === 'cancelled',
  };
}

function actionStatusKey(transfer: FileActionTransfer): TransferStatusKey {
  const sameProfile = transfer.origin === 'same-profile';
  switch (transfer.status) {
    case 'running':
      if (!sameProfile) return 'running';
      return transfer.operation === 'copy' ? 'copying' : 'moving';
    case 'completed':
      if (!sameProfile) return 'completed';
      return transfer.operation === 'copy' ? 'copied' : 'moved';
    default:
      return transfer.status;
  }
}

export function actionTransferToItem(transfer: FileActionTransfer): TransferItem {
  const sameProfile = transfer.origin === 'same-profile';
  const progress: TransferProgress = sameProfile
    ? { mode: 'pulse', done: transfer.status === 'completed' ? 1 : 0, total: 1 }
    : { mode: 'count', done: transfer.processedCount, total: transfer.itemCount };
  return {
    id: transfer.id,
    origin: transfer.origin,
    operation: transfer.operation,
    state: transfer.status === 'running' ? 'active' : transfer.status,
    statusKey: actionStatusKey(transfer),
    subject: { type: 'route', source: transfer.sourceLabel, destination: transfer.destinationLabel },
    progress,
    error: transfer.error,
    retryable: false,
  };
}

export type TransferSummary = {
  total: number;
  completed: number;
  inProgress: number;
  failed: number;
  /** Overall percentage, byte-weighted while uploads are present, otherwise by units of work. */
  percent: number;
};

function percentOf(done: number, total: number): number {
  return Math.min(100, Math.round((done / total) * 100));
}

export function summarizeTransfers(items: TransferItem[]): TransferSummary {
  let completed = 0;
  let inProgress = 0;
  let failed = 0;
  let byteDone = 0;
  let byteTotal = 0;
  let unitDone = 0;
  let unitTotal = 0;
  for (const item of items) {
    if (item.state === 'completed') completed += 1;
    else if (item.state === 'queued' || item.state === 'active') inProgress += 1;
    else if (item.state === 'failed') failed += 1;
    if (item.progress.mode === 'bytes') {
      byteDone += item.progress.done;
      byteTotal += item.progress.total;
    } else {
      unitDone += item.progress.done;
      unitTotal += item.progress.total;
    }
  }
  const percent = byteTotal > 0
    ? percentOf(byteDone, byteTotal)
    : unitTotal > 0
      ? percentOf(unitDone, unitTotal)
      : completed > 0 ? 100 : 0;
  return { total: items.length, completed, inProgress, failed, percent };
}
