import { useCallback, useMemo, useRef, useState } from 'react';
import { listFileEntries, type FileEntry } from '../../api/files';
import type { ConnectionProfilePublic } from '../../api/profiles';
import type { FileManagerBreadcrumb } from './FileManagerTypes';

export type DestinationPicker = {
  profileId: string;
  breadcrumbs: FileManagerBreadcrumb[];
  entries: FileEntry[];
  loading: boolean;
  onProfileChange: (profileId: string) => void;
  onBreadcrumbSelect: (index: number) => void;
  onDirectorySelect: (entry: FileEntry) => void;
};

type UseDestinationPickerOptions = {
  apiUrl: string;
  token: string;
  profiles: ConnectionProfilePublic[];
  translateApiError: (code: string) => string;
  onError: (error: string) => void;
};

/** Owns destination navigation so mutation workflows consume one stable picker interface. */
export function useDestinationPicker({ apiUrl, token, profiles, translateApiError, onError }: UseDestinationPickerOptions) {
  const [profileId, setProfileId] = useState('');
  const [breadcrumbs, setBreadcrumbs] = useState<FileManagerBreadcrumb[]>([]);
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const requestRef = useRef<AbortController | null>(null);

  const loadEntries = useCallback(async (parentRef: string | null, targetProfileId: string) => {
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    setLoading(true);
    const result = await listFileEntries(apiUrl, token, targetProfileId, parentRef, undefined, controller.signal);
    if (controller.signal.aborted) return;
    setEntries(result.ok ? result.data.entries.filter((entry) => entry.kind === 'directory') : []);
    if (result.ok === false) onError(translateApiError(result.errorCode));
    setLoading(false);
  }, [apiUrl, onError, token, translateApiError]);

  const open = useCallback((targetProfileId: string, rootName: string) => {
    setProfileId(targetProfileId);
    setBreadcrumbs([{ ref: null, name: rootName }]);
    setEntries([]);
    onError('');
    void loadEntries(null, targetProfileId);
  }, [loadEntries, onError]);

  const onProfileChange = useCallback((targetProfileId: string) => {
    const target = profiles.find((profile) => profile.id === targetProfileId);
    if (target) open(targetProfileId, target.name);
  }, [open, profiles]);

  const onBreadcrumbSelect = useCallback((index: number) => {
    const next = breadcrumbs.slice(0, index + 1);
    setBreadcrumbs(next);
    void loadEntries(next[next.length - 1]?.ref ?? null, profileId);
  }, [breadcrumbs, loadEntries, profileId]);

  const onDirectorySelect = useCallback((entry: FileEntry) => {
    const next = [...breadcrumbs, { ref: entry.ref, name: entry.name }];
    setBreadcrumbs(next);
    void loadEntries(entry.ref, profileId);
  }, [breadcrumbs, loadEntries, profileId]);

  const picker = useMemo<DestinationPicker>(() => ({
    profileId, breadcrumbs, entries, loading, onProfileChange, onBreadcrumbSelect, onDirectorySelect,
  }), [breadcrumbs, entries, loading, onBreadcrumbSelect, onDirectorySelect, onProfileChange, profileId]);

  const abort = useCallback(() => requestRef.current?.abort(), []);

  return { picker, open, abort };
}
