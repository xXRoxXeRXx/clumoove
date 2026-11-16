import { useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { LoadingIndicator } from '../../LoadingIndicator';
import type {
  BatchDeleteDialogModel,
  ConflictDialogModel,
  CreateFolderDialogModel,
  DeleteDialogModel,
  RenameDialogModel,
} from './dialogModels';
import { ModalDialog } from './ModalDialog';

export function CreateFolderDialog({ model }: { model: CreateFolderDialogModel }) {
  const { t } = useTranslation();
  const cancelRef = useRef<HTMLButtonElement>(null);

  return (
    <ModalDialog
      labelledBy="create-dir-title"
      initialFocusRef={cancelRef}
      onEscape={model.close}
      className="ui-card w-full max-w-md p-5"
    >
      <h2 id="create-dir-title" className="text-lg font-semibold text-[var(--color-text-primary)]">
        {t('files.newFolder')}
      </h2>
      {model.error && (
        <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">
          {model.error}
        </p>
      )}
      <form onSubmit={(event) => { event.preventDefault(); void model.submit(); }} className="mt-4 space-y-4">
        <div>
          <label htmlFor="new-folder-name-input" className="block text-xs font-semibold text-[var(--color-text-secondary)] uppercase tracking-wider mb-1">
            {t('files.folderName')}
          </label>
          <input
            id="new-folder-name-input"
            type="text"
            value={model.name}
            onChange={(e) => model.setName(e.target.value)}
            placeholder={t('files.folderNamePlaceholder')}
            disabled={model.busy}
            className="ui-input w-full py-2 px-3 text-sm"
            autoFocus
          />
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <button
            ref={cancelRef}
            type="button"
            onClick={model.close}
            disabled={model.busy}
            className="ui-button-secondary px-3 py-2 text-sm"
          >
            {t('common.cancel')}
          </button>
          <button
            type="submit"
            disabled={!model.name.trim() || model.busy}
            className="ui-button-primary inline-flex items-center gap-2 px-3 py-2 text-sm disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {model.busy ? (
              <>
                <LoadingIndicator label={t('common.loading')} size="sm" />
                <span>{t('common.loading')}</span>
              </>
            ) : (
              <span>{t('common.create')}</span>
            )}
          </button>
        </div>
      </form>
    </ModalDialog>
  );
}

