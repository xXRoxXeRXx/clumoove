import { useCallback, useEffect, useRef, useState, lazy, Suspense } from 'react';
import { createPortal } from 'react-dom';
import {
  ArrowDownTrayIcon,
  ArrowLeftIcon,
  ArrowPathIcon,
  ArrowUpIcon,
  ChevronRightIcon,
  FileIcon,
  FolderIcon,
  FolderPlusIcon,
  ListBulletIcon,
  ProviderIcon,
  Squares2X2Icon,
  WrenchScrewdriverIcon,
} from '../icons';
import { useTranslation } from 'react-i18next';
import { createDirectory, getFileCapabilities, listFileEntries, createDownloadTicket, type FileBreadcrumb, type FileCapabilities, type FileEntry } from '../../api/files';
import { listConnectionProfiles, type ConnectionProfilePublic } from '../../api/profiles';
import { LoadingIndicator } from '../LoadingIndicator';
import { useApiError } from '../../utils/apiError';
import { useFormat } from '../../utils/format';
import { useFocusTrap } from '../../hooks/useFocusTrap';
import { FileUploadControl } from './FileUploadControl';
import { canPreview } from './filePreview';

const FilePreviewDialog = lazy(() => import('./FilePreviewDialog').then((m) => ({ default: m.FilePreviewDialog })));

type Breadcrumb = {
  ref: string | null;
  name: string;
};

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

const unavailableCapabilities: FileCapabilities = {
  browse: false,
  native_pagination: false,
  download: false,
  upload: false,
  mkdir: false,
  rename: false,
  move: false,
  delete_file: false,
  delete_empty_directory: false,
  delete_recursive_directory: false,
  conflict_skip: false,
  conflict_overwrite: false,
  conflict_overwrite_atomic: false,
  conflict_rename: false,
  native_copy: false,
  range_download: false,
  thumbnails: false,
};

