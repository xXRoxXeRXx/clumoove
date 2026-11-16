import { useCallback, useEffect, useMemo, useState } from 'react';
import type { BatchItemResult } from '../../../api/files';

export const BATCH_SUMMARY_DISMISS_MS = 4000;

/** Outcome of a finished batch operation, as shown to the user. */
export type BatchSummary = {
  success: number;
  failed: number;
};

/**
 * Status shared by every batch operation on the selection (archive download, batch delete,
 * cross-profile batch transfer): whether one is in flight and the last per-item outcome,
 * which dismisses itself after a few seconds.
 */
export function useBatchStatus() {
  const [busy, setBusy] = useState(false);
  const [results, setResults] = useState<BatchItemResult[]>([]);

  useEffect(() => {
    if (results.length === 0) return;
    const timer = window.setTimeout(() => setResults([]), BATCH_SUMMARY_DISMISS_MS);
    return () => window.clearTimeout(timer);
  }, [results]);

  const summary = useMemo<BatchSummary | null>(
    () =>
      results.length === 0
        ? null
        : {
            success: results.filter((item) => ['deleted', 'copied', 'moved'].includes(item.status)).length,
            failed: results.filter((item) => item.status === 'failed').length,
          },
    [results],
  );
  const dismissSummary = useCallback(() => setResults([]), []);

  return { busy, setBusy, summary, showResults: setResults, dismissSummary };
}

export type BatchStatus = ReturnType<typeof useBatchStatus>;
