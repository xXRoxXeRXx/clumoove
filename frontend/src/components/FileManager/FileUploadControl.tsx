import { useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ArrowUpTrayIcon, FileIcon } from '../icons';
import { useTranslation } from 'react-i18next';
import type { FileCapabilities, UploadConflictStrategy } from '../../api/files';
import { useFormat } from '../../utils/format';
import { useFocusTrap } from '../../hooks/useFocusTrap';

type FileUploadControlProps = {
  capabilities: FileCapabilities;
  disabled?: boolean;
  /** The user confirmed these files and the conflict strategy; the caller owns the upload itself. */
  onUpload: (files: File[], strategy: UploadConflictStrategy) => void;
};

function availableStrategies(capabilities: FileCapabilities): UploadConflictStrategy[] {
  const strategies: UploadConflictStrategy[] = ['SKIP'];
  if (capabilities.conflict_overwrite) strategies.push('OVERWRITE');
  if (capabilities.conflict_rename) strategies.push('RENAME');
  return strategies;
}

/** Upload picker: file chooser, drop target and conflict-strategy dialog. Uploading is the caller's concern. */
export function FileUploadControl({ capabilities, disabled = false, onUpload }: FileUploadControlProps) {
  const { t } = useTranslation();
  const { formatBytes } = useFormat();
  const inputRef = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState<File[]>([]);
  const [strategy, setStrategy] = useState<UploadConflictStrategy>('SKIP');
  const dialogRef = useRef<HTMLDivElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const strategies = availableStrategies(capabilities);
  useFocusTrap(dialogRef, cancelRef, () => setPending([]), pending.length > 0);

  const isUploadDisabled = disabled || !capabilities.upload;

  const selectFiles = (files: FileList | File[]) => {
    if (isUploadDisabled) return;
    const selected = Array.from(files);
    if (selected.length) {
      setStrategy('SKIP');
      setPending(selected);
    }
  };

  const confirm = () => {
    onUpload(pending, strategy);
    setPending([]);
  };

  return (
    <>
      <div className="relative" onDragOver={(event) => event.preventDefault()} onDrop={(event) => { event.preventDefault(); if (!isUploadDisabled) selectFiles(event.dataTransfer.files); }}>
        <input ref={inputRef} type="file" multiple disabled={isUploadDisabled} className="sr-only" onChange={(event) => { selectFiles(event.target.files ?? []); event.currentTarget.value = ''; }} />
        <button
          type="button"
          onClick={() => inputRef.current?.click()}
          disabled={isUploadDisabled}
          title={!capabilities.upload ? t('files.uploadUnavailable') : t('files.upload')}
          aria-label={t('files.upload')}
          className="ui-button-secondary inline-flex items-center gap-2 px-3 py-2 text-sm disabled:cursor-not-allowed disabled:opacity-50"
        >
          <ArrowUpTrayIcon className="h-4 w-4" aria-hidden="true" />
          <span className="hidden sm:inline">{t('files.upload')}</span>
        </button>
      </div>

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
                <button type="button" onClick={confirm} className="ui-button-primary px-3 py-2 text-sm">
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
