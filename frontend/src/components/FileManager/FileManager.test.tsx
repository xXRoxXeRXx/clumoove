import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import { FileManager } from './FileManager';
import { getFileCapabilities, listFileEntries, createDownloadTicket, type FileEntry } from '../../api/files';
import { listConnectionProfiles } from '../../api/profiles';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../../api/files', () => ({
  getFileCapabilities: vi.fn(),
  listFileEntries: vi.fn(),
  createDownloadTicket: vi.fn(),
}));

vi.mock('../../api/profiles', () => ({
  listConnectionProfiles: vi.fn(),
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
    await i18n.changeLanguage('en');
    onProfileChange.mockReset();
    onBack.mockReset();
    onOpenManager.mockReset();
    vi.mocked(listConnectionProfiles).mockReset();
    vi.mocked(getFileCapabilities).mockReset();
    vi.mocked(listFileEntries).mockReset();
    vi.mocked(createDownloadTicket).mockReset();

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

  it('navigates with next and previous cursor pagination', async () => {
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

    const nextBtn = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Next page') || b.textContent?.includes('Nächste Seite'));
    expect(nextBtn).toBeDefined();
    expect(nextBtn?.disabled).toBe(false);

    // Click Next Page
    await act(async () => {
      nextBtn?.click();
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
    expect(container.textContent).toContain('archive.zip');

    // Next page should now not be rendered (no next_cursor), and Previous page enabled
    const nextBtnPage2 = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Next page') || b.textContent?.includes('Nächste Seite'));
    expect(nextBtnPage2).toBeUndefined();
    const prevBtn = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Previous page') || b.textContent?.includes('Vorherige Seite'));
    expect(prevBtn).toBeDefined();
    expect(prevBtn?.disabled).toBe(false);

    // Click Previous Page
    await act(async () => {
      prevBtn?.click();
      await Promise.resolve();
    });
    await flushAsync();

    expect(container.textContent).toContain('report.txt');
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

    const downloadBtn = container.querySelector('button[title*="report.txt"]');
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
    expect(assignMock).toHaveBeenCalledWith('https://api.example.test/api/files/download/ticket-xyz');

    Object.defineProperty(window, 'location', {
      value: originalLocation,
      writable: true,
      configurable: true,
    });
  });
});
