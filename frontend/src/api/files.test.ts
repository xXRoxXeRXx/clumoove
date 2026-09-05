import { afterEach, describe, expect, it, vi } from 'vitest';
import { apiJson } from '../utils/apiClient';
import { batchDeleteFileEntries, batchMutateFileEntries, copyFileEntry, createArchiveTicket, createDownloadTicket, deleteFileEntry, getFileThumbnail, listFileEntries, moveFileEntry, renameFileEntry, resolveFilePath, uploadFile } from './files';

vi.mock('../utils/apiClient', () => ({ apiJson: vi.fn() }));

class MockXMLHttpRequest {
  static instances: MockXMLHttpRequest[] = [];
  static completeOnSend = true;
  method = '';
  url = '';
  headers = new Map<string, string>();
  status = 0;
  responseText = '';
  body: Document | XMLHttpRequestBodyInit | null = null;
  upload = {} as XMLHttpRequestUpload;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  onload: (() => void) | null = null;

  constructor() {
    MockXMLHttpRequest.instances.push(this);
  }

  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }

  setRequestHeader(name: string, value: string) {
    this.headers.set(name, value);
  }

  send(body: Document | XMLHttpRequestBodyInit | null) {
    this.body = body;
    this.upload.onprogress?.call(this as unknown as XMLHttpRequest, { lengthComputable: true, loaded: 2, total: 4 } as ProgressEvent);
    if (!MockXMLHttpRequest.completeOnSend) return;
    this.status = 201;
    this.responseText = JSON.stringify({ status: 'uploaded', name: 'uploaded.txt' });
    this.onload?.();
  }

  abort() {
    this.onabort?.();
  }
}

afterEach(() => {
  MockXMLHttpRequest.instances = [];
  MockXMLHttpRequest.completeOnSend = true;
  vi.unstubAllGlobals();
});

