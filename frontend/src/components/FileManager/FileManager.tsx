import { useEffect, useMemo, useState, lazy, Suspense } from 'react';
import { ArrowLeftIcon, FolderIcon } from '../icons';
import { useTranslation } from 'react-i18next';
import type { FileBreadcrumb, FileEntry, UploadConflictStrategy } from '../../api/files';
import { LoadingIndicator } from '../LoadingIndicator';
import { useFormat } from '../../utils/format';
import { FileUploadControl } from './FileUploadControl';
import { FileThumbnail } from './FileThumbnail';
import { TransferPanel } from './TransferPanel';
import { useFileManagerActions } from './useFileManagerActions';
import { useFileManagerBrowsing } from './useFileManagerBrowsing';
import { useTransferCenter } from './useTransferCenter';
import { BatchSummaryBanner } from './views/BatchSummaryBanner';
import { BrowseToolbar } from './views/BrowseToolbar';
import { EntryActionMenu } from './views/EntryActionMenu';
import { EntryGrid } from './views/EntryGrid';
import { EntryList } from './views/EntryList';
import { LoadMoreFooter } from './views/LoadMoreFooter';
import { ProfileSidebar } from './views/ProfileSidebar';
import { SelectionToolbar } from './views/SelectionToolbar';
import { entryMenuActions, nextMenuState, type EntryMenuAction, type EntryMenuState } from './views/entryMenu';
import { toEntryRow, type MenuAnchor } from './views/entryRows';
import { useViewMode } from './views/useViewMode';

const FilePreviewDialog = lazy(() => import('./FilePreviewDialog').then((m) => ({ default: m.FilePreviewDialog })));

type FileManagerProps = {
  apiUrl: string;
  token: string;
  profileId: string;
  initialBreadcrumbs?: FileBreadcrumb[];
  initialPathFallback?: boolean;
  onProfileChange: (profileId: string) => void;
  onOpenManager?: () => void;
  onBack?: () => void;
};

/**
 * Composition root of the file manager: wires browsing, preview, file actions and transfers to
 * the presentation modules in `views/`. It owns the request credentials and passes the
 * presentation modules view models and intent callbacks only.
 */
