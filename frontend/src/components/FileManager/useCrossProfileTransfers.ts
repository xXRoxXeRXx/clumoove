import { useCallback, useEffect, useRef, useState } from 'react';
import { cancelCrossProfileFileTransfer, listCrossProfileFileTransfers, type FileTransferSummary } from '../../api/files';

type UseCrossProfileTransfersOptions = {
  apiUrl: string;
  token: string;
  profileId: string;
  onError: (errorCode: string) => void;
};

/** Owns the remote transfer queue and its lifecycle independently from entry mutations. */
export function useCrossProfileTransfers({ apiUrl, token, profileId, onError }: UseCrossProfileTransfersOptions) {
  const [transfers, setTransfers] = useState<FileTransferSummary[]>([]);
  const loadingRef = useRef(false);

  const loadTransfers = useCallback(async () => {
    if (!profileId || loadingRef.current) return;
    loadingRef.current = true;
    try {
      const result = await listCrossProfileFileTransfers(apiUrl, token, profileId);
      if (result.ok) setTransfers(result.data.transfers ?? []);
    } finally {
      loadingRef.current = false;
    }
  }, [apiUrl, profileId, token]);

  useEffect(() => {
    const timer = window.setInterval(() => void loadTransfers(), 5000);
    const initialLoad = window.setTimeout(() => void loadTransfers(), 0);
    return () => {
      window.clearInterval(timer);
      window.clearTimeout(initialLoad);
    };
  }, [loadTransfers]);

  const cancelTransfer = useCallback(async (transferId: string) => {
    const result = await cancelCrossProfileFileTransfer(apiUrl, token, transferId);
    if (result.ok === false) onError(result.errorCode);
    else void loadTransfers();
  }, [apiUrl, loadTransfers, onError, token]);

  const dismissTransfer = useCallback((transferId: string) => {
    setTransfers((current) => current.filter((transfer) => transfer.id !== transferId));
  }, []);

  return { transfers, loadTransfers, cancelTransfer, dismissTransfer };
}
