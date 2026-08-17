import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import { FileManager } from './FileManager';
import { getFileCapabilities, listFileEntries, createDownloadTicket, createDirectory, type FileEntry } from '../../api/files';
import { listConnectionProfiles } from '../../api/profiles';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../../api/files', () => ({
  getFileCapabilities: vi.fn(),
  listFileEntries: vi.fn(),
  createDownloadTicket: vi.fn(),
  createDirectory: vi.fn(),
}));

vi.mock('../../api/profiles', () => ({
  listConnectionProfiles: vi.fn(),
}));

vi.mock('./FilePreviewDialog', () => ({
  FilePreviewDialog: ({ entry, onClose }: { entry: { name: string }; onClose: () => void }) => (
    <div role="dialog">
      <span>{entry.name}</span>
      <button onClick={onClose}>Close</button>
    </div>
  ),
}));

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

const mockCapabilities = {
  browse: true,
  native_pagination: false,
  download: true,
  upload: true,
  mkdir: false,
  rename: false,
  move: false,
  delete_file: false,
  delete_empty_directory: false,
  delete_recursive_directory: false,
  conflict_skip: true,
  conflict_overwrite: true,
  conflict_overwrite_atomic: false,
  conflict_rename: true,
  native_copy: false,
  range_download: false,
  thumbnails: false,
};

const rootEntries: FileEntry[] = [
  {
    ref: 'ref-dir-1',
    name: 'Documents',
    display_path: '/Documents',
    kind: 'directory',
    size: 0,
    allowed_actions: ['download', 'upload'],
  },
  {
    ref: 'ref-file-1',
    name: 'report.txt',
    display_path: '/report.txt',
    kind: 'file',
    size: 2048,
    modified_at: '2026-08-01T12:00:00Z',
    mime_type: 'text/plain',
    allowed_actions: ['download'],
  },
];

async function flushAsync(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 25));
    });
  }
}

