import { useEffect, useRef, useState, lazy, Suspense } from 'react';
import { createPortal } from 'react-dom';
import {
  ArrowDownTrayIcon,
  ArrowLeftIcon,
  ArrowPathIcon,
  ArrowRightIcon,
  ArrowsRightLeftIcon,
  ArrowUpIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ChevronUpIcon,
  ChevronUpDownIcon,
	ClipboardDocumentIcon,
  EllipsisHorizontalIcon,
	EllipsisVerticalIcon,
  FolderIcon,
  FolderPlusIcon,
  ListBulletIcon,
  PencilIcon,
  ProviderIcon,
  Squares2X2Icon,
  TrashIcon,
  WrenchScrewdriverIcon,
  XMarkIcon,
} from '../icons';
import { useTranslation } from 'react-i18next';
import type { FileBreadcrumb, FileEntry } from '../../api/files';
import { LoadingIndicator } from '../LoadingIndicator';
import { useFormat } from '../../utils/format';
import { FileUploadControl } from './FileUploadControl';
import { FileThumbnail } from './FileThumbnail';
import { useFileManagerActions } from './useFileManagerActions';
import { useFileManagerBrowsing, type SortField } from './useFileManagerBrowsing';

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

export function FileManager({ apiUrl, token, profileId, initialBreadcrumbs, initialPathFallback = false, onProfileChange, onOpenManager, onBack }: FileManagerProps) {
  const { t } = useTranslation();
  const { formatBytes, formatDateTime } = useFormat();
  const browsing = useFileManagerBrowsing({
    apiUrl,
    token,
    profileId,
    initialBreadcrumbs,
    onProfileChange,
  });
  const [menuState, setMenuState] = useState<{
    entry: FileEntry;
    x: number;
    y: number;
    align?: 'left' | 'right';
  } | null>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const [isBreadcrumbMenuOpen, setIsBreadcrumbMenuOpen] = useState(false);
  const breadcrumbMenuRef = useRef<HTMLDivElement>(null);
  const [viewMode, setViewMode] = useState<'list' | 'grid'>(() => {
    try {
      const stored = localStorage.getItem('clumoove_file_manager_view_mode');
      return stored === 'grid' ? 'grid' : 'list';
    } catch {
      return 'list';
    }
  });

  const handleViewModeChange = (mode: 'list' | 'grid') => {
    setViewMode(mode);
    try {
      localStorage.setItem('clumoove_file_manager_view_mode', mode);
    } catch {
      // ignore storage errors
    }
  };

  const ariaSortFor = (field: SortField) => {
    if (browsing.sortField !== field) return 'none';
    return browsing.sortDirection === 'asc' ? 'ascending' : 'descending';
  };

  const renderSortHeader = (field: SortField, label: string, sortLabel: string) => {
    const isActive = browsing.sortField === field;
    return (
      <button
        type="button"
        onClick={() => browsing.sortBy(field)}
        className="group -mx-1 -my-1 px-1 py-1 inline-flex items-center gap-1.5 font-medium hover:text-[var(--color-text-primary)] transition-colors select-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-focus)] rounded text-left"
        title={isActive ? (browsing.sortDirection === 'asc' ? t('files.sortDescending') : t('files.sortAscending')) : sortLabel}
        aria-label={`${label}: ${isActive ? (browsing.sortDirection === 'asc' ? t('files.sortAscending') : t('files.sortDescending')) : sortLabel}`}
      >
        <span className={isActive ? 'text-[var(--color-text-primary)] font-semibold' : 'text-[var(--color-text-secondary)] group-hover:text-[var(--color-text-primary)] transition-colors'}>
          {label}
        </span>
        {isActive ? (
          <span className="text-[var(--color-text-primary)] flex items-center shrink-0">
            {browsing.sortDirection === 'asc' ? (
              <ChevronUpIcon className="h-3.5 w-3.5 stroke-[2.5]" aria-hidden="true" />
            ) : (
              <ChevronDownIcon className="h-3.5 w-3.5 stroke-[2.5]" aria-hidden="true" />
            )}
          </span>
        ) : (
          <span className="opacity-0 group-hover:opacity-100 group-focus-visible:opacity-100 transition-opacity text-[var(--color-text-secondary)] flex items-center shrink-0">
            <ChevronUpDownIcon className="h-3.5 w-3.5" aria-hidden="true" />
          </span>
        )}
      </button>
    );
  };

  const selectAllRef = useRef<HTMLInputElement>(null);
  const sentinelRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!menuState) return;
    const handlePointerDown = (event: PointerEvent | MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) {
        setMenuState(null);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setMenuState(null);
      }
    };
    const handleScroll = () => {
      setMenuState(null);
    };
    window.addEventListener('pointerdown', handlePointerDown);
    window.addEventListener('keydown', handleKeyDown);
    window.addEventListener('scroll', handleScroll, true);
    return () => {
      window.removeEventListener('pointerdown', handlePointerDown);
      window.removeEventListener('keydown', handleKeyDown);
      window.removeEventListener('scroll', handleScroll, true);
    };
  }, [menuState]);

  useEffect(() => {
    if (!isBreadcrumbMenuOpen) return;
    const handlePointerDown = (event: PointerEvent | MouseEvent) => {
      if (breadcrumbMenuRef.current && !breadcrumbMenuRef.current.contains(event.target as Node)) {
        setIsBreadcrumbMenuOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setIsBreadcrumbMenuOpen(false);
      }
    };
    window.addEventListener('pointerdown', handlePointerDown);
    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('pointerdown', handlePointerDown);
      window.removeEventListener('keydown', handleKeyDown);
    };
  }, [isBreadcrumbMenuOpen]);

  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = browsing.selection.partiallySelected;
  }, [browsing.selection.partiallySelected]);

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

  const clearActionError = actions.clearError;
  useEffect(() => {
    // A new listing request clears stale action errors, as it does for listing errors.
    if (browsing.loading) clearActionError();
  }, [browsing.loading, clearActionError]);

  const loadMoreEntries = browsing.loadMore;
  useEffect(() => {
    if (!browsing.hasMore || browsing.loading || browsing.loadingMore || !browsing.capabilities.browse) return;
    const target = sentinelRef.current;
    if (!target || typeof IntersectionObserver === 'undefined') return;

    const observer = new IntersectionObserver(
      (observedEntries) => {
        if (observedEntries[0]?.isIntersecting) {
          void loadMoreEntries();
        }
      },
      { rootMargin: '250px' }
    );

    observer.observe(target);
    return () => observer.disconnect();
  }, [browsing.capabilities.browse, browsing.hasMore, browsing.loading, browsing.loadingMore, loadMoreEntries]);

  const selectProfile = (id: string) => {
    setIsBreadcrumbMenuOpen(false);
    browsing.selectProfile(id);
  };

  const goUp = () => {
    setIsBreadcrumbMenuOpen(false);
    browsing.goUp();
  };

  const goToBreadcrumb = (index: number) => {
    setIsBreadcrumbMenuOpen(false);
    browsing.goToBreadcrumb(index);
  };

  const handleEntryClick = (entry: FileEntry, event?: React.MouseEvent | React.KeyboardEvent) => {
    setIsBreadcrumbMenuOpen(false);
    browsing.activateEntry(entry, { toggleSelection: Boolean(event?.ctrlKey || event?.metaKey) });
  };

  const toggleMenu = (event: React.MouseEvent, entry: FileEntry) => {
    event.stopPropagation();
    if (menuState?.entry.ref === entry.ref) {
      setMenuState(null);
      return;
    }
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
    setMenuState({
      entry,
      x: rect.right,
      y: rect.bottom + 4,
      align: 'right',
    });
  };

  const handleContextMenu = (event: React.MouseEvent, entry: FileEntry) => {
    if (!actions.permissions.hasEntryActions(entry)) return;
    event.preventDefault();
    event.stopPropagation();
    setMenuState({
      entry,
      x: event.clientX,
      y: event.clientY,
      align: 'left',
    });
  };

  const renderActionMenuPortal = () => {
    if (!menuState) return null;
    const entry = menuState.entry;
    const menuWidth = 180;
    const menuHeight = 220;
    let left = menuState.align === 'right' ? menuState.x - menuWidth : menuState.x;
    let top = menuState.y;

    if (left + menuWidth > window.innerWidth - 8) {
      left = window.innerWidth - menuWidth - 8;
    }
    if (left < 8) {
      left = 8;
    }
    if (top + menuHeight > window.innerHeight - 8) {
      top = Math.max(8, top - menuHeight - 8);
    }

    return createPortal(
      <div
        ref={menuRef}
        role="menu"
        aria-label={t('files.actionsFor', { name: entry.name })}
        style={{ position: 'fixed', top: `${top}px`, left: `${left}px`, zIndex: 50 }}
        className="w-44 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-primary)] p-1.5 shadow-xl ring-1 ring-black/5 dark:ring-white/10 animate-in fade-in zoom-in-95 duration-100"
        onClick={(event) => event.stopPropagation()}
      >
        {actions.permissions.canRenameEntry(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              actions.rename(entry);
            }}
          >
            <PencilIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.rename')}</span>
          </button>
        )}
        {actions.permissions.canCopyEntry(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              actions.copy(entry);
            }}
          >
            <ClipboardDocumentIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.copy')}</span>
          </button>
        )}
        {actions.permissions.canMoveEntry(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              actions.move(entry);
            }}
          >
            <ArrowRightIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.move')}</span>
          </button>
        )}
        {actions.permissions.canDownloadEntry(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              void actions.download(entry);
            }}
          >
            <ArrowDownTrayIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.downloadAction')}</span>
          </button>
        )}
        {actions.permissions.canDeleteEntry(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-danger)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              actions.delete(entry);
            }}
          >
            <TrashIcon className="h-4 w-4 text-[var(--color-danger)] shrink-0" aria-hidden="true" />
            <span>{t('files.deleteAction')}</span>
          </button>
        )}
      </div>,
      document.body
    );
  };

  const shouldCollapseBreadcrumbs = browsing.breadcrumbs.length > 3;
  const rootBreadcrumb = browsing.breadcrumbs[0];
  const collapsedBreadcrumbs = shouldCollapseBreadcrumbs ? browsing.breadcrumbs.slice(1, -2) : [];
  const parentBreadcrumb = shouldCollapseBreadcrumbs ? browsing.breadcrumbs[browsing.breadcrumbs.length - 2] : null;
  const currentBreadcrumbItem = shouldCollapseBreadcrumbs ? browsing.breadcrumbs[browsing.breadcrumbs.length - 1] : null;

  return (
    <section className="w-full space-y-5" aria-labelledby="file-manager-title">
      {/* Back Header */}
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
        <aside className="ui-card flex flex-col justify-between p-3 min-h-[160px] self-start lg:sticky lg:top-20 z-10" aria-label={t('files.profiles')}>
          <div>
            <div className="mb-2 px-2">
              <h2 className="text-sm font-semibold">{t('files.profiles')}</h2>
            </div>
            {browsing.profilesLoading ? (
              <div className="px-2 py-4"><LoadingIndicator label={t('common.loading')} size="sm" /></div>
            ) : browsing.profiles.length === 0 ? (
              <p className="ui-empty px-2 py-4 text-sm">{t('files.noProfiles')}</p>
            ) : (
              <ul className="space-y-1" aria-label={t('files.profiles')}>
                {browsing.profiles.map((profile) => (
                  <li key={profile.id}>
                    <button
                      type="button"
                      onClick={() => selectProfile(profile.id)}
                      aria-current={profile.id === profileId ? 'page' : undefined}
                      className={`flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm transition-colors ${profile.id === profileId ? 'bg-[var(--color-selection-bg)] text-[var(--color-selection-text)]' : 'text-[var(--color-text-secondary)] hover:bg-[var(--color-hover)]'}`}
                    >
                      <ProviderIcon provider={profile.provider} className="h-4 w-4 shrink-0" />
                      <span className="truncate font-medium">{profile.name}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>

          {onOpenManager && (
            <div className="mt-3 border-t border-[var(--color-border)] pt-2">
              <button
                type="button"
                onClick={onOpenManager}
                className="flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-hover)] hover:text-[var(--color-text-primary)]"
              >
                <WrenchScrewdriverIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                <span className="truncate">{t('files.manageProfiles')}</span>
              </button>
            </div>
          )}
        </aside>

        <div className="ui-card min-w-0 min-h-[560px] flex flex-col">
          {!browsing.selectedProfile ? (
            <div className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.selectProfile')}</div>
          ) : (
            <>
              {browsing.selection.active ? (
                <div
                  className="sticky top-16 lg:top-20 z-10 flex items-center justify-between gap-2 sm:gap-3 border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-2.5 sm:px-3 py-2 sm:py-2.5 rounded-t-[var(--radius-lg)] -mt-[1px] -mx-[1px] shadow-xs relative before:content-[''] before:hidden lg:before:block before:absolute before:bottom-full before:mb-[1px] before:left-0 before:right-0 before:h-5 before:bg-[var(--color-bg-primary)] before:pointer-events-none"
                  role="toolbar"
                  aria-label={t('files.selectionActions')}
                >
                  <div className="absolute inset-0 bg-[var(--color-selection-bg)]/15 pointer-events-none rounded-t-[inherit]" />
                  <div className="flex items-center gap-1.5 sm:gap-2 min-w-0 relative z-10">
                    <button
                      type="button"
                      onClick={browsing.selection.clear}
                      className="ui-icon-button p-1.5 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] shrink-0"
                      title={t('files.clearSelection')}
                      aria-label={t('files.clearSelection')}
                    >
                      <XMarkIcon className="h-5 w-5" aria-hidden="true" />
                    </button>
                    <span className="text-sm font-semibold text-[var(--color-text-primary)] truncate" title={t('files.selectedCount', { count: browsing.selection.entries.length })}>
                      <span className="hidden sm:inline">
                        {t('files.selectedCount', { count: browsing.selection.entries.length })}
                      </span>
                      <span className="sm:hidden">
                        {t('files.selectedCountShort', { count: browsing.selection.entries.length })}
                      </span>
                    </span>
                  </div>

                  <div className="flex items-center gap-1 sm:gap-2 shrink-0 relative z-10">
                    <button
                      type="button"
                      className="ui-button-secondary inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0"
                      disabled={!actions.permissions.canBatchDownload || actions.batchBusy}
                      onClick={() => void actions.downloadArchive()}
                      title={t('files.downloadArchive')}
                      aria-label={t('files.downloadArchive')}
                    >
                      <ArrowDownTrayIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                      <span className="hidden md:inline">{t('files.downloadArchive')}</span>
                    </button>
                    <button
                      type="button"
                      className="ui-button-secondary inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0"
                      disabled={!actions.permissions.canBatchCopy || actions.batchBusy}
                      onClick={() => actions.copy()}
                      title={t('files.copy')}
                      aria-label={t('files.copy')}
                    >
                      <ClipboardDocumentIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                      <span className="hidden md:inline">{t('files.copy')}</span>
                    </button>
                    <button
                      type="button"
                      className="ui-button-secondary inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0"
                      disabled={!actions.permissions.canBatchMove || actions.batchBusy}
                      onClick={() => actions.move()}
                      title={t('files.move')}
                      aria-label={t('files.move')}
                    >
                      <ArrowsRightLeftIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                      <span className="hidden md:inline">{t('files.move')}</span>
                    </button>
                    <button
                      type="button"
                      className="ui-button-danger inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0"
                      disabled={!actions.permissions.canBatchDelete || actions.batchBusy}
                      onClick={() => actions.delete()}
                      title={t('files.deleteAction')}
                      aria-label={t('files.deleteAction')}
                    >
                      <TrashIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                      <span className="hidden md:inline">{t('files.deleteAction')}</span>
                    </button>
                  </div>
                </div>
              ) : (
                <div className="sticky top-16 lg:top-20 z-10 flex items-center justify-between gap-3 border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3 rounded-t-[var(--radius-lg)] -mt-[1px] -mx-[1px] shadow-xs relative before:content-[''] before:hidden lg:before:block before:absolute before:bottom-full before:mb-[1px] before:left-0 before:right-0 before:h-5 before:bg-[var(--color-bg-primary)] before:pointer-events-none">
                  <div className="flex min-w-0 items-center gap-1.5 flex-1">
                    <button
                      type="button"
                      onClick={goUp}
                      disabled={!browsing.capabilities.browse || browsing.breadcrumbs.length <= 1 || browsing.loading}
                      className="ui-icon-button p-2 hover:bg-[var(--color-hover)] shrink-0 disabled:opacity-40 disabled:cursor-not-allowed"
                      aria-label={t('files.up')}
                      title={t('files.up')}
                    >
                      <ArrowUpIcon className="h-4 w-4" aria-hidden="true" />
                    </button>
                    <nav className="flex min-w-0 flex-nowrap items-center gap-1 text-sm overflow-hidden" aria-label={t('files.breadcrumb')}>
                      {shouldCollapseBreadcrumbs ? (
                        <>
                          {rootBreadcrumb && (
                            <span className="inline-flex min-w-0 items-center gap-1 shrink-0">
                              <button
                                type="button"
                                onClick={() => goToBreadcrumb(0)}
                                className="max-w-28 sm:max-w-36 md:max-w-44 truncate rounded px-1 py-0.5 hover:bg-[var(--color-hover)]"
                                title={rootBreadcrumb.name}
                              >
                                {rootBreadcrumb.name}
                              </button>
                            </span>
                          )}
                          <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />
                          <div className="relative inline-flex items-center shrink-0" ref={breadcrumbMenuRef}>
                            <button
                              type="button"
                              onClick={() => setIsBreadcrumbMenuOpen((open) => !open)}
                              aria-expanded={isBreadcrumbMenuOpen}
                              aria-haspopup="menu"
                              aria-label={t('files.moreFolders')}
                              title={t('files.moreFolders')}
                              className="rounded px-1.5 py-0.5 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] transition-colors"
                            >
                              <EllipsisHorizontalIcon className="h-4 w-4" aria-hidden="true" />
                            </button>
                            {isBreadcrumbMenuOpen && (
                              <div
                                role="menu"
                                aria-label={t('files.moreFolders')}
                                className="absolute left-0 top-full mt-1 z-30 min-w-48 max-w-64 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-primary)] p-1 shadow-lg backdrop-blur-xs"
                              >
                                {collapsedBreadcrumbs.map((crumb, idx) => {
                                  const targetIndex = 1 + idx;
                                  return (
                                    <button
                                      key={crumb.ref ?? targetIndex}
                                      type="button"
                                      role="menuitem"
                                      onClick={() => {
                                        goToBreadcrumb(targetIndex);
                                        setIsBreadcrumbMenuOpen(false);
                                      }}
                                      className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-hover)] hover:text-[var(--color-text-primary)] transition-colors"
                                      title={crumb.name}
                                    >
                                      <FolderIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />
                                      <span className="truncate">{crumb.name}</span>
                                    </button>
                                  );
                                })}
                              </div>
                            )}
                          </div>
                          <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />
                          {parentBreadcrumb && (
                            <span className="inline-flex min-w-0 items-center gap-1 shrink-0">
                              <button
                                type="button"
                                onClick={goUp}
                                className="max-w-28 sm:max-w-36 md:max-w-44 truncate rounded px-1 py-0.5 hover:bg-[var(--color-hover)]"
                                title={parentBreadcrumb.name}
                              >
                                {parentBreadcrumb.name}
                              </button>
                            </span>
                          )}
                          <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />
                          {currentBreadcrumbItem && (
                            <span className="inline-flex min-w-0 items-center gap-1 shrink-0">
                              <button
                                type="button"
                                disabled
                                className="max-w-28 sm:max-w-36 md:max-w-44 truncate rounded px-1 py-0.5 font-semibold text-[var(--color-text-primary)]"
                                title={currentBreadcrumbItem.name}
                              >
                                {currentBreadcrumbItem.name}
                              </button>
                            </span>
                          )}
                        </>
                      ) : (
                        browsing.breadcrumbs.map((breadcrumb, index) => (
                          <span key={breadcrumb.ref ?? 'root'} className="inline-flex min-w-0 items-center gap-1 shrink-0">
                            {index > 0 && <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />}
                            <button
                              type="button"
                              onClick={() => goToBreadcrumb(index)}
                              disabled={index === browsing.breadcrumbs.length - 1}
                              className="max-w-28 sm:max-w-36 md:max-w-44 truncate rounded px-1 py-0.5 disabled:text-[var(--color-text-primary)] disabled:font-semibold hover:bg-[var(--color-hover)]"
                              title={breadcrumb.name}
                            >
                              {breadcrumb.name}
                            </button>
                          </span>
                        ))
                      )}
                    </nav>
                  </div>

                  <div className="flex items-center gap-2 shrink-0">
                    <FileUploadControl
                      apiUrl={apiUrl}
                      token={token}
                      profileId={profileId}
                      parentRef={browsing.currentRef}
                      capabilities={browsing.capabilities}
                      disabled={browsing.loading}
                      onCompleted={browsing.notifyUploadCompleted}
                      transfers={actions.transfers}
                      onCancelTransfer={actions.cancelTransfer}
                      onDismissTransfer={actions.dismissTransfer}
                    />
                    <button
                      type="button"
                      onClick={() => actions.createFolder()}
                      disabled={browsing.loading || !actions.permissions.canCreateFolder}
                      className="ui-button-secondary inline-flex items-center gap-2 px-3 py-2 text-sm disabled:cursor-not-allowed disabled:opacity-50"
                      title={!actions.permissions.canCreateFolder ? t('files.mkdirUnavailable') : t('files.newFolder')}
                      aria-label={t('files.newFolder')}
                    >
                      <FolderPlusIcon className="h-4 w-4" aria-hidden="true" />
                      <span className="hidden sm:inline">{t('files.newFolder')}</span>
                    </button>
                    <div className="flex items-center rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-0.5" role="group" aria-label={t('files.viewMode')}>
                      <button
                        type="button"
                        onClick={() => handleViewModeChange('list')}
                        aria-pressed={viewMode === 'list'}
                        aria-label={t('files.viewList')}
                        title={t('files.viewList')}
                        className={`p-1.5 rounded-md transition-colors ${
                          viewMode === 'list'
                            ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] shadow-xs'
                            : 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'
                        }`}
                      >
                        <ListBulletIcon className="h-4 w-4" aria-hidden="true" />
                      </button>
                      <button
                        type="button"
                        onClick={() => handleViewModeChange('grid')}
                        aria-pressed={viewMode === 'grid'}
                        aria-label={t('files.viewGrid')}
                        title={t('files.viewGrid')}
                        className={`p-1.5 rounded-md transition-colors ${
                          viewMode === 'grid'
                            ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] shadow-xs'
                            : 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'
                        }`}
                      >
                        <Squares2X2Icon className="h-4 w-4" aria-hidden="true" />
                      </button>
                    </div>
                    <button
                      type="button"
                      onClick={browsing.refresh}
                      disabled={!browsing.capabilities.browse}
                      className="ui-icon-button p-2 hover:bg-[var(--color-hover)]"
                      aria-label={t('files.refresh')}
                      title={t('files.refresh')}
                    >
                      <ArrowPathIcon className="h-4 w-4" aria-hidden="true" />
                    </button>
                  </div>
                </div>
              )}
              {actions.batchSummary && (
                <div className="mx-3 mt-3 flex items-center justify-between gap-2 ui-alert px-3 py-2 text-sm" role="status">
                  <span>
                    {t('files.batchResultSummary', actions.batchSummary)}
                  </span>
                  <button
                    type="button"
                    onClick={actions.dismissBatchSummary}
                    className="ui-icon-button p-0.5 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] shrink-0"
                    aria-label={t('common.cancel')}
                    title={t('common.cancel')}
                  >
                    <XMarkIcon className="h-4 w-4" aria-hidden="true" />
                  </button>
                </div>
              )}

              {!browsing.capabilities.browse && !browsing.loading ? (
                <p className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.listUnavailable')}</p>
              ) : browsing.loading ? (
                <div className="p-8 flex-1 flex items-center justify-center"><LoadingIndicator label={t('common.loading')} /></div>
              ) : browsing.entries.length === 0 ? (
                <p className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.emptyDirectory')}</p>
              ) : viewMode === 'list' ? (
                <div className="overflow-x-auto flex-1">
                  <table className="ui-table w-full table-fixed text-sm">
                    <thead className="bg-[var(--color-bg-tertiary)] text-left text-xs text-[var(--color-text-secondary)]">
                      <tr>
                        <th scope="col" className="w-10 px-3 py-2 font-medium"><input ref={selectAllRef} type="checkbox" checked={browsing.selection.allSelected} onChange={browsing.selection.toggleAll} aria-label={t('files.selectAll')} /></th>
                        <th
                          scope="col"
                          aria-sort={ariaSortFor('name')}
                          className="px-3 py-2 font-medium"
                        >
                          {renderSortHeader('name', t('files.name'), t('files.sortByName'))}
                        </th>
                        <th
                          scope="col"
                          aria-sort={ariaSortFor('size')}
                          className="hidden sm:table-cell w-28 sm:w-32 px-3 py-2 font-medium whitespace-nowrap shrink-0"
                        >
                          {renderSortHeader('size', t('files.size'), t('files.sortBySize'))}
                        </th>
                        <th
                          scope="col"
                          aria-sort={ariaSortFor('modified')}
                          className="hidden sm:table-cell w-36 sm:w-44 px-3 py-2 font-medium whitespace-nowrap shrink-0"
                        >
                          {renderSortHeader('modified', t('files.modified'), t('files.sortByModified'))}
                        </th>
                        <th scope="col" className="w-12 sm:w-16 px-2 sm:px-3 py-2 font-medium text-right shrink-0"><span className="sr-only">{t('files.actions')}</span></th>
                      </tr>
                    </thead>
                    <tbody>
                      {browsing.entries.map((entry) => {
                        const isInteractive = browsing.isInteractive(entry);
                        const isSelected = browsing.selection.isSelected(entry.ref);
                        return (
                          <tr
                            key={entry.ref}
                            aria-selected={browsing.selection.active ? isSelected : undefined}
                            onClick={(event) => handleEntryClick(entry, event)}
                            onContextMenu={(event) => handleContextMenu(event, entry)}
                            className={`border-t border-[var(--color-border)] transition-colors ${
                              isSelected
                                ? 'bg-[var(--color-selection-bg)]/20 hover:bg-[var(--color-selection-bg)]/30'
                                : 'hover:bg-[var(--color-hover)]'
                            } ${isInteractive || browsing.selection.active ? 'cursor-pointer' : ''}`}
                          >
                            <td className="w-10 px-3 py-2"><input type="checkbox" checked={isSelected} onClick={(event) => event.stopPropagation()} onChange={() => browsing.selection.toggle(entry.ref)} aria-label={t('files.selectEntry', { name: entry.name })} /></td>
                            <td data-label={t('files.name')} className="px-3 py-2 min-w-0 max-w-0">
                              <button
                                type="button"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  handleEntryClick(entry, e);
                                }}
                                disabled={!browsing.selection.active && entry.kind === 'directory' && (!browsing.capabilities.browse || browsing.loading)}
                                className="inline-flex w-full items-center gap-2.5 sm:gap-3 min-w-0 text-left ui-link disabled:cursor-not-allowed disabled:opacity-55"
                                title={entry.name}
                              >
                                <div className="relative w-9 h-9 sm:w-10 sm:h-10 shrink-0 rounded-lg overflow-hidden bg-[var(--color-bg-tertiary)]/40 border border-[var(--color-border)]/60 flex items-center justify-center">
                                  <FileThumbnail
                                    apiUrl={apiUrl}
                                    token={token}
                                    profileId={profileId}
                                    entry={entry}
                                    thumbnailsEnabled={browsing.capabilities.thumbnails}
                                    size="sm"
                                    className="w-full h-full flex items-center justify-center"
                                    imageClassName="w-full h-full object-cover"
                                    fallbackIconClassName="h-5 w-5"
                                  />
                                </div>
                                <div className="min-w-0 flex-1">
                                  <span className="truncate block font-medium text-sm text-[var(--color-text-primary)]">{entry.name}</span>
                                  <div className="flex items-center gap-1.5 text-[11px] text-[var(--color-text-secondary)] sm:hidden mt-0.5 truncate font-normal">
                                    <span>{entry.kind === 'directory' ? t('files.directory') : formatBytes(entry.size)}</span>
                                    {entry.modified_at && (
                                      <>
                                        <span aria-hidden="true">•</span>
                                        <span className="truncate">{formatDateTime(entry.modified_at)}</span>
                                      </>
                                    )}
                                  </div>
                                </div>
                              </button>
                            </td>
                            <td data-label={t('files.size')} className="hidden sm:table-cell w-28 sm:w-32 px-3 py-2 text-[var(--color-text-secondary)] whitespace-nowrap shrink-0">
                              {entry.kind === 'directory' ? t('files.directory') : formatBytes(entry.size)}
                            </td>
                            <td data-label={t('files.modified')} className="hidden sm:table-cell w-36 sm:w-44 px-3 py-2 text-[var(--color-text-secondary)] whitespace-nowrap shrink-0">
                              {entry.modified_at ? formatDateTime(entry.modified_at) : t('common.unspecified')}
                            </td>
                            <td data-label={t('files.actions')} className="w-12 sm:w-16 px-2 sm:px-3 py-2 text-right whitespace-nowrap shrink-0">
                              {actions.permissions.hasEntryActions(entry) && (
                                <button
                                  type="button"
                                  className="ui-icon-button p-2 hover:bg-[var(--color-hover)]"
                                  aria-label={t('files.actionsFor', { name: entry.name })}
                                  aria-haspopup="menu"
                                  aria-expanded={menuState?.entry.ref === entry.ref}
                                  onClick={(event) => toggleMenu(event, entry)}
                                >
                                  <EllipsisVerticalIcon className="h-5 w-5" aria-hidden="true" />
                                </button>
                              )}
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              ) : (
                <div className="p-4 grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-3 flex-1 auto-rows-max" role="grid" aria-label={t('files.title')}>
                  {browsing.entries.map((entry) => {
                    const isInteractive = browsing.isInteractive(entry);
                    const isSelected = browsing.selection.isSelected(entry.ref);

                    return (
                      <div
                        key={entry.ref}
                        role="gridcell"
                        aria-selected={browsing.selection.active ? isSelected : undefined}
                        tabIndex={isInteractive || browsing.selection.active ? 0 : undefined}
                        onClick={(event) => handleEntryClick(entry, event)}
                        onContextMenu={(event) => handleContextMenu(event, entry)}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter' || e.key === ' ') {
                            e.preventDefault();
                            handleEntryClick(entry, e);
                          }
                        }}
                        className={`group isolate relative flex flex-col justify-between rounded-xl border transition-all ${
                          isSelected
                            ? 'border-[var(--color-text-primary)] ring-2 ring-[var(--color-text-primary)]/20 bg-[var(--color-selection-bg)]/20 shadow-xs'
                            : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)] hover:bg-[var(--color-hover)] hover:border-[var(--color-border-hover,var(--color-border))] hover:shadow-xs'
                        } ${
                          isInteractive || browsing.selection.active ? 'cursor-pointer focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-focus)]' : 'opacity-70'
                        }`}
                        title={entry.name}
                      >
                        <input type="checkbox" className="absolute left-2 top-2 z-10" checked={isSelected} onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()} onChange={() => browsing.selection.toggle(entry.ref)} aria-label={t('files.selectEntry', { name: entry.name })} />
                        {actions.permissions.hasEntryActions(entry) && (
                          <div className="absolute right-2 top-2 z-10 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
                            <button
                              type="button"
                              className="ui-icon-button p-1.5 rounded-lg bg-[var(--color-bg-primary)]/80 backdrop-blur-xs border border-[var(--color-border)]/60 shadow-xs hover:bg-[var(--color-hover)]"
                              aria-label={t('files.actionsFor', { name: entry.name })}
                              aria-haspopup="menu"
                              aria-expanded={menuState?.entry.ref === entry.ref}
                              onClick={(event) => toggleMenu(event, entry)}
                            >
                              <EllipsisVerticalIcon className="h-4 w-4" aria-hidden="true" />
                            </button>
                          </div>
                        )}
                        <div className="relative w-full aspect-[4/3] flex items-center justify-center bg-[var(--color-bg-tertiary)]/40 overflow-hidden">
                          <FileThumbnail
                            apiUrl={apiUrl}
                            token={token}
                            profileId={profileId}
                            entry={entry}
                            thumbnailsEnabled={browsing.capabilities.thumbnails}
                            size="lg"
                            className="w-full h-full flex items-center justify-center"
                            imageClassName="w-full h-full object-cover"
                            fallbackIconClassName="h-12 w-12 drop-shadow-xs"
                          />
                        </div>
                        <div className="w-full min-w-0 p-2.5 flex flex-col text-left border-t border-[var(--color-border)]/60 bg-[var(--color-bg-secondary)]">
                          <span className="w-full truncate text-xs font-medium text-[var(--color-text-primary)]">
                            {entry.name}
                          </span>
                          <span className="text-[11px] text-[var(--color-text-secondary)] mt-0.5 truncate">
                            {entry.kind === 'directory' ? t('files.directory') : formatBytes(entry.size)}
                          </span>
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}

              {browsing.hasMore && (
                <div ref={sentinelRef} className="flex justify-center p-4 border-t border-[var(--color-border)]">
                  {browsing.loadingMore ? (
                    <LoadingIndicator label={t('common.loading')} size="sm" />
                  ) : (
                    <button
                      type="button"
                      onClick={() => void browsing.loadMore()}
                      className="ui-button-secondary inline-flex items-center gap-1.5 px-3 py-1.5 text-xs text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                    >
                      <ArrowPathIcon className="h-3.5 w-3.5" aria-hidden="true" />
                      {t('files.loadMore')}
                    </button>
                  )}
                </div>
              )}
            </>
          )}
        </div>
      </div>
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
      {renderActionMenuPortal()}
    </section>
  );
}
