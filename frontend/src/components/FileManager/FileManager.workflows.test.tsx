import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import { FileManager } from './FileManager';
import {
  batchDeleteFileEntries,
  batchMutateFileEntries,
  cancelCrossProfileFileTransfer,
  copyFileEntry,
  createArchiveTicket,
  createDirectory,
  createDownloadTicket,
  deleteFileEntry,
  getFileCapabilities,
  getFileThumbnailResult,
  listCrossProfileFileTransfers,
  listFileEntries,
  moveFileEntry,
  renameFileEntry,
  startCrossProfileFileTransfer,
  uploadFile,
  type FileCapabilities,
  type FileEntry,
} from '../../api/files';
import { listConnectionProfiles, type ConnectionProfilePublic } from '../../api/profiles';
import { clearThumbnailCaches } from '../../utils/thumbnailLoader';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../../api/files', () => ({
  getFileCapabilities: vi.fn(),
  listFileEntries: vi.fn(),
  createDownloadTicket: vi.fn(),
  createArchiveTicket: vi.fn(),
  createDirectory: vi.fn(),
  deleteFileEntry: vi.fn(),
  batchDeleteFileEntries: vi.fn(),
  renameFileEntry: vi.fn(),
  copyFileEntry: vi.fn(),
  moveFileEntry: vi.fn(),
  batchMutateFileEntries: vi.fn(),
  listCrossProfileFileTransfers: vi.fn(),
  cancelCrossProfileFileTransfer: vi.fn(),
  startCrossProfileFileTransfer: vi.fn(),
  uploadFile: vi.fn(),
  getFileThumbnailResult: vi.fn(),
}));

vi.mock('../../api/profiles', () => ({
  listConnectionProfiles: vi.fn(),
}));

vi.mock('react-pdf', () => ({
  Document: ({ children }: { children: React.ReactNode }) => <div data-testid="mock-pdf-document">{children}</div>,
  Page: ({ pageNumber }: { pageNumber: number }) => <div data-testid="mock-pdf-page">Page {pageNumber}</div>,
  pdfjs: { GlobalWorkerOptions: { workerSrc: '' } },
}));

vi.mock('./docxPreview.worker.ts?worker', () => ({
  default: class MockWorker {
    postMessage = vi.fn();
    terminate = vi.fn();
    onmessage = null;
    onerror = null;
  },
}));

vi.mock('./xlsxPreview.worker.ts?worker', () => ({
  default: class MockWorker {
    postMessage = vi.fn();
    terminate = vi.fn();
    onmessage = null;
    onerror = null;
  },
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

const defaultRootEntries: FileEntry[] = [
  {
    ref: 'ref-dir-docs',
    name: 'Documents',
    display_path: '/Documents',
    kind: 'directory',
    size: 0,
    modified_at: '2026-05-01T10:00:00Z',
    allowed_actions: ['download', 'upload', 'rename', 'copy', 'move', 'delete'],
  },
  {
    ref: 'ref-file-notes',
    name: 'notes.txt',
    display_path: '/notes.txt',
    kind: 'file',
    size: 512,
    modified_at: '2026-05-02T10:00:00Z',
    mime_type: 'text/plain',
    allowed_actions: ['download', 'rename', 'copy', 'move', 'delete'],
  },
  {
    ref: 'ref-file-archive',
    name: 'archive.zip',
    display_path: '/archive.zip',
    kind: 'file',
    size: 4096,
    modified_at: '2026-05-03T10:00:00Z',
    mime_type: 'application/zip',
    allowed_actions: ['download', 'rename', 'copy', 'move', 'delete'],
  },
];

const nestedDocsEntries: FileEntry[] = [
  {
    ref: 'ref-file-nested',
    name: 'readme.md',
    display_path: '/Documents/readme.md',
    kind: 'file',
    size: 256,
    mime_type: 'text/markdown',
    allowed_actions: ['download', 'rename', 'copy', 'move', 'delete'],
  },
];

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

async function flushAsync(cycles = 4): Promise<void> {
  for (let i = 0; i < cycles; i++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 15));
    });
  }
}

function setInputValue(input: HTMLInputElement, value: string): void {
  const nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set;
  nativeSetter?.call(input, value);
  input.dispatchEvent(new Event('input', { bubbles: true }));
  input.dispatchEvent(new Event('change', { bubbles: true }));
}

function setSelectValue(select: HTMLSelectElement, value: string): void {
  const nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value')?.set;
  nativeSetter?.call(select, value);
  select.dispatchEvent(new Event('change', { bubbles: true }));
}

function findButton(rootEl: ParentNode, text: string): HTMLButtonElement | undefined {
  return Array.from(rootEl.querySelectorAll<HTMLButtonElement>('button')).find(
    (button) => button.textContent?.trim() === text || button.getAttribute('aria-label') === text
  );
}

function makeFile(name: string, content = 'file body'): File {
  const blob = new Blob([content], { type: 'text/plain' });
  return new File([blob], name, { type: 'text/plain', lastModified: Date.now() });
}