describe('FileManager component', () => {
  let container: HTMLDivElement;
  let root: Root;
  const onProfileChange = vi.fn();
  const onBack = vi.fn();
  const onOpenManager = vi.fn();

  beforeEach(async () => {
    localStorage.clear();
    await i18n.changeLanguage('en');
    onProfileChange.mockReset();
    onBack.mockReset();
    onOpenManager.mockReset();
    vi.mocked(listConnectionProfiles).mockReset();
    vi.mocked(getFileCapabilities).mockReset();
    vi.mocked(listFileEntries).mockReset();
    vi.mocked(createDownloadTicket).mockReset();
    vi.mocked(createDirectory).mockReset();

    vi.mocked(listConnectionProfiles).mockResolvedValue({
      ok: true,
      status: 200,
      data: {
        profiles: [
          { id: 'profile-1', name: 'Google Drive', provider: 'google', has_password: true, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' },
          { id: 'profile-2', name: 'Dropbox', provider: 'dropbox', has_password: true, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' },
        ],
      },
    });

    vi.mocked(getFileCapabilities).mockResolvedValue({
      ok: true,
      status: 200,
      data: { capabilities: mockCapabilities },
    });

    vi.mocked(listFileEntries).mockImplementation((_url, _token, _prof, parentRef, cursor) => {
      if (parentRef === null && cursor === 'cursor-page-2') {
        return Promise.resolve({
          ok: true as const,
          status: 200,
          data: {
            entries: [
              {
                ref: 'ref-file-2',
                name: 'archive.zip',
                display_path: '/archive.zip',
                kind: 'file' as const,
                size: 4096,
                allowed_actions: ['download'],
              },
            ],
            next_cursor: null,
          },
        });
      }
      if (parentRef === 'ref-dir-1') {
        return Promise.resolve({
          ok: true as const,
          status: 200,
          data: {
            entries: [
              {
                ref: 'ref-nested-1',
                name: 'invoice.pdf',
                display_path: '/Documents/invoice.pdf',
                kind: 'file' as const,
                size: 10240,
                allowed_actions: ['download'],
              },
            ],
            next_cursor: null,
          },
        });
      }
      return Promise.resolve({
        ok: true as const,
        status: 200,
        data: {
          entries: rootEntries,
          next_cursor: 'cursor-page-2',
        },
      });
    });

    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    vi.restoreAllMocks();
  });

  it('renders profiles and lists directory entries', async () => {
    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
          onBack={onBack}
          onOpenManager={onOpenManager}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    expect(container.textContent).toContain('Google Drive');
    expect(container.textContent).toContain('Documents');
    expect(container.textContent).toContain('report.txt');
  });

  it('handles profile selection via onProfileChange', async () => {
    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    const dropboxBtn = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Dropbox'));
    expect(dropboxBtn).toBeDefined();

    await act(async () => {
      dropboxBtn?.click();
      await Promise.resolve();
    });

    expect(onProfileChange).toHaveBeenCalledWith('profile-2');
  });

  it('loads more entries with cursor pagination and appends to the list', async () => {
    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    expect(container.textContent).toContain('report.txt');

    const loadMoreBtn = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Load more') || b.textContent?.includes('Mehr laden'));
    expect(loadMoreBtn).toBeDefined();

    // Click Load more
    await act(async () => {
      loadMoreBtn?.click();
      await Promise.resolve();
    });
    await flushAsync();

    expect(vi.mocked(listFileEntries)).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      null,
      'cursor-page-2',
      expect.any(AbortSignal)
    );
    // Both initial and appended items are in the view
    expect(container.textContent).toContain('report.txt');
    expect(container.textContent).toContain('archive.zip');

    // Load more button should now not be rendered (no next_cursor)
    const loadMoreBtnPage2 = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Load more') || b.textContent?.includes('Mehr laden'));
    expect(loadMoreBtnPage2).toBeUndefined();
  });

  it('navigates into directory and up via breadcrumbs', async () => {
    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    // Click "Documents" directory
    const dirBtn = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Documents'));
    await act(async () => {
      dirBtn?.click();
      await Promise.resolve();
    });
    await flushAsync();

    expect(container.textContent).toContain('invoice.pdf');
    expect(vi.mocked(listFileEntries)).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      'ref-dir-1',
      undefined,
      expect.any(AbortSignal)
    );

    // Click "Up" button
    const upBtn = container.querySelector('button[title="Up"], button[title="Ebene nach oben"], button[aria-label="Up"], button[aria-label="Ebene nach oben"]');
    await act(async () => {
      (upBtn as HTMLButtonElement)?.click();
      await Promise.resolve();
    });
    await flushAsync();

    expect(container.textContent).toContain('Documents');
  });

  it('discards late directory entry responses when rapid navigation occurs', async () => {
    const deferredSlow = deferred<{ ok: true; status: 200; data: { entries: FileEntry[]; next_cursor: null } }>();

    vi.mocked(listFileEntries).mockImplementation((_url, _token, _profileId, parentRef, _cursor, signal) => {
      if (parentRef === null) {
        return Promise.resolve({
          ok: true as const,
          status: 200,
          data: {
            entries: [
              { ref: 'dir-slow', name: 'SlowDir', display_path: '/SlowDir', kind: 'directory' as const, size: 0, allowed_actions: ['download'] },
              { ref: 'root-file', name: 'root.txt', display_path: '/root.txt', kind: 'file' as const, size: 100, allowed_actions: ['download'] },
            ],
            next_cursor: null,
          },
        });
      }
      if (parentRef === 'dir-slow') {
        return new Promise((resolve) => {
          deferredSlow.promise.then(resolve);
          signal?.addEventListener('abort', () => {
            resolve({ ok: false as const, status: 0, errorCode: 'ABORTED', networkError: true });
          });
        });
      }
      return Promise.resolve({ ok: false as const, status: 500, errorCode: 'ERROR', networkError: false });
    });

    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    // Click SlowDir (starts slow request)
    const slowBtn = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('SlowDir'));
    await act(async () => {
      slowBtn?.click();
      await Promise.resolve();
    });

    // While slow request is pending, click root breadcrumb ("Google Drive")
    const rootBreadcrumb = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Google Drive') && b.closest('nav'));
    expect(rootBreadcrumb).toBeDefined();

    await act(async () => {
      rootBreadcrumb?.click();
      await Promise.resolve();
    });
    await flushAsync();

    expect(container.textContent).toContain('root.txt');

    // Now resolve the slow request from SlowDir
    await act(async () => {
      deferredSlow.resolve({
        ok: true as const,
        status: 200,
        data: {
          entries: [
            { ref: 'slow-item', name: 'slow-result.txt', display_path: '/SlowDir/slow-result.txt', kind: 'file' as const, size: 200, allowed_actions: ['download'] },
          ],
          next_cursor: null,
        },
      });
      await Promise.resolve();
    });
    await flushAsync();

    // Root result must NOT be overwritten by the late SlowDir response
    expect(container.textContent).toContain('root.txt');
    expect(container.textContent).not.toContain('slow-result.txt');
  });

  it('handles download ticket generation when download button is clicked', async () => {
    const originalLocation = window.location;
    const assignMock = vi.fn();
    Object.defineProperty(window, 'location', {
      value: { ...originalLocation, assign: assignMock },
      writable: true,
      configurable: true,
    });

    vi.mocked(createDownloadTicket).mockResolvedValueOnce({
      ok: true,
      status: 201,
      data: { download_url: '/api/files/download/ticket-xyz' },
    });

    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    const downloadBtn = container.querySelector('button[aria-label="Download report.txt"]');
    expect(downloadBtn).not.toBeNull();

    await act(async () => {
      (downloadBtn as HTMLButtonElement)?.click();
      await Promise.resolve();
    });
    await flushAsync();

    expect(vi.mocked(createDownloadTicket)).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      'ref-file-1',
      expect.any(AbortSignal)
    );
    expect(assignMock).toHaveBeenCalledWith('https://api.example.test/api/files/download/ticket-xyz');

    Object.defineProperty(window, 'location', {
      value: originalLocation,
      writable: true,
      configurable: true,
    });
  });

  it('navigates into directory when table row is clicked anywhere', async () => {
    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    // Find the row containing "Documents"
    const dirRow = Array.from(container.querySelectorAll('tbody tr')).find((row) => row.textContent?.includes('Documents'));
    expect(dirRow).toBeDefined();

    // Click directly on the row <tr>
    await act(async () => {
      dirRow?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await Promise.resolve();
    });
    await flushAsync();

    expect(container.textContent).toContain('invoice.pdf');
    expect(vi.mocked(listFileEntries)).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      'ref-dir-1',
      undefined,
      expect.any(AbortSignal)
    );
  });

  it('creates new directory via dialog and refreshes entries', async () => {
    vi.mocked(getFileCapabilities).mockResolvedValue({
      ok: true,
      status: 200,
      data: { capabilities: { ...mockCapabilities, mkdir: true } },
    });

    vi.mocked(createDirectory).mockResolvedValue({
      ok: true,
      status: 201,
      data: { success: true, name: 'New Folder' },
    });

    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    // Find and click "New folder" button
    const newFolderBtn = container.querySelector('button[aria-label="New folder"], button[title="New folder"]');
    expect(newFolderBtn).toBeDefined();

    await act(async () => {
      (newFolderBtn as HTMLButtonElement)?.click();
      await Promise.resolve();
    });
    await flushAsync();

    // Input folder name in the dialog portal
    const input = document.body.querySelector('#new-folder-name-input') as HTMLInputElement;
    expect(input).toBeDefined();

    const nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set;
    await act(async () => {
      nativeSetter?.call(input, 'My New Folder');
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.dispatchEvent(new Event('change', { bubbles: true }));
      await Promise.resolve();
    });

    // Submit form
    const submitBtn = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent === 'Create' || b.textContent === 'Erstellen');
    expect(submitBtn).toBeDefined();

    await act(async () => {
      submitBtn?.click();
      await Promise.resolve();
    });
    await flushAsync();

    expect(vi.mocked(createDirectory)).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      'My New Folder',
      null
    );

    // Dialog should be closed
    expect(document.body.querySelector('#new-folder-name-input')).toBeNull();
  });

  it('renders table-fixed layout and truncate classes for filenames', async () => {
    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    const table = container.querySelector('table');
    expect(table).not.toBeNull();
    expect(table?.className).toContain('table-fixed');

    const nameCell = container.querySelector('tbody td[data-label="Name"]');
    expect(nameCell).not.toBeNull();
    expect(nameCell?.className).toContain('min-w-0');
  });

  it('toggles between list view and grid view and persists to localStorage', async () => {
    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    // Default view should be list (table is present, grid is not)
    expect(container.querySelector('table')).not.toBeNull();
    expect(container.querySelector('[role="grid"]')).toBeNull();

    // Switch to grid view
    const gridBtn = container.querySelector('button[aria-label="Grid view"], button[title="Grid view"]');
    expect(gridBtn).toBeDefined();

    await act(async () => {
      (gridBtn as HTMLButtonElement)?.click();
      await Promise.resolve();
    });
    await flushAsync();

    // Table should now be absent and grid present
    expect(container.querySelector('table')).toBeNull();
    const grid = container.querySelector('[role="grid"]');
    expect(grid).not.toBeNull();

    const cells = container.querySelectorAll('[role="gridcell"]');
    expect(cells.length).toBe(2);
    expect(cells[0].textContent).toContain('Documents');
    expect(cells[1].textContent).toContain('report.txt');

    expect(localStorage.getItem('clumoove_file_manager_view_mode')).toBe('grid');

    // Switch back to list view
    const listBtn = container.querySelector('button[aria-label="List view"], button[title="List view"]');
    expect(listBtn).toBeDefined();

    await act(async () => {
      (listBtn as HTMLButtonElement)?.click();
      await Promise.resolve();
    });
    await flushAsync();

    expect(container.querySelector('table')).not.toBeNull();
    expect(container.querySelector('[role="grid"]')).toBeNull();
    expect(localStorage.getItem('clumoove_file_manager_view_mode')).toBe('list');
  });

  it('navigates into directory from grid view', async () => {
    localStorage.setItem('clumoove_file_manager_view_mode', 'grid');

    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    expect(container.querySelector('[role="grid"]')).not.toBeNull();

    // Find directory card
    const dirCard = Array.from(container.querySelectorAll('[role="gridcell"]')).find((cell) => cell.textContent?.includes('Documents'));
    expect(dirCard).toBeDefined();

    await act(async () => {
      dirCard?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await Promise.resolve();
    });
    await flushAsync();

    expect(container.textContent).toContain('invoice.pdf');
    expect(vi.mocked(listFileEntries)).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      'ref-dir-1',
      undefined,
      expect.any(AbortSignal)
    );
  });

  it('downloads file directly from grid card download button', async () => {
    localStorage.setItem('clumoove_file_manager_view_mode', 'grid');

    vi.mocked(createDownloadTicket).mockResolvedValue({
      ok: true,
      status: 200,
      data: {
        download_url: '/api/files/download/ticket-123',
      },
    });

    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    const fileCard = Array.from(container.querySelectorAll('[role="gridcell"]')).find((cell) => cell.textContent?.includes('report.txt'));
    expect(fileCard).toBeDefined();

    const downloadBtn = fileCard?.querySelector('button[aria-label="Download report.txt"]');
    expect(downloadBtn).toBeDefined();

    await act(async () => {
      (downloadBtn as HTMLButtonElement)?.click();
      await Promise.resolve();
    });
    await flushAsync();

    expect(vi.mocked(createDownloadTicket)).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      'ref-file-1',
      expect.any(AbortSignal)
    );
  });

  it('opens preview dialog when clicking any file in list view', async () => {
    vi.mocked(listFileEntries).mockResolvedValue({
      ok: true,
      status: 200,
      data: {
        entries: [
          {
            ref: 'ref-archive',
            name: 'backup.tar.gz',
            display_path: '/backup.tar.gz',
            kind: 'file',
            size: 1048576,
            mime_type: 'application/gzip',
            allowed_actions: ['download'],
          },
        ],
        next_cursor: null,
      },
    });

    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    const fileRow = container.querySelector('tbody tr');
    expect(fileRow).not.toBeNull();
    expect(fileRow?.classList.contains('cursor-pointer')).toBe(true);

    const fileButton = fileRow?.querySelector<HTMLButtonElement>('button');
    expect(fileButton?.disabled).toBe(false);

    await act(async () => {
      fileButton?.click();
      await Promise.resolve();
    });
    await flushAsync();

    // Dialog should open
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.textContent).toContain('backup.tar.gz');
  });

  it('calls onOpenManager when clicking manage profiles button and has no refresh button in sidebar', async () => {
    await act(async () => {
      root.render(
        <FileManager
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          onProfileChange={onProfileChange}
          onOpenManager={onOpenManager}
        />
      );
      await Promise.resolve();
    });

    await flushAsync();

    const aside = container.querySelector('aside');
    expect(aside).not.toBeNull();

    // Verify there is no refresh button inside aside
    const refreshBtnInAside = aside?.querySelector('button[aria-label="Refresh"], button[aria-label="Aktualisieren"]');
    expect(refreshBtnInAside).toBeNull();

    // Find and click the manage profiles button
    const allButtons = Array.from(aside?.querySelectorAll('button') ?? []);
    const manageBtn = allButtons.find((btn) => btn.textContent?.includes(i18n.t('files.manageProfiles')));
    expect(manageBtn).toBeDefined();

    await act(async () => {
      manageBtn?.click();
      await Promise.resolve();
    });

    expect(onOpenManager).toHaveBeenCalledTimes(1);
  });
});