export function DeleteDialog({ model }: { model: DeleteDialogModel }) {
  const { t } = useTranslation();
  const cancelRef = useRef<HTMLButtonElement>(null);
  const { entry } = model;
  const warning =
    entry.kind === 'file'
      ? t('files.deleteFileWarning', { name: entry.name })
      : model.recursive
        ? t('files.deleteRecursiveWarning', { name: entry.name })
        : t('files.deleteDirectoryWarning', { name: entry.name });

  return (
    <ModalDialog
      labelledBy="delete-entry-title"
      initialFocusRef={cancelRef}
      onEscape={model.close}
      className="ui-card w-full max-w-md p-5"
    >
      <h2 id="delete-entry-title" className="text-lg font-semibold text-[var(--color-text-primary)]">{t('files.deleteTitle')}</h2>
      <p className="mt-3 text-sm text-[var(--color-text-secondary)]">{warning}</p>
      {model.error && <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">{model.error}</p>}
      {model.onlyRecursiveSupported && <p className="mt-3 text-sm text-[var(--color-text-secondary)]">{t('files.deleteOnlyRecursiveSupported')}</p>}
      <div className="mt-5 flex justify-end gap-2">
        <button ref={cancelRef} type="button" onClick={model.close} disabled={model.busy} className="ui-button-secondary px-3 py-2 text-sm">{t('common.cancel')}</button>
        <button type="button" onClick={() => void model.confirm()} disabled={model.busy || !model.canConfirm} className="ui-button-danger px-3 py-2 text-sm disabled:cursor-not-allowed disabled:opacity-50">
          {model.busy ? t('common.loading') : model.recursive ? t('files.deleteRecursively') : t('files.deleteConfirm')}
        </button>
      </div>
    </ModalDialog>
  );
}

export function BatchDeleteDialog({ model }: { model: BatchDeleteDialogModel }) {
  const { t } = useTranslation();
  const cancelRef = useRef<HTMLButtonElement>(null);

  return (
    <ModalDialog
      labelledBy="batch-delete-title"
      initialFocusRef={cancelRef}
      onEscape={model.close}
      className="ui-card w-full max-w-md p-5"
    >
      <h2 id="batch-delete-title" className="text-lg font-semibold">{t('files.batchDeleteTitle')}</h2>
      <p className="mt-3 text-sm text-[var(--color-text-secondary)]">{t('files.batchDeleteWarning', { count: model.itemCount })}</p>
      {model.hasDirectories && <p className="mt-2 text-sm text-[var(--color-text-secondary)]">{t('files.batchDeleteRecursive')}</p>}
      <div className="mt-5 flex justify-end gap-2">
        <button ref={cancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={model.busy} onClick={model.close}>{t('common.cancel')}</button>
        <button type="button" className="ui-button-danger px-3 py-2 text-sm" disabled={model.busy} onClick={() => void model.confirm()}>{model.busy ? t('common.loading') : t('files.deleteRecursively')}</button>
      </div>
    </ModalDialog>
  );
}

function ConflictDialog({ model }: { model: ConflictDialogModel }) {
  const { t } = useTranslation();
  const cancelRef = useRef<HTMLButtonElement>(null);

  return (
    <ModalDialog
      labelledBy="mutation-conflict-title"
      initialFocusRef={cancelRef}
      onEscape={model.cancel}
      className="ui-card w-full max-w-md p-5"
    >
      <h2 id="mutation-conflict-title" className="text-lg font-semibold">{t('files.conflictTitle')}</h2>
      <p className="mt-2 text-sm text-[var(--color-text-secondary)]">{t('files.conflictDescription')}</p>
      <div className="mt-4 flex flex-wrap justify-end gap-2">
        <button ref={cancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={model.busy} onClick={model.cancel}>{t('common.cancel')}</button>
        {model.strategies.map((strategy) => (
          <button key={strategy} type="button" className="ui-button-primary px-3 py-2 text-sm" disabled={model.busy} onClick={() => void model.resolve(strategy)}>
            {t(`files.conflict${strategy[0]}${strategy.slice(1).toLowerCase()}`)}
          </button>
        ))}
      </div>
    </ModalDialog>
  );
}

/** The rename dialog, with the conflict-strategy dialog stacked on top when the new name is taken. */
export function RenameDialog({ model }: { model: RenameDialogModel }) {
  const { t } = useTranslation();
  const cancelRef = useRef<HTMLButtonElement>(null);

  return (
    <>
      <ModalDialog
        labelledBy="rename-entry-title"
        initialFocusRef={cancelRef}
        onEscape={model.close}
        className="ui-card w-full max-w-md p-5"
      >
        <h2 id="rename-entry-title" className="text-lg font-semibold">{t('files.renameTitle')}</h2>
        {model.error && <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">{model.error}</p>}
        <form className="mt-4 space-y-4" onSubmit={(event) => { event.preventDefault(); void model.submit(); }}>
          <label className="block text-sm font-medium" htmlFor="rename-entry-name">{t('files.name')}</label>
          <input id="rename-entry-name" className="ui-input w-full px-3 py-2" value={model.name} disabled={model.busy} onChange={(event) => model.setName(event.target.value)} autoFocus />
          <div className="flex justify-end gap-2">
            <button ref={cancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={model.busy} onClick={model.close}>{t('common.cancel')}</button>
            <button type="submit" className="ui-button-primary px-3 py-2 text-sm" disabled={!model.name.trim() || model.busy}>{model.busy ? t('files.mutating') : t('files.rename')}</button>
          </div>
        </form>
      </ModalDialog>
      {model.conflict && <ConflictDialog model={model.conflict} />}
    </>
  );
}
