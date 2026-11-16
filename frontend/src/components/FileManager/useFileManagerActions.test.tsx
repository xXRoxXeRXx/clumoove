import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import {
  batchDeleteFileEntries,
  batchMutateFileEntries,
  cancelCrossProfileFileTransfer,
  copyFileEntry,
  createArchiveTicket,
  createDirectory,
  createDownloadTicket,
  deleteFileEntry,
  listCrossProfileFileTransfers,
  listFileEntries,
  moveFileEntry,
  renameFileEntry,
  startCrossProfileFileTransfer,
  type FileCapabilities,
  type FileEntry,
} from '../../api/files';
import type { ConnectionProfilePublic } from '../../api/profiles';
import {
  useFileManagerActions,
  type FileManagerActions,
  type UseFileManagerActionsOptions,
} from './useFileManagerActions';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../../api/files', () => ({
  createDownloadTicket: vi.fn(),
  createArchiveTicket: vi.fn(),
  createDirectory: vi.fn(),
  deleteFileEntry: vi.fn(),
  batchDeleteFileEntries: vi.fn(),
  renameFileEntry: vi.fn(),
  copyFileEntry: vi.fn(),
  moveFileEntry: vi.fn(),
  batchMutateFileEntries: vi.fn(),
  listFileEntries: vi.fn(),
  listCrossProfileFileTransfers: vi.fn(),
  cancelCrossProfileFileTransfer: vi.fn(),
  startCrossProfileFileTransfer: vi.fn(),
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

const dirEntry: FileEntry = {
  ref: 'ref-dir-docs',
  name: 'Documents',
  display_path: '/Documents',
  kind: 'directory',
  size: 0,
  allowed_actions: ['download', 'upload', 'rename', 'copy', 'move', 'delete'],
};

const fileEntry: FileEntry = {
  ref: 'ref-file-notes',
  name: 'notes.txt',
  display_path: '/notes.txt',
  kind: 'file',
  size: 512,
  mime_type: 'text/plain',
  allowed_actions: ['download', 'rename', 'copy', 'move', 'delete'],
};

const archiveEntry: FileEntry = {
  ref: 'ref-file-archive',
  name: 'archive.zip',
  display_path: '/archive.zip',
  kind: 'file',
  size: 4096,
  mime_type: 'application/zip',
  allowed_actions: ['copy'],
};

const subDirEntry: FileEntry = {
  ref: 'ref-sub-dir',
  name: 'Sub',
  display_path: '/Documents/Sub',
  kind: 'directory',
  size: 0,
  allowed_actions: [],
};

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (err: unknown) => void;
};

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (err: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

async function flushMicrotasks(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    if (vi.isFakeTimers()) {
      await vi.advanceTimersByTimeAsync(0);
    } else {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  });
}

/** The module renders its own dialogs; tests find them the way a user would: by title and button text. */
function dialogByTitle(titleId: string): HTMLElement | null {
  return document.getElementById(titleId)?.closest<HTMLElement>('[role="dialog"]') ?? null;
}

const createDialog = () => dialogByTitle('create-dir-title');
const deleteDialog = () => dialogByTitle('delete-entry-title');
const batchDeleteDialog = () => dialogByTitle('batch-delete-title');
const renameDialog = () => dialogByTitle('rename-entry-title');
const conflictDialog = () => dialogByTitle('mutation-conflict-title');
const pickerDialog = () => dialogByTitle('destination-picker-title');

function findButton(scope: ParentNode, text: string): HTMLButtonElement {
  const button = Array.from(scope.querySelectorAll('button')).find((candidate) => candidate.textContent?.trim() === text);
  if (!button) throw new Error(`No button "${text}" in ${(scope as HTMLElement).textContent}`);
  return button;
}

async function press(scope: ParentNode, text: string): Promise<void> {
  await act(async () => {
    findButton(scope, text).click();
    await Promise.resolve();
  });
}

