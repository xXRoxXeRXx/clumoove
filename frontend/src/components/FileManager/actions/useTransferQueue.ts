import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { FileTransferSummary } from '../../../api/files';
import type { ActionEnvironment } from './actionEnvironment';

export type FileTransferOperation = 'copy' | 'move';

export type FileActionTransferOrigin = 'same-profile' | 'cross-profile';

export type FileActionTransferStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled';

export type FileActionTransfer = {
  id: string;
  origin: FileActionTransferOrigin;
  operation: FileTransferOperation;
  status: FileActionTransferStatus;
  sourceLabel: string;
  destinationLabel: string;
  itemCount: number;
  processedCount: number;
  failedCount: number;
  skippedCount: number;
  error?: string;
  /** Status exactly as reported by the server; set for cross-profile transfers only. */
  serverStatus?: string;
};

/** A same-profile copy/move to run in the background; `run` reports how it ended. */
export type SameProfileTransferJob = {
  operation: FileTransferOperation;
  sourceLabel: string;
  destinationLabel: string;
  itemCount: number;
  controller: AbortController;
  run: () => Promise<
    | { ok: true }
    | { ok: false; processedCount: number; failedCount: number; error: string; refreshOnPartial?: boolean }
  >;
  onSuccess?: () => void;
};

const CROSS_PROFILE_POLL_MS = 5000;

function nextTransferID(): string {
  return crypto.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
}

function mapCrossProfileStatus(transfer: FileTransferSummary): FileActionTransferStatus {
  if (transfer.status === 'COMPLETED' || (transfer.total_files > 0 && transfer.processed_files >= transfer.total_files)) {
    return 'completed';
  }
  if (transfer.status === 'CANCELLED' || transfer.status === 'CANCELED') {
    return 'cancelled';
  }
  if (transfer.status === 'FAILED') {
    return 'failed';
  }
  if (transfer.status === 'PENDING') {
    return 'queued';
  }
  return 'running';
}

function toCrossProfileTransfer(transfer: FileTransferSummary): FileActionTransfer {
  return {
    id: transfer.id,
    origin: 'cross-profile',
    operation: transfer.operation,
    status: mapCrossProfileStatus(transfer),
    sourceLabel: transfer.source_profile_name,
    destinationLabel: transfer.target_profile_name,
    itemCount: transfer.total_files,
    processedCount: transfer.processed_files,
    failedCount: transfer.failed_files,
    skippedCount: transfer.skipped_files,
    serverStatus: transfer.status,
  };
}

/**
 * One list of background transfers for the current profile: same-profile jobs started here
 * (cancelled through their abort controller) and server-side cross-profile transfers
 * (polled, cancelled through the API). Same-profile jobs outlive a profile change and are
 * only aborted on unmount.
 */
export function useTransferQueue(env: ActionEnvironment) {
  const { requests, request, translateApiError, reportError, refreshListing, beginSession } = env;
  const [sameProfile, setSameProfile] = useState<FileActionTransfer[]>([]);
  const [crossProfile, setCrossProfile] = useState<FileTransferSummary[]>([]);
  const listRequestRef = useRef<AbortController | null>(null);
  const controllersRef = useRef(new Map<string, AbortController>());
  const sameProfileIdsRef = useRef(new Set<string>());

  useEffect(() => {
    return () => listRequestRef.current?.abort();
  }, [request.profileId]);

  useEffect(() => {
    const controllers = controllersRef.current;
    return () => {
      controllers.forEach((controller) => controller.abort());
      controllers.clear();
    };
  }, []);

  const reload = useCallback(async () => {
    if (!request.profileId) return;
    listRequestRef.current?.abort();
    const controller = new AbortController();
    listRequestRef.current = controller;
    const result = await requests.listTransfers(controller.signal);
    if (controller.signal.aborted) return;
    if (result.ok) {
      setCrossProfile(result.data.transfers ?? []);
    }
  }, [request.profileId, requests]);

  useEffect(() => {
    const timer = window.setInterval(() => void reload(), CROSS_PROFILE_POLL_MS);
    const initialLoad = window.setTimeout(() => void reload(), 0);
    return () => {
      window.clearInterval(timer);
      window.clearTimeout(initialLoad);
    };
  }, [reload]);

  const patchSameProfile = useCallback((transferId: string, patch: Partial<FileActionTransfer>) => {
    setSameProfile((prev) => prev.map((item) => (item.id === transferId ? { ...item, ...patch } : item)));
  }, []);

  const enqueue = useCallback(
    (job: SameProfileTransferJob) => {
      const transferId = nextTransferID();
      sameProfileIdsRef.current.add(transferId);
      controllersRef.current.set(transferId, job.controller);
      setSameProfile((prev) => [
        ...prev,
        {
          id: transferId,
          origin: 'same-profile',
          operation: job.operation,
          status: 'running',
          sourceLabel: job.sourceLabel,
          destinationLabel: job.destinationLabel,
          itemCount: job.itemCount,
          processedCount: 0,
          failedCount: 0,
          skippedCount: 0,
        },
      ]);

      job
        .run()
        .then((outcome) => {
          controllersRef.current.delete(transferId);
          if (job.controller.signal.aborted) return;
          if (outcome.ok === false) {
            patchSameProfile(transferId, {
              status: 'failed',
              processedCount: outcome.processedCount,
              failedCount: outcome.failedCount,
              error: outcome.error,
            });
            if (outcome.refreshOnPartial) {
              void refreshListing();
            }
            return;
          }
          patchSameProfile(transferId, { status: 'completed', processedCount: job.itemCount });
          job.onSuccess?.();
          void refreshListing();
        })
        .catch(() => {
          controllersRef.current.delete(transferId);
          if (job.controller.signal.aborted) return;
          patchSameProfile(transferId, {
            status: 'failed',
            failedCount: job.itemCount,
            error: translateApiError(null),
          });
        });
    },
    [patchSameProfile, refreshListing, translateApiError],
  );

  const cancel = useCallback(
    async (transferId: string) => {
      const controller = controllersRef.current.get(transferId);
      if (controller) {
        controller.abort();
        controllersRef.current.delete(transferId);
        patchSameProfile(transferId, { status: 'cancelled' });
        return;
      }
      if (sameProfileIdsRef.current.has(transferId)) {
        return;
      }
      const isCurrent = beginSession();
      const result = await requests.cancelTransfer(transferId);
      if (!isCurrent()) return;
      if (result.ok === false) {
        reportError(translateApiError(result.errorCode));
      } else {
        void reload();
      }
    },
    [beginSession, patchSameProfile, reload, reportError, requests, translateApiError],
  );

  const dismiss = useCallback((transferId: string) => {
    sameProfileIdsRef.current.delete(transferId);
    setSameProfile((prev) => prev.filter((item) => item.id !== transferId));
    setCrossProfile((prev) => prev.filter((item) => item.id !== transferId));
  }, []);

  const transfers = useMemo<FileActionTransfer[]>(
    () => [...sameProfile, ...crossProfile.map(toCrossProfileTransfer)],
    [crossProfile, sameProfile],
  );

  return { transfers, enqueue, cancel, dismiss, reload };
}

export type TransferQueue = ReturnType<typeof useTransferQueue>;
