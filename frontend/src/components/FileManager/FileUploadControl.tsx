import { useCallback, useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  ArrowPathIcon,
  ArrowRightIcon,
  ArrowUpTrayIcon,
  CheckCircleIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  ClipboardDocumentIcon,
  FileIcon,
  XMarkIcon,
} from '../icons';
import { useTranslation } from 'react-i18next';
import { uploadFile, type FileCapabilities, type FileTransferSummary, type UploadConflictStrategy } from '../../api/files';
import { useApiError } from '../../utils/apiError';
import { useFormat } from '../../utils/format';
import { useFocusTrap } from '../../hooks/useFocusTrap';

export type InternalTransfer = {
  id: string;
  operation: 'copy' | 'move';
  sourceName: string;
  destinationName: string;
  itemCount: number;
  status: 'queued' | 'copying' | 'moving' | 'copied' | 'moved' | 'failed' | 'cancelled';
  error?: string;
};

type QueueTask = {
  id: string;
  file: File;
  profileId: string;
  parentRef: string | null;
  strategy: UploadConflictStrategy;
  status: 'queued' | 'uploading' | 'uploaded' | 'skipped' | 'renamed' | 'failed' | 'cancelled';
  loaded: number;
  error?: string;
};

type FileUploadControlProps = {
  apiUrl: string;
  token: string;
  profileId: string;
  parentRef: string | null;
  capabilities: FileCapabilities;
  disabled?: boolean;
  onCompleted: (profileId: string) => void;
  backgroundTransfers?: FileTransferSummary[];
  onCancelBackgroundTransfer?: (transferId: string) => void;
  onDismissBackgroundTransfer?: (transferId: string) => void;
  internalTransfers?: InternalTransfer[];
  onCancelInternalTransfer?: (transferId: string) => void;
  onDismissInternalTransfer?: (transferId: string) => void;
};

function availableStrategies(capabilities: FileCapabilities): UploadConflictStrategy[] {
  const strategies: UploadConflictStrategy[] = ['SKIP'];
  if (capabilities.conflict_overwrite) strategies.push('OVERWRITE');
  if (capabilities.conflict_rename) strategies.push('RENAME');
  return strategies;
}

function nextTaskID(): string {
  return crypto.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
}