export function FileManager({ apiUrl, token, profileId, initialBreadcrumbs, initialPathFallback = false, onProfileChange, onOpenManager, onBack }: FileManagerProps) {
  const { t } = useTranslation();
  const { formatBytes, formatDateTime } = useFormat();
  const translateApiError = useApiError();
  const [profiles, setProfiles] = useState<ConnectionProfilePublic[]>([]);
  const [profilesLoading, setProfilesLoading] = useState(true);
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [entriesLoading, setEntriesLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [capabilities, setCapabilities] = useState<FileCapabilities>(unavailableCapabilities);
  const [breadcrumbs, setBreadcrumbs] = useState<Breadcrumb[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [error, setError] = useState('');
  const [downloadingRef, setDownloadingRef] = useState<string | null>(null);
  const [previewEntry, setPreviewEntry] = useState<FileEntry | null>(null);
  const [isCreateDirOpen, setIsCreateDirOpen] = useState(false);
  const [newDirName, setNewDirName] = useState('');
  const [creatingDir, setCreatingDir] = useState(false);
  const [createDirError, setCreateDirError] = useState('');
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
  const createDirDialogRef = useRef<HTMLDivElement>(null);
  const createDirCancelRef = useRef<HTMLButtonElement>(null);
  const profileRequestRef = useRef<AbortController | null>(null);
  const entriesRequestRef = useRef<AbortController | null>(null);
  const latestEntriesRequestRef = useRef(0);
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  const uploadRefreshTimeoutRef = useRef<number | null>(null);

  useFocusTrap(createDirDialogRef, createDirCancelRef, () => {
    if (!creatingDir) {
      setIsCreateDirOpen(false);
      setNewDirName('');
      setCreateDirError('');
    }
  }, isCreateDirOpen);

  useEffect(() => {
    return () => {
      if (uploadRefreshTimeoutRef.current !== null) {
        window.clearTimeout(uploadRefreshTimeoutRef.current);
      }
    };
  }, []);

  const selectedProfile = profiles.find((profile) => profile.id === profileId) ?? null;
  const currentBreadcrumb = breadcrumbs[breadcrumbs.length - 1];
  const currentRef = currentBreadcrumb?.ref ?? null;

  const loadEntries = useCallback(async (parentRef: string | null) => {
    if (!profileId || !capabilities.browse) return;
    entriesRequestRef.current?.abort();
    const controller = new AbortController();
    entriesRequestRef.current = controller;
    const request = ++latestEntriesRequestRef.current;
    setEntriesLoading(true);
    setError('');
    const result = await listFileEntries(apiUrl, token, profileId, parentRef, undefined, controller.signal);
    if (controller.signal.aborted || latestEntriesRequestRef.current !== request) return;
    if (result.ok === false) {
      setEntries([]);
      setNextCursor(null);
      setError(translateApiError(result.errorCode));
    } else {
      setEntries(result.data.entries ?? []);
      setNextCursor(result.data.next_cursor ?? null);
    }
    setEntriesLoading(false);
  }, [apiUrl, capabilities.browse, profileId, token, translateApiError]);

  const loadMore = useCallback(async () => {
    if (!profileId || !capabilities.browse || !nextCursor || entriesLoading || loadingMore) return;
    setLoadingMore(true);
    const controller = new AbortController();
    const result = await listFileEntries(apiUrl, token, profileId, currentRef, nextCursor, controller.signal);
    if (result.ok === false) {
      setError(translateApiError(result.errorCode));
    } else {
      setEntries((current) => [...current, ...(result.data.entries ?? [])]);
      setNextCursor(result.data.next_cursor ?? null);
    }
    setLoadingMore(false);
  }, [apiUrl, capabilities.browse, currentRef, entriesLoading, loadingMore, nextCursor, profileId, token, translateApiError]);

  const loadProfiles = useCallback(async () => {
    profileRequestRef.current?.abort();
    const controller = new AbortController();
    profileRequestRef.current = controller;
    setProfilesLoading(true);
    const result = await listConnectionProfiles(apiUrl, token, controller.signal);
    if (controller.signal.aborted) return;
    if (result.ok === true) {
      setProfiles(result.data.profiles ?? []);
    } else {
      setProfiles([]);
      setError(translateApiError(result.errorCode));
    }
    setProfilesLoading(false);
  }, [apiUrl, token, translateApiError]);

  useEffect(() => {
    const timeoutId = window.setTimeout(() => void loadProfiles(), 0);
    return () => {
      window.clearTimeout(timeoutId);
      profileRequestRef.current?.abort();
      entriesRequestRef.current?.abort();
    };
  }, [loadProfiles]);

  useEffect(() => {
    let controller: AbortController | null = null;
    const timeoutId = window.setTimeout(() => {
      if (!selectedProfile) {
        setCapabilities(unavailableCapabilities);
        setBreadcrumbs([]);
        setEntries([]);
        setNextCursor(null);
        setEntriesLoading(false);
        return;
      }

      controller = new AbortController();
      entriesRequestRef.current?.abort();
      entriesRequestRef.current = controller;
      const request = ++latestEntriesRequestRef.current;

      const resolvedBreadcrumbs = initialBreadcrumbs?.length
        ? initialBreadcrumbs.map((breadcrumb) => ({ ref: breadcrumb.ref || null, name: breadcrumb.name }))
        : [{ ref: null, name: selectedProfile.name }];
      setBreadcrumbs(resolvedBreadcrumbs);
      setEntries([]);
      setNextCursor(null);
      setError('');
      setEntriesLoading(true);

      const targetRef = resolvedBreadcrumbs[resolvedBreadcrumbs.length - 1]?.ref ?? null;

      async function initProfile() {
        const capResult = await getFileCapabilities(apiUrl, token, selectedProfile!.id, controller!.signal);
        if (controller!.signal.aborted || latestEntriesRequestRef.current !== request) return;

        if (capResult.ok === false) {
          setCapabilities(unavailableCapabilities);
          setEntries([]);
          setNextCursor(null);
          setError(translateApiError(capResult.errorCode));
          setEntriesLoading(false);
          return;
        }

        setCapabilities(capResult.data.capabilities);

        if (!capResult.data.capabilities.browse) {
          setEntries([]);
          setNextCursor(null);
          setEntriesLoading(false);
          return;
        }

        const listResult = await listFileEntries(apiUrl, token, selectedProfile!.id, targetRef, undefined, controller!.signal);
        if (controller!.signal.aborted || latestEntriesRequestRef.current !== request) return;

        if (listResult.ok === false) {
          setEntries([]);
          setNextCursor(null);
          setError(translateApiError(listResult.errorCode));
        } else {
          setEntries(listResult.data.entries ?? []);
          setNextCursor(listResult.data.next_cursor ?? null);
        }
        setEntriesLoading(false);
      }

      void initProfile();
    }, 0);

    return () => {
      window.clearTimeout(timeoutId);
      controller?.abort();
    };
  }, [apiUrl, initialBreadcrumbs, selectedProfile, token, translateApiError]);

  useEffect(() => {
    if (!nextCursor || entriesLoading || loadingMore || !capabilities.browse) return;
    const target = sentinelRef.current;
    if (!target || typeof IntersectionObserver === 'undefined') return;

    const observer = new IntersectionObserver(
      (observedEntries) => {
        if (observedEntries[0]?.isIntersecting) {
          void loadMore();
        }
      },
      { rootMargin: '250px' }
    );

    observer.observe(target);
    return () => observer.disconnect();
  }, [capabilities.browse, entriesLoading, loadMore, loadingMore, nextCursor]);

  const selectProfile = (id: string) => {
    if (id === profileId) return;
    onProfileChange(id);
  };

  const openDirectory = (entry: FileEntry) => {
    if (entry.kind !== 'directory' || !capabilities.browse) return;
    setBreadcrumbs((current) => [...current, { ref: entry.ref, name: entry.name }]);
    void loadEntries(entry.ref);
  };

  const openEntry = (entry: FileEntry) => {
    if (entry.kind === 'directory') {
      openDirectory(entry);
      return;
    }
    if (entry.allowed_actions.includes('download') && capabilities.download && canPreview(entry)) {
      setPreviewEntry(entry);
    }
  };

  const goUp = () => {
    if (breadcrumbs.length <= 1) return;
    const parentBreadcrumbs = breadcrumbs.slice(0, -1);
    const parentRef = parentBreadcrumbs[parentBreadcrumbs.length - 1]?.ref ?? null;
    setBreadcrumbs(parentBreadcrumbs);
    void loadEntries(parentRef);
  };

  const refresh = () => {
    if (!capabilities.browse) return;
    void loadEntries(currentRef);
  };

  const goToBreadcrumb = (index: number) => {
    if (index === breadcrumbs.length - 1) return;
    const targetBreadcrumbs = breadcrumbs.slice(0, index + 1);
    const targetRef = targetBreadcrumbs[targetBreadcrumbs.length - 1]?.ref ?? null;
    setBreadcrumbs(targetBreadcrumbs);
    void loadEntries(targetRef);
  };

  const download = async (entry: FileEntry) => {
    if (entry.kind === 'directory' || !capabilities.download || downloadingRef) return;
    const controller = new AbortController();
    setDownloadingRef(entry.ref);
    setError('');
    const result = await createDownloadTicket(apiUrl, token, profileId, entry.ref, controller.signal);
    if (result.ok === false) {
      setError(translateApiError(result.errorCode));
    } else {
      window.location.assign(new URL(result.data.download_url, apiUrl).toString());
    }
    setDownloadingRef(null);
  };

  const uploadCompleted = useCallback((completedProfileID: string) => {
    if (completedProfileID !== profileId) return;
    if (uploadRefreshTimeoutRef.current !== null) {
      window.clearTimeout(uploadRefreshTimeoutRef.current);
    }
    uploadRefreshTimeoutRef.current = window.setTimeout(() => {
      uploadRefreshTimeoutRef.current = null;
      void loadEntries(currentRef);
    }, 500);
  }, [currentRef, loadEntries, profileId]);

  const handleCreateDirectory = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = newDirName.trim();
    if (!trimmed || creatingDir || !capabilities.mkdir) return;
    setCreatingDir(true);
    setCreateDirError('');
    const result = await createDirectory(apiUrl, token, profileId, trimmed, currentRef);
    if (result.ok === false) {
      setCreateDirError(translateApiError(result.errorCode));
      setCreatingDir(false);
    } else {
      setCreatingDir(false);
      setIsCreateDirOpen(false);
      setNewDirName('');
      setCreateDirError('');
      void loadEntries(currentRef);
    }
  };

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

      {error && <p className="ui-alert ui-alert-error px-3 py-2 text-sm" role="alert">{error}</p>}
      {initialPathFallback && <p className="ui-alert px-3 py-2 text-sm" role="status">{t('files.pathFallback')}</p>}

      <div className="grid gap-5 lg:grid-cols-[15rem_minmax(0,1fr)]">
        <aside className="ui-card flex flex-col justify-between p-3" aria-label={t('files.profiles')}>
          <div>
            <div className="mb-2 flex items-center justify-between gap-2 px-2">
              <h2 className="text-sm font-semibold">{t('files.profiles')}</h2>
              <button type="button" onClick={() => void loadProfiles()} disabled={profilesLoading} className="ui-icon-button p-1.5 hover:bg-[var(--color-hover)]" aria-label={t('files.refresh')} title={t('files.refresh')}>
                <ArrowPathIcon className="h-4 w-4" aria-hidden="true" />
              </button>
            </div>
            {profilesLoading ? (
              <div className="px-2 py-4"><LoadingIndicator label={t('common.loading')} size="sm" /></div>
            ) : profiles.length === 0 ? (
              <p className="ui-empty px-2 py-4 text-sm">{t('files.noProfiles')}</p>
            ) : (
              <ul className="space-y-1" aria-label={t('files.profiles')}>
                {profiles.map((profile) => (
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

        <div className="ui-card min-w-0 overflow-hidden min-h-[400px] flex flex-col">
          {!selectedProfile ? (
            <div className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.selectProfile')}</div>
          ) : (
            <>
              <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--color-border)] p-3">
                <div className="flex min-w-0 items-center gap-1.5 flex-1">
                  <button
                    type="button"
                    onClick={goUp}
                    disabled={!capabilities.browse || breadcrumbs.length <= 1 || entriesLoading}
                    className="ui-icon-button p-2 hover:bg-[var(--color-hover)] shrink-0 disabled:opacity-40 disabled:cursor-not-allowed"
                    aria-label={t('files.up')}
                    title={t('files.up')}
                  >
                    <ArrowUpIcon className="h-4 w-4" aria-hidden="true" />
                  </button>
                  <nav className="flex min-w-0 flex-wrap items-center gap-1 text-sm" aria-label={t('files.breadcrumb')}>
                    {breadcrumbs.map((breadcrumb, index) => (
                      <span key={breadcrumb.ref ?? 'root'} className="inline-flex min-w-0 items-center gap-1">
                        {index > 0 && <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />}
                        <button
                          type="button"
                          onClick={() => goToBreadcrumb(index)}
                          disabled={index === breadcrumbs.length - 1}
                          className="max-w-44 truncate rounded px-1 py-0.5 disabled:text-[var(--color-text-primary)] hover:bg-[var(--color-hover)]"
                        >
                          {breadcrumb.name}
                        </button>
                      </span>
                    ))}
                  </nav>
                </div>
                <div className="flex items-center gap-2 shrink-0">
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
                  <FileUploadControl
                    apiUrl={apiUrl}
                    token={token}
                    profileId={profileId}
                    parentRef={currentRef}
                    capabilities={capabilities}
                    disabled={entriesLoading}
                    onCompleted={uploadCompleted}
                  />
                  <button
                    type="button"
                    onClick={() => {
                      setIsCreateDirOpen(true);
                      setNewDirName('');
                      setCreateDirError('');
                    }}
                    disabled={entriesLoading || !capabilities.mkdir}
                    className="ui-button-secondary inline-flex items-center gap-2 px-3 py-2 text-sm disabled:cursor-not-allowed disabled:opacity-50"
                    title={!capabilities.mkdir ? t('files.mkdirUnavailable') : t('files.newFolder')}
                    aria-label={t('files.newFolder')}
                  >
                    <FolderPlusIcon className="h-4 w-4" aria-hidden="true" />
                    <span className="hidden sm:inline">{t('files.newFolder')}</span>
                  </button>
                  <button
                    type="button"
                    onClick={refresh}
                    disabled={!capabilities.browse}
                    className="ui-icon-button p-2 hover:bg-[var(--color-hover)]"
                    aria-label={t('files.refresh')}
                    title={t('files.refresh')}
                  >
                    <ArrowPathIcon className="h-4 w-4" aria-hidden="true" />
                  </button>
                </div>
              </div>

              {!capabilities.browse && !entriesLoading ? (
                <p className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.listUnavailable')}</p>
              ) : entriesLoading ? (
                <div className="p-8 flex-1 flex items-center justify-center"><LoadingIndicator label={t('common.loading')} /></div>
              ) : entries.length === 0 ? (
                <p className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.emptyDirectory')}</p>
              ) : viewMode === 'list' ? (
                <div className="overflow-x-auto flex-1">
                  <table className="ui-table ui-responsive-table w-full table-fixed text-sm">
                    <thead className="bg-[var(--color-bg-tertiary)] text-left text-xs text-[var(--color-text-secondary)]">
                      <tr>
                        <th scope="col" className="px-3 py-2 font-medium">{t('files.name')}</th>
                        <th scope="col" className="w-28 sm:w-32 px-3 py-2 font-medium whitespace-nowrap shrink-0">{t('files.size')}</th>
                        <th scope="col" className="w-36 sm:w-44 px-3 py-2 font-medium whitespace-nowrap shrink-0">{t('files.modified')}</th>
                        <th scope="col" className="w-14 sm:w-16 px-3 py-2 font-medium text-right shrink-0"><span className="sr-only">{t('files.actions')}</span></th>
                      </tr>
                    </thead>
                    <tbody>
                      {entries.map((entry) => {
                        const isInteractive = (entry.kind === 'directory' && capabilities.browse && !entriesLoading) || (entry.kind === 'file' && canPreview(entry));
                        return (
                          <tr
                            key={entry.ref}
                            onClick={() => isInteractive && openEntry(entry)}
                            className={`border-t border-[var(--color-border)] hover:bg-[var(--color-hover)] transition-colors ${isInteractive ? 'cursor-pointer' : ''}`}
                          >
                            <td data-label={t('files.name')} className="px-3 py-2 min-w-0 max-w-0">
                              <button
                                type="button"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  openEntry(entry);
                                }}
                                disabled={(entry.kind === 'directory' && (!capabilities.browse || entriesLoading)) || (entry.kind === 'file' && !canPreview(entry))}
                                className={`inline-flex max-w-full items-center gap-2 min-w-0 text-left ${entry.kind === 'directory' || canPreview(entry) ? 'ui-link disabled:cursor-not-allowed disabled:opacity-55' : ''}`}
                                title={entry.name}
                              >
                                <FileIcon name={entry.name} mimeType={entry.mime_type} isDir={entry.kind === 'directory'} className="h-5 w-5 shrink-0" />
                                <span className="truncate font-medium">{entry.name}</span>
                              </button>
                            </td>
                            <td data-label={t('files.size')} className="w-28 sm:w-32 px-3 py-2 text-[var(--color-text-secondary)] whitespace-nowrap shrink-0">
                              {entry.kind === 'directory' ? t('files.directory') : formatBytes(entry.size)}
                            </td>
                            <td data-label={t('files.modified')} className="w-36 sm:w-44 px-3 py-2 text-[var(--color-text-secondary)] whitespace-nowrap shrink-0">
                              {entry.modified_at ? formatDateTime(entry.modified_at) : t('common.unspecified')}
                            </td>
                            <td data-label={t('files.actions')} className="w-14 sm:w-16 px-3 py-2 text-right whitespace-nowrap shrink-0">
                              <button
                                type="button"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  void download(entry);
                                }}
                                disabled={entry.kind === 'directory' || !entry.allowed_actions.includes('download') || !capabilities.download || downloadingRef !== null}
                                className="ui-icon-button p-2 hover:bg-[var(--color-hover)]"
                                aria-label={t('files.download', { name: entry.name })}
                                title={t('files.download', { name: entry.name })}
                              >
                                <ArrowDownTrayIcon className="h-4 w-4" aria-hidden="true" />
                              </button>
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              ) : (
                <div className="p-4 grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-3 flex-1 auto-rows-max" role="grid" aria-label={t('files.title')}>
                  {entries.map((entry) => {
                    const isInteractive = (entry.kind === 'directory' && capabilities.browse && !entriesLoading) || (entry.kind === 'file' && canPreview(entry));
                    const canDownload = entry.kind === 'file' && entry.allowed_actions.includes('download') && capabilities.download;

                    return (
                      <div
                        key={entry.ref}
                        role="gridcell"
                        tabIndex={isInteractive ? 0 : undefined}
                        onClick={() => isInteractive && openEntry(entry)}
                        onKeyDown={(e) => {
                          if ((e.key === 'Enter' || e.key === ' ') && isInteractive) {
                            e.preventDefault();
                            openEntry(entry);
                          }
                        }}
                        className={`group relative flex flex-col items-center justify-between rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3 text-center transition-all hover:bg-[var(--color-hover)] hover:border-[var(--color-border-hover,var(--color-border))] hover:shadow-xs ${
                          isInteractive ? 'cursor-pointer focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-focus)]' : 'opacity-70'
                        }`}
                        title={entry.name}
                      >
                        {canDownload && (
                          <button
                            type="button"
                            onClick={(e) => {
                              e.stopPropagation();
                              void download(entry);
                            }}
                            disabled={downloadingRef !== null}
                            className="ui-icon-button absolute top-2 right-2 p-1.5 opacity-0 group-hover:opacity-100 focus:opacity-100 transition-opacity bg-[var(--color-bg-tertiary)]/90 hover:bg-[var(--color-hover)] rounded-md z-10"
                            aria-label={t('files.download', { name: entry.name })}
                            title={t('files.download', { name: entry.name })}
                          >
                            <ArrowDownTrayIcon className="h-3.5 w-3.5" aria-hidden="true" />
                          </button>
                        )}
                        <div className="my-2 flex items-center justify-center h-14 w-14">
                          <FileIcon
                            name={entry.name}
                            mimeType={entry.mime_type}
                            isDir={entry.kind === 'directory'}
                            className="h-12 w-12 shrink-0 drop-shadow-xs"
                          />
                        </div>
                        <div className="w-full min-w-0 flex flex-col items-center mt-1">
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

              {nextCursor && (
                <div ref={sentinelRef} className="flex justify-center p-4 border-t border-[var(--color-border)]">
                  {loadingMore ? (
                    <LoadingIndicator label={t('common.loading')} size="sm" />
                  ) : (
                    <button
                      type="button"
                      onClick={() => void loadMore()}
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
      {isCreateDirOpen &&
        createPortal(
          <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
            <div
              ref={createDirDialogRef}
              role="dialog"
              aria-modal="true"
              aria-labelledby="create-dir-title"
              tabIndex={-1}
              className="ui-card w-full max-w-md p-5"
            >
              <h2 id="create-dir-title" className="text-lg font-semibold text-[var(--color-text-primary)]">
                {t('files.newFolder')}
              </h2>
              {createDirError && (
                <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">
                  {createDirError}
                </p>
              )}
              <form onSubmit={handleCreateDirectory} className="mt-4 space-y-4">
                <div>
                  <label htmlFor="new-folder-name-input" className="block text-xs font-semibold text-[var(--color-text-secondary)] uppercase tracking-wider mb-1">
                    {t('files.folderName')}
                  </label>
                  <input
                    id="new-folder-name-input"
                    type="text"
                    value={newDirName}
                    onChange={(e) => setNewDirName(e.target.value)}
                    placeholder={t('files.folderNamePlaceholder')}
                    disabled={creatingDir}
                    className="ui-input w-full py-2 px-3 text-sm"
                    autoFocus
                  />
                </div>
                <div className="flex justify-end gap-2 pt-2">
                  <button
                    ref={createDirCancelRef}
                    type="button"
                    onClick={() => {
                      setIsCreateDirOpen(false);
                      setNewDirName('');
                      setCreateDirError('');
                    }}
                    disabled={creatingDir}
                    className="ui-button-secondary px-3 py-2 text-sm"
                  >
                    {t('common.cancel')}
                  </button>
                  <button
                    type="submit"
                    disabled={!newDirName.trim() || creatingDir}
                    className="ui-button-primary inline-flex items-center gap-2 px-3 py-2 text-sm disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {creatingDir ? (
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
            </div>
          </div>,
          document.body
        )}
      {previewEntry && (
        <Suspense fallback={null}>
          <FilePreviewDialog apiUrl={apiUrl} token={token} profileId={profileId} entry={previewEntry} onClose={() => setPreviewEntry(null)} onDownload={(entry) => void download(entry)} />
        </Suspense>
      )}
    </section>
  );
}