describe('FileManager screen workflows', () => {
  let container: HTMLDivElement;
  let root: Root;
  const onProfileChange = vi.fn();
  const onBack = vi.fn();
  const originalLocation = window.location;
  let assignMock: ReturnType<typeof vi.fn>;

  async function renderManager(props?: Partial<React.ComponentProps<typeof FileManager>>) {
    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
          onBack={onBack}
          {...props}
        />
      );
      await Promise.resolve();
    });
    await flushAsync();
  }

  async function switchToGridView() {
    const gridBtn = container.querySelector<HTMLButtonElement>('button[aria-label="Grid view"]');
    expect(gridBtn).not.toBeNull();
    await act(async () => {
      gridBtn?.click();
      await Promise.resolve();
    });
    await flushAsync(2);
  }

  async function selectEntriesByName(...names: string[]) {
    for (const name of names) {
      const checkbox = container.querySelector<HTMLInputElement>(`input[aria-label="Select ${name}"]`);
      expect(checkbox).not.toBeNull();
      await act(async () => {
        checkbox?.click();
        await Promise.resolve();
      });
    }
  }

  async function openDirectoryByName(name: string) {
    const row = Array.from(container.querySelectorAll('tbody tr')).find((tr) => tr.textContent?.includes(name));
    expect(row).toBeDefined();
    await act(async () => {
      row?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await Promise.resolve();
    });
    await flushAsync();
  }

  async function expandTransferQueue() {
    const expandBtn = container.querySelector<HTMLButtonElement>('button[aria-label="Expand transfers"]');
    expect(expandBtn).not.toBeNull();
    await act(async () => {
      expandBtn?.click();
      await Promise.resolve();
    });
  }

  async function openEntryAction(entryName: string, actionLabel: string) {
    const actionsBtn = container.querySelector<HTMLButtonElement>(`button[aria-label="Actions for ${entryName}"]`);
    expect(actionsBtn).not.toBeNull();
    await act(async () => {
      actionsBtn?.click();
      await Promise.resolve();
    });
    const menuItem = Array.from(document.body.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')).find(
      (item) => item.textContent?.trim() === actionLabel
    );
    expect(menuItem).toBeDefined();
    await act(async () => {
      menuItem?.click();
      await Promise.resolve();
    });
    await flushAsync(2);
  }

  beforeEach(async () => {
    localStorage.clear();
    clearThumbnailCaches();
    await i18n.changeLanguage('en');

    onProfileChange.mockReset();
    onBack.mockReset();

    assignMock = vi.fn();
    Object.defineProperty(window, 'location', {
      value: { ...originalLocation, assign: assignMock },
      writable: true,
      configurable: true,
    });

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
      data: { entries: defaultRootEntries, next_cursor: null },
    });
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
      data: { success: true, name: 'Created' },
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
    vi.mocked(startCrossProfileFileTransfer).mockReset().mockResolvedValue({
      ok: true,
      status: 202,
      data: { transfer_id: 'transfer-1' },
    });
    vi.mocked(uploadFile).mockReset().mockResolvedValue({
      ok: true,
      status: 201,
      data: { status: 'uploaded', name: 'upload.txt' },
    });
    vi.mocked(getFileThumbnailResult).mockReset().mockResolvedValue({ blob: null, status: 404 });

    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    vi.useRealTimers();
    act(() => root?.unmount());
    container?.remove();
    Object.defineProperty(window, 'location', {
      value: originalLocation,
      writable: true,
      configurable: true,
    });
    clearThumbnailCaches();
    vi.restoreAllMocks();
  });

  describe('browsing workflows', () => {
    it('shows empty profile and unselected profile states', async () => {
      vi.mocked(listConnectionProfiles).mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: { profiles: [] },
      });

      await renderManager({ profileId: '' });

      expect(container.textContent).toContain('No saved connections are available.');
      expect(container.textContent).toContain('Select a connection to browse its files.');
    });

    it('shows empty directory message when folder has no entries', async () => {
      vi.mocked(listFileEntries).mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: { entries: [], next_cursor: null },
      });

      await renderManager();
      expect(container.textContent).toContain('This folder is empty.');
    });

    it('shows unsupported browse state and disables unavailable folder controls', async () => {
      vi.mocked(getFileCapabilities).mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: { capabilities: { ...fullCapabilities, browse: false, mkdir: false, upload: false } },
      });

      await renderManager();
      expect(container.textContent).toContain('Browsing is not supported by this connection.');
      expect(container.querySelector<HTMLButtonElement>('button[aria-label="Refresh files"]')?.disabled).toBe(true);
      expect(container.querySelector<HTMLButtonElement>('button[aria-label="New folder"]')?.disabled).toBe(true);
      expect(container.querySelector<HTMLButtonElement>('button[aria-label="Upload files"]')?.disabled).toBe(true);
    });

    it('displays translated alert when loading connection profiles fails', async () => {
      vi.mocked(listConnectionProfiles).mockResolvedValueOnce({
        ok: false,
        status: 500,
        errorCode: 'INTERNAL_ERROR',
        networkError: false,
      });

      await renderManager();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain(
        'An internal server error occurred. Please try again later.'
      );
    });

    it('displays translated alert when loading capabilities fails', async () => {
      vi.mocked(getFileCapabilities).mockResolvedValueOnce({
        ok: false,
        status: 403,
        errorCode: 'FORBIDDEN',
        networkError: false,
      });

      await renderManager();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('Access forbidden.');
    });

    it('displays translated alert when listing directory entries fails', async () => {
      vi.mocked(listFileEntries).mockResolvedValueOnce({
        ok: false,
        status: 400,
        errorCode: 'FILES_DIRECTORY_TOO_LARGE',
        networkError: false,
      });

      await renderManager();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain(
        'This directory contains too many entries to browse safely.'
      );
    });

    it('displays path fallback notice and triggers onBack when Back is clicked', async () => {
      await renderManager({ initialPathFallback: true });

      expect(container.querySelector('[role="status"]')?.textContent).toContain(
        'The saved folder is no longer available. Opened its nearest available parent instead.'
      );

      await act(async () => {
        findButton(container, 'Back')?.click();
        await Promise.resolve();
      });
      expect(onBack).toHaveBeenCalledTimes(1);
    });

    it('refreshes the active folder and navigates back via root breadcrumb', async () => {
      vi.mocked(listFileEntries)
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: defaultRootEntries, next_cursor: null } })
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: nestedDocsEntries, next_cursor: null } })
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          data: {
            entries: [
              ...nestedDocsEntries,
              {
                ref: 'ref-file-added',
                name: 'changelog.md',
                display_path: '/Documents/changelog.md',
                kind: 'file',
                size: 128,
                allowed_actions: ['download'],
              },
            ],
            next_cursor: null,
          },
        })
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: defaultRootEntries, next_cursor: null } });

      await renderManager();
      await openDirectoryByName('Documents');
      expect(container.textContent).toContain('readme.md');

      const refreshBtn = container.querySelector<HTMLButtonElement>('button[aria-label="Refresh files"]');
      await act(async () => {
        refreshBtn?.click();
        await Promise.resolve();
      });
      await flushAsync();
      expect(container.textContent).toContain('changelog.md');

      const rootBreadcrumb = Array.from(container.querySelectorAll<HTMLButtonElement>('nav button')).find((b) =>
        b.textContent?.includes('Google Drive')
      );
      await act(async () => {
        rootBreadcrumb?.click();
        await Promise.resolve();
      });
      await flushAsync();
      expect(container.textContent).toContain('notes.txt');
      expect(container.textContent).toContain('archive.zip');
    });

    it('persists sort preference across remount and applies sorting in grid view', async () => {
      await renderManager();

      const sizeHeaderBtn = container.querySelectorAll('thead th')[2]?.querySelector('button');
      await act(async () => {
        sizeHeaderBtn?.click();
        await Promise.resolve();
      });

      await switchToGridView();

      // Unmount and remount on a fresh root to verify persisted sort and view mode
      act(() => root.unmount());
      root = createRoot(container);
      await renderManager();

      const gridCells = Array.from(container.querySelectorAll('[role="gridcell"]'));
      expect(gridCells.map((cell) => cell.getAttribute('title'))).toEqual([
        'Documents',
        'archive.zip',
        'notes.txt',
      ]);
    });

    it('renders image thumbnails in the listing when thumbnails capability is enabled', async () => {
      vi.mocked(getFileCapabilities).mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: { capabilities: { ...fullCapabilities, thumbnails: true } },
      });
      vi.mocked(listFileEntries).mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: {
          entries: [
            {
              ref: 'ref-img-1',
              name: 'photo.jpg',
              display_path: '/photo.jpg',
              kind: 'file',
              size: 2048,
              mime_type: 'image/jpeg',
              allowed_actions: ['download'],
            },
          ],
          next_cursor: null,
        },
      });
      vi.mocked(getFileThumbnailResult).mockResolvedValueOnce({
        blob: new Blob(['img'], { type: 'image/jpeg' }),
        status: 200,
      });
      vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:https://api.example.test/thumb-1');

      await renderManager();

      const img = container.querySelector<HTMLImageElement>('img[alt="photo.jpg"]');
      expect(img).not.toBeNull();
      expect(img?.getAttribute('src')).toBe('blob:https://api.example.test/thumb-1');
    });
  });

  describe('selection workflows', () => {
    it('supports select-all, indeterminate partial selection, and clearing selection', async () => {
      await renderManager();

      const selectAll = container.querySelector<HTMLInputElement>('input[aria-label="Select all loaded items"]');
      expect(selectAll).not.toBeNull();
      expect(selectAll?.checked).toBe(false);
      expect(selectAll?.indeterminate).toBe(false);

      await selectEntriesByName('notes.txt');
      expect(selectAll?.checked).toBe(false);
      expect(selectAll?.indeterminate).toBe(true);
      expect(container.querySelector('[role="toolbar"]')?.textContent).toContain('1 item selected');

      await act(async () => {
        selectAll?.click();
        await Promise.resolve();
      });
      expect(selectAll?.checked).toBe(true);
      expect(selectAll?.indeterminate).toBe(false);
      expect(container.querySelector('[role="toolbar"]')?.textContent).toContain('3 items selected');

      await act(async () => {
        selectAll?.click();
        await Promise.resolve();
      });
      expect(container.querySelector('[role="toolbar"]')).toBeNull();
    });

    it('clears active selection when switching to another connection profile', async () => {
      await renderManager();
      await selectEntriesByName('notes.txt');
      expect(container.querySelector('[role="toolbar"]')).not.toBeNull();

      const dropboxBtn = Array.from(container.querySelectorAll<HTMLButtonElement>('aside button')).find((b) =>
        b.textContent?.includes('Dropbox')
      );
      await act(async () => {
        dropboxBtn?.click();
        await Promise.resolve();
      });
      expect(onProfileChange).toHaveBeenCalledWith('profile-2');

      // Re-render with updated profileId prop as the parent container would
      await renderManager({ profileId: 'profile-2' });
      expect(container.querySelector('[role="toolbar"]')).toBeNull();
    });

    it('gates toolbar batch actions by archive capability and intersection of allowed_actions', async () => {
      vi.mocked(getFileCapabilities).mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: { capabilities: { ...fullCapabilities, archive: false } },
      });
      vi.mocked(listFileEntries).mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: {
          entries: [
            {
              ref: 'ref-a',
              name: 'a.txt',
              display_path: '/a.txt',
              kind: 'file',
              size: 100,
              allowed_actions: ['copy', 'move', 'delete'],
            },
            {
              ref: 'ref-b',
              name: 'b.txt',
              display_path: '/b.txt',
              kind: 'file',
              size: 200,
              allowed_actions: ['copy'],
            },
          ],
          next_cursor: null,
        },
      });

      await renderManager();
      await selectEntriesByName('a.txt');

      const toolbar = container.querySelector('[role="toolbar"]');
      expect(toolbar?.querySelector<HTMLButtonElement>('button[aria-label="Download ZIP"]')?.disabled).toBe(true);
      expect(toolbar?.querySelector<HTMLButtonElement>('button[aria-label="Copy"]')?.disabled).toBe(false);
      expect(toolbar?.querySelector<HTMLButtonElement>('button[aria-label="Move"]')?.disabled).toBe(false);
      expect(toolbar?.querySelector<HTMLButtonElement>('button[aria-label="Delete"]')?.disabled).toBe(false);

      await selectEntriesByName('b.txt');

      expect(toolbar?.querySelector<HTMLButtonElement>('button[aria-label="Copy"]')?.disabled).toBe(false);
      expect(toolbar?.querySelector<HTMLButtonElement>('button[aria-label="Move"]')?.disabled).toBe(true);
      expect(toolbar?.querySelector<HTMLButtonElement>('button[aria-label="Delete"]')?.disabled).toBe(true);
    });

    it('supports keyboard Enter in grid view to toggle selection during selection mode', async () => {
      await renderManager();
      await switchToGridView();

      await selectEntriesByName('notes.txt');
      expect(container.querySelector('[role="toolbar"]')?.textContent).toContain('1 item selected');

      const docsCard = Array.from(container.querySelectorAll<HTMLElement>('[role="gridcell"]')).find(
        (c) => c.getAttribute('title') === 'Documents'
      );
      await act(async () => {
        docsCard?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
        await Promise.resolve();
      });

      expect(container.querySelector('[role="toolbar"]')?.textContent).toContain('2 items selected');
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });
  });

  describe('preview workflows', () => {
    it('opens real preview dialog from grid view and renders fetched text content', async () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(
        new Response('Hello preview world', {
          status: 200,
          headers: { 'Content-Type': 'text/plain', 'Content-Length': '19' },
        })
      );

      await renderManager();
      await switchToGridView();

      const notesCard = Array.from(container.querySelectorAll<HTMLElement>('[role="gridcell"]')).find(
        (cell) => cell.getAttribute('title') === 'notes.txt'
      );
      await act(async () => {
        notesCard?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        await Promise.resolve();
      });
      await flushAsync(6);

      const dialog = document.querySelector('[role="dialog"]');
      expect(dialog).not.toBeNull();
      expect(dialog?.textContent).toContain('Preview: notes.txt');
      expect(dialog?.textContent).toContain('Hello preview world');
    });

    it('navigates between files in preview skipping directories, downloads from preview, and closes', async () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(
        new Response('Hello preview world', {
          status: 200,
          headers: { 'Content-Type': 'text/plain', 'Content-Length': '19' },
        })
      );

      await renderManager();

      const notesBtn = Array.from(container.querySelectorAll<HTMLButtonElement>('tbody button')).find((btn) =>
        btn.textContent?.includes('notes.txt')
      );
      await act(async () => {
        notesBtn?.click();
        await Promise.resolve();
      });
      await flushAsync(6);

      const dialog = document.querySelector('[role="dialog"]');
      expect(dialog?.textContent).toContain('2 / 2');

      const prevBtn = dialog?.querySelector<HTMLButtonElement>('button[aria-label="Previous file"]');
      expect(prevBtn?.disabled).toBe(false);
      await act(async () => {
        prevBtn?.click();
        await Promise.resolve();
      });
      await flushAsync(4);

      expect(dialog?.textContent).toContain('Preview: archive.zip');
      expect(dialog?.textContent).toContain('1 / 2');
      expect(dialog?.textContent).toContain('This file cannot be safely previewed.');

      const downloadBtn = dialog?.querySelector<HTMLButtonElement>('header button[aria-label="Download archive.zip"]');
      await act(async () => {
        downloadBtn?.click();
        await Promise.resolve();
      });
      await flushAsync(2);
      expect(assignMock).toHaveBeenCalledWith('https://api.example.test/api/files/download/ticket-1');

      const closeBtn = dialog?.querySelector<HTMLButtonElement>('button[aria-label="Close"]');
      await act(async () => {
        closeBtn?.click();
        await Promise.resolve();
      });
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });
  });

  describe('transfer workflows', () => {
    it('displays translated error alert when single download ticket generation fails', async () => {
      vi.mocked(createDownloadTicket).mockResolvedValueOnce({
        ok: false,
        status: 403,
        errorCode: 'FORBIDDEN',
        networkError: false,
      });

      await renderManager();
      await openEntryAction('notes.txt', 'Download');

      expect(container.querySelector('[role="alert"]')?.textContent).toContain('Access forbidden.');
      expect(assignMock).not.toHaveBeenCalled();
    });

    it('clears a stale action error when the listing reloads', async () => {
      vi.mocked(createDownloadTicket).mockResolvedValueOnce({
        ok: false,
        status: 403,
        errorCode: 'FORBIDDEN',
        networkError: false,
      });

      await renderManager();
      await openEntryAction('notes.txt', 'Download');
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('Access forbidden.');

      const refreshBtn = container.querySelector<HTMLButtonElement>('button[aria-label="Refresh files"]');
      await act(async () => {
        refreshBtn?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(container.querySelector('[role="alert"]')).toBeNull();
    });

    it('creates ZIP archive download for selected entries', async () => {
      await renderManager();
      await selectEntriesByName('Documents', 'notes.txt');

      const downloadZipBtn = container.querySelector<HTMLButtonElement>('[role="toolbar"] button[aria-label="Download ZIP"]');
      await act(async () => {
        downloadZipBtn?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(vi.mocked(createArchiveTicket)).toHaveBeenCalledWith(
        'https://api.example.test',
        'jwt-token',
        'profile-1',
        ['ref-dir-docs', 'ref-file-notes']
      );
      expect(assignMock).toHaveBeenCalledWith('https://api.example.test/api/files/archive/ticket-zip');
    });

    it('surfaces an error alert when ZIP archive creation fails', async () => {
      vi.mocked(createArchiveTicket).mockResolvedValueOnce({
        ok: false,
        status: 500,
        errorCode: 'INTERNAL_ERROR',
        networkError: false,
      });

      await renderManager();
      await selectEntriesByName('notes.txt');

      const downloadZipBtn = container.querySelector<HTMLButtonElement>('[role="toolbar"] button[aria-label="Download ZIP"]');
      await act(async () => {
        downloadZipBtn?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(container.querySelector('[role="alert"]')?.textContent).toContain(
        'An internal server error occurred. Please try again later.'
      );
    });

    it('uploads a file into a nested directory with overwrite strategy and refreshes the listing', async () => {
      vi.mocked(listFileEntries)
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: defaultRootEntries, next_cursor: null } })
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: nestedDocsEntries, next_cursor: null } })
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          data: {
            entries: [
              ...nestedDocsEntries,
              {
                ref: 'ref-file-spec',
                name: 'spec.txt',
                display_path: '/Documents/spec.txt',
                kind: 'file',
                size: 5,
                allowed_actions: ['download'],
              },
            ],
            next_cursor: null,
          },
        });

      await renderManager();
      await openDirectoryByName('Documents');

      const fileInput = container.querySelector<HTMLInputElement>('input[type="file"]');
      expect(fileInput).not.toBeNull();

      const upload = makeFile('spec.txt', 'hello');
      await act(async () => {
        Object.defineProperty(fileInput, 'files', { value: [upload], writable: true });
        fileInput?.dispatchEvent(new Event('change', { bubbles: true }));
        await Promise.resolve();
      });

      const dialog = document.querySelector('[role="dialog"]');
      expect(dialog?.textContent).toContain('Upload conflict handling');

      const strategySelect = dialog?.querySelector('select');
      expect(strategySelect).not.toBeNull();
      await act(async () => {
        if (strategySelect) setSelectValue(strategySelect, 'OVERWRITE');
        await Promise.resolve();
      });
      expect(dialog?.textContent).toContain('This connection cannot overwrite files atomically.');

      await act(async () => {
        findButton(dialog!, 'Start upload')?.click();
        await Promise.resolve();
      });

      for (let i = 0; i < 40 && !container.textContent?.includes('spec.txt'); i++) {
        await flushAsync(2);
      }
      expect(container.textContent).toContain('spec.txt');
    });

    it('shows upload error in queue and allows retrying to completion', async () => {
      vi.mocked(uploadFile)
        .mockResolvedValueOnce({
          ok: false,
          status: 403,
          errorCode: 'FORBIDDEN',
          networkError: false,
        })
        .mockResolvedValueOnce({
          ok: true,
          status: 201,
          data: { status: 'uploaded', name: 'retry.txt' },
        });

      await renderManager();

      const fileInput = container.querySelector<HTMLInputElement>('input[type="file"]');
      await act(async () => {
        Object.defineProperty(fileInput, 'files', { value: [makeFile('retry.txt')], writable: true });
        fileInput?.dispatchEvent(new Event('change', { bubbles: true }));
        await Promise.resolve();
      });

      const dialog = document.querySelector('[role="dialog"]');
      await act(async () => {
        findButton(dialog!, 'Start upload')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      await expandTransferQueue();
      expect(container.textContent).toContain('Access forbidden.');

      const retryBtn = container.querySelector<HTMLButtonElement>('button[aria-label="Retry"]');
      await act(async () => {
        retryBtn?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(container.textContent).toContain('Uploaded');
    });

    it('moves a file into a picked destination folder via non-blocking queue and refreshes entries', async () => {
      vi.mocked(listFileEntries)
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: defaultRootEntries, next_cursor: null } })
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: defaultRootEntries, next_cursor: null } })
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: nestedDocsEntries, next_cursor: null } })
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          data: { entries: [defaultRootEntries[0], defaultRootEntries[2]], next_cursor: null },
        });

      await renderManager();
      await openEntryAction('notes.txt', 'Move');

      const dialog = document.querySelector('[role="dialog"]');
      expect(dialog?.textContent).toContain('Move item');

      await act(async () => {
        findButton(dialog!, 'Documents')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);
      expect(dialog?.textContent).toContain('Selected destination: Documents');

      await act(async () => {
        findButton(dialog!, 'Move')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(document.querySelector('[role="dialog"]')).toBeNull();
      expect(container.textContent).not.toContain('notes.txt');

      await expandTransferQueue();
      expect(container.textContent).toContain('Move: notes.txt → Documents');
      expect(container.textContent).toContain('Moved');
    });

    it('cancels an in-flight same-profile copy in the expanded transfer queue', async () => {
      const copyDeferred = deferred<{ ok: true; status: 200; data: { success: true; status: string; name: string; native: boolean } }>();
      vi.mocked(copyFileEntry).mockReturnValueOnce(copyDeferred.promise);

      await renderManager();
      await openEntryAction('notes.txt', 'Copy');

      const dialog = document.querySelector('[role="dialog"]');
      await act(async () => {
        findButton(dialog!, 'Copy')?.click();
        await Promise.resolve();
      });

      await expandTransferQueue();

      const cancelTransferBtn = container.querySelector<HTMLButtonElement>('button[aria-label="Cancel transfer"]');
      expect(cancelTransferBtn).not.toBeNull();
      await act(async () => {
        cancelTransferBtn?.click();
        await Promise.resolve();
      });

      expect(container.textContent).toContain('Cancelled');

      await act(async () => {
        copyDeferred.resolve({ ok: true, status: 200, data: { success: true, status: 'copied', name: 'notes.txt', native: true } });
        await Promise.resolve();
      });
      expect(container.textContent).toContain('Cancelled');
      expect(container.textContent).not.toContain('Copied');
    });

    it('surfaces partial operation warning in transfer queue and refreshes listing on failed mutation', async () => {
      vi.mocked(copyFileEntry).mockResolvedValueOnce({
        ok: false,
        status: 409,
        errorCode: 'FILES_PARTIAL_OPERATION',
        networkError: false,
      });
      vi.mocked(listFileEntries)
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: defaultRootEntries, next_cursor: null } })
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: defaultRootEntries, next_cursor: null } })
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          data: {
            entries: [
              ...defaultRootEntries,
              {
                ref: 'ref-file-partial',
                name: 'notes (partial).txt',
                display_path: '/notes (partial).txt',
                kind: 'file',
                size: 512,
                allowed_actions: ['download'],
              },
            ],
            next_cursor: null,
          },
        });

      await renderManager();
      await openEntryAction('notes.txt', 'Copy');

      const dialog = document.querySelector('[role="dialog"]');
      await act(async () => {
        findButton(dialog!, 'Copy')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      await expandTransferQueue();
      expect(container.textContent).toContain('Some items could not be completed. The folder has been refreshed.');
      expect(container.textContent).toContain('notes (partial).txt');
    });

    it('excludes selected directories in batch destination picker and reports batch mutation summary on partial failure', async () => {
      vi.mocked(batchMutateFileEntries).mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: {
          results: [
            { ref: 'ref-dir-docs', status: 'copied' },
            { ref: 'ref-file-notes', status: 'failed', error_code: 'FORBIDDEN' },
          ],
        },
      });

      await renderManager();
      await selectEntriesByName('Documents', 'notes.txt');

      const copyToolbarBtn = container.querySelector<HTMLButtonElement>('[role="toolbar"] button[aria-label="Copy"]');
      await act(async () => {
        copyToolbarBtn?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      const dialog = document.querySelector('[role="dialog"]');
      expect(dialog?.textContent).toContain('Choose one destination folder for 2 selected items.');
      expect(findButton(dialog!, 'Documents')).toBeUndefined();

      await act(async () => {
        findButton(dialog!, 'Copy')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(container.querySelector('[role="toolbar"]')).toBeNull();
      await expandTransferQueue();
      expect(container.textContent).toContain('1 items completed; 1 failed.');
    });

    it('keeps destination picker open with error if cross-profile batch transfer fails, then succeeds on retry', async () => {
      vi.mocked(listFileEntries)
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: defaultRootEntries, next_cursor: null } })
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: defaultRootEntries, next_cursor: null } })
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          data: {
            entries: [
              {
                ref: 'dropbox-dir-inbox',
                name: 'Inbox',
                display_path: '/Inbox',
                kind: 'directory',
                size: 0,
                allowed_actions: ['upload'],
              },
            ],
            next_cursor: null,
          },
        })
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: [], next_cursor: null } })
        .mockResolvedValueOnce({ ok: true, status: 200, data: { entries: defaultRootEntries, next_cursor: null } });

      await renderManager();
      await selectEntriesByName('notes.txt', 'archive.zip');

      const moveToolbarBtn = container.querySelector<HTMLButtonElement>('[role="toolbar"] button[aria-label="Move"]');
      await act(async () => {
        moveToolbarBtn?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      const dialog = document.querySelector('[role="dialog"]');
      const profileSelect = dialog?.querySelector<HTMLSelectElement>('#destination-profile');
      await act(async () => {
        if (profileSelect) setSelectValue(profileSelect, 'profile-2');
        await Promise.resolve();
      });
      await flushAsync(2);

      await act(async () => {
        findButton(dialog!, 'Inbox')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      vi.mocked(startCrossProfileFileTransfer).mockResolvedValueOnce({
        ok: false,
        status: 403,
        errorCode: 'FORBIDDEN',
        networkError: false,
      });
      await act(async () => {
        findButton(dialog!, 'Move')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(document.querySelector('[role="dialog"]')).not.toBeNull();
      expect(dialog?.querySelector('[role="alert"]')?.textContent).toContain('Access forbidden.');

      await act(async () => {
        findButton(dialog!, 'Move')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(vi.mocked(startCrossProfileFileTransfer)).toHaveBeenLastCalledWith(
        'https://api.example.test',
        'jwt-token',
        'profile-1',
        ['ref-file-notes', 'ref-file-archive'],
        'profile-2',
        'dropbox-dir-inbox',
        'move',
        undefined
      );
      expect(document.querySelector('[role="dialog"]')).toBeNull();
      expect(container.querySelector('[role="toolbar"]')).toBeNull();
    });

    it('renders active background cross-profile transfers and surfaces cancellation failures', async () => {
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
          ],
        },
      });
      vi.mocked(cancelCrossProfileFileTransfer)
        .mockResolvedValueOnce({ ok: false, status: 403, errorCode: 'FORBIDDEN', networkError: false })
        .mockResolvedValueOnce({ ok: true, status: 200, data: {} });

      await renderManager();
      await expandTransferQueue();

      expect(container.textContent).toContain('Copy: Google Drive → Dropbox');
      expect(container.textContent).toContain('1/4');

      const cancelBtn = container.querySelector<HTMLButtonElement>('button[aria-label="Cancel transfer"]');
      await act(async () => {
        cancelBtn?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(container.querySelector('[role="alert"]')?.textContent).toContain('Access forbidden.');
      // The failed cancel leaves the transfer visible and cancellable in the panel.
      expect(container.textContent).toContain('Copy: Google Drive → Dropbox');
      expect(container.querySelector('button[aria-label="Cancel transfer"]')).not.toBeNull();

      await act(async () => {
        cancelBtn?.click();
        await Promise.resolve();
      });

      expect(vi.mocked(cancelCrossProfileFileTransfer)).toHaveBeenCalledWith(
        'https://api.example.test',
        'jwt-token',
        'bg-transfer-1'
      );
    });
  });

  describe('destructive-action workflows', () => {
    it('validates new folder input and displays errors when folder creation fails', async () => {
      vi.mocked(createDirectory).mockResolvedValueOnce({
        ok: false,
        status: 403,
        errorCode: 'FORBIDDEN',
        networkError: false,
      });

      await renderManager();

      const newFolderBtn = container.querySelector<HTMLButtonElement>('button[aria-label="New folder"]');
      await act(async () => {
        newFolderBtn?.click();
        await Promise.resolve();
      });

      const dialog = document.querySelector('[role="dialog"]');
      const createBtn = findButton(dialog!, 'Create');
      expect(createBtn?.disabled).toBe(true);

      const input = dialog?.querySelector<HTMLInputElement>('#new-folder-name-input');
      await act(async () => {
        if (input) setInputValue(input, 'Restricted');
        await Promise.resolve();
      });
      expect(createBtn?.disabled).toBe(false);

      await act(async () => {
        createBtn?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(document.querySelector('[role="dialog"]')).not.toBeNull();
      expect(dialog?.querySelector('[role="alert"]')?.textContent).toContain('Access forbidden.');

      await act(async () => {
        findButton(dialog!, 'Cancel')?.click();
        await Promise.resolve();
      });
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });

    it('validates empty rename submission and displays conflict resolution on FILES_CONFLICT', async () => {
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
          ok: true,
          status: 200,
          data: { success: true, status: 'renamed', name: 'notes (1).txt', native: true },
        });

      await renderManager();
      await openEntryAction('notes.txt', 'Rename');

      const renameDialog = document.querySelector('[role="dialog"]');
      const input = renameDialog?.querySelector<HTMLInputElement>('#rename-entry-name');
      expect(input?.value).toBe('notes.txt');

      // Submitting whitespace displays validation message without calling API
      await act(async () => {
        if (input) setInputValue(input, '   ');
        await Promise.resolve();
      });
      const form = renameDialog?.querySelector('form');
      await act(async () => {
        form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
        await Promise.resolve();
      });
      expect(renameDialog?.querySelector('[role="alert"]')?.textContent).toContain('Enter a name.');
      expect(vi.mocked(renameFileEntry)).not.toHaveBeenCalled();

      await act(async () => {
        if (input) setInputValue(input, 'existing.txt');
        await Promise.resolve();
      });
      await act(async () => {
        form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
        await Promise.resolve();
      });
      await flushAsync(2);

      const dialogs = Array.from(document.querySelectorAll('[role="dialog"]'));
      const conflictDialog = dialogs.find((el) => el.textContent?.includes('Item already exists'));
      expect(conflictDialog).toBeDefined();

      await act(async () => {
        findButton(conflictDialog!, 'Keep both files')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(vi.mocked(renameFileEntry)).toHaveBeenLastCalledWith(
        'https://api.example.test',
        'jwt-token',
        'profile-1',
        'ref-file-notes',
        'existing.txt',
        'RENAME',
        expect.any(AbortSignal)
      );
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });

    it('defaults to recursive directory deletion when only recursive deletion is supported and surfaces errors', async () => {
      vi.mocked(getFileCapabilities).mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: {
          capabilities: {
            ...fullCapabilities,
            delete_empty_directory: false,
            delete_recursive_directory: true,
          },
        },
      });
      vi.mocked(deleteFileEntry).mockResolvedValueOnce({
        ok: false,
        status: 403,
        errorCode: 'FORBIDDEN',
        networkError: false,
      });

      await renderManager();
      await openEntryAction('Documents', 'Delete');

      const dialog = document.querySelector('[role="dialog"]');
      expect(dialog?.textContent).toContain('Delete Documents and all of its contents?');
      expect(dialog?.textContent).toContain('This connection supports deleting folders only recursively.');

      await act(async () => {
        findButton(dialog!, 'Delete recursively')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(vi.mocked(deleteFileEntry)).toHaveBeenCalledWith(
        'https://api.example.test',
        'jwt-token',
        'profile-1',
        'ref-dir-docs',
        true,
        expect.any(AbortSignal)
      );
      expect(dialog?.querySelector('[role="alert"]')?.textContent).toContain('Access forbidden.');

      await act(async () => {
        findButton(dialog!, 'Cancel')?.click();
        await Promise.resolve();
      });
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });

    it('confirms batch deletion with per-kind recursive flags and displays dismissible summary banner', async () => {
      vi.mocked(batchDeleteFileEntries).mockResolvedValueOnce({
        ok: true,
        status: 200,
        data: {
          results: [
            { ref: 'ref-dir-docs', status: 'deleted' },
            { ref: 'ref-file-notes', status: 'failed', error_code: 'FORBIDDEN' },
          ],
        },
      });

      await renderManager();
      await selectEntriesByName('Documents', 'notes.txt');

      const deleteToolbarBtn = container.querySelector<HTMLButtonElement>('[role="toolbar"] button[aria-label="Delete"]');
      await act(async () => {
        deleteToolbarBtn?.click();
        await Promise.resolve();
      });

      const dialog = document.querySelector('[role="dialog"]');
      expect(dialog?.textContent).toContain('Delete selected items');
      expect(dialog?.textContent).toContain('Delete 2 selected items?');
      expect(dialog?.textContent).toContain('Selected folders and their contents will be deleted recursively.');

      await act(async () => {
        findButton(dialog!, 'Delete recursively')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(vi.mocked(batchDeleteFileEntries)).toHaveBeenCalledWith(
        'https://api.example.test',
        'jwt-token',
        'profile-1',
        [
          { ref: 'ref-dir-docs', recursive: true },
          { ref: 'ref-file-notes', recursive: false },
        ]
      );
      expect(document.querySelector('[role="dialog"]')).toBeNull();
      expect(container.querySelector('[role="toolbar"]')).toBeNull();

      const statusBanner = container.querySelector('[role="status"]');
      expect(statusBanner?.textContent).toContain('1 items completed; 1 failed.');

      const dismissBannerBtn = statusBanner?.querySelector<HTMLButtonElement>('button[aria-label="Cancel"]');
      await act(async () => {
        dismissBannerBtn?.click();
        await Promise.resolve();
      });
      expect(container.querySelector('[role="status"]')).toBeNull();
    });

    it('surfaces an error alert when batch deletion fails', async () => {
      vi.mocked(batchDeleteFileEntries).mockResolvedValueOnce({
        ok: false,
        status: 403,
        errorCode: 'FORBIDDEN',
        networkError: false,
      });

      await renderManager();
      await selectEntriesByName('notes.txt');

      const deleteToolbarBtn = container.querySelector<HTMLButtonElement>('[role="toolbar"] button[aria-label="Delete"]');
      await act(async () => {
        deleteToolbarBtn?.click();
        await Promise.resolve();
      });

      const dialog = document.querySelector('[role="dialog"]');
      await act(async () => {
        findButton(dialog!, 'Delete recursively')?.click();
        await Promise.resolve();
      });
      await flushAsync(2);

      expect(document.querySelector('[role="dialog"]')).toBeNull();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('Access forbidden.');
    });
  });
});
