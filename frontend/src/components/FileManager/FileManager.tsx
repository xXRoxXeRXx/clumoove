import { useCallback, useEffect, useRef, useState, lazy, Suspense } from 'react';
import {
  ArrowDownTrayIcon,
  ArrowLeftIcon,
  ArrowPathIcon,
  ArrowUpIcon,
  ChevronRightIcon,
  DocumentIcon,
  FolderIcon,
  WrenchScrewdriverIcon,
} from '@heroicons/react/24/outline';
import { useTranslation } from 'react-i18next';
import { getFileCapabilities, listFileEntries, createDownloadTicket, type FileBreadcrumb, type FileCapabilities, type FileEntry } from '../../api/files';
import { listConnectionProfiles, type ConnectionProfilePublic } from '../../api/profiles';
import { LoadingIndicator } from '../LoadingIndicator';
import { useApiError } from '../../utils/apiError';
import { useFormat } from '../../utils/format';
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
  const profileRequestRef = useRef<AbortController | null>(null);
  const entriesRequestRef = useRef<AbortController | null>(null);
  const latestEntriesRequestRef = useRef(0);
  const sentinelRef = useRef<HTMLDivElement | null>(null);

  const selectedProfile = profiles.find((profile) => profile.id === profileId) ?? null;
  const currentBreadcrumb = breadcrumbs[breadcrumbs.length - 1];
  const currentRef = currentBreadcrumb?.ref ?? null;

  const loadEntries = useCallback(async (parentRef: string | null) => {
    if (!profileId || !capabilities.browse) return;
    entriesRequestRef.current?.abort();
    const controller = new AbortController();
    entriesRequestRef.current = controller;
    const request = latestEntriesRequestRef.current + 1;
    latestEntriesRequestRef.current = request;
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
      setEntries([]);
      setNextCursor(null);
      setError('');
      entriesRequestRef.current?.abort();
      if (!selectedProfile) {
        setCapabilities(unavailableCapabilities);
        setBreadcrumbs([]);
        return;
      }

      const resolvedBreadcrumbs = initialBreadcrumbs?.length
        ? initialBreadcrumbs.map((breadcrumb) => ({ ref: breadcrumb.ref || null, name: breadcrumb.name }))
        : [{ ref: null, name: selectedProfile.name }];
      setBreadcrumbs(resolvedBreadcrumbs);
      controller = new AbortController();
      void getFileCapabilities(apiUrl, token, selectedProfile.id, controller.signal).then((result) => {
        if (controller?.signal.aborted) return;
        if (result.ok === false) {
          setCapabilities(unavailableCapabilities);
          setError(translateApiError(result.errorCode));
          return;
        }
        setCapabilities(result.data.capabilities);
      });
    }, 0);
    return () => {
      window.clearTimeout(timeoutId);
      controller?.abort();
    };
  }, [apiUrl, initialBreadcrumbs, selectedProfile, token, translateApiError]);

  useEffect(() => {
    if (!selectedProfile || !capabilities.browse || breadcrumbs.length === 0) return;
    const timeoutId = window.setTimeout(() => void loadEntries(currentRef), 0);
    return () => window.clearTimeout(timeoutId);
  }, [breadcrumbs.length, capabilities.browse, currentRef, loadEntries, selectedProfile]);

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
    onProfileChange(id);
  };

  const openDirectory = (entry: FileEntry) => {
    if (entry.kind !== 'directory' || !capabilities.browse || entriesLoading) return;
    setBreadcrumbs((current) => [...current, { ref: entry.ref, name: entry.name }]);
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
    if (breadcrumbs.length <= 1 || entriesLoading) return;
    setBreadcrumbs((current) => current.slice(0, -1));
  };

  const refresh = () => {
    if (!capabilities.browse || entriesLoading) return;
    void loadEntries(currentRef);
  };

  const goToBreadcrumb = (index: number) => {
    if (entriesLoading || index === breadcrumbs.length - 1) return;
    setBreadcrumbs((current) => current.slice(0, index + 1));
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

  const uploadCompleted = (completedProfileID: string) => {
    if (completedProfileID !== profileId) return;
    void loadEntries(currentRef);
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
                      className={`w-full rounded-md px-2 py-2 text-left text-sm ${profile.id === profileId ? 'bg-[var(--color-selection-bg)] text-[var(--color-selection-text)]' : 'text-[var(--color-text-secondary)] hover:bg-[var(--color-hover)]'}`}
                    >
                      <span className="block truncate font-medium">{profile.name}</span>
                      <span className="block truncate text-xs opacity-75">{profile.provider}</span>
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

        <div className="ui-card min-w-0 overflow-hidden">
          {!selectedProfile ? (
            <div className="ui-empty p-8 text-sm">{t('files.selectProfile')}</div>
          ) : (
            <>
              <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--color-border)] p-3">
                <nav className="flex min-w-0 flex-wrap items-center gap-1 text-sm" aria-label={t('files.breadcrumb')}>
                  {breadcrumbs.map((breadcrumb, index) => (
                    <span key={breadcrumb.ref ?? 'root'} className="inline-flex min-w-0 items-center gap-1">
                      {index > 0 && <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />}
                      <button type="button" onClick={() => goToBreadcrumb(index)} disabled={entriesLoading || index === breadcrumbs.length - 1} className="max-w-44 truncate rounded px-1 py-0.5 disabled:text-[var(--color-text-primary)] hover:bg-[var(--color-hover)]">
                        {breadcrumb.name}
                      </button>
                    </span>
                  ))}
                </nav>
                 <div className="flex items-center gap-1">
                   <FileUploadControl apiUrl={apiUrl} token={token} profileId={profileId} parentRef={currentRef} capabilities={capabilities} disabled={entriesLoading} onCompleted={uploadCompleted} />
                   <button type="button" onClick={goUp} disabled={!capabilities.browse || breadcrumbs.length <= 1 || entriesLoading} className="ui-icon-button p-2 hover:bg-[var(--color-hover)]" aria-label={t('files.up')} title={t('files.up')}>
                    <ArrowUpIcon className="h-4 w-4" aria-hidden="true" />
                  </button>
                  <button type="button" onClick={refresh} disabled={!capabilities.browse || entriesLoading} className="ui-icon-button p-2 hover:bg-[var(--color-hover)]" aria-label={t('files.refresh')} title={t('files.refresh')}>
                    <ArrowPathIcon className="h-4 w-4" aria-hidden="true" />
                  </button>
                </div>
              </div>

              {!capabilities.browse ? (
                <p className="ui-empty p-8 text-sm">{t('files.listUnavailable')}</p>
              ) : entriesLoading ? (
                <div className="p-8"><LoadingIndicator label={t('common.loading')} /></div>
              ) : entries.length === 0 ? (
                <p className="ui-empty p-8 text-sm">{t('files.emptyDirectory')}</p>
              ) : (
                <div className="overflow-x-auto">
                  <table className="ui-table ui-responsive-table w-full text-sm">
                    <thead className="bg-[var(--color-bg-tertiary)] text-left text-xs text-[var(--color-text-secondary)]">
                      <tr>
                        <th scope="col" className="px-3 py-2 font-medium">{t('files.name')}</th>
                        <th scope="col" className="px-3 py-2 font-medium">{t('files.size')}</th>
                        <th scope="col" className="px-3 py-2 font-medium">{t('files.modified')}</th>
                        <th scope="col" className="px-3 py-2 font-medium"><span className="sr-only">{t('files.actions')}</span></th>
                      </tr>
                    </thead>
                    <tbody>
                      {entries.map((entry) => (
                        <tr key={entry.ref} className="border-t border-[var(--color-border)]">
                          <td data-label={t('files.name')} className="min-w-56 px-3 py-2">
                            <button type="button" onClick={() => openEntry(entry)} disabled={(entry.kind === 'directory' && (!capabilities.browse || entriesLoading)) || (entry.kind === 'file' && !canPreview(entry))} className={`inline-flex max-w-full items-center gap-2 truncate text-left ${entry.kind === 'directory' || canPreview(entry) ? 'ui-link disabled:cursor-not-allowed disabled:opacity-55' : ''}`}>
                              {entry.kind === 'directory' ? <FolderIcon className="h-5 w-5 shrink-0 text-[var(--color-file-folder)]" aria-hidden="true" /> : <DocumentIcon className="h-5 w-5 shrink-0 text-[var(--color-file-default)]" aria-hidden="true" />}
                              <span className="truncate">{entry.name}</span>
                            </button>
                          </td>
                          <td data-label={t('files.size')} className="px-3 py-2 text-[var(--color-text-secondary)]">{entry.kind === 'directory' ? t('files.directory') : formatBytes(entry.size)}</td>
                          <td data-label={t('files.modified')} className="px-3 py-2 text-[var(--color-text-secondary)]">{entry.modified_at ? formatDateTime(entry.modified_at) : t('common.unspecified')}</td>
                          <td data-label={t('files.actions')} className="px-3 py-2 text-right">
                            <button type="button" onClick={() => void download(entry)} disabled={entry.kind === 'directory' || !entry.allowed_actions.includes('download') || !capabilities.download || downloadingRef !== null} className="ui-icon-button p-2 hover:bg-[var(--color-hover)]" aria-label={t('files.download', { name: entry.name })} title={t('files.download', { name: entry.name })}>
                              <ArrowDownTrayIcon className="h-4 w-4" aria-hidden="true" />
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
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
      {previewEntry && (
        <Suspense fallback={null}>
          <FilePreviewDialog apiUrl={apiUrl} token={token} profileId={profileId} entry={previewEntry} onClose={() => setPreviewEntry(null)} onDownload={(entry) => void download(entry)} />
        </Suspense>
      )}
    </section>
  );
}