export function FileUploadControl({
  apiUrl,
  token,
  profileId,
  parentRef,
  capabilities,
  disabled = false,
  onCompleted,
  backgroundTransfers = [],
  onCancelBackgroundTransfer = () => undefined,
  onDismissBackgroundTransfer = () => undefined,
  internalTransfers = [],
  onCancelInternalTransfer = () => undefined,
  onDismissInternalTransfer = () => undefined,
}: FileUploadControlProps) {
  const { t } = useTranslation();
  const { formatBytes } = useFormat();
  const translateApiError = useApiError();
  const inputRef = useRef<HTMLInputElement>(null);
  const controllers = useRef(new Map<string, AbortController>());
  const running = useRef(new Set<string>());
  const backoffTimeoutRef = useRef<number | null>(null);
  const backoffUntilRef = useRef(0);
  const [pending, setPending] = useState<File[]>([]);
  const [strategy, setStrategy] = useState<UploadConflictStrategy>('SKIP');
  const [tasks, setTasks] = useState<QueueTask[]>([]);
  const [isExpanded, setIsExpanded] = useState(false);
  const dialogRef = useRef<HTMLDivElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const strategies = availableStrategies(capabilities);
  useFocusTrap(dialogRef, cancelRef, () => setPending([]), pending.length > 0);

  // Auto-dismiss tracking for completed items
  const [dismissedIds, setDismissedIds] = useState<Set<string>>(() => new Set());
  const dismissTimers = useRef(new Map<string, number>());

  const dismissItem = useCallback((id: string) => {
    const timer = dismissTimers.current.get(id);
    if (timer !== undefined) {
      window.clearTimeout(timer);
      dismissTimers.current.delete(id);
    }
    setDismissedIds((prev) => {
      const next = new Set(prev);
      next.add(id);
      return next;
    });
    setTasks((current) => current.filter((t) => t.id !== id));
    onDismissBackgroundTransfer(id);
    onDismissInternalTransfer(id);
  }, [onDismissBackgroundTransfer, onDismissInternalTransfer]);

  const scheduleDismiss = useCallback((id: string) => {
    if (dismissTimers.current.has(id)) return;
    const timer = window.setTimeout(() => {
      dismissTimers.current.delete(id);
      dismissItem(id);
    }, 4000);
    dismissTimers.current.set(id, timer);
  }, [dismissItem]);

  useEffect(() => {
    const timers = dismissTimers.current;
    const activeControllers = controllers.current;
    return () => {
      activeControllers.forEach((controller) => controller.abort());
      if (backoffTimeoutRef.current !== null) {
        window.clearTimeout(backoffTimeoutRef.current);
      }
      timers.forEach((timer) => window.clearTimeout(timer));
      timers.clear();
    };
  }, []);

  // Watch for completed tasks and schedule auto-dismissal
  useEffect(() => {
    for (const task of tasks) {
      if (!dismissedIds.has(task.id) && (task.status === 'uploaded' || task.status === 'skipped' || task.status === 'renamed')) {
        scheduleDismiss(task.id);
      }
    }
    for (const it of internalTransfers) {
      if (!dismissedIds.has(it.id) && (it.status === 'copied' || it.status === 'moved')) {
        scheduleDismiss(it.id);
      }
    }
    for (const bt of backgroundTransfers) {
      if (!dismissedIds.has(bt.id)) {
        const isDone = bt.status === 'COMPLETED' || (bt.total_files > 0 && bt.processed_files >= bt.total_files);
        if (isDone) {
          scheduleDismiss(bt.id);
        }
      }
    }
  }, [backgroundTransfers, dismissedIds, internalTransfers, scheduleDismiss, tasks]);

  useEffect(() => {
    if (backoffUntilRef.current > Date.now()) {
      const remaining = backoffUntilRef.current - Date.now();
      if (backoffTimeoutRef.current === null) {
        backoffTimeoutRef.current = window.setTimeout(() => {
          backoffTimeoutRef.current = null;
          setTasks((current) => [...current]);
        }, remaining);
      }
      return;
    }

    const slots = 4 - running.current.size;
    if (slots <= 0) return;
    const queued = tasks.filter((task) => task.status === 'queued' && !running.current.has(task.id)).slice(0, slots);
    for (const task of queued) {
      running.current.add(task.id);
      const controller = new AbortController();
      controllers.current.set(task.id, controller);
      setTasks((current) => current.map((item) => item.id === task.id ? { ...item, status: 'uploading' } : item));
      void uploadFile(apiUrl, token, task.profileId, task.file, task.parentRef, task.strategy, ({ loaded }) => {
        setTasks((current) => current.map((item) => item.id === task.id ? { ...item, loaded } : item));
      }, controller.signal).then((result) => {
        if (controller.signal.aborted) {
          setTasks((current) => current.map((item) => item.id === task.id ? { ...item, status: 'cancelled' } : item));
          return;
        }
        if (result.ok === false) {
          if (result.errorCode === 'RATE_LIMITED' || result.errorCode === 'FILES_STREAM_LIMIT_REACHED') {
            backoffUntilRef.current = Date.now() + 1500;
          }
          setTasks((current) => current.map((item) => item.id === task.id ? { ...item, status: 'failed', error: translateApiError(result.errorCode) } : item));
          return;
        }
        setTasks((current) => current.map((item) => item.id === task.id ? { ...item, status: result.data.status, loaded: item.file.size } : item));
        onCompleted(task.profileId);
      }).finally(() => {
        running.current.delete(task.id);
        controllers.current.delete(task.id);
        setTasks((current) => [...current]);
      });
    }
  }, [apiUrl, onCompleted, tasks, token, translateApiError]);

  const selectFiles = (files: FileList | File[]) => {
    if (disabled || !capabilities.upload) return;
    const selected = Array.from(files);
    if (selected.length) {
      setStrategy('SKIP');
      setPending(selected);
    }
  };

  const enqueue = () => {
    setTasks((current) => [...current, ...pending.map((file) => ({
      id: nextTaskID(), file, profileId, parentRef, strategy, status: 'queued' as const, loaded: 0,
    }))]);
    setPending([]);
  };

  const cancelTask = (id: string) => {
    const controller = controllers.current.get(id);
    if (controller) controller.abort();
    setTasks((current) => current.map((task) => task.id === id && task.status !== 'uploaded' && task.status !== 'skipped' && task.status !== 'renamed' ? { ...task, status: 'cancelled' } : task));
  };

  const retryTask = (id: string) => {
    setTasks((current) => current.map((task) => task.id === id ? { ...task, status: 'queued', loaded: 0, error: undefined } : task));
  };

  const isUploadDisabled = disabled || !capabilities.upload;

  // Filter visible items that haven't been dismissed
  const visibleTasks = tasks.filter((t) => !dismissedIds.has(t.id));
  const visibleInternalTransfers = internalTransfers.filter((it) => !dismissedIds.has(it.id));
  const isBackgroundTransferActive = (status: string) => ['PENDING', 'INDEXING', 'RUNNING', 'VERIFYING', 'PAUSED', 'PAUSED_CONNECTION_LOSS'].includes(status);
  const visibleBackgroundTransfers = backgroundTransfers.filter((bt) => !dismissedIds.has(bt.id));

  // Summary counts
  const completedUploadCount = visibleTasks.filter((t) => t.status === 'uploaded' || t.status === 'skipped' || t.status === 'renamed').length;
  const inProgressUploadCount = visibleTasks.filter((t) => t.status === 'uploading' || t.status === 'queued').length;

  const completedInternalCount = visibleInternalTransfers.filter((it) => it.status === 'copied' || it.status === 'moved').length;
  const inProgressInternalCount = visibleInternalTransfers.filter((it) => it.status === 'copying' || it.status === 'moving' || it.status === 'queued').length;

  const completedBackgroundCount = visibleBackgroundTransfers.filter((bt) => bt.status === 'COMPLETED' || (bt.total_files > 0 && bt.processed_files >= bt.total_files)).length;
  const inProgressBackgroundCount = visibleBackgroundTransfers.filter((bt) => isBackgroundTransferActive(bt.status) && bt.processed_files < bt.total_files).length;

  const totalCount = visibleTasks.length + visibleInternalTransfers.length + visibleBackgroundTransfers.length;
  const completedCount = completedUploadCount + completedInternalCount + completedBackgroundCount;
  const inProgressCount = inProgressUploadCount + inProgressInternalCount + inProgressBackgroundCount;
  const failedCount = visibleTasks.filter((t) => t.status === 'failed').length + visibleInternalTransfers.filter((it) => it.status === 'failed').length;

  // Overall percent calculation
  const totalBytes = visibleTasks.reduce((sum, t) => sum + Math.max(t.file.size, 1), 0);
  const loadedBytes = visibleTasks.reduce((sum, t) => {
    if (t.status === 'uploaded' || t.status === 'skipped' || t.status === 'renamed') {
      return sum + Math.max(t.file.size, 1);
    }
    if (t.status === 'uploading') {
      return sum + Math.min(t.loaded, t.file.size);
    }
    return sum;
  }, 0);

  const backgroundTotalFiles = visibleBackgroundTransfers.reduce((sum, transfer) => sum + transfer.total_files, 0);
  const backgroundProcessedFiles = visibleBackgroundTransfers.reduce((sum, transfer) => sum + transfer.processed_files, 0);

  const overallPercent = totalBytes > 0
    ? Math.min(100, Math.round((loadedBytes / totalBytes) * 100))
    : (backgroundTotalFiles + visibleInternalTransfers.length) > 0
      ? Math.min(100, Math.round(((backgroundProcessedFiles + completedInternalCount) / (backgroundTotalFiles + visibleInternalTransfers.length)) * 100))
      : completedCount > 0 ? 100 : 0;

  const canClear = totalCount > 0 && inProgressCount === 0;

  const clearCompleted = () => {
    visibleTasks.forEach((t) => {
      if (t.status === 'uploaded' || t.status === 'skipped' || t.status === 'renamed') dismissItem(t.id);
    });
    visibleInternalTransfers.forEach((it) => {
      if (it.status === 'copied' || it.status === 'moved') dismissItem(it.id);
    });
    visibleBackgroundTransfers.forEach((bt) => {
      if (bt.status === 'COMPLETED' || (bt.total_files > 0 && bt.processed_files >= bt.total_files)) dismissItem(bt.id);
    });
  };

  const clearAllQueue = () => {
    visibleTasks.forEach((t) => dismissItem(t.id));
    visibleInternalTransfers.forEach((it) => dismissItem(it.id));
    visibleBackgroundTransfers.forEach((bt) => dismissItem(bt.id));
  };

  return (
    <>
      <style>{`
        @keyframes transferCountdownDrain {
          from { width: 100%; }
          to { width: 0%; }
        }
      `}</style>
      <div className="relative" onDragOver={(event) => event.preventDefault()} onDrop={(event) => { event.preventDefault(); if (!isUploadDisabled) selectFiles(event.dataTransfer.files); }}>
        <input ref={inputRef} type="file" multiple disabled={isUploadDisabled} className="sr-only" onChange={(event) => { selectFiles(event.target.files); event.currentTarget.value = ''; }} />
        <button
          type="button"
          onClick={() => inputRef.current?.click()}
          disabled={isUploadDisabled}
          title={!capabilities.upload ? t('files.uploadUnavailable') : t('files.upload')}
          className="ui-button-secondary inline-flex items-center gap-2 px-3 py-2 text-sm disabled:cursor-not-allowed disabled:opacity-50"
        >
          <ArrowUpTrayIcon className="h-4 w-4" aria-hidden="true" />
          {t('files.upload')}
        </button>
      </div>

      {totalCount > 0 && (
        <aside aria-label={t('files.uploadQueue')}>
          {!isExpanded ? (
            /* Compact Floating Pill (Bottom Center) */
            <div className="fixed bottom-6 left-1/2 -translate-x-1/2 z-[var(--layer-toast)] flex items-center gap-3 rounded-full border border-[var(--color-border)] bg-[var(--color-bg-secondary)]/95 px-4 py-2.5 shadow-xl backdrop-blur-md transition-all duration-200 hover:border-[var(--color-text-secondary)] select-none">
              <button
                type="button"
                onClick={() => setIsExpanded(true)}
                className="flex items-center gap-3 text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-focus)] rounded-full"
                aria-expanded={false}
                aria-label={t('files.expandQueue')}
              >
                <div className="relative flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)]">
                  <ArrowUpTrayIcon className={`h-4 w-4 ${inProgressCount > 0 ? 'animate-pulse text-[var(--color-focus)]' : ''}`} aria-hidden="true" />
                </div>
                <div className="flex flex-col gap-0.5">
                  <div className="flex items-center gap-2 text-xs font-semibold text-[var(--color-text-primary)]">
                    <span>{completedCount}/{totalCount}</span>
                    <span className="text-[var(--color-text-secondary)] font-normal hidden sm:inline">
                      {t('files.uploadProgressSummary', { completed: completedCount, total: totalCount })}
                    </span>
                    <span className="font-mono text-[var(--color-text-secondary)]">({overallPercent}%)</span>
                  </div>
                  <div className="h-1.5 w-28 sm:w-36 rounded-full bg-[var(--color-progress-track)] overflow-hidden">
                    <div
                      className={`h-full transition-all duration-300 ${failedCount > 0 && inProgressCount === 0 ? 'bg-[var(--color-progress-error)]' : 'bg-[var(--color-progress-success)]'}`}
                      style={{ width: `${overallPercent}%` }}
                    />
                  </div>
                </div>
                <ChevronUpIcon className="h-4 w-4 shrink-0 text-[var(--color-text-secondary)]" aria-hidden="true" />
              </button>
              {canClear && (
                <button
                  type="button"
                  onClick={clearAllQueue}
                  className="ui-icon-button -mr-1 p-1 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                  aria-label={t('files.clearQueue')}
                  title={t('files.clearQueue')}
                >
                  <XMarkIcon className="h-4 w-4" aria-hidden="true" />
                </button>
              )}
            </div>
          ) : (
            /* Expanded Floating Card (Bottom Center) */
            <div className="fixed bottom-6 left-1/2 -translate-x-1/2 z-[var(--layer-toast)] w-[calc(100vw-2rem)] max-w-lg md:max-w-xl rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] shadow-2xl overflow-hidden flex flex-col">
              {/* Header */}
              <div className="flex items-center justify-between gap-2 border-b border-[var(--color-border)] px-4 py-3 bg-[var(--color-bg-secondary)]">
                <div className="flex items-center gap-2 min-w-0">
                  <ArrowUpTrayIcon className="h-4 w-4 shrink-0 text-[var(--color-text-primary)]" aria-hidden="true" />
                  <h2 className="text-sm font-semibold text-[var(--color-text-primary)] truncate">{t('files.uploadQueue')}</h2>
                  <span className="ui-badge text-xs px-2 py-0.5 font-medium bg-[var(--color-bg-tertiary)] text-[var(--color-text-secondary)]">
                    {completedCount}/{totalCount}
                  </span>
                </div>
                <div className="flex items-center gap-1">
                  {completedCount > 0 && inProgressCount > 0 && (
                    <button
                      type="button"
                      onClick={clearCompleted}
                      className="text-xs px-2 py-1 rounded text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
                      title={t('files.clearCompleted')}
                    >
                      {t('files.clearCompleted')}
                    </button>
                  )}
                  <button
                    type="button"
                    onClick={() => setIsExpanded(false)}
                    className="ui-icon-button p-1 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                    aria-label={t('files.minimizeQueue')}
                    title={t('files.minimizeQueue')}
                  >
                    <ChevronDownIcon className="h-4 w-4" aria-hidden="true" />
                  </button>
                  {canClear && (
                    <button
                      type="button"
                      onClick={clearAllQueue}
                      className="ui-icon-button p-1 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                      aria-label={t('files.clearQueue')}
                      title={t('files.clearQueue')}
                    >
                      <XMarkIcon className="h-4 w-4" aria-hidden="true" />
                    </button>
                  )}
                </div>
              </div>

              {/* Progress Line */}
              <div className="h-1 w-full bg-[var(--color-progress-track)] overflow-hidden">
                <div
                  className={`h-full transition-all duration-300 ${failedCount > 0 && inProgressCount === 0 ? 'bg-[var(--color-progress-error)]' : 'bg-[var(--color-progress-success)]'}`}
                  style={{ width: `${overallPercent}%` }}
                />
              </div>

              {/* Scrollable List of Transfers */}
              <ul className="max-h-[380px] overflow-y-auto divide-y divide-[var(--color-border)]/50 p-2">
                {/* 1. Same-provider internal copy/move transfers */}
                {visibleInternalTransfers.map((it) => {
                  const isDone = it.status === 'copied' || it.status === 'moved';
                  const isActive = it.status === 'copying' || it.status === 'moving' || it.status === 'queued';
                  const Icon = it.operation === 'move' ? ArrowRightIcon : ClipboardDocumentIcon;
                  return (
                    <li key={it.id} className="flex items-center gap-2.5 px-2 py-2 text-sm rounded-md hover:bg-[var(--color-hover)] transition-colors">
                      <div className="relative flex h-5 w-5 shrink-0 items-center justify-center">
                        {isDone ? (
                          <CheckCircleIcon className="h-5 w-5 text-[var(--color-success-text)]" aria-hidden="true" />
                        ) : (
                          <Icon className={`h-5 w-5 ${isActive ? 'animate-pulse text-[var(--color-info-text)]' : 'text-[var(--color-text-secondary)]'}`} aria-hidden="true" />
                        )}
                      </div>
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center justify-between gap-2">
                          <span className="truncate font-medium text-[var(--color-text-primary)] text-xs sm:text-sm">
                            {it.operation === 'move' ? t('files.move') : t('files.copy')}: {it.sourceName} → {it.destinationName}
                          </span>
                          <span className={`text-xs ${isDone ? 'text-[var(--color-success-text)] font-medium' : it.status === 'failed' ? 'text-[var(--color-error-text)] font-semibold' : 'text-[var(--color-info-text)]'}`}>
                            {t(`files.uploadStatus.${it.status}`)}
                          </span>
                        </div>
                        {isActive && (
                          <div className="h-1 w-full rounded-full bg-[var(--color-progress-track)] overflow-hidden mt-1.5">
                            <div className="h-full bg-[var(--color-info-text)] animate-pulse w-full" />
                          </div>
                        )}
                        {isDone && (
                          <div className="h-0.5 w-full rounded-full bg-[var(--color-progress-track)] overflow-hidden mt-1.5" aria-hidden="true">
                            <div
                              className="h-full bg-[var(--color-progress-success)]"
                              style={{ animation: 'transferCountdownDrain 4000ms linear forwards' }}
                            />
                          </div>
                        )}
                        {it.status === 'failed' && (
                          <span role="alert" className="text-xs text-[var(--color-error-text)] mt-0.5 truncate block">
                            {it.error}
                          </span>
                        )}
                      </div>
                      <div className="shrink-0">
                        {isActive ? (
                          <button
                            type="button"
                            onClick={() => onCancelInternalTransfer(it.id)}
                            className="ui-icon-button p-1 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-error-text)]"
                            aria-label={t('files.cancelTransfer')}
                            title={t('files.cancelTransfer')}
                          >
                            <XMarkIcon className="h-4 w-4" aria-hidden="true" />
                          </button>
                        ) : (
                          <button
                            type="button"
                            onClick={() => dismissItem(it.id)}
                            className="ui-icon-button p-1 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                            aria-label={t('files.dismissTransfer')}
                            title={t('files.dismissTransfer')}
                          >
                            <XMarkIcon className="h-4 w-4" aria-hidden="true" />
                          </button>
                        )}
                      </div>
                    </li>
                  );
                })}

                {/* 2. Cross-profile background transfers */}
                {visibleBackgroundTransfers.map((transfer) => {
                  const isDone = transfer.status === 'COMPLETED' || (transfer.total_files > 0 && transfer.processed_files >= transfer.total_files);
                  const active = !isDone && isBackgroundTransferActive(transfer.status);
                  const progress = transfer.total_files > 0 ? Math.min(100, Math.round((transfer.processed_files / transfer.total_files) * 100)) : 0;
                  return (
                    <li key={transfer.id} className="flex items-center gap-2.5 px-2 py-2 text-sm rounded-md hover:bg-[var(--color-hover)] transition-colors">
                      {isDone ? (
                        <CheckCircleIcon className="h-5 w-5 shrink-0 text-[var(--color-success-text)]" aria-hidden="true" />
                      ) : (
                        <ArrowPathIcon className={`h-5 w-5 shrink-0 ${active ? 'animate-spin text-[var(--color-info-text)]' : 'text-[var(--color-text-secondary)]'}`} aria-hidden="true" />
                      )}
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center justify-between gap-2">
                          <span className="truncate font-medium text-[var(--color-text-primary)] text-xs sm:text-sm">
                            {transfer.operation === 'move' ? t('files.move') : t('files.copy')}: {transfer.source_profile_name} → {transfer.target_profile_name}
                          </span>
                          <div className="flex items-center gap-2 shrink-0">
                            <span className="text-xs text-[var(--color-text-secondary)]">{transfer.processed_files}/{transfer.total_files}</span>
                            {isDone && (
                              <span className="text-xs font-medium text-[var(--color-success-text)]">
                                {t('files.uploadStatus.completed')}
                              </span>
                            )}
                          </div>
                        </div>
                        {active && (
                          <div className="h-1 w-full rounded-full bg-[var(--color-progress-track)] overflow-hidden mt-1.5">
                            <div className={`h-full transition-all duration-200 ${transfer.failed_files > 0 ? 'bg-[var(--color-progress-error)]' : 'bg-[var(--color-info-text)]'}`} style={{ width: `${progress}%` }} />
                          </div>
                        )}
                        {isDone && (
                          <div className="h-0.5 w-full rounded-full bg-[var(--color-progress-track)] overflow-hidden mt-1.5" aria-hidden="true">
                            <div
                              className="h-full bg-[var(--color-progress-success)]"
                              style={{ animation: 'transferCountdownDrain 4000ms linear forwards' }}
                            />
                          </div>
                        )}
                      </div>
                      <div className="shrink-0">
                        {active ? (
                          <button
                            type="button"
                            onClick={() => onCancelBackgroundTransfer(transfer.id)}
                            className="ui-icon-button p-1 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-error-text)]"
                            aria-label={t('files.cancelTransfer')}
                            title={t('files.cancelTransfer')}
                          >
                            <XMarkIcon className="h-4 w-4" aria-hidden="true" />
                          </button>
                        ) : (
                          <button
                            type="button"
                            onClick={() => dismissItem(transfer.id)}
                            className="ui-icon-button p-1 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                            aria-label={t('files.dismissTransfer')}
                            title={t('files.dismissTransfer')}
                          >
                            <XMarkIcon className="h-4 w-4" aria-hidden="true" />
                          </button>
                        )}
                      </div>
                    </li>
                  );
                })}

                {/* 3. Direct browser uploads */}
                {visibleTasks.map((task) => {
                  const isDone = task.status === 'uploaded' || task.status === 'skipped' || task.status === 'renamed';
                  return (
                    <li key={task.id} className="flex items-center gap-2.5 px-2 py-2 text-sm rounded-md hover:bg-[var(--color-hover)] transition-colors">
                      <FileIcon name={task.file.name} mimeType={task.file.type} className="h-5 w-5 shrink-0" />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center justify-between gap-2">
                          <span className="truncate font-medium text-[var(--color-text-primary)] text-xs sm:text-sm" title={task.file.name}>
                            {task.file.name}
                          </span>
                          <div className="flex items-center gap-2 shrink-0">
                            <span className="text-xs text-[var(--color-text-secondary)]">
                              {formatBytes(task.file.size)}
                            </span>
                            {task.status === 'uploading' ? (
                              <span className="text-xs font-semibold text-[var(--color-info-text)]">
                                {Math.round(task.loaded / Math.max(task.file.size, 1) * 100)}%
                              </span>
                            ) : isDone ? (
                              <span className="text-xs font-medium text-[var(--color-success-text)] flex items-center gap-0.5">
                                <CheckCircleIcon className="h-3.5 w-3.5" aria-hidden="true" />
                                {t(`files.uploadStatus.${task.status}`)}
                              </span>
                            ) : (
                              <span className={`text-xs ${task.status === 'failed' ? 'text-[var(--color-error-text)] font-semibold' : 'text-[var(--color-text-secondary)]'}`}>
                                {t(`files.uploadStatus.${task.status}`)}
                              </span>
                            )}
                          </div>
                        </div>

                        {task.status === 'uploading' && (
                          <div className="h-1 w-full rounded-full bg-[var(--color-progress-track)] overflow-hidden mt-1.5">
                            <div
                              className="h-full bg-[var(--color-info-text)] transition-all duration-200"
                              style={{ width: `${Math.round(task.loaded / Math.max(task.file.size, 1) * 100)}%` }}
                            />
                          </div>
                        )}

                        {isDone && (
                          <div className="h-0.5 w-full rounded-full bg-[var(--color-progress-track)] overflow-hidden mt-1.5" aria-hidden="true">
                            <div
                              className="h-full bg-[var(--color-progress-success)]"
                              style={{ animation: 'transferCountdownDrain 4000ms linear forwards' }}
                            />
                          </div>
                        )}

                        {task.status === 'failed' && (
                          <span role="alert" className="text-xs text-[var(--color-error-text)] mt-0.5 truncate block">
                            {task.error}
                          </span>
                        )}
                      </div>

                      <div className="shrink-0 flex items-center gap-1">
                        {(task.status === 'queued' || task.status === 'uploading') && (
                          <button
                            type="button"
                            onClick={() => cancelTask(task.id)}
                            className="ui-icon-button p-1 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-error-text)]"
                            aria-label={t('files.cancelUpload', { name: task.file.name })}
                            title={t('files.cancelUpload', { name: task.file.name })}
                          >
                            <XMarkIcon className="h-4 w-4" aria-hidden="true" />
                          </button>
                        )}
                        {(task.status === 'failed' || task.status === 'cancelled') && (
                          <button
                            type="button"
                            onClick={() => retryTask(task.id)}
                            className="ui-icon-button p-1 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                            aria-label={t('common.retry')}
                            title={t('common.retry')}
                          >
                            <ArrowPathIcon className="h-4 w-4" aria-hidden="true" />
                          </button>
                        )}
                        {(isDone || task.status === 'failed' || task.status === 'cancelled') && (
                          <button
                            type="button"
                            onClick={() => dismissItem(task.id)}
                            className="ui-icon-button p-1 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                            aria-label={t('files.dismissTransfer')}
                            title={t('files.dismissTransfer')}
                          >
                            <XMarkIcon className="h-4 w-4" aria-hidden="true" />
                          </button>
                        )}
                      </div>
                    </li>
                  );
                })}
              </ul>
            </div>
          )}
        </aside>
      )}

      {pending.length > 0 &&
        createPortal(
          <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
            <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby="upload-conflict-title" tabIndex={-1} className="ui-card w-full max-w-lg p-5">
              <h2 id="upload-conflict-title" className="text-lg font-semibold">{t('files.uploadConflictTitle')}</h2>
              <p className="mt-2 text-sm text-[var(--color-text-secondary)]">{t('files.uploadConflictDescription', { count: pending.length })}</p>
              <ul className="mt-3 max-h-32 overflow-auto text-sm">
                {pending.map((file, index) => (
                  <li key={`${file.name}-${file.lastModified}-${index}`} className="flex items-center gap-2 py-0.5">
                    <FileIcon name={file.name} mimeType={file.type} className="h-4 w-4 shrink-0" />
                    <span className="truncate">{file.name}</span>
                    <span className="text-[var(--color-text-secondary)] shrink-0">({formatBytes(file.size)})</span>
                  </li>
                ))}
              </ul>
              <label className="mt-4 block text-sm font-medium">
                {t('files.conflictStrategy')}
                <select value={strategy} onChange={(event) => setStrategy(event.target.value as UploadConflictStrategy)} className="ui-input mt-1 block w-full">
                  <option value="SKIP">{t('files.conflictSkip')}</option>
                  {strategies.includes('OVERWRITE') && <option value="OVERWRITE">{t('files.conflictOverwrite')}</option>}
                  {strategies.includes('RENAME') && <option value="RENAME">{t('files.conflictRename')}</option>}
                </select>
              </label>
              {strategy === 'OVERWRITE' && !capabilities.conflict_overwrite_atomic && (
                <p className="ui-alert mt-3 text-sm">{t('files.nonAtomicOverwriteWarning')}</p>
              )}
              <div className="mt-5 flex justify-end gap-2">
                <button ref={cancelRef} type="button" onClick={() => setPending([])} className="ui-button-secondary px-3 py-2 text-sm">
                  {t('common.cancel')}
                </button>
                <button type="button" onClick={enqueue} className="ui-button-primary px-3 py-2 text-sm">
                  {t('files.startUpload')}
                </button>
              </div>
            </div>
          </div>,
          document.body
        )}
    </>
  );
}