export function FileManager({ apiUrl, token, profileId, initialBreadcrumbs, initialPathFallback = false, onProfileChange, onOpenManager, onBack }: FileManagerProps) {
  const { t } = useTranslation();
  const { formatBytes, formatDateTime } = useFormat();
  const [viewMode, setViewMode] = useViewMode();
  const [menu, setMenu] = useState<EntryMenuState | null>(null);

  const browsing = useFileManagerBrowsing({ apiUrl, token, profileId, initialBreadcrumbs, onProfileChange });
  const actions = useFileManagerActions({
    request: { apiUrl, token, profileId },
    profileName: browsing.selectedProfile?.name ?? '',
    profiles: browsing.profiles,
    currentRef: browsing.currentRef,
    capabilities: browsing.capabilities,
    selectedEntries: browsing.selection.entries,
    previewEntryRef: browsing.preview.entry?.ref ?? null,
    refreshListing: browsing.refresh,
    clearSelection: browsing.selection.clear,
    clearPreview: browsing.preview.close,
  });
  const transfers = useTransferCenter({
    apiUrl,
    token,
    transfers: actions.transfers,
    onCancelTransfer: actions.cancelTransfer,
    onDismissTransfer: actions.dismissTransfer,
    onUploadCompleted: browsing.notifyUploadCompleted,
  });

  const clearActionError = actions.clearError;
  useEffect(() => {
    // A new listing request clears stale action errors, as it does for listing errors.
    if (browsing.loading) clearActionError();
  }, [browsing.loading, clearActionError]);

  const entriesByRef = useMemo(() => new Map(browsing.entries.map((entry) => [entry.ref, entry])), [browsing.entries]);
  const { permissions } = actions;
  const selectionActive = browsing.selection.active;
  const menuEntry = menu ? entriesByRef.get(menu.ref) : undefined;

  const rows = useMemo(
    () =>
      browsing.entries.map((entry) =>
        toEntryRow(entry, {
          selectionActive,
          isSelected: browsing.selection.isSelected,
          isInteractive: browsing.isInteractive,
          canBrowse: browsing.capabilities.browse,
          loading: browsing.loading,
          hasEntryActions: permissions.hasEntryActions,
          menuRef: menu?.ref ?? null,
          formatBytes,
          formatDateTime,
          renderThumbnail: (target) => (
            <FileThumbnail
              apiUrl={apiUrl}
              token={token}
              profileId={profileId}
              entry={target}
              thumbnailsEnabled={browsing.capabilities.thumbnails}
              size={viewMode === 'list' ? 'sm' : 'lg'}
              className="w-full h-full flex items-center justify-center"
              imageClassName="w-full h-full object-cover"
              fallbackIconClassName={viewMode === 'list' ? 'h-5 w-5' : 'h-12 w-12 drop-shadow-xs'}
            />
          ),
        }),
      ),
    [apiUrl, token, profileId, viewMode, menu?.ref, selectionActive, permissions, formatBytes, formatDateTime, browsing.entries, browsing.selection.isSelected, browsing.isInteractive, browsing.capabilities, browsing.loading],
  );

  const activateEntry = (ref: string, options: { toggleSelection: boolean }) => {
    const entry = entriesByRef.get(ref);
    if (entry) browsing.activateEntry(entry, options);
  };
  const openMenu = (ref: string, anchor: MenuAnchor) => setMenu((current) => nextMenuState(current, ref, anchor));
  const runMenuAction = (entry: FileEntry, action: EntryMenuAction) => {
    setMenu(null);
    if (action === 'rename') actions.rename(entry);
    else if (action === 'copy') actions.copy(entry);
    else if (action === 'move') actions.move(entry);
    else if (action === 'download') void actions.download(entry);
    else actions.delete(entry);
  };
  const upload = (files: File[], strategy: UploadConflictStrategy) =>
    transfers.enqueueUploads(files, { profileId, parentRef: browsing.currentRef, strategy });

  const collectionIntents = { onActivate: activateEntry, onToggleSelect: browsing.selection.toggle, onOpenMenu: openMenu };

  return (
    <section className="w-full space-y-5" aria-labelledby="file-manager-title">
      <div className="flex items-center justify-between pb-4 border-b border-[var(--color-border)]/50">
        {onBack ? (
          <button
            type="button"
            onClick={onBack}
            className="ui-button-secondary flex items-center gap-2 px-3 py-2 text-sm font-medium hover:bg-[var(--color-bg-tertiary)]"
          >
            <ArrowLeftIcon className="w-4 h-4" aria-hidden="true" />
            {t('common.back')}
          </button>
        ) : <div />}
        <div className="flex items-center gap-2">
          <FolderIcon className="w-5 h-5 text-[var(--color-text-primary)]" aria-hidden="true" />
          <h1 id="file-manager-title" className="font-display font-semibold text-xl text-[var(--color-text-primary)] leading-none">{t('files.title')}</h1>
        </div>
      </div>

      {(browsing.error || actions.error) && <p className="ui-alert ui-alert-error px-3 py-2 text-sm" role="alert">{browsing.error || actions.error}</p>}
      {initialPathFallback && <p className="ui-alert px-3 py-2 text-sm" role="status">{t('files.pathFallback')}</p>}

      <div className="grid gap-5 lg:grid-cols-[15rem_minmax(0,1fr)] items-start">
        <ProfileSidebar
          profiles={browsing.profiles}
          activeId={profileId}
          loading={browsing.profilesLoading}
          onSelect={browsing.selectProfile}
          onOpenManager={onOpenManager}
        />

        <div className="ui-card min-w-0 min-h-[560px] flex flex-col">
          {!browsing.selectedProfile ? (
            <div className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.selectProfile')}</div>
          ) : (
            <>
              {selectionActive ? (
                <SelectionToolbar
                  count={browsing.selection.entries.length}
                  busy={actions.batchBusy}
                  canDownloadArchive={permissions.canBatchDownload}
                  canCopy={permissions.canBatchCopy}
                  canMove={permissions.canBatchMove}
                  canDelete={permissions.canBatchDelete}
                  onClear={browsing.selection.clear}
                  onDownloadArchive={() => void actions.downloadArchive()}
                  onCopy={() => actions.copy()}
                  onMove={() => actions.move()}
                  onDelete={() => actions.delete()}
                />
              ) : (
                <BrowseToolbar
                  breadcrumbs={browsing.breadcrumbs}
                  canGoUp={browsing.capabilities.browse && browsing.breadcrumbs.length > 1 && !browsing.loading}
                  onUp={browsing.goUp}
                  onNavigate={browsing.goToBreadcrumb}
                  uploadSlot={<FileUploadControl capabilities={browsing.capabilities} disabled={browsing.loading} onUpload={upload} />}
                  newFolder={{
                    disabled: browsing.loading || !permissions.canCreateFolder,
                    unavailable: !permissions.canCreateFolder,
                    onClick: () => actions.createFolder(),
                  }}
                  viewMode={viewMode}
                  onViewModeChange={setViewMode}
                  refresh={{ disabled: !browsing.capabilities.browse, onClick: browsing.refresh }}
                />
              )}
              {actions.batchSummary && <BatchSummaryBanner summary={actions.batchSummary} onDismiss={actions.dismissBatchSummary} />}

              {!browsing.capabilities.browse && !browsing.loading ? (
                <p className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.listUnavailable')}</p>
              ) : browsing.loading ? (
                <div className="p-8 flex-1 flex items-center justify-center"><LoadingIndicator label={t('common.loading')} /></div>
              ) : rows.length === 0 ? (
                <p className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.emptyDirectory')}</p>
              ) : viewMode === 'list' ? (
                <EntryList
                  rows={rows}
                  selection={{
                    active: selectionActive,
                    allSelected: browsing.selection.allSelected,
                    partiallySelected: browsing.selection.partiallySelected,
                    onToggleAll: browsing.selection.toggleAll,
                  }}
                  sort={{ field: browsing.sortField, direction: browsing.sortDirection, onSort: browsing.sortBy }}
                  {...collectionIntents}
                />
              ) : (
                <EntryGrid rows={rows} selectionActive={selectionActive} {...collectionIntents} />
              )}

              {browsing.hasMore && (
                <LoadMoreFooter
                  loading={browsing.loadingMore}
                  autoLoad={!browsing.loading && !browsing.loadingMore && browsing.capabilities.browse}
                  onLoadMore={() => void browsing.loadMore()}
                />
              )}
            </>
          )}
        </div>
      </div>

      <TransferPanel items={transfers.items} onCancel={transfers.cancel} onDismiss={transfers.dismiss} onRetry={transfers.retry} />
      {actions.dialogs}
      {browsing.preview.entry && (
        <Suspense fallback={null}>
          <FilePreviewDialog
            apiUrl={apiUrl}
            token={token}
            profileId={profileId}
            entry={browsing.preview.entry}
            entries={browsing.entries}
            supportsThumbnails={browsing.capabilities.thumbnails}
            onNavigate={browsing.preview.open}
            onClose={browsing.preview.close}
            onDownload={(entry) => void actions.download(entry)}
          />
        </Suspense>
      )}
      {menu && menuEntry && (
        <EntryActionMenu
          name={menuEntry.name}
          position={menu.position}
          actions={entryMenuActions(menuEntry, permissions)}
          onSelect={(action) => runMenuAction(menuEntry, action)}
          onDismiss={() => setMenu(null)}
        />
      )}
    </section>
  );
}
