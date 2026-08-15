import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from './i18n';
import App from './App';
import { resolveFilePath, type ResolveFilePathResponse } from './api/files';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('./api/files', async () => {
  const actual = await vi.importActual<typeof import('./api/files')>('./api/files');
  return {
    ...actual,
    resolveFilePath: vi.fn(),
    getFileCapabilities: vi.fn(() => Promise.resolve({
      ok: true,
      status: 200,
      data: {
        capabilities: {
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
        },
      },
    })),
    listFileEntries: vi.fn(() => Promise.resolve({
      ok: true,
      status: 200,
      data: { entries: [], next_cursor: null },
    })),
  };
});

vi.mock('./api/profiles', () => ({
  listConnectionProfiles: vi.fn(() => Promise.resolve({
    ok: true,
    status: 200,
    data: {
      profiles: [
        { id: 'profile-a', name: 'Profile A', provider: 'google' },
        { id: 'profile-b', name: 'Profile B', provider: 'dropbox' },
      ],
    },
  })),
}));

vi.mock('./utils/apiClient', async () => {
  const actual = await vi.importActual<typeof import('./utils/apiClient')>('./utils/apiClient');
  return {
    ...actual,
    apiFetch: vi.fn((url) => {
      const path = String(url);
      if (path.endsWith('/api/migrations')) {
        return Promise.resolve(new Response(JSON.stringify([
          {
            id: 'mig-1',
            status: 'COMPLETED',
            source_provider: 'google',
            source_url: 'https://drive.google.com',
            source_profile_id: 'profile-a',
            target_provider: 'dropbox',
            target_url: 'https://dropbox.com',
            target_profile_id: 'profile-b',
            processed_files: 10,
            total_files: 10,
            processed_bytes: 1024,
            total_bytes: 1024,
            selected_paths: ['/FolderA/file.txt'],
            created_at: '2026-01-01T00:00:00Z',
          },
        ])));
      }
      if (path.endsWith('/api/sync-jobs')) {
        return Promise.resolve(new Response(JSON.stringify([])));
      }
      if (path.endsWith('/api/auth/me')) {
        return Promise.resolve(new Response(JSON.stringify({
          id: 'user-1',
          email: 'user@example.com',
          display_name: 'Test User',
          role: 'USER',
          language: 'en',
        })));
      }
      return Promise.resolve(new Response(JSON.stringify({})));
    }),
    apiJson: vi.fn((url) => {
      const path = String(url);
      if (path.endsWith('/api/migration')) {
        return Promise.resolve({
          ok: true as const,
          status: 200,
          data: [
            {
              id: 'mig-1',
              status: 'COMPLETED',
              source_provider: 'google',
              source_url: 'https://drive.google.com',
              source_profile_id: 'profile-a',
              target_provider: 'dropbox',
              target_url: 'https://dropbox.com',
              target_profile_id: 'profile-b',
              processed_files: 10,
              total_files: 10,
              processed_bytes: 1024,
              total_bytes: 1024,
              selected_paths: ['/FolderA/file.txt'],
              created_at: '2026-01-01T00:00:00Z',
            },
          ],
        });
      }
      if (path.endsWith('/api/sync')) {
        return Promise.resolve({ ok: true as const, status: 200, data: [] });
      }
      return Promise.resolve({ ok: true as const, status: 200, data: [] });
    }),
  };
});

vi.mock('./utils/sse', () => ({
  connectSseLoop: vi.fn(() => new Promise<void>(() => {})),
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

describe('App openFileManagerAtPath race condition', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    localStorage.setItem('has_session', 'true');
    vi.stubGlobal('fetch', vi.fn((url) => {
      const path = String(url);
      if (path.endsWith('/api/auth/refresh')) {
        return Promise.resolve(new Response(JSON.stringify({ access_token: 'fake-jwt' })));
      }
      if (path.endsWith('/api/settings')) {
        return Promise.resolve(new Response(JSON.stringify({ local_storage_enabled: true, oauth_providers: {} })));
      }
      return Promise.resolve(new Response(JSON.stringify({})));
    }));
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    localStorage.clear();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('ignores late resolve response of A after quick selection B has succeeded', async () => {
    const deferredA = deferred<{ ok: true; status: 200; data: ResolveFilePathResponse }>();

    vi.mocked(resolveFilePath).mockImplementation((_url, _token, profileId, _path, signal) => {
      if (profileId === 'profile-a') {
        return new Promise((resolve) => {
          deferredA.promise.then(resolve);
          signal?.addEventListener('abort', () => {
            // Signal was aborted as expected when B started
          });
        });
      }
      if (profileId === 'profile-b') {
        return Promise.resolve({
          ok: true as const,
          status: 200,
          data: {
            ref: 'ref-b',
            breadcrumbs: [{ ref: 'ref-b', name: 'FolderB' }],
            fallback: false,
          },
        });
      }
      return Promise.resolve({
        ok: false as const,
        status: 404,
        errorCode: 'NOT_FOUND',
        networkError: false,
      });
    });

    await act(async () => {
      root.render(<App />);
      await Promise.resolve();
    });

    // Wait for auth initialization to complete and render MigrationsDashboard (overview)
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });

    // Find the quick-access buttons for profile-a (source) and profile-b (target)
    const buttonA = container.querySelector<HTMLButtonElement>(`button[aria-label="${i18n.t('files.openSource')}"]`);
    const buttonB = container.querySelector<HTMLButtonElement>(`button[aria-label="${i18n.t('files.openTarget')}"]`);

    expect(buttonA).not.toBeNull();
    expect(buttonB).not.toBeNull();

    // 1. Click button A -> starts slow resolve request A
    await act(async () => {
      buttonA?.click();
      await Promise.resolve();
    });

    // 2. Click button B -> starts fast resolve request B and completes
    await act(async () => {
      buttonB?.click();
      await Promise.resolve();
    });

    // Flush async lazy loading and profile resolution
    for (let i = 0; i < 5; i++) {
      await act(async () => {
        await new Promise((r) => setTimeout(r, 25));
      });
    }

    // Verify we navigated to FileManager with profile-b active
    const profileBItem = container.querySelector('[aria-current="page"]');
    expect(profileBItem).not.toBeNull();
    expect(profileBItem?.textContent).toContain('Profile B');

    // 3. Now resolve the slow request A
    await act(async () => {
      deferredA.resolve({
        ok: true as const,
        status: 200,
        data: {
          ref: 'ref-a',
          breadcrumbs: [{ ref: 'ref-a', name: 'FolderA' }],
          fallback: false,
        },
      });
      await Promise.resolve();
    });

    for (let i = 0; i < 5; i++) {
      await act(async () => {
        await new Promise((r) => setTimeout(r, 25));
      });
    }

    // 4. Verify we are STILL on profile-b and NOT reverted to profile-a
    const profileItemAfterLateResolve = container.querySelector('[aria-current="page"]');
    expect(profileItemAfterLateResolve).not.toBeNull();
    expect(profileItemAfterLateResolve?.textContent).toContain('Profile B');
  });
});