describe('file API', () => {
  it('sends directory refs and cursors in the entries-list body', async () => {
    await listFileEntries('https://api.example.test', 'token', 'profile id', 'opaque-directory-ref', 'next-page');

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/files/profiles/profile%20id/entries:list',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ resource_type: 'files', parent_ref: 'opaque-directory-ref', cursor: 'next-page' }),
      }),
    );
  });

  it('uses the entry ref only in the download-ticket body', async () => {
    await createDownloadTicket('https://api.example.test', 'token', 'profile-id', 'opaque-file-ref');

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/files/profiles/profile-id/download-tickets',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ ref: 'opaque-file-ref' }) }),
    );
  });

  it('creates an archive ticket from opaque refs only', async () => {
    await createArchiveTicket('https://api.example.test', 'token', 'profile-id', ['sealed-file', 'sealed-directory']);

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/files/profiles/profile-id/archive-tickets',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ refs: ['sealed-file', 'sealed-directory'] }) }),
    );
  });

  it('deletes an entry with only its opaque ref and recursive flag', async () => {
    await deleteFileEntry('https://api.example.test', 'token', 'profile id', 'opaque-entry-ref', true);

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/files/profiles/profile%20id/entries',
      expect.objectContaining({ method: 'DELETE', body: JSON.stringify({ ref: 'opaque-entry-ref', recursive: true }) }),
    );
  });

  it('sends batch delete items with opaque refs and recursive flags', async () => {
    await batchDeleteFileEntries('https://api.example.test', 'token', 'profile-id', [{ ref: 'sealed-file', recursive: false }, { ref: 'sealed-directory', recursive: true }]);

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/files/profiles/profile-id/entries:batch-delete',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ items: [{ ref: 'sealed-file', recursive: false }, { ref: 'sealed-directory', recursive: true }] }) }),
    );
  });

  it('sends rename, copy, and move mutations with sealed refs only', async () => {
    vi.mocked(apiJson).mockClear();
    await renameFileEntry('https://api.example.test', 'token', 'profile-id', 'sealed-source', 'renamed.txt');
    await copyFileEntry('https://api.example.test', 'token', 'profile-id', 'sealed-source', 'sealed-destination', 'RENAME');
    await moveFileEntry('https://api.example.test', 'token', 'profile-id', 'sealed-source', null, 'SKIP');

    expect(apiJson).toHaveBeenNthCalledWith(1, 'https://api.example.test/api/files/profiles/profile-id/entries:rename', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ ref: 'sealed-source', new_name: 'renamed.txt' }),
    }));
    expect(apiJson).toHaveBeenNthCalledWith(2, 'https://api.example.test/api/files/profiles/profile-id/entries:copy', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ ref: 'sealed-source', destination_parent_ref: 'sealed-destination', conflict_strategy: 'RENAME' }),
    }));
    expect(apiJson).toHaveBeenNthCalledWith(3, 'https://api.example.test/api/files/profiles/profile-id/entries:move', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ ref: 'sealed-source', conflict_strategy: 'SKIP' }),
    }));
  });

  it('sends batch mutations with a shared sealed destination and explicit retry strategy', async () => {
    await batchMutateFileEntries('copy', 'https://api.example.test', 'token', 'profile-id', ['sealed-a', 'sealed-b'], 'sealed-destination', 'OVERWRITE');

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/files/profiles/profile-id/entries:batch-copy',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ refs: ['sealed-a', 'sealed-b'], destination_parent_ref: 'sealed-destination', conflict_strategy: 'OVERWRITE' }) }),
    );
  });

  it('sends quick-link paths only in the resolve request body', async () => {
    await resolveFilePath('https://api.example.test', 'token', 'profile-id', '/documents/reports');

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/files/profiles/profile-id/entries:resolve',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ resource_type: 'files', path: '/documents/reports' }) }),
    );
  });

  it('streams a raw file with encoded metadata and progress', async () => {
    vi.stubGlobal('XMLHttpRequest', MockXMLHttpRequest);
    const progress = vi.fn();
    const result = await uploadFile('https://api.example.test', 'token', 'profile id', new File(['test'], 'fä.txt'), 'opaque-parent-ref', 'SKIP', progress);

    const request = MockXMLHttpRequest.instances[0];
    expect(request.method).toBe('PUT');
    expect(request.url).toBe('https://api.example.test/api/files/profiles/profile%20id/content');
    expect(request.headers).toEqual(new Map([
      ['Authorization', 'Bearer token'],
      ['X-Clumoove-File-Name', 'ZsOkLnR4dA'],
      ['X-Clumoove-Conflict-Strategy', 'SKIP'],
      ['X-Clumoove-Parent-Ref', 'opaque-parent-ref'],
    ]));
    expect(progress).toHaveBeenCalledWith({ loaded: 2, total: 4 });
    expect(result).toEqual({ ok: true, status: 201, data: { status: 'uploaded', name: 'uploaded.txt' } });
  });

  it('propagates cancellation to the XHR upload', async () => {
    vi.stubGlobal('XMLHttpRequest', MockXMLHttpRequest);
    MockXMLHttpRequest.completeOnSend = false;
    const controller = new AbortController();
    const result = uploadFile('https://api.example.test', 'token', 'profile-id', new File(['test'], 'test.txt'), null, 'SKIP', vi.fn(), controller.signal);
    controller.abort();

    await expect(result).resolves.toMatchObject({ ok: false, networkError: true });
  });

  it('fetches thumbnail with dimensions via POST', async () => {
    const mockBlob = new Blob(['image-data'], { type: 'image/jpeg' });
    const mockFetch = vi.fn().mockResolvedValue({
      ok: true,
      blob: vi.fn().mockResolvedValue(mockBlob),
    });
    vi.stubGlobal('fetch', mockFetch);

    const blob = await getFileThumbnail('https://api.example.test', 'token', 'profile-1', 'ref-photo', 128, 128);

    expect(mockFetch).toHaveBeenCalledWith(
      'https://api.example.test/api/files/profiles/profile-1/thumbnail',
      expect.objectContaining({
        method: 'POST',
        headers: {
          Authorization: 'Bearer token',
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ ref: 'ref-photo', width: 128, height: 128 }),
      }),
    );
    expect(blob).toBe(mockBlob);
  });
});

