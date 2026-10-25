import { useCallback, useMemo, useRef, useState } from 'react';
import { createDirectory, getFileCapabilities, listFileEntries, type FileEntry } from '../../api/files';
import type { ConnectionProfilePublic } from '../../api/profiles';
import type { FileManagerBreadcrumb } from './FileManagerTypes';

export type DestinationPicker = {
  profileId: string;
  breadcrumbs: FileManagerBreadcrumb[];
  entries: FileEntry[];
  loading: boolean;
  error: string;
  canCreateDirectory: boolean;
  creatingDirectory: boolean;
  newDirectoryName: string;
  onProfileChange: (profileId: string) => void;
  onBreadcrumbSelect: (index: number) => void;
  onDirectorySelect: (entry: FileEntry) => void;
  onNewDirectoryNameChange: (name: string) => void;
  onCreateDirectory: () => void;
};

type UseDestinationPickerOptions = {
  apiUrl: string;
  token: string;
  profiles: ConnectionProfilePublic[];
  translateApiError: (code: string) => string;
  onError: (error: string) => void;
  invalidNameMessage: string;
};

/** Mirrors the manager API's single-component filename validation before egress. */
export function validateManagerEntryName(value: string): boolean {
  const name = value.trim();
  if (!name || name === '.' || name === '..' || name.includes('/') || name.includes('\\') || name.includes('\0') || name.length > 255) return false;
  for (const character of name) {
    const code = character.codePointAt(0)!;
    if (code <= 0x1f || code === 0x7f || (code >= 0xd800 && code <= 0xdfff)) return false;
  }
  return new TextEncoder().encode(name).length <= 255;
}

/** Owns destination navigation so mutation workflows consume one stable picker interface. */
export function useDestinationPicker({ apiUrl, token, profiles, translateApiError, onError, invalidNameMessage }: UseDestinationPickerOptions) {
  const [profileId, setProfileId] = useState('');
  const [breadcrumbs, setBreadcrumbs] = useState<FileManagerBreadcrumb[]>([]);
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [canCreateDirectory, setCanCreateDirectory] = useState(false);
  const [creatingDirectory, setCreatingDirectory] = useState(false);
  const [newDirectoryName, setNewDirectoryName] = useState('');
  const requestRef = useRef<AbortController | null>(null);
  const createRequestRef = useRef<AbortController | null>(null);
  const capabilitiesRequestRef = useRef<AbortController | null>(null);

  const loadEntries = useCallback(async (parentRef: string | null, targetProfileId: string) => {
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    setLoading(true);
    const result = await listFileEntries(apiUrl, token, targetProfileId, parentRef, undefined, controller.signal);
    if (controller.signal.aborted) return;
    setEntries(result.ok ? result.data.entries.filter((entry) => entry.kind === 'directory') : []);
    if (result.ok === false) {
      const message = translateApiError(result.errorCode);
      setError(message);
      onError(message);
    }
    setLoading(false);
  }, [apiUrl, onError, token, translateApiError]);

  const loadCapabilities = useCallback(async (targetProfileId: string) => {
    capabilitiesRequestRef.current?.abort();
    const controller = new AbortController();
    capabilitiesRequestRef.current = controller;
    const result = await getFileCapabilities(apiUrl, token, targetProfileId, controller.signal);
    if (!controller.signal.aborted) setCanCreateDirectory(result.ok && result.data.capabilities.mkdir);
  }, [apiUrl, token]);

  const open = useCallback((targetProfileId: string, rootName: string) => {
    setProfileId(targetProfileId);
    setBreadcrumbs([{ ref: null, name: rootName }]);
    setEntries([]);
    setError('');
    setNewDirectoryName('');
    setCanCreateDirectory(false);
    onError('');
    void loadEntries(null, targetProfileId);
    void loadCapabilities(targetProfileId);
  }, [loadCapabilities, loadEntries, onError]);

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

  const onCreateDirectory = useCallback(async () => {
    const name = newDirectoryName.trim();
    if (creatingDirectory) return;
    if (!validateManagerEntryName(name)) {
      setError(invalidNameMessage);
      onError(invalidNameMessage);
      return;
    }
    createRequestRef.current?.abort();
    const controller = new AbortController();
    createRequestRef.current = controller;
    setCreatingDirectory(true);
    setError('');
    onError('');
    const parentRef = breadcrumbs[breadcrumbs.length - 1]?.ref ?? null;
    const result = await createDirectory(apiUrl, token, profileId, name, parentRef, controller.signal);
    if (controller.signal.aborted) return;
    setCreatingDirectory(false);
    if (result.ok === false) {
      const message = translateApiError(result.errorCode);
      setError(message);
      onError(message);
      return;
    }
    setNewDirectoryName('');
    void loadEntries(parentRef, profileId);
  }, [apiUrl, breadcrumbs, creatingDirectory, invalidNameMessage, loadEntries, newDirectoryName, onError, profileId, token, translateApiError]);

  const picker = useMemo<DestinationPicker>(() => ({
    profileId, breadcrumbs, entries, loading, error, canCreateDirectory, creatingDirectory, newDirectoryName,
    onProfileChange, onBreadcrumbSelect, onDirectorySelect, onNewDirectoryNameChange: setNewDirectoryName, onCreateDirectory,
  }), [breadcrumbs, canCreateDirectory, creatingDirectory, entries, error, loading, newDirectoryName, onBreadcrumbSelect, onCreateDirectory, onDirectorySelect, onProfileChange, profileId]);

  const abort = useCallback(() => {
    requestRef.current?.abort();
    createRequestRef.current?.abort();
    capabilitiesRequestRef.current?.abort();
    setLoading(false);
    setCreatingDirectory(false);
  }, []);

  return { picker, open, abort };
}
