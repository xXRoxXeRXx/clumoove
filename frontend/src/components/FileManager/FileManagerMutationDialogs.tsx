import type { RefObject } from 'react';
import { createPortal } from 'react-dom';
import { FolderIcon } from '../icons';
import { LoadingIndicator } from '../LoadingIndicator';
import type { ConnectionProfilePublic } from '../../api/profiles';
import type { FileEntry, FileMutationConflictStrategy } from '../../api/files';
import type { FileManagerBreadcrumb } from './FileManagerTypes';

type Translate = (key: string, values?: Record<string, unknown>) => string;

type DestinationPicker = {
  profileId: string;
  breadcrumbs: FileManagerBreadcrumb[];
  entries: FileEntry[];
  loading: boolean;
  onProfileChange: (profileId: string) => void;
  onBreadcrumbSelect: (index: number) => void;
  onDirectorySelect: (entry: FileEntry) => void;
};

type DialogRefs = {
  rename: RefObject<HTMLDivElement | null>;
  renameCancel: RefObject<HTMLButtonElement | null>;
  picker: RefObject<HTMLDivElement | null>;
  pickerCancel: RefObject<HTMLButtonElement | null>;
  conflict: RefObject<HTMLDivElement | null>;
  conflictCancel: RefObject<HTMLButtonElement | null>;
};

export type FileManagerMutationDialogsProps = {
  t: Translate;
  profiles: ConnectionProfilePublic[];
  selectedEntries: FileEntry[];
  renameEntry: FileEntry | null;
  renameName: string;
  mutationEntry: FileEntry | null;
  mutationOperation: 'copy' | 'move' | null;
  batchOperation: 'copy' | 'move' | null;
  batchConflictRefs: string[];
  conflictStrategies: FileMutationConflictStrategy[] | null;
  mutationError: string;
  mutationBusy: boolean;
  batchBusy: boolean;
  destinationPicker: DestinationPicker;
  dialogRefs: DialogRefs;
  onRenameNameChange: (name: string) => void;
  onClose: () => void;
  onExecuteMutation: (strategy?: FileMutationConflictStrategy) => void;
  onExecuteBatchMutation: (strategy?: FileMutationConflictStrategy) => void;
};

