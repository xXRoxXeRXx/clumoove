import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  ArrowPathIcon,
  ArrowUpTrayIcon,
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
  const [pending, setPending] = useState<File[]>([]);
  const [strategy, setStrategy] = useState<UploadConflictStrategy>('SKIP');
  const [tasks, setTasks] = useState<QueueTask[]>([]);
  const dialogRef = useRef<HTMLDivElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const strategies = availableStrategies(capabilities);
  useFocusTrap(dialogRef, cancelRef, () => setPending([]), pending.length > 0);

  useEffect(() => () => {
    controllers.current.forEach((controller) => controller.abort());
  }, []);

  useEffect(() => {
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

  const isUploadDisabled = disabled || !capabilities.upload;

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
        <section className="border-t border-[var(--color-border)] p-3" aria-label={t('files.uploadQueue')}>
          <ul className="space-y-2">
            {tasks.map((task) => (
              <li key={task.id} className="flex flex-wrap items-center gap-2 text-sm">
                <FileIcon name={task.file.name} mimeType={task.file.type} className="h-4 w-4 shrink-0" />
                <span className="min-w-0 flex-1 truncate">{task.file.name}</span>
                <span className="text-[var(--color-text-secondary)]">
                  {task.status === 'uploading' ? `${Math.round(task.loaded / Math.max(task.file.size, 1) * 100)}%` : t(`files.uploadStatus.${task.status}`)}
                </span>
                {task.status === 'failed' && <span role="alert" className="text-[var(--color-danger)]">{task.error}</span>}
                {(task.status === 'queued' || task.status === 'uploading') && (
                  <button type="button" onClick={() => cancelTask(task.id)} className="ui-icon-button p-1" aria-label={t('files.cancelUpload', { name: task.file.name })}>
                    <XMarkIcon className="h-4 w-4" aria-hidden="true" />
                  </button>
                )}
                {(task.status === 'failed' || task.status === 'cancelled') && (
                  <button type="button" onClick={() => retryTask(task.id)} className="ui-icon-button p-1" aria-label={t('common.retry')}>
                    <ArrowPathIcon className="h-4 w-4" aria-hidden="true" />
                  </button>
                )}
              </li>
            ))}
          </ul>
        </section>
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
