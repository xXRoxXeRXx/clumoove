import { useCallback, useMemo, useState } from 'react';
import type { FileEntry } from '../../../api/files';
import type { ConnectionProfilePublic } from '../../../api/profiles';
import type { FileManagerBreadcrumb } from '../fileManagerTypes';
import { useRequestSlot, type ActionEnvironment } from './actionEnvironment';

type Options = {
  profiles: ConnectionProfilePublic[];
  profileName: string;
};

/**
 * Browsing for a destination folder, possibly in another profile: which profile is
 * chosen, the path walked so far, and the sub-folders listed at that path.
 */
export function useDestinationBrowser(env: ActionEnvironment, { profiles, profileName }: Options) {
  const { requests, request, t, translateApiError } = env;
  const [profileId, setProfileId] = useState(request.profileId);
  const [breadcrumbs, setBreadcrumbs] = useState<FileManagerBreadcrumb[]>([]);
  const [folders, setFolders] = useState<FileEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const { begin: beginRequest, abort } = useRequestSlot(request.profileId);

  const load = useCallback(
    async (parentRef: string | null, targetProfileId: string) => {
      const controller = beginRequest();
      setLoading(true);
      const result = await requests.listDirectories(targetProfileId, parentRef, controller.signal);
      if (controller.signal.aborted) return;
      setFolders(result.ok ? result.data.entries.filter((entry) => entry.kind === 'directory') : []);
      if (result.ok === false) setError(translateApiError(result.errorCode));
      setLoading(false);
    },
    [beginRequest, requests, translateApiError],
  );

  /** Starts at the root of the current profile. */
  const reset = useCallback(() => {
    setError('');
    setProfileId(request.profileId);
    setBreadcrumbs([{ ref: null, name: profileName || t('files.title') }]);
    setFolders([]);
    void load(null, request.profileId);
  }, [load, profileName, request.profileId, t]);

  const selectProfile = useCallback(
    (targetProfileId: string) => {
      const target = profiles.find((profile) => profile.id === targetProfileId);
      if (!target) return;
      setProfileId(targetProfileId);
      setBreadcrumbs([{ ref: null, name: target.name }]);
      setFolders([]);
      setError('');
      void load(null, targetProfileId);
    },
    [load, profiles],
  );

  const openFolder = useCallback(
    (entry: FileEntry) => {
      setBreadcrumbs([...breadcrumbs, { ref: entry.ref, name: entry.name }]);
      void load(entry.ref, profileId);
    },
    [breadcrumbs, load, profileId],
  );

  const goToBreadcrumb = useCallback(
    (index: number) => {
      const next = breadcrumbs.slice(0, index + 1);
      setBreadcrumbs(next);
      void load(next[next.length - 1]?.ref ?? null, profileId);
    },
    [breadcrumbs, load, profileId],
  );

  const clearError = useCallback(() => setError(''), []);

  const destination = useMemo(() => {
    const current = breadcrumbs[breadcrumbs.length - 1];
    return { ref: current?.ref ?? null, name: current?.name ?? t('files.title') };
  }, [breadcrumbs, t]);

  return { profileId, breadcrumbs, folders, loading, error, destination, reset, selectProfile, openFolder, goToBreadcrumb, abort, clearError };
}
