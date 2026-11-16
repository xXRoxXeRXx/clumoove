import { useEffect, useMemo, lazy, Suspense } from 'react';
import type { FileBreadcrumb, UploadConflictStrategy } from '../../api/files';
import type { FileRequestContext } from './actions/fileRequests';
import { FileUploadControl } from './FileUploadControl';
import { TransferPanel } from './TransferPanel';
import { useEntryMenu } from './useEntryMenu';
import { useEntryRows } from './useEntryRows';
import { useFileManagerActions } from './useFileManagerActions';
import { useFileManagerBrowsing } from './useFileManagerBrowsing';
import { useTransferCenter } from './useTransferCenter';
import { BatchSummaryBanner } from './views/BatchSummaryBanner';
import { BrowseToolbar } from './views/BrowseToolbar';
import { EntryCollection } from './views/EntryCollection';
import { LoadMoreFooter } from './views/LoadMoreFooter';
import { ProfileSidebar } from './views/ProfileSidebar';
import { ScreenHeader } from './views/ScreenHeader';
import { SelectionToolbar } from './views/SelectionToolbar';
import { StatusBanners } from './views/StatusBanners';
import { listingState } from './views/entryRows';
import { useViewMode } from './views/useViewMode';

const FilePreviewDialog = lazy(() => import('./FilePreviewDialog').then((m) => ({ default: m.FilePreviewDialog })));

type FileManagerProps = FileRequestContext & {
  initialBreadcrumbs?: FileBreadcrumb[];
  initialPathFallback?: boolean;
  onProfileChange: (profileId: string) => void;
  onOpenManager?: () => void;
  onBack?: () => void;
};

/**
 * Composition root of the file manager: wires browsing, preview, file actions and transfers to
 * the presentation modules in `views/`. It owns the request context and passes the presentation
 * modules view models and intent callbacks only.
 */
export function FileManager({ apiUrl, token, profileId, initialBreadcrumbs, initialPathFallback = false, onProfileChange, onOpenManager, onBack }: FileManagerProps) {
  const request = useMemo<FileRequestContext>(() => ({ apiUrl, token, profileId }), [apiUrl, token, profileId]);
  const [viewMode, setViewMode] = useViewMode();

  const browsing = useFileManagerBrowsing({ request, initialBreadcrumbs, onProfileChange });
  const actions = useFileManagerActions({
    request,
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
    request,
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

  const { permissions } = actions;
  const hasProfile = browsing.selectedProfile !== null;
  const selectionActive = browsing.selection.active;
  const entriesByRef = useMemo(() => new Map(browsing.entries.map((entry) => [entry.ref, entry])), [browsing.entries]);
  const entryMenu = useEntryMenu(browsing.entries, actions);
  const rows = useEntryRows({ browsing, permissions, request, viewMode, menuRef: entryMenu.openRef });

  const activateEntry = (ref: string, options: { toggleSelection: boolean }) => {
    const entry = entriesByRef.get(ref);
    if (entry) browsing.activateEntry(entry, options);
  };
  const upload = (files: File[], strategy: UploadConflictStrategy) =>
    transfers.enqueueUploads(files, { profileId, parentRef: browsing.currentRef, strategy });

  return (
    <section className="w-full space-y-5" aria-labelledby="file-manager-title">
      <ScreenHeader onBack={onBack} />
      <StatusBanners error={browsing.error || actions.error} pathFallback={initialPathFallback} />

      <div className="grid gap-5 lg:grid-cols-[15rem_minmax(0,1fr)] items-start">
        <ProfileSidebar
          profiles={browsing.profiles}
          activeId={profileId}
          loading={browsing.profilesLoading}
          onSelect={browsing.selectProfile}
          onOpenManager={onOpenManager}
        />

        <div className="ui-card min-w-0 min-h-[560px] flex flex-col">
          {hasProfile && selectionActive && (
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
          )}
          {hasProfile && !selectionActive && (
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
          {hasProfile && actions.batchSummary && (
            <BatchSummaryBanner summary={actions.batchSummary} onDismiss={actions.dismissBatchSummary} />
          )}

          <EntryCollection
            state={listingState({ hasProfile, canBrowse: browsing.capabilities.browse, loading: browsing.loading })}
            rows={rows}
            viewMode={viewMode}
            selection={{
              active: selectionActive,
              allSelected: browsing.selection.allSelected,
              partiallySelected: browsing.selection.partiallySelected,
              onToggleAll: browsing.selection.toggleAll,
            }}
            sort={{ field: browsing.sortField, direction: browsing.sortDirection, onSort: browsing.sortBy }}
            onActivate={activateEntry}
            onToggleSelect={browsing.selection.toggle}
            onOpenMenu={entryMenu.openMenu}
          />

          {hasProfile && browsing.hasMore && (
            <LoadMoreFooter
              loading={browsing.loadingMore}
              autoLoad={!browsing.loading && !browsing.loadingMore && browsing.capabilities.browse}
              onLoadMore={() => void browsing.loadMore()}
            />
          )}
        </div>
      </div>

      <TransferPanel items={transfers.items} onCancel={transfers.cancel} onDismiss={transfers.dismiss} onRetry={transfers.retry} />
      {actions.dialogs}
      {browsing.preview.entry && (
        <Suspense fallback={null}>
          <FilePreviewDialog
            request={request}
            entry={browsing.preview.entry}
            entries={browsing.entries}
            supportsThumbnails={browsing.capabilities.thumbnails}
            onNavigate={browsing.preview.open}
            onClose={browsing.preview.close}
            onDownload={(entry) => void actions.download(entry)}
          />
        </Suspense>
      )}
      {entryMenu.element}
    </section>
  );
}
