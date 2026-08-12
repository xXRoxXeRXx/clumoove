import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { formatDuration } from '../utils/format';

const SPEED_WINDOW_MS = 15_000;

export type TransferMetricsInput = {
  status: string;
  processed_bytes?: number;
  live_bytes?: number;
  total_bytes?: number;
};

/**
 * Sliding-window transfer speed + ETA, shared by migration and sync dashboards.
 */
export function useTransferMetrics() {
  const { t } = useTranslation();
  const [speed, setSpeed] = useState(0);
  const [eta, setEta] = useState(() => t('dashboard.eta.computing'));
  const progressHistory = useRef<{ timestamp: number; bytes: number }[]>([]);
  const lastActiveSpeed = useRef(0);
  const lastActiveTime = useRef(0);
  const prevStatusRef = useRef('');

  const staticEtaForStatus = useCallback((status: string): string | undefined => {
    switch (status) {
      case 'COMPLETED':
      case 'COMPLETED_WITH_ERRORS':
        return t('dashboard.eta.done');
      case 'FAILED':
        return t('dashboard.eta.failed');
      case 'CANCELLED':
        return t('dashboard.eta.cancelled');
      case 'INDEXING':
        return t('dashboard.eta.indexing');
      case 'PENDING':
      case 'SCHEDULED':
        return t('dashboard.eta.pending');
      case 'VERIFYING':
        return t('dashboard.eta.verifying');
      case 'PAUSED':
      case 'IDLE':
        return '-';
      case 'PAUSED_CONNECTION_LOSS':
        return t('dashboard.eta.waitingConn');
      default:
        return undefined;
    }
  }, [t]);

  // Static labels must update when the user changes language even if the job
  // no longer emits progress updates.
  useEffect(() => {
    const staticEta = staticEtaForStatus(prevStatusRef.current);
    if (staticEta !== undefined) {
      setSpeed(0);
      setEta(staticEta);
    }
  }, [staticEtaForStatus]);

  const reset = useCallback(() => {
    progressHistory.current = [];
    lastActiveSpeed.current = 0;
    lastActiveTime.current = 0;
    prevStatusRef.current = '';
    setSpeed(0);
    setEta(t('dashboard.eta.computing'));
  }, [t]);

  const updateMetrics = useCallback(
    (data: TransferMetricsInput) => {
      if (data.status !== prevStatusRef.current) {
        progressHistory.current = [];
        lastActiveSpeed.current = 0;
        lastActiveTime.current = 0;
      }
      prevStatusRef.current = data.status;

      const staticEta = staticEtaForStatus(data.status);
      if (staticEta !== undefined) {
        setSpeed(0);
        setEta(staticEta);
        return;
      }

      const processedBytes = data.processed_bytes || 0;
      const liveBytes = typeof data.live_bytes === 'number' ? data.live_bytes : processedBytes;
      const totalBytes = data.total_bytes || 0;
      const now = Date.now();

      progressHistory.current.push({ timestamp: now, bytes: liveBytes });
      const windowLimit = now - SPEED_WINDOW_MS;
      progressHistory.current = progressHistory.current.filter((item) => item.timestamp >= windowLimit);

      if (progressHistory.current.length < 2) {
        setSpeed(0);
        setEta(t('dashboard.eta.computing'));
        return;
      }

      const oldest = progressHistory.current[0];
      const newest = progressHistory.current[progressHistory.current.length - 1];
      const bytesDiff = newest.bytes - oldest.bytes;
      if (bytesDiff < 0) {
        progressHistory.current = [{ timestamp: now, bytes: liveBytes }];
        lastActiveSpeed.current = 0;
        lastActiveTime.current = 0;
        setSpeed(0);
        setEta(t('dashboard.eta.computing'));
        return;
      }

      const timeDiffSec = (newest.timestamp - oldest.timestamp) / 1000;
      if (timeDiffSec <= 0.5) {
        return;
      }

      let calculatedSpeed: number;
      if (bytesDiff > 0) {
        calculatedSpeed = bytesDiff / timeDiffSec;
        lastActiveSpeed.current = calculatedSpeed;
        lastActiveTime.current = now;
      } else {
        const timeSinceLastActive = now - lastActiveTime.current;
        if (lastActiveSpeed.current > 0 && timeSinceLastActive < SPEED_WINDOW_MS) {
          calculatedSpeed = lastActiveSpeed.current;
        } else {
          calculatedSpeed = 0;
        }
      }

      setSpeed(calculatedSpeed);

      const effectiveBytes = Math.min(totalBytes, Math.max(processedBytes, liveBytes));
      const remainingBytes = Math.max(0, totalBytes - effectiveBytes);
      if (remainingBytes <= 0 && totalBytes > 0) {
        setEta(t('dashboard.eta.done'));
      } else if (calculatedSpeed > 0 && totalBytes > 0) {
        setEta(formatDuration(remainingBytes / calculatedSpeed, t));
      } else {
        setEta(t('dashboard.eta.computing'));
      }
    },
    [staticEtaForStatus, t],
  );

  return { speed, eta, updateMetrics, reset, prevStatusRef };
}