/** Keeps the rename, destination-picking, and conflict-resolution workflow local to FileManager. */
export function FileManagerMutationDialogs({
  t, profiles, selectedEntries, renameEntry, renameName, mutationEntry, mutationOperation,
  batchOperation, batchConflictRefs, conflictStrategies, mutationError, mutationBusy, batchBusy,
  destinationPicker,
  dialogRefs: {
    rename: renameDialogRef,
    renameCancel: renameCancelRef,
    picker: pickerDialogRef,
    pickerCancel: pickerCancelRef,
    conflict: conflictDialogRef,
    conflictCancel: conflictCancelRef,
  },
  onRenameNameChange, onClose, onExecuteMutation, onExecuteBatchMutation,
}: FileManagerMutationDialogsProps) {
  const operation = mutationOperation ?? batchOperation;
  const isBusy = mutationBusy || batchBusy;

  return <>
    {renameEntry && createPortal(
      <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
        <div ref={renameDialogRef} role="dialog" aria-modal="true" aria-labelledby="rename-entry-title" tabIndex={-1} className="ui-card w-full max-w-md p-5">
          <h2 id="rename-entry-title" className="text-lg font-semibold">{t('files.renameTitle')}</h2>
          {mutationError && <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">{mutationError}</p>}
          <form className="mt-4 space-y-4" onSubmit={(event) => { event.preventDefault(); onExecuteMutation(); }}>
            <label className="block text-sm font-medium" htmlFor="rename-entry-name">{t('files.name')}</label>
            <input id="rename-entry-name" className="ui-input w-full px-3 py-2" value={renameName} disabled={mutationBusy} onChange={(event) => onRenameNameChange(event.target.value)} autoFocus />
            <div className="flex justify-end gap-2"><button ref={renameCancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={mutationBusy} onClick={onClose}>{t('common.cancel')}</button><button type="submit" className="ui-button-primary px-3 py-2 text-sm" disabled={!renameName.trim() || mutationBusy}>{mutationBusy ? t('files.mutating') : t('files.rename')}</button></div>
          </form>
        </div>
      </div>, document.body)}
    {((mutationEntry && mutationOperation) || batchOperation) && conflictStrategies === null && createPortal(
      <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
        <div ref={pickerDialogRef} role="dialog" aria-modal="true" aria-labelledby="destination-picker-title" tabIndex={-1} className="ui-card flex max-h-[85vh] w-full max-w-lg flex-col p-5">
          <h2 id="destination-picker-title" className="text-lg font-semibold">{operation === 'copy' ? t('files.copyTitle') : t('files.moveTitle')}</h2>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">{mutationEntry ? t('files.destinationPickerDescription', { name: mutationEntry.name }) : t('files.batchDestinationPickerDescription', { count: selectedEntries.length })}</p>
          {mutationError && <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">{mutationError}</p>}
          <label className="mt-3 block text-sm font-medium" htmlFor="destination-profile">{t('files.destinationProfile')}</label>
          <select id="destination-profile" className="ui-input mt-1 w-full px-3 py-2" value={destinationPicker.profileId} disabled={destinationPicker.loading || isBusy} onChange={(event) => destinationPicker.onProfileChange(event.target.value)}>
            {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
          </select>
          <nav className="mt-3 flex flex-wrap gap-1 text-sm" aria-label={t('files.destinationBreadcrumb')}>
            {destinationPicker.breadcrumbs.map((breadcrumb, index) => <button key={breadcrumb.ref ?? 'root'} type="button" className="rounded px-1 py-0.5 hover:bg-[var(--color-hover)]" disabled={destinationPicker.loading || index === destinationPicker.breadcrumbs.length - 1} onClick={() => destinationPicker.onBreadcrumbSelect(index)}>{breadcrumb.name}{index < destinationPicker.breadcrumbs.length - 1 ? ' /' : ''}</button>)}
          </nav>
          <div className="mt-3 min-h-36 overflow-y-auto rounded border border-[var(--color-border)]">
            {destinationPicker.loading ? <div className="p-4"><LoadingIndicator label={t('common.loading')} size="sm" /></div> : destinationPicker.breadcrumbs.some((breadcrumb) => selectedEntries.some((entry) => entry.kind === 'directory' && breadcrumb.ref === entry.ref)) ? <p className="p-3 text-sm text-[var(--color-text-secondary)]">{t('files.noValidDestination')}</p> : destinationPicker.entries.filter((entry) => !selectedEntries.some((selected) => selected.ref === entry.ref)).map((entry) => <button key={entry.ref} type="button" className="flex w-full items-center gap-2 border-b border-[var(--color-border)] px-3 py-2 text-left text-sm hover:bg-[var(--color-hover)]" onClick={() => destinationPicker.onDirectorySelect(entry)}><FolderIcon className="h-4 w-4 ui-file-folder" aria-hidden="true" />{entry.name}</button>)}
          </div>
          <p className="mt-3 text-sm text-[var(--color-text-secondary)]">{t('files.destinationSelected', { name: destinationPicker.breadcrumbs[destinationPicker.breadcrumbs.length - 1]?.name })}</p>
          <div className="mt-5 flex justify-end gap-2"><button ref={pickerCancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={isBusy} onClick={onClose}>{t('common.cancel')}</button><button type="button" className="ui-button-primary px-3 py-2 text-sm" disabled={isBusy} onClick={() => batchOperation ? onExecuteBatchMutation() : onExecuteMutation()}>{isBusy ? t('files.mutating') : operation === 'copy' ? t('files.copy') : t('files.move')}</button></div>
        </div>
      </div>, document.body)}
    {conflictStrategies && (renameEntry || mutationEntry || batchOperation) && createPortal(
      <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
        <div ref={conflictDialogRef} role="dialog" aria-modal="true" aria-labelledby="mutation-conflict-title" tabIndex={-1} className="ui-card w-full max-w-md p-5">
          <h2 id="mutation-conflict-title" className="text-lg font-semibold">{t('files.conflictTitle')}</h2><p className="mt-2 text-sm text-[var(--color-text-secondary)]">{batchOperation ? t('files.batchConflictDescription', { count: batchConflictRefs.length }) : t('files.conflictDescription')}</p>
          <div className="mt-4 flex flex-wrap justify-end gap-2"><button ref={conflictCancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={isBusy} onClick={onClose}>{t('common.cancel')}</button>{conflictStrategies.map((strategy) => <button key={strategy} type="button" className="ui-button-primary px-3 py-2 text-sm" disabled={isBusy} onClick={() => batchOperation ? onExecuteBatchMutation(strategy) : onExecuteMutation(strategy)}>{t(`files.conflict${strategy[0]}${strategy.slice(1).toLowerCase()}`)}</button>)}</div>
        </div>
      </div>, document.body)}
  </>;
}
