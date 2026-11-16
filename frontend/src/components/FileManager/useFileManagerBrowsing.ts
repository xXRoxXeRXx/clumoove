import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  getFileCapabilities,
  listFileEntries,
  type FileBreadcrumb,
  type FileCapabilities,
  type FileEntry,
} from '../../api/files';
import { listConnectionProfiles, type ConnectionProfilePublic } from '../../api/profiles';
import { useApiError } from '../../utils/apiError';
import type { FileRequestContext } from './actions/fileRequests';
import type { FileManagerBreadcrumb } from './fileManagerTypes';

export type SortField = 'name' | 'size' | 'modified';
export type SortDirection = 'asc' | 'desc';

export type UseFileManagerBrowsingOptions = {
  /** Where requests go and which profile is browsed. */
  request: FileRequestContext;
  initialBreadcrumbs?: FileBreadcrumb[];
  onProfileChange: (profileId: string) => void;
};

export type FileManagerBrowsingSelection = {
  entries: FileEntry[];
  allSelected: boolean;
  partiallySelected: boolean;
  active: boolean;
  isSelected: (ref: string) => boolean;
  toggle: (ref: string) => void;
  toggleAll: () => void;
  clear: () => void;
};

export type FileManagerBrowsingPreview = {
  entry: FileEntry | null;
  open: (entry: FileEntry) => void;
  close: () => void;
};

export type FileManagerBrowsing = {
  profiles: ConnectionProfilePublic[];
  profilesLoading: boolean;
  selectedProfile: ConnectionProfilePublic | null;
  capabilities: FileCapabilities;
  entries: FileEntry[];
  loading: boolean;
  loadingMore: boolean;
  hasMore: boolean;
  error: string;
  breadcrumbs: FileManagerBreadcrumb[];
  currentRef: string | null;
  sortField: SortField;
  sortDirection: SortDirection;
  selection: FileManagerBrowsingSelection;
  preview: FileManagerBrowsingPreview;
  selectProfile: (profileId: string) => void;
  isInteractive: (entry: FileEntry) => boolean;
  activateEntry: (entry: FileEntry, options?: { toggleSelection?: boolean }) => void;
  goUp: () => void;
  goToBreadcrumb: (index: number) => void;
  refresh: () => void;
  loadMore: () => Promise<void>;
  notifyUploadCompleted: (completedProfileId: string) => void;
  sortBy: (field: SortField) => void;
};

const SORT_FIELD_KEY = 'clumoove_file_manager_sort_field';
const SORT_DIR_KEY = 'clumoove_file_manager_sort_dir';

