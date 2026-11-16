import { useCallback, useRef } from 'react';
import type { FileCapabilities, FileEntry } from '../../../api/files';
import { useRequestSlot, type ActionEnvironment } from './actionEnvironment';
import type { BatchStatus } from './useBatchStatus';

type Options = {
  capabilities: FileCapabilities;
  canDownloadArchive: boolean;
  selectedEntries: FileEntry[];
  batch: BatchStatus;
  navigateToDownload?: (url: string) => void;
};

/** Single-file downloads and ZIP archives of the selection, both via short-lived download tickets. */
export function useDownloads(
  env: ActionEnvironment,
  { capabilities, canDownloadArchive, selectedEntries, batch, navigateToDownload }: Options,
) {
  const { requests, request, translateApiError, reportError, clearError, beginSession } = env;
  const downloadingRef = useRef<string | null>(null);
  const { begin: beginRequest } = useRequestSlot(request.profileId);

  const triggerDownloadUrl = useCallback(
    (downloadUrl: string) => {
      const resolvedUrl = new URL(downloadUrl, request.apiUrl).toString();
      if (navigateToDownload) {
        navigateToDownload(resolvedUrl);
      } else {
        window.location.assign(resolvedUrl);
      }
    },
    [navigateToDownload, request.apiUrl],
  );

  const download = useCallback(
    async (entry: FileEntry) => {
      if (entry.kind === 'directory' || !capabilities.download || downloadingRef.current) return;
      const controller = beginRequest();
      downloadingRef.current = entry.ref;
      clearError();
      const result = await requests.downloadTicket(entry.ref, controller.signal);
      if (downloadingRef.current === entry.ref) {
        downloadingRef.current = null;
      }
      if (controller.signal.aborted) return;
      if (result.ok === false) {
        reportError(translateApiError(result.errorCode));
      } else {
        triggerDownloadUrl(result.data.download_url);
      }
    },
    [beginRequest, capabilities.download, clearError, reportError, requests, translateApiError, triggerDownloadUrl],
  );

  const downloadArchive = useCallback(async () => {
    if (!canDownloadArchive || batch.busy) return;
    const isCurrent = beginSession();
    batch.setBusy(true);
    clearError();
    batch.dismissSummary();
    const result = await requests.archiveTicket(selectedEntries.map((entry) => entry.ref));
    if (!isCurrent()) return;
    if (result.ok === false) {
      reportError(translateApiError(result.errorCode));
    } else {
      triggerDownloadUrl(result.data.download_url);
    }
    batch.setBusy(false);
  }, [batch, beginSession, canDownloadArchive, clearError, reportError, requests, selectedEntries, translateApiError, triggerDownloadUrl]);

  return { download, downloadArchive };
}
