import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import {
  getFileCapabilities,
  listFileEntries,
  type FileCapabilities,
  type FileEntry,
} from '../../api/files';
import { listConnectionProfiles, type ConnectionProfilePublic } from '../../api/profiles';
import {
  useFileManagerBrowsing,
  type FileManagerBrowsing,
  type UseFileManagerBrowsingOptions,
} from './useFileManagerBrowsing';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../../api/files', () => ({
  getFileCapabilities: vi.fn(),
  listFileEntries: vi.fn(),
}));

vi.mock('../../api/profiles', () => ({
  listConnectionProfiles: vi.fn(),
}));

const fullCapabilities: FileCapabilities = {
  browse: true,
  native_pagination: false,
  download: true,
  archive: true,
  upload: true,
  mkdir: true,
  rename: true,
  move: true,
  copy: true,
  delete_file: true,
  delete_empty_directory: true,
  delete_recursive_directory: true,
  conflict_skip: true,
  conflict_overwrite: true,
  conflict_overwrite_atomic: false,
  conflict_rename: true,
  native_copy: false,
  range_download: false,
  thumbnails: false,
};

const defaultProfiles: ConnectionProfilePublic[] = [
  {
    id: 'profile-1',
    name: 'Google Drive',
    provider: 'google',
    has_password: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 'profile-2',
    name: 'Dropbox',
    provider: 'dropbox',
    has_password: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
];

const docsDir: FileEntry = {
  ref: 'ref-dir-docs',
  name: 'Documents',
  display_path: '/Documents',
  kind: 'directory',
  size: 0,
  modified_at: '2026-05-01T10:00:00Z',
  allowed_actions: ['download'],
};

const projectsDir: FileEntry = {
  ref: 'ref-dir-projects',
  name: 'Projects',
  display_path: '/Projects',
  kind: 'directory',
  size: 0,
  modified_at: '2026-05-04T10:00:00Z',
  allowed_actions: ['download'],
};

const notesFile: FileEntry = {
  ref: 'ref-file-notes',
  name: 'notes.txt',
  display_path: '/notes.txt',
  kind: 'file',
  size: 512,
  modified_at: '2026-05-02T10:00:00Z',
  mime_type: 'text/plain',
  allowed_actions: ['download'],
};

const archiveFile: FileEntry = {
  ref: 'ref-file-archive',
  name: 'archive.zip',
  display_path: '/archive.zip',
  kind: 'file',
  size: 4096,
  modified_at: '2026-05-03T10:00:00Z',
  mime_type: 'application/zip',
  allowed_actions: ['download'],
};

const rootEntries: FileEntry[] = [notesFile, docsDir, archiveFile];

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
};

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

async function flushAsync(cycles = 3): Promise<void> {
  for (let i = 0; i < cycles; i++) {
    await act(async () => {
      await Promise.resolve();
      if (vi.isFakeTimers()) {
        await vi.advanceTimersByTimeAsync(0);
      } else {
        await new Promise((resolve) => setTimeout(resolve, 0));
      }
    });
  }
}