const unavailableCapabilities: FileCapabilities = {
  browse: false,
  native_pagination: false,
  download: false,
  archive: false,
  upload: false,
  mkdir: false,
  rename: false,
  move: false,
  copy: false,
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

function readStoredPreference<T extends string>(key: string, isValid: (value: string) => value is T, fallback: T): T {
  try {
    const stored = localStorage.getItem(key);
    if (stored && isValid(stored)) return stored;
  } catch {
    // ignore storage errors
  }
  return fallback;
}

function isSortField(value: string): value is SortField {
  return value === 'name' || value === 'size' || value === 'modified';
}

function isSortDirection(value: string): value is SortDirection {
  return value === 'asc' || value === 'desc';
}

function compareEntryNames(a: FileEntry, b: FileEntry): number {
  return a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' });
}

function sortFileEntries(entries: FileEntry[], sortField: SortField, sortDirection: SortDirection): FileEntry[] {
  return [...entries].sort((a, b) => {
    // Directories always appear first
    if (a.kind !== b.kind) {
      return a.kind === 'directory' ? -1 : 1;
    }

    if (sortField === 'name') {
      const cmp = compareEntryNames(a, b);
      return sortDirection === 'asc' ? cmp : -cmp;
    }

    if (sortField === 'size') {
      if (a.kind === 'directory') {
        return compareEntryNames(a, b);
      }
      const sizeA = a.size ?? 0;
      const sizeB = b.size ?? 0;
      if (sizeA !== sizeB) {
        return sortDirection === 'asc' ? sizeA - sizeB : sizeB - sizeA;
      }
      return compareEntryNames(a, b);
    }

    if (sortField === 'modified') {
      const hasA = Boolean(a.modified_at);
      const hasB = Boolean(b.modified_at);
      if (!hasA && !hasB) {
        const cmp = compareEntryNames(a, b);
        return sortDirection === 'asc' ? cmp : -cmp;
      }
      if (!hasA) return 1;
      if (!hasB) return -1;

      const timeA = new Date(a.modified_at!).getTime();
      const timeB = new Date(b.modified_at!).getTime();
      if (timeA !== timeB) {
        return sortDirection === 'asc' ? timeA - timeB : timeB - timeA;
      }
      return compareEntryNames(a, b);
    }

    return 0;
  });
}

export function useFileManagerBrowsing({
  request,
  initialBreadcrumbs,
  onProfileChange,
}: UseFileManagerBrowsingOptions): FileManagerBrowsing {
  const { apiUrl, token, profileId } = request;
  const translateApiError = useApiError();
  const [profiles, setProfiles] = useState<ConnectionProfilePublic[]>([]);
  const [profilesLoading, setProfilesLoading] = useState(true);
  const [capabilities, setCapabilities] = useState<FileCapabilities>(unavailableCapabilities);
  const [rawEntries, setRawEntries] = useState<FileEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [error, setError] = useState('');
  const [breadcrumbs, setBreadcrumbs] = useState<FileManagerBreadcrumb[]>([]);
  const [selectedRefs, setSelectedRefs] = useState<Set<string>>(() => new Set());
  const [previewEntry, setPreviewEntry] = useState<FileEntry | null>(null);
  const [sortField, setSortField] = useState<SortField>(() => readStoredPreference(SORT_FIELD_KEY, isSortField, 'name'));
  const [sortDirection, setSortDirection] = useState<SortDirection>(() => readStoredPreference(SORT_DIR_KEY, isSortDirection, 'asc'));

  const profileRequestRef = useRef<AbortController | null>(null);
  const latestProfilesRequestRef = useRef(0);
  const entriesRequestRef = useRef<AbortController | null>(null);
  const loadMoreRequestRef = useRef<AbortController | null>(null);
  const latestEntriesRequestRef = useRef(0);
  const loadingMoreRef = useRef(false);
  const uploadRefreshTimeoutRef = useRef<number | null>(null);

  const selectedProfile = useMemo(
    () => profiles.find((profile) => profile.id === profileId) ?? null,
    [profileId, profiles],
  );
  const currentRef = breadcrumbs[breadcrumbs.length - 1]?.ref ?? null;

  const sortedEntries = useMemo(
    () => sortFileEntries(rawEntries, sortField, sortDirection),
    [rawEntries, sortDirection, sortField],
  );

  const cancelUploadRefresh = useCallback(() => {
    if (uploadRefreshTimeoutRef.current !== null) {
      window.clearTimeout(uploadRefreshTimeoutRef.current);
      uploadRefreshTimeoutRef.current = null;
    }
  }, []);

  const abortListingRequests = useCallback(() => {
    entriesRequestRef.current?.abort();
    loadMoreRequestRef.current?.abort();
    loadingMoreRef.current = false;
  }, []);

  const fetchDirectoryListing = useCallback(async (
    targetProfileId: string,
    parentRef: string | null,
    controller: AbortController,
    request: number,
  ) => {
    const listResult = await listFileEntries(apiUrl, token, targetProfileId, parentRef, undefined, controller.signal);
    if (controller.signal.aborted || latestEntriesRequestRef.current !== request) return;
    if (listResult.ok === false) {
      setRawEntries([]);
      setNextCursor(null);
      setError(translateApiError(listResult.errorCode));
    } else {
      setRawEntries(listResult.data.entries ?? []);
      setNextCursor(listResult.data.next_cursor ?? null);
    }
    setLoading(false);
  }, [apiUrl, token, translateApiError]);

  const loadProfiles = useCallback(async () => {
    profileRequestRef.current?.abort();
    const controller = new AbortController();
    profileRequestRef.current = controller;
    const request = ++latestProfilesRequestRef.current;
    setProfilesLoading(true);
    const result = await listConnectionProfiles(apiUrl, token, controller.signal);
    if (controller.signal.aborted || latestProfilesRequestRef.current !== request) return;
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
      cancelUploadRefresh();
      profileRequestRef.current?.abort();
      abortListingRequests();
    };
  }, [abortListingRequests, cancelUploadRefresh, loadProfiles]);

  useEffect(() => {
    abortListingRequests();
    const request = ++latestEntriesRequestRef.current;
    let controller: AbortController | null = null;
    const timeoutId = window.setTimeout(() => {
      cancelUploadRefresh();
      if (!selectedProfile) {
        setCapabilities(unavailableCapabilities);
        setBreadcrumbs([]);
        setSelectedRefs(new Set());
        setRawEntries([]);
        setNextCursor(null);
        setLoadingMore(false);
        setLoading(false);
        return;
      }

      controller = new AbortController();
      entriesRequestRef.current = controller;

      const resolvedBreadcrumbs = initialBreadcrumbs?.length
        ? initialBreadcrumbs.map((breadcrumb) => ({ ref: breadcrumb.ref || null, name: breadcrumb.name }))
        : [{ ref: null, name: selectedProfile.name }];
      setBreadcrumbs(resolvedBreadcrumbs);
      setSelectedRefs(new Set());
      setRawEntries([]);
      setNextCursor(null);
      setLoadingMore(false);
      setError('');
      setLoading(true);

      const targetRef = resolvedBreadcrumbs[resolvedBreadcrumbs.length - 1]?.ref ?? null;

      async function initProfile() {
        const capResult = await getFileCapabilities(apiUrl, token, selectedProfile!.id, controller!.signal);
        if (controller!.signal.aborted || latestEntriesRequestRef.current !== request) return;

        if (capResult.ok === false) {
          setCapabilities(unavailableCapabilities);
          setRawEntries([]);
          setNextCursor(null);
          setError(translateApiError(capResult.errorCode));
          setLoading(false);
          return;
        }

        setCapabilities(capResult.data.capabilities);

        if (!capResult.data.capabilities.browse) {
          setRawEntries([]);
          setNextCursor(null);
          setLoading(false);
          return;
        }

        await fetchDirectoryListing(selectedProfile!.id, targetRef, controller!, request);
      }

      void initProfile();
    }, 0);

    return () => {
      window.clearTimeout(timeoutId);
      controller?.abort();
    };
  }, [abortListingRequests, apiUrl, cancelUploadRefresh, fetchDirectoryListing, initialBreadcrumbs, selectedProfile, token, translateApiError]);

  const loadEntries = useCallback(async (parentRef: string | null) => {
    if (!profileId || !capabilities.browse) return;
    abortListingRequests();
    const controller = new AbortController();
    entriesRequestRef.current = controller;
    const request = ++latestEntriesRequestRef.current;
    setLoading(true);
    setLoadingMore(false);
    setError('');
    await fetchDirectoryListing(profileId, parentRef, controller, request);
  }, [abortListingRequests, capabilities.browse, fetchDirectoryListing, profileId]);

  const loadMore = useCallback(async () => {
    if (!profileId || !capabilities.browse || !nextCursor || loading || loadingMore || loadingMoreRef.current) return;
    loadingMoreRef.current = true;
    setLoadingMore(true);
    const controller = new AbortController();
    loadMoreRequestRef.current = controller;
    const request = latestEntriesRequestRef.current;
    const result = await listFileEntries(apiUrl, token, profileId, currentRef, nextCursor, controller.signal);
    if (controller.signal.aborted || latestEntriesRequestRef.current !== request) {
      loadingMoreRef.current = false;
      return;
    }
    if (result.ok === false) {
      setError(translateApiError(result.errorCode));
    } else {
      setRawEntries((current) => [...current, ...(result.data.entries ?? [])]);
      setNextCursor(result.data.next_cursor ?? null);
    }
    loadingMoreRef.current = false;
    setLoadingMore(false);
  }, [apiUrl, capabilities.browse, currentRef, loading, loadingMore, nextCursor, profileId, token, translateApiError]);

  const selectProfile = useCallback((nextProfileId: string) => {
    if (nextProfileId === profileId) return;
    onProfileChange(nextProfileId);
  }, [onProfileChange, profileId]);

  const openDirectory = useCallback((entry: FileEntry) => {
    if (entry.kind !== 'directory' || !capabilities.browse) return;
    cancelUploadRefresh();
    setBreadcrumbs((current) => [...current, { ref: entry.ref, name: entry.name }]);
    setSelectedRefs(new Set());
    void loadEntries(entry.ref);
  }, [cancelUploadRefresh, capabilities.browse, loadEntries]);

  const goToBreadcrumb = useCallback((index: number) => {
    if (index < 0 || index >= breadcrumbs.length - 1) return;
    cancelUploadRefresh();
    const targetBreadcrumbs = breadcrumbs.slice(0, index + 1);
    const targetRef = targetBreadcrumbs[targetBreadcrumbs.length - 1]?.ref ?? null;
    setBreadcrumbs(targetBreadcrumbs);
    setSelectedRefs(new Set());
    void loadEntries(targetRef);
  }, [breadcrumbs, cancelUploadRefresh, loadEntries]);

  const goUp = useCallback(() => {
    goToBreadcrumb(breadcrumbs.length - 2);
  }, [breadcrumbs.length, goToBreadcrumb]);

  const refresh = useCallback(() => {
    if (!capabilities.browse) return;
    cancelUploadRefresh();
    void loadEntries(currentRef);
  }, [cancelUploadRefresh, capabilities.browse, currentRef, loadEntries]);

  const notifyUploadCompleted = useCallback((completedProfileId: string) => {
    if (completedProfileId !== profileId) return;
    cancelUploadRefresh();
    uploadRefreshTimeoutRef.current = window.setTimeout(() => {
      uploadRefreshTimeoutRef.current = null;
      void loadEntries(currentRef);
    }, 500);
  }, [cancelUploadRefresh, currentRef, loadEntries, profileId]);

  const sortBy = useCallback((field: SortField) => {
    let nextDir: SortDirection;
    if (sortField === field) {
      nextDir = sortDirection === 'asc' ? 'desc' : 'asc';
    } else {
      // Start size/modified columns descending (largest/newest first)
      nextDir = field === 'name' ? 'asc' : 'desc';
    }
    setSortField(field);
    setSortDirection(nextDir);
    try {
      localStorage.setItem(SORT_FIELD_KEY, field);
      localStorage.setItem(SORT_DIR_KEY, nextDir);
    } catch {
      // ignore storage errors
    }
  }, [sortDirection, sortField]);

  const toggleSelection = useCallback((ref: string) => {
    setSelectedRefs((current) => {
      const next = new Set(current);
      if (next.has(ref)) next.delete(ref);
      else next.add(ref);
      return next;
    });
  }, []);

  const allLoadedSelected = rawEntries.length > 0 && rawEntries.every((entry) => selectedRefs.has(entry.ref));
  const someLoadedSelected = rawEntries.some((entry) => selectedRefs.has(entry.ref));

  const toggleSelectAll = useCallback(() => {
    setSelectedRefs((current) => {
      const next = new Set(current);
      if (allLoadedSelected) {
        rawEntries.forEach((entry) => next.delete(entry.ref));
      } else {
        rawEntries.forEach((entry) => next.add(entry.ref));
      }
      return next;
    });
  }, [allLoadedSelected, rawEntries]);

  const clearSelection = useCallback(() => {
    setSelectedRefs(new Set());
  }, []);

  const selectedEntries = useMemo(
    () => rawEntries.filter((entry) => selectedRefs.has(entry.ref)),
    [rawEntries, selectedRefs],
  );

  const selection = useMemo<FileManagerBrowsingSelection>(() => ({
    entries: selectedEntries,
    allSelected: allLoadedSelected,
    partiallySelected: someLoadedSelected && !allLoadedSelected,
    active: selectedRefs.size > 0,
    isSelected: (ref: string) => selectedRefs.has(ref),
    toggle: toggleSelection,
    toggleAll: toggleSelectAll,
    clear: clearSelection,
  }), [allLoadedSelected, clearSelection, selectedEntries, selectedRefs, someLoadedSelected, toggleSelectAll, toggleSelection]);

  const openPreview = useCallback((entry: FileEntry) => {
    if (entry.kind !== 'file') return;
    setPreviewEntry(entry);
  }, []);

  const closePreview = useCallback(() => {
    setPreviewEntry(null);
  }, []);

  const preview = useMemo<FileManagerBrowsingPreview>(() => ({
    entry: previewEntry,
    open: openPreview,
    close: closePreview,
  }), [closePreview, openPreview, previewEntry]);

  const isInteractive = useCallback(
    (entry: FileEntry) => entry.kind === 'file' || (capabilities.browse && !loading),
    [capabilities.browse, loading],
  );

  const activateEntry = useCallback((entry: FileEntry, options?: { toggleSelection?: boolean }) => {
    if (selection.active || options?.toggleSelection) {
      toggleSelection(entry.ref);
      return;
    }
    if (entry.kind === 'directory') {
      if (capabilities.browse && !loading) {
        openDirectory(entry);
      }
      return;
    }
    openPreview(entry);
  }, [capabilities.browse, loading, openDirectory, openPreview, selection.active, toggleSelection]);

  return {
    profiles,
    profilesLoading,
    selectedProfile,
    capabilities,
    entries: sortedEntries,
    loading,
    loadingMore,
    hasMore: nextCursor !== null,
    error,
    breadcrumbs,
    currentRef,
    sortField,
    sortDirection,
    selection,
    preview,
    selectProfile,
    isInteractive,
    activateEntry,
    goUp,
    goToBreadcrumb,
    refresh,
    loadMore,
    notifyUploadCompleted,
    sortBy,
  };
}
