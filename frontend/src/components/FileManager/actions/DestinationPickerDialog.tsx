import { useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { FolderIcon } from '../../icons';
import { LoadingIndicator } from '../../LoadingIndicator';
import type { DestinationPickerDialogModel } from './dialogModels';
import { ModalDialog } from './ModalDialog';

export function DestinationPickerDialog({ model }: { model: DestinationPickerDialogModel }) {
  const { t } = useTranslation();
  const cancelRef = useRef<HTMLButtonElement>(null);
  const lastBreadcrumbIndex = model.breadcrumbs.length - 1;

  return (
    <ModalDialog
      labelledBy="destination-picker-title"
      initialFocusRef={cancelRef}
      onEscape={model.close}
      className="ui-card flex max-h-[85vh] w-full max-w-lg flex-col p-5"
    >
      <h2 id="destination-picker-title" className="text-lg font-semibold">{model.operation === 'copy' ? t('files.copyTitle') : t('files.moveTitle')}</h2>
      <p className="mt-1 text-sm text-[var(--color-text-secondary)]">{model.entryName ? t('files.destinationPickerDescription', { name: model.entryName }) : t('files.batchDestinationPickerDescription', { count: model.itemCount })}</p>
      {model.error && <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">{model.error}</p>}
      <label className="mt-3 block text-sm font-medium" htmlFor="destination-profile">{t('files.destinationProfile')}</label>
      <select id="destination-profile" className="ui-input mt-1 w-full px-3 py-2" value={model.profileId} disabled={model.loading || model.busy} onChange={(event) => model.selectProfile(event.target.value)}>
        {model.profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
      </select>
      <nav className="mt-3 flex flex-wrap gap-1 text-sm" aria-label={t('files.destinationBreadcrumb')}>
        {model.breadcrumbs.map((breadcrumb, index) => (
          <button key={breadcrumb.ref ?? 'root'} type="button" className="rounded px-1 py-0.5 hover:bg-[var(--color-hover)]" disabled={model.loading || index === lastBreadcrumbIndex} onClick={() => model.goToBreadcrumb(index)}>
            {breadcrumb.name}{index < lastBreadcrumbIndex ? ' /' : ''}
          </button>
        ))}
      </nav>
      <div data-testid="destination-entries" className="mt-3 min-h-36 overflow-y-auto rounded border border-[var(--color-border)]">
        {model.loading ? (
          <div className="p-4"><LoadingIndicator label={t('common.loading')} size="sm" /></div>
        ) : model.hasInvalidDestination ? (
          <p className="p-3 text-sm text-[var(--color-text-secondary)]">{t('files.noValidDestination')}</p>
        ) : (
          model.folders.map((entry) => (
            <button key={entry.ref} type="button" className="flex w-full items-center gap-2 border-b border-[var(--color-border)] px-3 py-2 text-left text-sm hover:bg-[var(--color-hover)]" onClick={() => model.openFolder(entry)}>
              <FolderIcon className="h-4 w-4 ui-file-folder" aria-hidden="true" />{entry.name}
            </button>
          ))
        )}
      </div>
      <p className="mt-3 text-sm text-[var(--color-text-secondary)]">{t('files.destinationSelected', { name: model.breadcrumbs[lastBreadcrumbIndex]?.name })}</p>
      <div className="mt-5 flex justify-end gap-2">
        <button ref={cancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={model.busy} onClick={model.close}>{t('common.cancel')}</button>
        <button type="button" className="ui-button-primary px-3 py-2 text-sm" disabled={model.busy} onClick={() => void model.submit()}>
          {model.busy ? t('files.mutating') : model.operation === 'copy' ? t('files.copy') : t('files.move')}
        </button>
      </div>
    </ModalDialog>
  );
}
