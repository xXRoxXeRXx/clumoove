import { useCallback, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { formatDuration } from '../utils/format';

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

      if (data.status === 'COMPLETED' || data.status === 'COMPLETED_WITH_ERRORS') {
        setSpeed(0);
        setEta(t('dashboard.eta.done'));
        return;
      }
      if (data.status === 'FAILED') {
        setSpeed(0);
        setEta(t('dashboard.eta.failed'));
        return;
      }
      if (data.status === 'INDEXING') {
        setSpeed(0);
        setEta(t('dashboard.eta.indexing'));
        return;
      }
      if (data.status === 'PENDING') {
        setSpeed(0);
        setEta(t('dashboard.eta.pending'));
        return;
      }
      if (data.status === 'PAUSED' || data.status === 'IDLE') {
        setSpeed(0);
        setEta('-');
        return;
      }
      if (data.status === 'PAUSED_CONNECTION_LOSS') {
        setSpeed(0);
        setEta(t('dashboard.eta.waitingConn'));
        return;
      }

      const processedBytes = data.processed_bytes || 0;
      const liveBytes = typeof data.live_bytes === 'number' ? data.live_bytes : processedBytes;
      const totalBytes = data.total_bytes || 0;
      const now = Date.now();

      progressHistory.current.push({ timestamp: now, bytes: liveBytes });
      const windowLimit = now - 15000;
      progressHistory.current = progressHistory.current.filter((item) => item.timestamp >= windowLimit);

      if (progressHistory.current.length < 2) {
        setSpeed(0);
        setEta(t('dashboard.eta.computing'));
        return;
      }

      const oldest = progressHistory.current[0];
      const newest = progressHistory.current[progressHistory.current.length - 1];
      const timeDiffSec = (newest.timestamp - oldest.timestamp) / 1000;
      if (timeDiffSec <= 0.5) {
        return;
      }

      const bytesDiff = newest.bytes - oldest.bytes;
      let calculatedSpeed: number;
      if (bytesDiff > 0) {
        calculatedSpeed = bytesDiff / timeDiffSec;
        lastActiveSpeed.current = calculatedSpeed;
        lastActiveTime.current = now;
      } else {
        const timeSinceLastActive = now - lastActiveTime.current;
        if (lastActiveSpeed.current > 0 && timeSinceLastActive < 15000) {
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
    [t],
  );

  return { speed, eta, updateMetrics, reset, prevStatusRef };
}
