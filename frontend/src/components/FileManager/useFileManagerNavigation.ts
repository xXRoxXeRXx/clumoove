import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getFileCapabilities, listFileEntries, type FileBreadcrumb, type FileCapabilities, type FileEntry } from '../../api/files';
import { listConnectionProfiles, type ConnectionProfilePublic } from '../../api/profiles';
import type { FileManagerBreadcrumb } from './FileManagerTypes';

const unavailableCapabilities: FileCapabilities = {
  browse: false, native_pagination: false, download: false, archive: false, upload: false, mkdir: false,
  rename: false, move: false, copy: false, delete_file: false, delete_empty_directory: false,
  delete_recursive_directory: false, conflict_skip: false, conflict_overwrite: false,
  conflict_overwrite_atomic: false, conflict_rename: false, native_copy: false, range_download: false, thumbnails: false,
};

type Options = {
  apiUrl: string; token: string; profileId: string; initialBreadcrumbs?: FileBreadcrumb[];
  translateApiError: (code: string) => string; onError: (message: string) => void; onResetSelection: () => void;
};

/** Owns profile resolution, opaque directory navigation, and cursor paging. */
export function useFileManagerNavigation({ apiUrl, token, profileId, initialBreadcrumbs, translateApiError, onError, onResetSelection }: Options) {
  const [profiles, setProfiles] = useState<ConnectionProfilePublic[]>([]);
  const [profilesLoading, setProfilesLoading] = useState(true);
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [entriesLoading, setEntriesLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [capabilities, setCapabilities] = useState<FileCapabilities>(unavailableCapabilities);
  const [breadcrumbs, setBreadcrumbs] = useState<FileManagerBreadcrumb[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const profileRequestRef = useRef<AbortController | null>(null);
  const entriesRequestRef = useRef<AbortController | null>(null);
  const latestRequestRef = useRef(0);
  const selectedProfile = useMemo(() => profiles.find((profile) => profile.id === profileId) ?? null, [profileId, profiles]);
  const currentRef = breadcrumbs[breadcrumbs.length - 1]?.ref ?? null;

  const loadEntries = useCallback(async (parentRef: string | null) => {
    if (!profileId || !capabilities.browse) return;
    entriesRequestRef.current?.abort();
    const controller = new AbortController(); entriesRequestRef.current = controller;
    const request = ++latestRequestRef.current;
    setEntriesLoading(true); onError('');
    const result = await listFileEntries(apiUrl, token, profileId, parentRef, undefined, controller.signal);
    if (controller.signal.aborted || latestRequestRef.current !== request) return;
    if (result.ok === false) { setEntries([]); setNextCursor(null); onError(translateApiError(result.errorCode)); }
    else { setEntries(result.data.entries ?? []); setNextCursor(result.data.next_cursor ?? null); }
    setEntriesLoading(false);
  }, [apiUrl, capabilities.browse, onError, profileId, token, translateApiError]);

  const loadMore = useCallback(async () => {
    if (!profileId || !capabilities.browse || !nextCursor || entriesLoading || loadingMore) return;
    setLoadingMore(true);
    const controller = new AbortController();
    const result = await listFileEntries(apiUrl, token, profileId, currentRef, nextCursor, controller.signal);
    if (result.ok === false) onError(translateApiError(result.errorCode));
    else { setEntries((current) => [...current, ...(result.data.entries ?? [])]); setNextCursor(result.data.next_cursor ?? null); }
    setLoadingMore(false);
  }, [apiUrl, capabilities.browse, currentRef, entriesLoading, loadingMore, nextCursor, onError, profileId, token, translateApiError]);

  const loadProfiles = useCallback(async () => {
    profileRequestRef.current?.abort(); const controller = new AbortController(); profileRequestRef.current = controller;
    setProfilesLoading(true);
    const result = await listConnectionProfiles(apiUrl, token, controller.signal);
    if (controller.signal.aborted) return;
    if (result.ok === true) setProfiles(result.data.profiles ?? []);
    else { setProfiles([]); onError(translateApiError(result.errorCode)); }
    setProfilesLoading(false);
  }, [apiUrl, onError, token, translateApiError]);

  useEffect(() => { const timer = window.setTimeout(() => void loadProfiles(), 0); return () => { window.clearTimeout(timer); profileRequestRef.current?.abort(); }; }, [loadProfiles]);
  useEffect(() => {
    let controller: AbortController | null = null;
    const timer = window.setTimeout(() => {
      if (!selectedProfile) { setCapabilities(unavailableCapabilities); setBreadcrumbs([]); setEntries([]); setNextCursor(null); setEntriesLoading(false); return; }
      controller = new AbortController(); entriesRequestRef.current?.abort(); entriesRequestRef.current = controller;
      const request = ++latestRequestRef.current;
      const resolved = initialBreadcrumbs?.length ? initialBreadcrumbs.map((item) => ({ ref: item.ref || null, name: item.name })) : [{ ref: null, name: selectedProfile.name }];
      setBreadcrumbs(resolved); onResetSelection(); setEntries([]); setNextCursor(null); onError(''); setEntriesLoading(true);
      const targetRef = resolved[resolved.length - 1]?.ref ?? null;
      void (async () => {
        const caps = await getFileCapabilities(apiUrl, token, selectedProfile.id, controller!.signal);
        if (controller!.signal.aborted || latestRequestRef.current !== request) return;
        if (caps.ok === false) { setCapabilities(unavailableCapabilities); setEntries([]); setNextCursor(null); onError(translateApiError(caps.errorCode)); setEntriesLoading(false); return; }
        setCapabilities(caps.data.capabilities);
        if (!caps.data.capabilities.browse) { setEntries([]); setNextCursor(null); setEntriesLoading(false); return; }
        const listed = await listFileEntries(apiUrl, token, selectedProfile.id, targetRef, undefined, controller!.signal);
        if (controller!.signal.aborted || latestRequestRef.current !== request) return;
        if (listed.ok === false) { setEntries([]); setNextCursor(null); onError(translateApiError(listed.errorCode)); }
        else { setEntries(listed.data.entries ?? []); setNextCursor(listed.data.next_cursor ?? null); }
        setEntriesLoading(false);
      })();
    }, 0);
    return () => { window.clearTimeout(timer); controller?.abort(); };
  }, [apiUrl, initialBreadcrumbs, onError, onResetSelection, selectedProfile, token, translateApiError]);

  const abort = useCallback(() => entriesRequestRef.current?.abort(), []);
  return { profiles, profilesLoading, entries, entriesLoading, loadingMore, capabilities, breadcrumbs, nextCursor, selectedProfile, currentRef, setBreadcrumbs, loadEntries, loadMore, abort };
}