function typeInto(input: HTMLInputElement, value: string) {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

/** Submits through the form so validation inside the module is exercised even when the button is disabled. */
async function submitForm(scope: HTMLElement): Promise<void> {
  await act(async () => {
    scope.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await Promise.resolve();
  });
}

function alertText(scope: HTMLElement | null): string {
  return scope?.querySelector('[role="alert"]')?.textContent ?? '';
}

function pickerFolders(): string[] {
  const list = pickerDialog()?.querySelector('[data-testid="destination-entries"]');
  return Array.from(list?.querySelectorAll('button') ?? []).map((button) => button.textContent?.trim() ?? '');
}

function pickerBreadcrumbs(): string[] {
  const nav = pickerDialog()?.querySelector('nav');
  return Array.from(nav?.querySelectorAll('button') ?? []).map((button) =>
    (button.textContent ?? '').replace(/\s*\/$/, '').trim(),
  );
}

async function selectDestinationProfile(profileId: string): Promise<void> {
  const select = pickerDialog()!.querySelector<HTMLSelectElement>('#destination-profile')!;
  act(() => {
    select.value = profileId;
    select.dispatchEvent(new Event('change', { bubbles: true }));
  });
  await flushMicrotasks();
}

describe('useFileManagerActions', () => {
  let container: HTMLDivElement;
  let root: Root;
  let latest: FileManagerActions;
  const refreshListing = vi.fn();
  const clearSelection = vi.fn();
  const clearPreview = vi.fn();
  const navigateToDownload = vi.fn();

  function Probe(props: UseFileManagerActionsOptions) {
    latest = useFileManagerActions(props);
    return <>{latest.dialogs}</>;
  }

  async function renderActions(overrides?: Partial<UseFileManagerActionsOptions> & { profileId?: string }) {
    const { profileId, ...rest } = overrides ?? {};
    const props: UseFileManagerActionsOptions = {
      request: { apiUrl: 'https://api.example.test', token: 'jwt-token', profileId: profileId ?? 'profile-1' },
      profileName: 'Google Drive',
      profiles: defaultProfiles,
      currentRef: 'ref-parent',
      capabilities: fullCapabilities,
      selectedEntries: [],
      previewEntryRef: null,
      refreshListing,
      clearSelection,
      clearPreview,
      navigateToDownload,
      ...rest,
    };
    await act(async () => {
      root.render(<Probe {...props} />);
      await Promise.resolve();
    });
    await flushMicrotasks();
  }

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    refreshListing.mockReset();
    clearSelection.mockReset();
    clearPreview.mockReset();
    navigateToDownload.mockReset();

    vi.mocked(listCrossProfileFileTransfers).mockReset().mockResolvedValue({
      ok: true,
      status: 200,
      data: { transfers: [] },
    });
    vi.mocked(cancelCrossProfileFileTransfer).mockReset().mockResolvedValue({ ok: true, status: 200, data: {} });
    vi.mocked(createDownloadTicket).mockReset().mockResolvedValue({
      ok: true,
      status: 201,
      data: { download_url: '/api/files/download/ticket-1' },
    });
    vi.mocked(createArchiveTicket).mockReset().mockResolvedValue({
      ok: true,
      status: 201,
      data: { download_url: '/api/files/archive/ticket-zip' },
    });
    vi.mocked(createDirectory).mockReset().mockResolvedValue({
      ok: true,
      status: 201,
      data: { success: true, name: 'Projects' },
    });
    vi.mocked(deleteFileEntry).mockReset().mockResolvedValue({ ok: true, status: 204, data: {} });
    vi.mocked(batchDeleteFileEntries).mockReset().mockResolvedValue({
      ok: true,
      status: 200,
      data: { results: [] },
    });
    vi.mocked(renameFileEntry).mockReset().mockResolvedValue({
      ok: true,
      status: 200,
      data: { success: true, status: 'renamed', name: 'renamed.txt', native: true },
    });
    vi.mocked(copyFileEntry).mockReset().mockResolvedValue({
      ok: true,
      status: 200,
      data: { success: true, status: 'copied', name: 'notes.txt', native: true },
    });
    vi.mocked(moveFileEntry).mockReset().mockResolvedValue({
      ok: true,
      status: 200,
      data: { success: true, status: 'moved', name: 'notes.txt', native: true },
    });
    vi.mocked(batchMutateFileEntries).mockReset().mockResolvedValue({
      ok: true,
      status: 200,
      data: { results: [] },
    });
    vi.mocked(listFileEntries).mockReset().mockResolvedValue({
      ok: true,
      status: 200,
      data: { entries: [dirEntry, fileEntry], next_cursor: null },
    });
    vi.mocked(startCrossProfileFileTransfer).mockReset().mockResolvedValue({
      ok: true,
      status: 202,
      data: { transfer_id: 'transfer-1' },
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

  it('exposes intents and results instead of dialog state', async () => {
    await renderActions();

    expect(Object.keys(latest).sort()).toEqual([
      'batchBusy',
      'batchSummary',
      'cancelTransfer',
      'clearError',
      'copy',
      'createFolder',
      'delete',
      'dialogs',
      'dismissBatchSummary',
      'dismissTransfer',
      'download',
      'downloadArchive',
      'error',
      'move',
      'permissions',
      'rename',
      'transfers',
    ]);
  });

  it('closes an open dialog on Escape', async () => {
    await renderActions();

    act(() => latest.createFolder());
    expect(createDialog()).not.toBeNull();

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    expect(createDialog()).toBeNull();
  });

  it('reports a batch cross-profile transfer as busy until the server answers', async () => {
    const transfer = deferred<Awaited<ReturnType<typeof startCrossProfileFileTransfer>>>();
    vi.mocked(startCrossProfileFileTransfer).mockReturnValueOnce(transfer.promise);

    await renderActions({ selectedEntries: [fileEntry] });
    act(() => latest.copy());
    await flushMicrotasks();
    await selectDestinationProfile('profile-2');

    let pending!: Promise<void>;
    act(() => {
      pending = press(pickerDialog()!, 'Copy');
    });
    expect(latest.batchBusy).toBe(true);
    expect(findButton(pickerDialog()!, 'Working...').disabled).toBe(true);

    await act(async () => {
      transfer.resolve({ ok: true, status: 202, data: { transfer_id: 'xfer' } });
      await pending;
    });
    expect(latest.batchBusy).toBe(false);
    expect(pickerDialog()).toBeNull();
  });

  it('computes single-entry and batch permissions from capabilities and allowed_actions', async () => {
    await renderActions({
      capabilities: { ...fullCapabilities, mkdir: false, archive: false, move: false },
      selectedEntries: [fileEntry, archiveEntry],
    });

    expect(latest.permissions.canCreateFolder).toBe(false);
    expect(latest.permissions.canDownloadEntry(dirEntry)).toBe(false);
    expect(latest.permissions.canDownloadEntry(fileEntry)).toBe(true);
    expect(latest.permissions.canRenameEntry(fileEntry)).toBe(true);
    expect(latest.permissions.canRenameEntry(archiveEntry)).toBe(false);
    expect(latest.permissions.canMoveEntry(fileEntry)).toBe(false);
    expect(latest.permissions.canCopyEntry(archiveEntry)).toBe(true);
    expect(latest.permissions.canDeleteEntry(archiveEntry)).toBe(false);
    expect(latest.permissions.hasEntryActions(archiveEntry)).toBe(true);
    expect(latest.permissions.hasEntryActions({ ...archiveEntry, allowed_actions: [] })).toBe(false);

    expect(latest.permissions.canBatchDownload).toBe(false);
    expect(latest.permissions.canBatchCopy).toBe(true);
    expect(latest.permissions.canBatchMove).toBe(false);
    expect(latest.permissions.canBatchDelete).toBe(false);
  });

  it('creates a folder, surfaces translated errors on failure, and refreshes listing on success', async () => {
    vi.mocked(createDirectory)
      .mockResolvedValueOnce({ ok: false, status: 403, errorCode: 'FORBIDDEN', networkError: false })
      .mockResolvedValueOnce({ ok: true, status: 201, data: { success: true, name: 'Projects' } });

    await renderActions();

    act(() => latest.createFolder());
    expect(createDialog()).not.toBeNull();
    const input = () => createDialog()!.querySelector<HTMLInputElement>('#new-folder-name-input')!;

    // Whitespace-only names do not invoke the API
    typeInto(input(), '   ');
    await submitForm(createDialog()!);
    expect(createDirectory).not.toHaveBeenCalled();

    // API failure keeps dialog open with translated error
    typeInto(input(), 'Projects');
    await submitForm(createDialog()!);
    expect(createDialog()).not.toBeNull();
    expect(alertText(createDialog())).toBe('Access forbidden.');
    expect(refreshListing).not.toHaveBeenCalled();

    // Retry succeeds, closes dialog, and refreshes browsing session
    await submitForm(createDialog()!);
    expect(createDirectory).toHaveBeenLastCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      'Projects',
      'ref-parent',
      expect.any(AbortSignal)
    );
    expect(createDialog()).toBeNull();
    expect(refreshListing).toHaveBeenCalledTimes(1);

    // A reopened dialog starts clean
    act(() => latest.createFolder());
    expect(input().value).toBe('');
    expect(alertText(createDialog())).toBe('');
  });

  it('downloads a single file, prevents concurrent downloads, and surfaces translated errors', async () => {
    const firstTicket = deferred<{ ok: true; status: number; data: { download_url: string } }>();
    vi.mocked(createDownloadTicket)
      .mockReturnValueOnce(firstTicket.promise)
      .mockResolvedValueOnce({ ok: false, status: 403, errorCode: 'FORBIDDEN', networkError: false });

    await renderActions();

    // Ignores directories
    await act(async () => {
      await latest.download(dirEntry);
    });
    expect(createDownloadTicket).not.toHaveBeenCalled();

    let downloadPromise!: Promise<void>;
    act(() => {
      downloadPromise = latest.download(fileEntry);
    });

    // Concurrent download attempt is ignored while active
    await act(async () => {
      await latest.download(archiveEntry);
    });
    expect(createDownloadTicket).toHaveBeenCalledTimes(1);

    await act(async () => {
      firstTicket.resolve({ ok: true, status: 201, data: { download_url: '/api/files/download/ticket-1' } });
      await downloadPromise;
    });
    expect(navigateToDownload).toHaveBeenCalledWith('https://api.example.test/api/files/download/ticket-1');

    // Subsequent download proceeds once prior download completes
    await act(async () => {
      await latest.download(fileEntry);
    });
    expect(createDownloadTicket).toHaveBeenCalledTimes(2);
    expect(latest.error).toBe('Access forbidden.');

    act(() => latest.clearError());
    expect(latest.error).toBe('');
  });

  it('creates batch ZIP archive for selected entries and surfaces translated errors on failure', async () => {
    vi.mocked(createArchiveTicket)
      .mockResolvedValueOnce({ ok: true, status: 201, data: { download_url: '/api/files/archive/ticket-zip' } })
      .mockResolvedValueOnce({ ok: false, status: 500, errorCode: 'INTERNAL_ERROR', networkError: false });

    await renderActions({ selectedEntries: [dirEntry, fileEntry] });

    await act(async () => {
      await latest.downloadArchive();
    });
    expect(createArchiveTicket).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      ['ref-dir-docs', 'ref-file-notes']
    );
    expect(navigateToDownload).toHaveBeenCalledWith('https://api.example.test/api/files/archive/ticket-zip');
    expect(latest.batchBusy).toBe(false);

    await act(async () => {
      await latest.downloadArchive();
    });
    expect(latest.error).toBe('An internal server error occurred. Please try again later.');
  });

  it('deletes a single entry, handles non-empty directory recursive fallback, and clears matching preview', async () => {
    vi.mocked(deleteFileEntry)
      .mockResolvedValueOnce({ ok: false, status: 409, errorCode: 'FILES_DIRECTORY_NOT_EMPTY', networkError: false })
      .mockResolvedValueOnce({ ok: true, status: 204, data: {} })
      .mockResolvedValueOnce({ ok: true, status: 204, data: {} });

    await renderActions({ previewEntryRef: fileEntry.ref });

    // Directory deletion starts non-recursive when empty dir deletion is supported, then falls back on FILES_DIRECTORY_NOT_EMPTY
    act(() => latest.delete(dirEntry));
    expect(deleteDialog()?.textContent).toContain('Delete the empty folder Documents?');

    await press(deleteDialog()!, 'Delete');
    expect(deleteDialog()).not.toBeNull();
    expect(alertText(deleteDialog())).toBe('This folder is not empty. Confirm recursive deletion to remove it and its contents.');
    expect(deleteDialog()?.textContent).toContain('Delete Documents and all of its contents?');

    await press(deleteDialog()!, 'Delete recursively');
    expect(deleteFileEntry).toHaveBeenLastCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      dirEntry.ref,
      true,
      expect.any(AbortSignal)
    );
    expect(deleteDialog()).toBeNull();
    expect(clearPreview).not.toHaveBeenCalled();
    expect(refreshListing).toHaveBeenCalledTimes(1);

    // Deleting the currently previewed file clears preview
    act(() => latest.delete(fileEntry));
    await press(deleteDialog()!, 'Delete');
    expect(clearPreview).toHaveBeenCalledTimes(1);
    expect(refreshListing).toHaveBeenCalledTimes(2);
  });

  it('defaults single directory deletion to recursive when only recursive deletion is supported', async () => {
    await renderActions({
      capabilities: { ...fullCapabilities, delete_empty_directory: false, delete_recursive_directory: true },
    });

    act(() => latest.delete(dirEntry));
    expect(deleteDialog()?.textContent).toContain('This connection supports deleting folders only recursively.');
    expect(findButton(deleteDialog()!, 'Delete recursively').disabled).toBe(false);
  });

  it('refuses to delete a directory non-recursively when the connection cannot delete empty directories', async () => {
    await renderActions({
      capabilities: { ...fullCapabilities, delete_empty_directory: false, delete_recursive_directory: false },
    });

    act(() => latest.delete(dirEntry));
    expect(findButton(deleteDialog()!, 'Delete').disabled).toBe(true);
  });

  it('executes batch deletion with per-kind recursive flags, reports partial results, and auto-dismisses', async () => {
    vi.mocked(batchDeleteFileEntries)
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: {
          results: [
            { ref: dirEntry.ref, status: 'deleted' },
            { ref: fileEntry.ref, status: 'failed', error_code: 'FORBIDDEN' },
          ],
        },
      })
      .mockResolvedValueOnce({
        ok: false,
        status: 403,
        errorCode: 'FORBIDDEN',
        networkError: false,
      });

    vi.useFakeTimers();
    await renderActions({ selectedEntries: [dirEntry, fileEntry] });

    // Deleting without an entry targets the current selection
    act(() => latest.delete());
    expect(batchDeleteDialog()?.textContent).toContain('Delete 2 selected items?');
    expect(batchDeleteDialog()?.textContent).toContain('Selected folders and their contents will be deleted recursively.');

    await press(batchDeleteDialog()!, 'Delete recursively');
    expect(batchDeleteFileEntries).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      [
        { ref: dirEntry.ref, recursive: true },
        { ref: fileEntry.ref, recursive: false },
      ]
    );
    expect(batchDeleteDialog()).toBeNull();
    expect(latest.batchSummary).toEqual({ success: 1, failed: 1 });
    expect(clearSelection).toHaveBeenCalledTimes(1);
    expect(refreshListing).toHaveBeenCalledTimes(1);

    // Auto-clears the batch summary after 4000ms
    act(() => {
      vi.advanceTimersByTime(4000);
    });
    expect(latest.batchSummary).toBeNull();

    // Batch failure sets translated top-level error
    act(() => latest.delete());
    await press(batchDeleteDialog()!, 'Delete recursively');
    expect(latest.error).toBe('Access forbidden.');
    expect(batchDeleteDialog()).toBeNull();
  });

  it('renames an entry with validation, conflict resolution, and partial-operation refresh', async () => {
    vi.mocked(renameFileEntry)
      .mockResolvedValueOnce({
        ok: false,
        status: 409,
        errorCode: 'FILES_CONFLICT',
        networkError: false,
        data: {
          success: false,
          status: 'conflict',
          name: 'existing.txt',
          native: true,
          conflict_strategies: ['SKIP', 'OVERWRITE', 'RENAME'],
        } as Awaited<ReturnType<typeof renameFileEntry>>['data'] & {
          conflict_strategies: ('SKIP' | 'OVERWRITE' | 'RENAME')[];
        },
      })
      .mockResolvedValueOnce({
        ok: false,
        status: 409,
        errorCode: 'FILES_PARTIAL_OPERATION',
        networkError: false,
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: { success: true, status: 'renamed', name: 'existing (1).txt', native: true },
      });

    await renderActions();

    act(() => latest.rename(fileEntry));
    const input = () => renameDialog()!.querySelector<HTMLInputElement>('#rename-entry-name')!;
    expect(input().value).toBe('notes.txt');

    // Empty name validation
    typeInto(input(), '   ');
    await submitForm(renameDialog()!);
    expect(alertText(renameDialog())).toBe('Enter a name.');
    expect(renameFileEntry).not.toHaveBeenCalled();

    // First attempt returns FILES_CONFLICT
    typeInto(input(), 'existing.txt');
    await submitForm(renameDialog()!);
    expect(conflictDialog()).not.toBeNull();
    expect(Array.from(conflictDialog()!.querySelectorAll('button')).map((b) => b.textContent)).toEqual([
      'Cancel',
      'Skip existing file',
      'Overwrite existing file',
      'Keep both files',
    ]);

    // Resolving with OVERWRITE fails with FILES_PARTIAL_OPERATION -> refreshes and shows partial warning
    await press(conflictDialog()!, 'Overwrite existing file');
    expect(alertText(renameDialog())).toBe('Some items could not be completed. The folder has been refreshed.');
    expect(refreshListing).toHaveBeenCalledTimes(1);

    // Resolving with RENAME succeeds and closes
    await press(conflictDialog()!, 'Keep both files');
    expect(renameFileEntry).toHaveBeenLastCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      fileEntry.ref,
      'existing.txt',
      'RENAME',
      expect.any(AbortSignal)
    );
    expect(renameDialog()).toBeNull();
    expect(conflictDialog()).toBeNull();
    expect(refreshListing).toHaveBeenCalledTimes(2);
  });

  it('aborts in-flight rename on unmount and ignores late settlement', async () => {
    const renameDeferred = deferred<Awaited<ReturnType<typeof renameFileEntry>>>();
    let capturedSignal: AbortSignal | undefined;
    vi.mocked(renameFileEntry).mockImplementationOnce((_u, _t, _p, _r, _n, _s, signal) => {
      capturedSignal = signal;
      return renameDeferred.promise;
    });

    await renderActions();
    act(() => latest.rename(fileEntry));
    typeInto(renameDialog()!.querySelector<HTMLInputElement>('#rename-entry-name')!, 'moved.txt');

    let submitPromise!: Promise<void>;
    act(() => {
      submitPromise = submitForm(renameDialog()!);
    });
    expect(capturedSignal?.aborted).toBe(false);

    act(() => root.unmount());
    expect(capturedSignal?.aborted).toBe(true);

    await act(async () => {
      renameDeferred.resolve({ ok: true, status: 200, data: { success: true, status: 'renamed', name: 'moved.txt', native: true } });
      await submitPromise;
    });
    expect(refreshListing).not.toHaveBeenCalled();

    // Re-create root so afterEach unmount succeeds cleanly
    root = createRoot(container);
  });

  it('navigates destination picker folders, excludes selected entries, and flags invalid destinations', async () => {
    vi.mocked(listFileEntries)
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [dirEntry, fileEntry, subDirEntry], next_cursor: null } })
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [subDirEntry], next_cursor: null } })
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [dirEntry, fileEntry], next_cursor: null } });

    await renderActions({ selectedEntries: [dirEntry, fileEntry] });

    act(() => latest.copy());
    await flushMicrotasks();

    expect(pickerDialog()?.textContent).toContain('Copy item');
    expect(pickerDialog()?.textContent).toContain('Choose one destination folder for 2 selected items.');
    // Files and selected directories are excluded
    expect(pickerFolders()).toEqual(['Sub']);
    expect(pickerDialog()?.textContent).not.toContain('This folder cannot be used as a destination.');

    // Navigating into a folder lists its content
    await press(pickerDialog()!.querySelector('[data-testid="destination-entries"]')!, 'Sub');
    await flushMicrotasks();
    expect(pickerBreadcrumbs()).toEqual(['Google Drive', 'Sub']);
    expect(pickerFolders()).toEqual(['Sub']);

    // Going back to the root breadcrumb restores the root listing
    await press(pickerDialog()!.querySelector('nav')!, 'Google Drive /');
    await flushMicrotasks();
    expect(pickerBreadcrumbs()).toEqual(['Google Drive']);
    expect(pickerDialog()?.textContent).toContain('Selected destination: Google Drive');
  });

  it('flags a destination inside a selected directory as invalid', async () => {
    vi.mocked(listFileEntries)
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [dirEntry], next_cursor: null } })
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [subDirEntry], next_cursor: null } });

    await renderActions({ selectedEntries: [fileEntry] });
    act(() => latest.move());
    await flushMicrotasks();
    await press(pickerDialog()!.querySelector('[data-testid="destination-entries"]')!, 'Documents');
    await flushMicrotasks();
    expect(pickerFolders()).toEqual(['Sub']);

    // The selection changes to include the folder being browsed
    await renderActions({ selectedEntries: [dirEntry] });
    expect(pickerFolders()).toEqual([]);
    expect(pickerDialog()?.textContent).toContain('This folder cannot be used as a destination.');
  });

  it('dispatches same-profile single move as unified transfer, refreshes listing, and clears matching preview', async () => {
    const moveDeferred = deferred<Awaited<ReturnType<typeof moveFileEntry>>>();
    vi.mocked(moveFileEntry).mockReturnValueOnce(moveDeferred.promise);

    await renderActions({ previewEntryRef: fileEntry.ref });

    act(() => latest.move(fileEntry));
    await flushMicrotasks();

    await press(pickerDialog()!.querySelector('[data-testid="destination-entries"]')!, 'Documents');
    await flushMicrotasks();

    await press(pickerDialog()!, 'Move');

    // Picker closes immediately while transfer runs in background
    expect(pickerDialog()).toBeNull();
    expect(moveFileEntry).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      fileEntry.ref,
      dirEntry.ref,
      undefined,
      expect.any(AbortSignal)
    );
    expect(latest.transfers).toHaveLength(1);
    expect(latest.transfers[0]).toMatchObject({
      origin: 'same-profile',
      operation: 'move',
      status: 'running',
      sourceLabel: 'notes.txt',
      destinationLabel: 'Documents',
      itemCount: 1,
    });

    await act(async () => {
      moveDeferred.resolve({ ok: true, status: 200, data: { success: true, status: 'moved', name: 'notes.txt', native: true } });
      await Promise.resolve();
    });

    expect(latest.transfers[0]).toMatchObject({
      origin: 'same-profile',
      operation: 'move',
      status: 'completed',
      processedCount: 1,
    });
    expect(clearPreview).toHaveBeenCalledTimes(1);
    expect(refreshListing).toHaveBeenCalledTimes(1);
  });

  it('cancels an in-flight same-profile transfer and preserves cancelled status', async () => {
    const copyDeferred = deferred<Awaited<ReturnType<typeof copyFileEntry>>>();
    vi.mocked(copyFileEntry).mockReturnValueOnce(copyDeferred.promise);

    await renderActions();
    act(() => latest.copy(fileEntry));
    await flushMicrotasks();

    await press(pickerDialog()!, 'Copy');

    const transferId = latest.transfers[0].id;
    await act(async () => {
      await latest.cancelTransfer(transferId);
    });
    expect(latest.transfers[0].status).toBe('cancelled');

    // Late settlement does not overwrite cancelled state
    await act(async () => {
      copyDeferred.resolve({ ok: true, status: 200, data: { success: true, status: 'copied', name: 'notes.txt', native: true } });
      await Promise.resolve();
    });
    expect(latest.transfers[0].status).toBe('cancelled');
    expect(refreshListing).not.toHaveBeenCalled();

    act(() => latest.dismissTransfer(transferId));
    expect(latest.transfers).toEqual([]);
  });

  it('surfaces partial-operation failure on same-profile single copy and refreshes listing', async () => {
    vi.mocked(copyFileEntry).mockResolvedValueOnce({
      ok: false,
      status: 409,
      errorCode: 'FILES_PARTIAL_OPERATION',
      networkError: false,
    });

    await renderActions();
    act(() => latest.copy(fileEntry));
    await flushMicrotasks();

    await press(pickerDialog()!, 'Copy');
    await flushMicrotasks();

    expect(latest.transfers[0]).toMatchObject({
      origin: 'same-profile',
      status: 'failed',
      error: 'Some items could not be completed. The folder has been refreshed.',
    });
    expect(refreshListing).toHaveBeenCalledTimes(1);
  });

  it('dispatches same-profile batch copy, clears selection, and reports partial failure summary', async () => {
    vi.mocked(batchMutateFileEntries).mockResolvedValueOnce({
      ok: true,
      status: 200,
      data: {
        results: [
          { ref: dirEntry.ref, status: 'copied' },
          { ref: fileEntry.ref, status: 'failed', error_code: 'FORBIDDEN' },
        ],
      },
    });

    await renderActions({ selectedEntries: [dirEntry, fileEntry] });
    act(() => latest.copy());
    await flushMicrotasks();

    await press(pickerDialog()!, 'Copy');
    await flushMicrotasks();

    expect(clearSelection).toHaveBeenCalledTimes(1);
    expect(refreshListing).toHaveBeenCalledTimes(1);
    expect(latest.transfers[0]).toMatchObject({
      origin: 'same-profile',
      operation: 'copy',
      status: 'failed',
      itemCount: 2,
      processedCount: 1,
      error: '1 items completed; 1 failed.',
    });
  });

  it('dispatches cross-profile single copy and keeps picker open on batch transfer failure until retry', async () => {
    vi.mocked(startCrossProfileFileTransfer)
      .mockResolvedValueOnce({ ok: true, status: 202, data: { transfer_id: 'xfer-single' } })
      .mockResolvedValueOnce({ ok: false, status: 403, errorCode: 'FORBIDDEN', networkError: false })
      .mockResolvedValueOnce({ ok: true, status: 202, data: { transfer_id: 'xfer-batch' } });

    await renderActions({
      selectedEntries: [fileEntry, { ...archiveEntry, allowed_actions: ['copy', 'move'] }],
    });

    // Single cross-profile copy
    act(() => latest.copy(fileEntry));
    await flushMicrotasks();
    await selectDestinationProfile('profile-2');
    expect(pickerBreadcrumbs()).toEqual(['Dropbox']);
    expect(listFileEntries).toHaveBeenLastCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-2',
      null,
      undefined,
      expect.any(AbortSignal)
    );

    await press(pickerDialog()!, 'Copy');
    expect(startCrossProfileFileTransfer).toHaveBeenNthCalledWith(
      1,
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      [fileEntry.ref],
      'profile-2',
      null,
      'copy',
      undefined,
      expect.any(AbortSignal)
    );
    expect(pickerDialog()).toBeNull();

    // Batch cross-profile move failure keeps picker open with translated error
    act(() => latest.move());
    await flushMicrotasks();
    await selectDestinationProfile('profile-2');

    await press(pickerDialog()!, 'Move');
    expect(pickerDialog()).not.toBeNull();
    expect(alertText(pickerDialog())).toBe('Access forbidden.');
    expect(clearSelection).not.toHaveBeenCalled();
    // Batch transfers keep the exact call arity: no signal argument
    expect(vi.mocked(startCrossProfileFileTransfer).mock.calls[1]).toHaveLength(8);
    expect(latest.batchBusy).toBe(false);

    // Retry succeeds
    await press(pickerDialog()!, 'Move');
    expect(pickerDialog()).toBeNull();
    expect(clearSelection).toHaveBeenCalledTimes(1);
    expect(refreshListing).toHaveBeenCalledTimes(1);
  });

  it('maps cross-profile transfers into the unified transfer model and handles cancel failure and dismissal', async () => {
    vi.mocked(listCrossProfileFileTransfers).mockResolvedValue({
      ok: true,
      status: 200,
      data: {
        transfers: [
          {
            id: 'bg-transfer-1',
            operation: 'copy',
            status: 'RUNNING',
            source_profile_name: 'Google Drive',
            target_profile_name: 'Dropbox',
            total_files: 4,
            processed_files: 1,
            failed_files: 0,
            skipped_files: 0,
          },
          {
            id: 'bg-transfer-2',
            operation: 'move',
            status: 'COMPLETED',
            source_profile_name: 'Google Drive',
            target_profile_name: 'Dropbox',
            total_files: 2,
            processed_files: 2,
            failed_files: 0,
            skipped_files: 0,
          },
        ],
      },
    });
    vi.mocked(cancelCrossProfileFileTransfer)
      .mockResolvedValueOnce({ ok: false, status: 403, errorCode: 'FORBIDDEN', networkError: false })
      .mockResolvedValueOnce({ ok: true, status: 200, data: {} });

    await renderActions();

    expect(latest.transfers).toHaveLength(2);
    expect(latest.transfers[0]).toMatchObject({
      id: 'bg-transfer-1',
      origin: 'cross-profile',
      operation: 'copy',
      status: 'running',
      sourceLabel: 'Google Drive',
      destinationLabel: 'Dropbox',
      itemCount: 4,
      processedCount: 1,
    });
    expect(latest.transfers[1]).toMatchObject({
      id: 'bg-transfer-2',
      origin: 'cross-profile',
      operation: 'move',
      status: 'completed',
    });

    await act(async () => {
      await latest.cancelTransfer('bg-transfer-1');
    });
    expect(latest.error).toBe('Access forbidden.');

    await act(async () => {
      await latest.cancelTransfer('bg-transfer-1');
    });
    expect(cancelCrossProfileFileTransfer).toHaveBeenCalledWith('https://api.example.test', 'jwt-token', 'bg-transfer-1');

    act(() => latest.dismissTransfer('bg-transfer-2'));
    expect(latest.transfers.map((t) => t.id)).toEqual(['bg-transfer-1']);
  });

  it('keeps an in-flight same-profile transfer running and settles it after the profile changes', async () => {
    const copyDeferred = deferred<Awaited<ReturnType<typeof copyFileEntry>>>();
    let copySignal: AbortSignal | undefined;
    vi.mocked(copyFileEntry).mockImplementationOnce((...args) => {
      copySignal = args[6] as AbortSignal | undefined;
      return copyDeferred.promise;
    });

    await renderActions();
    act(() => latest.copy(fileEntry));
    await flushMicrotasks();
    await press(pickerDialog()!, 'Copy');
    expect(latest.transfers[0].status).toBe('running');

    await renderActions({ profileId: 'profile-2' });
    expect(copySignal?.aborted).toBe(false);
    expect(latest.transfers[0].status).toBe('running');

    await act(async () => {
      copyDeferred.resolve({ ok: true, status: 200, data: { success: true, status: 'copied', name: 'notes.txt', native: true } });
      await Promise.resolve();
    });
    expect(latest.transfers[0].status).toBe('completed');
    expect(refreshListing).toHaveBeenCalledTimes(1);
  });

  it('preserves the server-reported status of cross-profile transfers', async () => {
    vi.mocked(listCrossProfileFileTransfers).mockResolvedValue({
      ok: true,
      status: 200,
      data: {
        transfers: [
          {
            id: 'bg-transfer-paused',
            operation: 'copy',
            status: 'PAUSED_CONNECTION_LOSS',
            source_profile_name: 'Google Drive',
            target_profile_name: 'Dropbox',
            total_files: 4,
            processed_files: 1,
            failed_files: 0,
            skipped_files: 0,
          },
        ],
      },
    });

    await renderActions();

    expect(latest.transfers[0]).toMatchObject({ id: 'bg-transfer-paused', serverStatus: 'PAUSED_CONNECTION_LOSS' });
  });

  it('aborts in-flight folder creation and single deletion on unmount without updating stale state', async () => {
    const mkdirDeferred = deferred<Awaited<ReturnType<typeof createDirectory>>>();
    const deleteDeferred = deferred<Awaited<ReturnType<typeof deleteFileEntry>>>();
    let mkdirSignal: AbortSignal | undefined;
    let deleteSignal: AbortSignal | undefined;

    vi.mocked(createDirectory).mockImplementationOnce((_u, _t, _p, _n, _parent, signal) => {
      mkdirSignal = signal;
      return mkdirDeferred.promise;
    });
    vi.mocked(deleteFileEntry).mockImplementationOnce((_u, _t, _p, _r, _rec, signal) => {
      deleteSignal = signal;
      return deleteDeferred.promise;
    });

    await renderActions();

    act(() => latest.createFolder());
    typeInto(createDialog()!.querySelector<HTMLInputElement>('#new-folder-name-input')!, 'AsyncFolder');
    let mkdirPromise!: Promise<void>;
    act(() => {
      mkdirPromise = submitForm(createDialog()!);
    });
    expect(mkdirSignal?.aborted).toBe(false);

    act(() => latest.delete(fileEntry));
    let deletePromise!: Promise<void>;
    act(() => {
      deletePromise = press(deleteDialog()!, 'Delete');
    });
    expect(deleteSignal?.aborted).toBe(false);

    act(() => root.unmount());
    expect(mkdirSignal?.aborted).toBe(true);
    expect(deleteSignal?.aborted).toBe(true);

    await act(async () => {
      mkdirDeferred.resolve({ ok: true, status: 201, data: { success: true, name: 'AsyncFolder' } });
      deleteDeferred.resolve({ ok: true, status: 204, data: {} });
      await Promise.all([mkdirPromise, deletePromise]);
    });
    expect(refreshListing).not.toHaveBeenCalled();

    root = createRoot(container);
  });

  it('cancels conflict resolution cleanly and excludes single-entry mutation target from picker', async () => {
    vi.mocked(renameFileEntry).mockResolvedValueOnce({
      ok: false,
      status: 409,
      errorCode: 'FILES_CONFLICT',
      networkError: false,
      data: {
        success: false,
        status: 'conflict',
        name: 'existing.txt',
        native: true,
        conflict_strategies: ['SKIP', 'OVERWRITE'],
      } as Awaited<ReturnType<typeof renameFileEntry>>['data'] & {
        conflict_strategies: ('SKIP' | 'OVERWRITE')[];
      },
    });
    vi.mocked(listFileEntries)
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [dirEntry, fileEntry], next_cursor: null } })
      .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [], next_cursor: null } });

    await renderActions();

    act(() => latest.rename(fileEntry));
    typeInto(renameDialog()!.querySelector<HTMLInputElement>('#rename-entry-name')!, 'existing.txt');
    await submitForm(renameDialog()!);
    expect(conflictDialog()).not.toBeNull();

    await press(conflictDialog()!, 'Cancel');
    expect(conflictDialog()).toBeNull();
    expect(renameDialog()).toBeNull();
    expect(renameFileEntry).toHaveBeenCalledTimes(1);
    expect(refreshListing).not.toHaveBeenCalled();

    // Single-entry destination picker excludes the mutation target directory
    act(() => latest.copy(dirEntry));
    await flushMicrotasks();
    expect(pickerDialog()?.textContent).toContain('Choose a destination folder for Documents.');
    expect(pickerFolders()).toEqual([]);
  });

  it('completes same-profile single copy without calling cross-profile cancel on settled transfer', async () => {
    vi.mocked(copyFileEntry).mockResolvedValueOnce({
      ok: true,
      status: 200,
      data: { success: true, status: 'copied', name: 'notes.txt', native: true },
    });

    await renderActions();
    act(() => latest.copy(fileEntry));
    await flushMicrotasks();

    await press(pickerDialog()!, 'Copy');
    await flushMicrotasks();

    expect(latest.transfers[0]).toMatchObject({
      origin: 'same-profile',
      operation: 'copy',
      status: 'completed',
      processedCount: 1,
    });
    expect(refreshListing).toHaveBeenCalledTimes(1);

    await act(async () => {
      await latest.cancelTransfer(latest.transfers[0].id);
    });
    expect(cancelCrossProfileFileTransfer).not.toHaveBeenCalled();
  });

  it('cancels an in-flight same-profile batch move cleanly', async () => {
    const batchDeferred = deferred<Awaited<ReturnType<typeof batchMutateFileEntries>>>();
    let batchSignal: AbortSignal | undefined;
    vi.mocked(batchMutateFileEntries).mockImplementationOnce(
      (_op, _url, _tok, _pid, _refs, _dest, _strat, signal) => {
        batchSignal = signal;
        return batchDeferred.promise;
      },
    );

    await renderActions({
      selectedEntries: [dirEntry, fileEntry],
    });
    act(() => latest.move());
    await flushMicrotasks();

    await press(pickerDialog()!, 'Move');
    expect(latest.transfers[0]).toMatchObject({
      origin: 'same-profile',
      operation: 'move',
      status: 'running',
      itemCount: 2,
    });
    expect(batchSignal?.aborted).toBe(false);

    await act(async () => {
      await latest.cancelTransfer(latest.transfers[0].id);
    });
    expect(batchSignal?.aborted).toBe(true);
    expect(latest.transfers[0].status).toBe('cancelled');
    expect(cancelCrossProfileFileTransfer).not.toHaveBeenCalled();

    await act(async () => {
      batchDeferred.resolve({ ok: true, status: 200, data: { results: [] } });
      await Promise.resolve();
    });
    expect(latest.transfers[0].status).toBe('cancelled');
    expect(refreshListing).not.toHaveBeenCalled();
  });
});
