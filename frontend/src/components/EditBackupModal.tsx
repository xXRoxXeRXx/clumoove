import React, { useState, useId, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { XMarkIcon as X, ArrowPathIcon as Spinner } from './icons';
import type { BackupJob } from '../types';
import { useFocusTrap } from '../hooks/useFocusTrap';
import { useApiError } from '../utils/apiError';
import { apiErrorMessage, apiJson } from '../utils/apiClient';
import { useToast } from '../contexts/useToast';
import { BackupOptionsForm } from './BackupOptionsForm';
import { Button } from './Button';

interface EditBackupModalProps {
  job: BackupJob;
  apiUrl: string;
  token: string;
  onClose: () => void;
  onSuccess: (updatedJob: BackupJob) => void;
}

export const EditBackupModal: React.FC<EditBackupModalProps> = ({
  job,
  apiUrl,
  token,
  onClose,
  onSuccess,
}) => {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const toast = useToast();
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();

  const [cronExpression, setCronExpression] = useState(job.cron_expression || '0 2 * * *');
  const [timezone, setTimezone] = useState(job.timezone || 'UTC');
  const [retentionCount, setRetentionCount] = useState(job.retention_count || 30);
  const [threads, setThreads] = useState(job.threads || 8);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useFocusTrap(dialogRef, closeButtonRef, onClose, true);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setError(null);

    try {
      const result = await apiJson<BackupJob>(`${apiUrl}/api/backup/${job.id}`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({
          cron_expression: cronExpression,
          timezone,
          retention_count: retentionCount,
          threads,
        }),
      });

      if (result.ok === false) {
        throw new Error(apiErrorMessage(result, translateApiError, t('backup.actionFailed')));
      }

      toast(t('backup.editSuccess'), 'success');
      onSuccess(result.data);
      onClose();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('backup.actionFailed');
      setError(msg);
      toast(msg, 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-[var(--layer-modal)] flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
    >
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="ui-card flex flex-col w-full max-w-2xl max-h-[90vh] overflow-hidden shadow-2xl border border-[var(--color-border)]"
      >
        {/* Header */}
        <div className="flex items-center justify-between p-4 border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)]">
          <h3 id={titleId} className="text-base font-semibold text-[var(--color-text-primary)]">
            {t('backup.editTitle')}
          </h3>
          <button
            ref={closeButtonRef}
            type="button"
            onClick={onClose}
            className="p-1 rounded-md text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] hover:bg-[var(--color-bg-tertiary)]"
            aria-label={t('paths.close')}
          >
            <X className="size-5" />
          </button>
        </div>

        {/* Form Body */}
        <form onSubmit={handleSave} className="flex flex-col flex-1 overflow-hidden">
          <div className="flex-1 overflow-y-auto p-4 space-y-4">
            {error && (
              <div className="ui-alert ui-alert-error p-3 text-xs" role="alert">
                {error}
              </div>
            )}

            <BackupOptionsForm
              cronExpression={cronExpression}
              setCronExpression={setCronExpression}
              timezone={timezone}
              setTimezone={setTimezone}
              retentionCount={retentionCount}
              setRetentionCount={setRetentionCount}
              threads={threads}
              setThreads={setThreads}
              error={error}
            />
          </div>

          {/* Footer Actions */}
          <div className="flex items-center justify-end gap-3 p-4 border-t border-[var(--color-border)] bg-[var(--color-bg-secondary)]">
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={onClose}
              disabled={saving}
            >
              {t('paths.close')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              size="sm"
              disabled={saving}
              className="flex items-center gap-2"
            >
              {saving ? (
                <>
                  <Spinner className="size-4 animate-spin" aria-hidden="true" />
                  <span>{t('common.saving')}</span>
                </>
              ) : (
                <span>{t('backup.editSave')}</span>
              )}
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
};
