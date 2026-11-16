import { useMemo } from 'react';
import { useFormat } from '../../utils/format';
import type { FileRequestContext } from './actions/fileRequests';
import { FileThumbnail } from './FileThumbnail';
import type { FileActionPermissions } from './useFileManagerActions';
import type { FileManagerBrowsing } from './useFileManagerBrowsing';
import { toEntryRow, type EntryRowModel } from './views/entryRows';
import type { ViewMode } from './views/useViewMode';

type UseEntryRowsOptions = {
  browsing: FileManagerBrowsing;
  permissions: FileActionPermissions;
  /** Thumbnails are fetched with these credentials, so row building lives outside the presentation modules. */
  request: FileRequestContext;
  viewMode: ViewMode;
  /** Ref of the entry whose action menu is open. */
  menuRef: string | null;
};

/** Compact view models for every listed entry, with the thumbnail slot filled in. */
export function useEntryRows({ browsing, permissions, request, viewMode, menuRef }: UseEntryRowsOptions): EntryRowModel[] {
  const { formatBytes, formatDateTime } = useFormat();
  const { entries, loading, capabilities, isInteractive } = browsing;
  const { active: selectionActive, isSelected } = browsing.selection;

  return useMemo(
    () =>
      entries.map((entry) =>
        toEntryRow(entry, {
          selectionActive,
          isSelected,
          isInteractive,
          canBrowse: capabilities.browse,
          loading,
          hasEntryActions: permissions.hasEntryActions,
          menuRef,
          formatBytes,
          formatDateTime,
          renderThumbnail: (target) => (
            <FileThumbnail
              request={request}
              entry={target}
              thumbnailsEnabled={capabilities.thumbnails}
              size={viewMode === 'list' ? 'sm' : 'lg'}
              className="w-full h-full flex items-center justify-center"
              imageClassName="w-full h-full object-cover"
              fallbackIconClassName={viewMode === 'list' ? 'h-5 w-5' : 'h-12 w-12 drop-shadow-xs'}
            />
          ),
        }),
      ),
    [entries, loading, capabilities, isInteractive, selectionActive, isSelected, permissions, menuRef, formatBytes, formatDateTime, request, viewMode],
  );
}
