import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowPathIcon, ArrowUpIcon, FolderPlusIcon, ListBulletIcon, Squares2X2Icon } from '../../icons';
import type { FileManagerBreadcrumb } from '../fileManagerTypes';
import { Breadcrumbs } from './Breadcrumbs';
import type { ViewMode } from './useViewMode';

type BrowseToolbarProps = {
  breadcrumbs: FileManagerBreadcrumb[];
  canGoUp: boolean;
  onUp: () => void;
  onNavigate: (index: number) => void;
  /** The upload picker, supplied by the composition module. */
  uploadSlot: ReactNode;
  newFolder: { disabled: boolean; unavailable: boolean; onClick: () => void };
  viewMode: ViewMode;
  onViewModeChange: (mode: ViewMode) => void;
  refresh: { disabled: boolean; onClick: () => void };
};

function ViewModeButton({ mode, active, label, onSelect, children }: { mode: ViewMode; active: boolean; label: string; onSelect: (mode: ViewMode) => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={() => onSelect(mode)}
      aria-pressed={active}
      aria-label={label}
      title={label}
      className={`p-1.5 rounded-md transition-colors ${
        active ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] shadow-xs' : 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'
      }`}
    >
      {children}
    </button>
  );
}

/** Navigation, upload, new folder, view mode and refresh controls for the current folder. */
export function BrowseToolbar({ breadcrumbs, canGoUp, onUp, onNavigate, uploadSlot, newFolder, viewMode, onViewModeChange, refresh }: BrowseToolbarProps) {
  const { t } = useTranslation();
  return (
    <div className="sticky top-16 lg:top-20 z-10 flex items-center justify-between gap-3 border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3 rounded-t-[var(--radius-lg)] -mt-[1px] -mx-[1px] shadow-xs relative before:content-[''] before:hidden lg:before:block before:absolute before:bottom-full before:mb-[1px] before:left-0 before:right-0 before:h-5 before:bg-[var(--color-bg-primary)] before:pointer-events-none">
      <div className="flex min-w-0 items-center gap-1.5 flex-1">
        <button
          type="button"
          onClick={onUp}
          disabled={!canGoUp}
          className="ui-icon-button p-2 hover:bg-[var(--color-hover)] shrink-0 disabled:opacity-40 disabled:cursor-not-allowed"
          aria-label={t('files.up')}
          title={t('files.up')}
        >
          <ArrowUpIcon className="h-4 w-4" aria-hidden="true" />
        </button>
        <Breadcrumbs breadcrumbs={breadcrumbs} onNavigate={onNavigate} onUp={onUp} />
      </div>

      <div className="flex items-center gap-2 shrink-0">
        {uploadSlot}
        <button
          type="button"
          onClick={newFolder.onClick}
          disabled={newFolder.disabled}
          className="ui-button-secondary inline-flex items-center gap-2 px-3 py-2 text-sm disabled:cursor-not-allowed disabled:opacity-50"
          title={newFolder.unavailable ? t('files.mkdirUnavailable') : t('files.newFolder')}
          aria-label={t('files.newFolder')}
        >
          <FolderPlusIcon className="h-4 w-4" aria-hidden="true" />
          <span className="hidden sm:inline">{t('files.newFolder')}</span>
        </button>
        <div className="flex items-center rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-0.5" role="group" aria-label={t('files.viewMode')}>
          <ViewModeButton mode="list" active={viewMode === 'list'} label={t('files.viewList')} onSelect={onViewModeChange}>
            <ListBulletIcon className="h-4 w-4" aria-hidden="true" />
          </ViewModeButton>
          <ViewModeButton mode="grid" active={viewMode === 'grid'} label={t('files.viewGrid')} onSelect={onViewModeChange}>
            <Squares2X2Icon className="h-4 w-4" aria-hidden="true" />
          </ViewModeButton>
        </div>
        <button
          type="button"
          onClick={refresh.onClick}
          disabled={refresh.disabled}
          className="ui-icon-button p-2 hover:bg-[var(--color-hover)]"
          aria-label={t('files.refresh')}
          title={t('files.refresh')}
        >
          <ArrowPathIcon className="h-4 w-4" aria-hidden="true" />
        </button>
      </div>
    </div>
  );
}
