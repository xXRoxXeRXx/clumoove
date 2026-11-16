import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { actionTransferToItem, uploadTaskToItem, type TransferItem } from './transferItems';
import type { FileActionTransfer } from './useFileManagerActions';
import { useUploadQueue, type UploadTarget } from './useUploadQueue';

/** How long a completed transfer stays visible before it dismisses itself. */
export const AUTO_DISMISS_MS = 4000;

export type UseTransferCenterOptions = {
  apiUrl: string;
  token: string;
  /** Same-profile and cross-profile transfers owned by the file actions. */
  transfers: FileActionTransfer[];
  onCancelTransfer: (transferId: string) => void | Promise<void>;
  onDismissTransfer: (transferId: string) => void;
  /** Called with the target profile after an upload reached the server. */
  onUploadCompleted: (profileId: string) => void;
};

export type TransferCenter = {
  /** Every visible transfer, whatever its origin. */
  items: TransferItem[];
  enqueueUploads: (files: File[], target: UploadTarget) => void;
  /** Cancel / dismiss / retry act on an item id; the center routes it to the owning source. */
  cancel: (itemId: string) => void;
  dismiss: (itemId: string) => void;
  retry: (itemId: string) => void;
};

/**
 * Joins browser uploads with same-profile and cross-profile transfers into one list and one
 * set of lifecycle actions. Callers never need to know which queue owns an item.
 */
export function useTransferCenter({
  apiUrl,
  token,
  transfers,
  onCancelTransfer,
  onDismissTransfer,
  onUploadCompleted,
}: UseTransferCenterOptions): TransferCenter {
  const {
    tasks,
    enqueue,
    cancel: cancelUpload,
    retry: retryUpload,
    dismiss: dismissUpload,
  } = useUploadQueue({ apiUrl, token, onCompleted: onUploadCompleted });
  const [dismissedIds, setDismissedIds] = useState<Set<string>>(() => new Set());
  const dismissTimers = useRef(new Map<string, number>());

  const uploadIds = useMemo(() => new Set(tasks.map((task) => task.id)), [tasks]);

  const items = useMemo<TransferItem[]>(
    () =>
      [...transfers.map(actionTransferToItem), ...tasks.map(uploadTaskToItem)].filter(
        (item) => !dismissedIds.has(item.id),
      ),
    [dismissedIds, tasks, transfers],
  );

  const dismiss = useCallback(
    (itemId: string) => {
      const timer = dismissTimers.current.get(itemId);
      if (timer !== undefined) {
        window.clearTimeout(timer);
        dismissTimers.current.delete(itemId);
      }
      setDismissedIds((prev) => new Set(prev).add(itemId));
      if (uploadIds.has(itemId)) {
        dismissUpload(itemId);
      } else {
        onDismissTransfer(itemId);
      }
    },
    [dismissUpload, onDismissTransfer, uploadIds],
  );

  const cancel = useCallback(
    (itemId: string) => {
      if (uploadIds.has(itemId)) {
        cancelUpload(itemId);
      } else {
        void onCancelTransfer(itemId);
      }
    },
    [cancelUpload, onCancelTransfer, uploadIds],
  );

  const retry = useCallback(
    (itemId: string) => {
      if (uploadIds.has(itemId)) retryUpload(itemId);
    },
    [retryUpload, uploadIds],
  );

  useEffect(() => {
    for (const item of items) {
      if (item.state !== 'completed' || dismissTimers.current.has(item.id)) continue;
      const timer = window.setTimeout(() => {
        dismissTimers.current.delete(item.id);
        dismiss(item.id);
      }, AUTO_DISMISS_MS);
      dismissTimers.current.set(item.id, timer);
    }
  }, [dismiss, items]);

  useEffect(() => {
    const timers = dismissTimers.current;
    return () => {
      timers.forEach((timer) => window.clearTimeout(timer));
      timers.clear();
    };
  }, []);

  return { items, enqueueUploads: enqueue, cancel, dismiss, retry };
}
