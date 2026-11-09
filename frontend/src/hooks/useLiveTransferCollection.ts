import { useCallback, useEffect, useRef, useState, type SetStateAction } from 'react';
import { useTranslation } from 'react-i18next';
import { useApiError } from '../utils/apiError';
import { apiErrorMessage, apiFetch, apiJson } from '../utils/apiClient';
import { connectSseLoop } from '../utils/sse';

interface UseLiveTransferCollectionOptions {
  apiUrl: string;
  token: string;
  snapshotPath: string;
  streamUrl: string;
  eventName: string;
  snapshotFailureKey: string;
  unknownSnapshotFailureKey?: string;
  streamFailureKey: string;
  includeNamedStreamErrors?: boolean;
}

interface LiveTransferCollection<T> {
  items: T[];
  loading: boolean;
  error: string;
  refresh: () => void;
  updateItems: (update: SetStateAction<T[]>) => void;
}

/**
 * Owns one transfer feed's snapshot/stream lifecycle. A refresh starts a new
 * ordering epoch: its snapshot may replace an older stream frame, but any
 * stream frame received afterwards wins over that snapshot.
 */
export function useLiveTransferCollection<T>({
  apiUrl,
  token,
  snapshotPath,
  streamUrl,
  eventName,
  snapshotFailureKey,
  unknownSnapshotFailureKey = snapshotFailureKey,
  streamFailureKey,
  includeNamedStreamErrors = false,
}: UseLiveTransferCollectionOptions): LiveTransferCollection<T> {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const [items, setItems] = useState<T[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [refreshEpoch, setRefreshEpoch] = useState(0);
  const connectionRef = useRef({ apiUrl, token });
  const connectionGenerationRef = useRef(0);
  const updateEpochRef = useRef(0);
  const hasDataRef = useRef(false);
  const snapshotControllerRef = useRef<AbortController | null>(null);
  const tRef = useRef(t);
  const translateApiErrorRef = useRef(translateApiError);

  useEffect(() => {
    tRef.current = t;
    translateApiErrorRef.current = translateApiError;
  }, [t, translateApiError]);

  useEffect(() => {
    const connectionChanged = connectionRef.current.apiUrl !== apiUrl || connectionRef.current.token !== token;
    if (connectionChanged) {
      connectionRef.current = { apiUrl, token };
      connectionGenerationRef.current += 1;
      hasDataRef.current = false;
      updateEpochRef.current = 0;
      queueMicrotask(() => {
        if (connectionRef.current.apiUrl === apiUrl && connectionRef.current.token === token) {
          setItems([]);
          setLoading(true);
          setError('');
        }
      });
    }
    const generation = connectionGenerationRef.current;
    const snapshotEpoch = updateEpochRef.current + 1;
    updateEpochRef.current = snapshotEpoch;
    const controller = new AbortController();
    snapshotControllerRef.current?.abort();
    snapshotControllerRef.current = controller;
    if (!hasDataRef.current) {
      setLoading(true);
      setError('');
    }

    void (async () => {
      try {
        const result = await apiJson<T[]>(`${apiUrl}${snapshotPath}`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: controller.signal,
        });
        if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiErrorRef.current, tRef.current(snapshotFailureKey)));
        if (controller.signal.aborted || connectionGenerationRef.current !== generation || updateEpochRef.current !== snapshotEpoch) return;
        hasDataRef.current = true;
        setItems(result.data || []);
        setError('');
        setLoading(false);
      } catch (cause: unknown) {
        if (controller.signal.aborted || connectionGenerationRef.current !== generation || updateEpochRef.current !== snapshotEpoch) return;
        setError(cause instanceof Error ? cause.message : tRef.current(unknownSnapshotFailureKey));
        setLoading(false);
      }
    })();

    return () => {
      controller.abort();
      if (snapshotControllerRef.current === controller) snapshotControllerRef.current = null;
    };
  }, [apiUrl, token, snapshotPath, snapshotFailureKey, unknownSnapshotFailureKey, refreshEpoch]);

  useEffect(() => {
    const generation = connectionGenerationRef.current;
    const streamController = new AbortController();
    void connectSseLoop({
      url: streamUrl,
      signal: streamController.signal,
      fetchImpl: apiFetch,
      handlers: {
        onEvent: (event, data) => {
          if (streamController.signal.aborted || connectionGenerationRef.current !== generation) return;
          if (event === eventName && data) {
            try {
              const updatedItems = JSON.parse(data) as T[];
              if (!Array.isArray(updatedItems)) return;
              updateEpochRef.current += 1;
              snapshotControllerRef.current?.abort();
              hasDataRef.current = true;
              setItems(updatedItems);
              setError('');
              setLoading(false);
            } catch {
              // Ignore malformed stream frames and retain the last stable state.
            }
          } else if (includeNamedStreamErrors && event === 'error') {
            setError(tRef.current(streamFailureKey));
            setLoading(false);
          }
        },
        onError: () => {
          if (streamController.signal.aborted || connectionGenerationRef.current !== generation) return;
          setError(tRef.current(streamFailureKey));
          setLoading(false);
        },
      },
    });

    return () => streamController.abort();
  }, [eventName, includeNamedStreamErrors, streamFailureKey, streamUrl, token]);

  const refresh = useCallback(() => setRefreshEpoch((epoch) => epoch + 1), []);
  const updateItems = useCallback((update: SetStateAction<T[]>) => setItems(update), []);
  return { items, loading, error, refresh, updateItems };
}
