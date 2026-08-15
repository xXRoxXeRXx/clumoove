import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  ArrowPathIcon,
  ArrowUpTrayIcon,
  CheckCircleIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  FileIcon,
  XMarkIcon,
} from '../icons';
import { useTranslation } from 'react-i18next';
import { uploadFile, type FileCapabilities, type UploadConflictStrategy } from '../../api/files';
import { useApiError } from '../../utils/apiError';
import { useFormat } from '../../utils/format';
import { useFocusTrap } from '../../hooks/useFocusTrap';

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

export function FileUploadControl({ apiUrl, token, profileId, parentRef, capabilities, disabled = false, onCompleted }: FileUploadControlProps) {
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

  useEffect(() => () => {
    controllers.current.forEach((controller) => controller.abort());
    if (backoffTimeoutRef.current !== null) {
      window.clearTimeout(backoffTimeoutRef.current);
    }
  }, []);

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
        // Trigger queue scheduling after a completion without polling the API.
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

  const clearCompleted = () => {
    setTasks((current) => current.filter((task) => task.status === 'uploading' || task.status === 'queued' || task.status === 'failed'));
  };

  const isUploadDisabled = disabled || !capabilities.upload;

  // Queue summary metrics
  const totalCount = tasks.length;
  const completedCount = tasks.filter((t) => t.status === 'uploaded' || t.status === 'skipped' || t.status === 'renamed').length;
  const inProgressCount = tasks.filter((t) => t.status === 'uploading' || t.status === 'queued').length;
  const failedCount = tasks.filter((t) => t.status === 'failed').length;

  const totalBytes = tasks.reduce((sum, t) => sum + Math.max(t.file.size, 1), 0);
  const loadedBytes = tasks.reduce((sum, t) => {
    if (t.status === 'uploaded' || t.status === 'skipped' || t.status === 'renamed') {
      return sum + Math.max(t.file.size, 1);
    }
    if (t.status === 'uploading') {
      return sum + Math.min(t.loaded, t.file.size);
    }
    return sum;
  }, 0);

  const overallPercent = totalBytes > 0 ? Math.min(100, Math.round((loadedBytes / totalBytes) * 100)) : 0;
  const isAllDone = totalCount > 0 && inProgressCount === 0;

  return (
    <>
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

      {tasks.length > 0 && (
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
              {isAllDone && (
                <button
                  type="button"
                  onClick={() => setTasks([])}
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
                  {isAllDone && (
                    <button
                      type="button"
                      onClick={() => setTasks([])}
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

              {/* Scrollable List of Files (Sized for ~10 visible items) */}
              <ul className="max-h-[380px] overflow-y-auto divide-y divide-[var(--color-border)]/50 p-2">
                {tasks.map((task) => (
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
                          ) : task.status === 'uploaded' ? (
                            <span className="text-xs font-medium text-[var(--color-success-text)] flex items-center gap-0.5">
                              <CheckCircleIcon className="h-3.5 w-3.5" aria-hidden="true" />
                              {t('files.uploadStatus.uploaded')}
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

                      {task.status === 'failed' && (
                        <span role="alert" className="text-xs text-[var(--color-error-text)] mt-0.5 truncate block">
                          {task.error}
                        </span>
                      )}
                    </div>

                    <div className="shrink-0">
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
                    </div>
                  </li>
                ))}
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