describe('useFileManagerBrowsing', () => {
  let container: HTMLDivElement;
  let root: Root;
  let latest: FileManagerBrowsing;
  const onProfileChange = vi.fn();

  function Probe(props: UseFileManagerBrowsingOptions) {
    latest = useFileManagerBrowsing(props);
    return null;
  }

  async function renderBrowsing(overrides?: Partial<UseFileManagerBrowsingOptions>) {
    const props: UseFileManagerBrowsingOptions = {
      apiUrl: 'https://api.example.test',
      token: 'jwt-token',
      profileId: 'profile-1',
      onProfileChange,
      ...overrides,
    };
    await act(async () => {
      root.render(<Probe {...props} />);
      await Promise.resolve();
    });
    await flushAsync();
  }

  beforeEach(async () => {
    localStorage.clear();
    await i18n.changeLanguage('en');
    onProfileChange.mockReset();

    vi.mocked(listConnectionProfiles).mockReset().mockResolvedValue({
      ok: true,
      status: 200,
      data: { profiles: defaultProfiles },
    });
    vi.mocked(getFileCapabilities).mockReset().mockResolvedValue({
      ok: true,
      status: 200,
      data: { capabilities: fullCapabilities },
    });
    vi.mocked(listFileEntries).mockReset().mockResolvedValue({
      ok: true,
      status: 200,
      data: { entries: rootEntries, next_cursor: null },
    });

    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    vi.useRealTimers();
    act(() => root?.unmount());
    container?.remove();
    vi.restoreAllMocks();
  });

  it('loads profiles, discovers capabilities, and lists sorted root entries for the active profile', async () => {
    await renderBrowsing();

    expect(latest.profilesLoading).toBe(false);
    expect(latest.profiles).toEqual(defaultProfiles);
    expect(latest.selectedProfile?.id).toBe('profile-1');
    expect(latest.capabilities).toEqual(fullCapabilities);
    expect(latest.breadcrumbs).toEqual([{ ref: null, name: 'Google Drive' }]);
    expect(latest.currentRef).toBeNull();
    expect(latest.loading).toBe(false);
    expect(latest.error).toBe('');
    expect(latest.entries.map((entry) => entry.name)).toEqual(['Documents', 'archive.zip', 'notes.txt']);
  });

  it('handles initialBreadcrumbs and resets state when no profile is selected', async () => {
    await renderBrowsing({
      initialBreadcrumbs: [
        { ref: '', name: 'Google Drive' },
        { ref: 'ref-dir-docs', name: 'Documents' },
      ],
    });

    expect(latest.breadcrumbs).toEqual([
      { ref: null, name: 'Google Drive' },
      { ref: 'ref-dir-docs', name: 'Documents' },
    ]);
    expect(latest.currentRef).toBe('ref-dir-docs');
    expect(listFileEntries).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      'ref-dir-docs',
      undefined,
      expect.any(AbortSignal),
    );

    await renderBrowsing({ profileId: '' });
    expect(latest.selectedProfile).toBeNull();
    expect(latest.capabilities.browse).toBe(false);
    expect(latest.breadcrumbs).toEqual([]);
    expect(latest.entries).toEqual([]);
    expect(latest.hasMore).toBe(false);
  });

  it('surfaces translated errors for profile, capability, and listing failures', async () => {
    vi.mocked(listConnectionProfiles).mockResolvedValueOnce({
      ok: false,
      status: 500,
      errorCode: 'INTERNAL_ERROR',
      networkError: false,
    });

    await renderBrowsing();
    expect(latest.profiles).toEqual([]);
    expect(latest.error).toBe(i18n.t('errors.INTERNAL_ERROR'));

    vi.mocked(listConnectionProfiles).mockResolvedValueOnce({
      ok: true,
      status: 200,
      data: { profiles: defaultProfiles },
    });
    vi.mocked(getFileCapabilities).mockResolvedValueOnce({
      ok: false,
      status: 403,
      errorCode: 'FORBIDDEN',
      networkError: false,
    });

    await renderBrowsing({ apiUrl: 'https://api-2.example.test' });
    expect(latest.capabilities.browse).toBe(false);
    expect(latest.error).toBe(i18n.t('errors.FORBIDDEN'));

    vi.mocked(getFileCapabilities).mockResolvedValueOnce({
      ok: true,
      status: 200,
      data: { capabilities: { ...fullCapabilities, browse: false } },
    });
    await renderBrowsing({ apiUrl: 'https://api-3.example.test' });
    expect(latest.capabilities.browse).toBe(false);
    expect(latest.entries).toEqual([]);
    expect(latest.error).toBe('');
  });

  it('navigates directories and breadcrumbs, and resets selection on navigation and profile change', async () => {
    const readmeFile: FileEntry = {
      ref: 'ref-file-readme',
      name: 'readme.md',
      display_path: '/Documents/readme.md',
      kind: 'file',
      size: 256,
      allowed_actions: ['download'],
    };
    const subDir: FileEntry = {
      ref: 'ref-dir-specs',
      name: 'Specs',
      display_path: '/Documents/Specs',
      kind: 'directory',
      size: 0,
      allowed_actions: ['download'],
    };
    const specFile: FileEntry = {
      ref: 'ref-file-spec',
      name: 'spec.md',
      display_path: '/Documents/Specs/spec.md',
      kind: 'file',
      size: 128,
      allowed_actions: ['download'],
    };

    vi.mocked(listFileEntries)
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: rootEntries, next_cursor: null } })
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [subDir, readmeFile], next_cursor: null } })
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [specFile], next_cursor: null } })
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [subDir, readmeFile], next_cursor: null } })
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: rootEntries, next_cursor: null } });

    await renderBrowsing();

    // Navigating into Documents and Specs, then testing selection reset via goUp, goToBreadcrumb, and profile switch
    act(() => {
      latest.activateEntry(docsDir);
    });
    await flushAsync();
    expect(latest.breadcrumbs).toEqual([
      { ref: null, name: 'Google Drive' },
      { ref: 'ref-dir-docs', name: 'Documents' },
    ]);
    expect(latest.currentRef).toBe('ref-dir-docs');
    expect(latest.entries.map((e) => e.name)).toEqual(['Specs', 'readme.md']);

    // Open Specs, select item, then goUp clears selection
    act(() => {
      latest.activateEntry(subDir);
    });
    await flushAsync();
    act(() => {
      latest.selection.toggle(specFile.ref);
    });
    expect(latest.selection.entries.length).toBe(1);

    act(() => {
      latest.goUp();
    });
    await flushAsync();
    expect(latest.selection.active).toBe(false);
    expect(latest.currentRef).toBe('ref-dir-docs');

    // Select again, then jump to root breadcrumb
    act(() => {
      latest.selection.toggle(readmeFile.ref);
      latest.goToBreadcrumb(0);
    });
    await flushAsync();
    expect(latest.selection.active).toBe(false);
    expect(latest.currentRef).toBeNull();

    // Selecting profile calls onProfileChange; re-rendering with new profile resets selection
    act(() => {
      latest.selection.toggle(notesFile.ref);
      latest.selectProfile('profile-2');
    });
    expect(onProfileChange).toHaveBeenCalledWith('profile-2');

    await renderBrowsing({ profileId: 'profile-2' });
    expect(latest.selection.active).toBe(false);
    expect(latest.breadcrumbs).toEqual([{ ref: null, name: 'Dropbox' }]);
  });

  it('keeps selection mode active after a refresh removes the selected entries', async () => {
    vi.mocked(listFileEntries)
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [docsDir, notesFile], next_cursor: null } })
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [docsDir], next_cursor: null } });

    await renderBrowsing();

    act(() => {
      latest.selection.toggle(notesFile.ref);
    });
    expect(latest.selection.active).toBe(true);
    expect(latest.selection.entries.map((entry) => entry.ref)).toEqual([notesFile.ref]);

    act(() => {
      latest.refresh();
    });
    await flushAsync();

    expect(latest.selection.entries).toEqual([]);
    expect(latest.selection.active).toBe(true);
  });

  it('discards stale directory and capability responses when rapid navigation or profile switching occurs', async () => {
    const docsDeferred = deferred<Awaited<ReturnType<typeof listFileEntries>>>();
    const rootJumpDeferred = deferred<Awaited<ReturnType<typeof listFileEntries>>>();

    vi.mocked(listFileEntries)
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [docsDir, projectsDir], next_cursor: null } })
      .mockImplementation((_url, _token, _profileId, parentRef) => {
        if (parentRef === docsDir.ref) return docsDeferred.promise;
        if (parentRef === null) return rootJumpDeferred.promise;
        return Promise.resolve({ ok: true, status: 200, data: { entries: [notesFile], next_cursor: null } });
      });

    await renderBrowsing();

    act(() => {
      latest.activateEntry(docsDir);
    });
    act(() => {
      latest.goToBreadcrumb(0);
    });

    // Resolve the newer root jump request first, then the stale Documents request
    rootJumpDeferred.resolve({
      ok: true,
      status: 200,
      data: { entries: [archiveFile], next_cursor: null },
    });
    await flushAsync();
    expect(latest.entries.map((e) => e.name)).toEqual(['archive.zip']);

    docsDeferred.resolve({
      ok: true,
      status: 200,
      data: { entries: [notesFile], next_cursor: null },
    });
    await flushAsync();
    expect(latest.entries.map((e) => e.name)).toEqual(['archive.zip']);
    expect(latest.currentRef).toBeNull();

    // Rapid profile switch: late capability response from profile-1 must not overwrite profile-2
    const profile1CapDeferred = deferred<Awaited<ReturnType<typeof getFileCapabilities>>>();
    const profile2CapDeferred = deferred<Awaited<ReturnType<typeof getFileCapabilities>>>();
    vi.mocked(getFileCapabilities)
      .mockImplementationOnce(() => profile1CapDeferred.promise)
      .mockImplementationOnce(() => profile2CapDeferred.promise);
    vi.mocked(listFileEntries).mockResolvedValueOnce({
      ok: true,
      status: 200,
      data: { entries: [notesFile], next_cursor: null },
    });

    await renderBrowsing({ apiUrl: 'https://api-stale.example.test', profileId: 'profile-1' });
    await renderBrowsing({ apiUrl: 'https://api-stale.example.test', profileId: 'profile-2' });

    profile2CapDeferred.resolve({
      ok: true,
      status: 200,
      data: { capabilities: { ...fullCapabilities, thumbnails: true } },
    });
    await flushAsync();
    expect(latest.capabilities.thumbnails).toBe(true);

    profile1CapDeferred.resolve({
      ok: true,
      status: 200,
      data: { capabilities: { ...fullCapabilities, thumbnails: false, browse: false } },
    });
    await flushAsync();
    expect(latest.capabilities.thumbnails).toBe(true);
    expect(latest.capabilities.browse).toBe(true);
    expect(latest.entries.map((e) => e.name)).toEqual(['notes.txt']);
  });

  it('paginates with cursor state, prevents duplicate concurrent loadMore, and discards stale page on navigation', async () => {
    const page2Deferred = deferred<Awaited<ReturnType<typeof listFileEntries>>>();
    const extraFile: FileEntry = {
      ref: 'ref-file-extra',
      name: 'zebra.txt',
      display_path: '/zebra.txt',
      kind: 'file',
      size: 64,
      allowed_actions: ['download'],
    };

    vi.mocked(listFileEntries)
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [notesFile], next_cursor: 'cursor-1' } })
      .mockImplementationOnce(() => page2Deferred.promise);

    await renderBrowsing();
    expect(latest.hasMore).toBe(true);
    expect(latest.entries.map((e) => e.name)).toEqual(['notes.txt']);

    // Trigger loadMore twice concurrently -> only one API request
    act(() => {
      void latest.loadMore();
      void latest.loadMore();
    });
    expect(latest.loadingMore).toBe(true);
    expect(vi.mocked(listFileEntries)).toHaveBeenCalledTimes(2);
    expect(vi.mocked(listFileEntries)).toHaveBeenLastCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      null,
      'cursor-1',
      expect.any(AbortSignal),
    );

    page2Deferred.resolve({ ok: true, status: 200, data: { entries: [extraFile], next_cursor: null } });
    await flushAsync();
    expect(latest.loadingMore).toBe(false);
    expect(latest.hasMore).toBe(false);
    expect(latest.entries.map((e) => e.name)).toEqual(['notes.txt', 'zebra.txt']);

    // Stale loadMore discarded when user navigates into a directory
    const stalePageDeferred = deferred<Awaited<ReturnType<typeof listFileEntries>>>();
    vi.mocked(listFileEntries)
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [docsDir, notesFile], next_cursor: 'cursor-2' } })
      .mockImplementationOnce(() => stalePageDeferred.promise)
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [archiveFile], next_cursor: null } });

    act(() => {
      latest.refresh();
    });
    await flushAsync();
    expect(latest.hasMore).toBe(true);

    act(() => {
      void latest.loadMore();
    });
    act(() => {
      latest.activateEntry(docsDir);
    });
    await flushAsync();
    expect(latest.entries.map((e) => e.name)).toEqual(['archive.zip']);

    stalePageDeferred.resolve({ ok: true, status: 200, data: { entries: [extraFile], next_cursor: 'cursor-3' } });
    await flushAsync();
    expect(latest.entries.map((e) => e.name)).toEqual(['archive.zip']);
    expect(latest.hasMore).toBe(false);
  });

  it('sorts directories first, persists sort preferences, and handles selection and preview coordination', async () => {
    await renderBrowsing();

    // Default: name asc, directories first
    expect(latest.sortField).toBe('name');
    expect(latest.sortDirection).toBe('asc');
    expect(latest.entries.map((e) => e.name)).toEqual(['Documents', 'archive.zip', 'notes.txt']);

    // Size starts desc (largest file first)
    act(() => {
      latest.sortBy('size');
    });
    expect(latest.sortField).toBe('size');
    expect(latest.sortDirection).toBe('desc');
    expect(latest.entries.map((e) => e.name)).toEqual(['Documents', 'archive.zip', 'notes.txt']);
    expect(localStorage.getItem('clumoove_file_manager_sort_field')).toBe('size');
    expect(localStorage.getItem('clumoove_file_manager_sort_dir')).toBe('desc');

    // Toggle size to asc
    act(() => {
      latest.sortBy('size');
    });
    expect(latest.sortDirection).toBe('asc');
    expect(latest.entries.map((e) => e.name)).toEqual(['Documents', 'notes.txt', 'archive.zip']);

    // Activate file opens preview; in selection mode it toggles selection instead
    act(() => {
      latest.activateEntry(notesFile);
    });
    expect(latest.preview.entry?.ref).toBe(notesFile.ref);
    act(() => {
      latest.preview.open(archiveFile);
    });
    expect(latest.preview.entry?.ref).toBe(archiveFile.ref);
    act(() => {
      latest.preview.close();
    });
    expect(latest.preview.entry).toBeNull();

    // Ctrl/Meta click enters selection mode; toggleAll selects and clears all
    act(() => {
      latest.activateEntry(notesFile, { toggleSelection: true });
    });
    expect(latest.selection.active).toBe(true);
    expect(latest.selection.partiallySelected).toBe(true);
    expect(latest.selection.allSelected).toBe(false);

    act(() => {
      latest.activateEntry(docsDir);
    });
    expect(latest.selection.isSelected(docsDir.ref)).toBe(true);
    expect(latest.currentRef).toBeNull();

    act(() => {
      latest.selection.toggleAll();
    });
    expect(latest.selection.allSelected).toBe(true);
    expect(latest.selection.entries.length).toBe(3);

    act(() => {
      latest.selection.toggleAll();
    });
    expect(latest.selection.active).toBe(false);
    expect(latest.selection.entries.length).toBe(0);
  });

  it('debounces upload completion refresh for the active profile only and cancels on manual refresh', async () => {
    vi.useFakeTimers();
    await renderBrowsing();
    const initialCalls = vi.mocked(listFileEntries).mock.calls.length;

    act(() => {
      latest.notifyUploadCompleted('profile-2');
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600);
    });
    expect(vi.mocked(listFileEntries)).toHaveBeenCalledTimes(initialCalls);

    act(() => {
      latest.notifyUploadCompleted('profile-1');
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(499);
    });
    expect(vi.mocked(listFileEntries)).toHaveBeenCalledTimes(initialCalls);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(vi.mocked(listFileEntries)).toHaveBeenCalledTimes(initialCalls + 1);

    // Manual refresh cancels pending debounced upload refresh
    act(() => {
      latest.notifyUploadCompleted('profile-1');
      latest.refresh();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600);
    });
    expect(vi.mocked(listFileEntries)).toHaveBeenCalledTimes(initialCalls + 2);
  });
});
