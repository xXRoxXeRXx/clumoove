import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import { createDownloadTicket, getFileThumbnailResult, type FileEntry } from '../../api/files';
import {
  useFilePreview,
  type FilePreviewState,
  type UseFilePreviewOptions,
} from './useFilePreview';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../../api/files', () => ({
  createDownloadTicket: vi.fn(),
  getFileThumbnailResult: vi.fn(),
}));

type MockWorkerInstance = {
  postMessage: ReturnType<typeof vi.fn>;
  terminate: ReturnType<typeof vi.fn>;
  onmessage: ((event: MessageEvent) => void) | null;
  onerror: ((error: ErrorEvent) => void) | null;
};

const mockXlsxWorkers: MockWorkerInstance[] = [];
const mockDocxWorkers: MockWorkerInstance[] = [];

vi.mock('./xlsxPreview.worker.ts?worker', () => ({
  default: class MockXlsxWorker {
    postMessage = vi.fn();
    terminate = vi.fn();
    onmessage = null;
    onerror = null;
    constructor() {
      mockXlsxWorkers.push(this);
    }
  },
}));

vi.mock('./docxPreview.worker.ts?worker', () => ({
  default: class MockDocxWorker {
    postMessage = vi.fn();
    terminate = vi.fn();
    onmessage = null;
    onerror = null;
    constructor() {
      mockDocxWorkers.push(this);
    }
  },
}));

function makeEntry(name: string, mimeType?: string, size = 1024, ref = 'ref-1'): FileEntry {
  return {
    ref,
    name,
    display_path: `/files/${name}`,
    kind: 'file',
    size,
    mime_type: mimeType,
    allowed_actions: ['download'],
  };
}

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
      await Promise.resolve();
      if (vi.isFakeTimers()) {
        await vi.advanceTimersByTimeAsync(0);
      } else {
        await new Promise((resolve) => setTimeout(resolve, 0));
      }
    });
  }
}

describe('useFilePreview', () => {
  let container: HTMLDivElement;
  let root: Root;
  let latest: FilePreviewState;

  function Probe(props: UseFilePreviewOptions) {
    latest = useFilePreview(props);
    return null;
  }

  async function renderPreview(entry: FileEntry, overrides?: Partial<UseFilePreviewOptions>) {
    const props: UseFilePreviewOptions = {
      apiUrl: 'https://api.example.test',
      token: 'jwt-token',
      profileId: 'profile-1',
      entry,
      supportsThumbnails: false,
      ...overrides,
    };
    await act(async () => {
      root.render(<Probe {...props} />);
      await Promise.resolve();
    });
    await flushAsync();
  }

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    mockXlsxWorkers.length = 0;
    mockDocxWorkers.length = 0;
    vi.mocked(createDownloadTicket).mockReset().mockResolvedValue({
      ok: true,
      status: 200,
      data: { download_url: '/api/files/download/ticket-1' },
    });
    vi.mocked(getFileThumbnailResult).mockReset().mockResolvedValue({
      blob: null,
      status: 404,
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

  it('transitions unsupported or oversized files directly to fallback without fetching', async () => {
    const unsupported = makeEntry('archive.zip', 'application/zip', 2048);
    await renderPreview(unsupported);

    expect(latest.kind).toBeNull();
    expect(latest.status).toBe('fallback');
    expect(latest.error).toBe('');
    expect(createDownloadTicket).not.toHaveBeenCalled();

    const oversizedText = makeEntry('huge.txt', 'text/plain', 3 * 1024 * 1024, 'ref-2');
    await renderPreview(oversizedText);

    expect(latest.kind).toBe('text');
    expect(latest.status).toBe('fallback');
    expect(createDownloadTicket).not.toHaveBeenCalled();
  });

  it('loads text preview via download ticket and surfaces translated errors on ticket or fetch failure', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(
      new Response('hello world', {
        status: 200,
        headers: { 'Content-Length': '11' },
      }),
    );

    const textEntry = makeEntry('notes.txt', 'text/plain', 11);
    await renderPreview(textEntry);

    expect(latest.kind).toBe('text');
    expect(latest.status).toBe('ready');
    expect(latest.text).toBe('hello world');

    vi.mocked(createDownloadTicket).mockResolvedValueOnce({
      ok: false,
      status: 403,
      errorCode: 'FORBIDDEN',
      networkError: false,
    });
    await renderPreview(makeEntry('secret.txt', 'text/plain', 11, 'ref-2'));
    expect(latest.status).toBe('fallback');
    expect(latest.error).toBe(i18n.t('errors.FORBIDDEN'));

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify({ error_code: 'FILES_NOT_FOUND' }), {
        status: 404,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    await renderPreview(makeEntry('missing.txt', 'text/plain', 11, 'ref-3'));
    expect(latest.status).toBe('fallback');
    expect(latest.error).toBe(i18n.t('errors.FILES_NOT_FOUND'));
  });

  it('prefers thumbnail for images when supported, falls back to ticket on 404, and revokes object URLs', async () => {
    const createUrlSpy = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:https://api.example.test/thumb-1');
    const revokeUrlSpy = vi.spyOn(URL, 'revokeObjectURL');

    vi.mocked(getFileThumbnailResult).mockResolvedValueOnce({
      blob: new Blob(['thumb'], { type: 'image/jpeg' }),
      status: 200,
    });

    await renderPreview(makeEntry('photo.jpg', 'image/jpeg', 100, 'ref-img-1'), { supportsThumbnails: true });
    expect(latest.status).toBe('ready');
    expect(latest.blobUrl).toBe('blob:https://api.example.test/thumb-1');
    expect(createDownloadTicket).not.toHaveBeenCalled();

    // Switching to another image revokes the previous object URL; 404 thumbnail falls back to download ticket
    createUrlSpy.mockReturnValue('blob:https://api.example.test/full-2');
    vi.mocked(getFileThumbnailResult).mockResolvedValueOnce({ blob: null, status: 404 });
    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(
      new Response('full', {
        status: 200,
        headers: { 'Content-Length': '4', 'Content-Type': 'image/png' },
      }),
    );

    await renderPreview(makeEntry('photo2.png', 'image/png', 100, 'ref-img-2'), { supportsThumbnails: true });
    expect(revokeUrlSpy).toHaveBeenCalledWith('blob:https://api.example.test/thumb-1');
    expect(createDownloadTicket).toHaveBeenCalledTimes(1);
    expect(latest.status).toBe('ready');
    expect(latest.blobUrl).toBe('blob:https://api.example.test/full-2');
  });

  it('sanitizes DOCX output, parses XLSX sheets, and discards stale in-flight preview loads on entry switch', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('bytes', {
        status: 200,
        headers: { 'Content-Length': '5', 'Content-Type': 'image/png' },
      }),
    );

    await renderPreview(makeEntry('report.docx', undefined, 50, 'ref-docx'));
    expect(mockDocxWorkers).toHaveLength(1);
    await act(async () => {
      mockDocxWorkers[0].onmessage?.({
        data: {
          ok: true,
          html: '<p>Safe</p><script>alert(1)</script><img src="https://evil.test/x.png">',
        },
      } as MessageEvent);
      await Promise.resolve();
    });
    expect(latest.status).toBe('ready');
    expect(latest.docxHtml).toContain('<p>Safe</p>');
    expect(latest.docxHtml).not.toContain('<script>');
    expect(latest.docxHtml).not.toContain('evil.test');

    // Stale ticket resolution after entry switch must be ignored
    const slowTicket = deferred<Awaited<ReturnType<typeof createDownloadTicket>>>();
    vi.mocked(createDownloadTicket)
      .mockImplementationOnce(() => slowTicket.promise)
      .mockResolvedValueOnce({ ok: true, status: 200, data: { download_url: '/api/files/download/fast' } });
    fetchSpy.mockResolvedValueOnce(
      new Response('fast text', {
        status: 200,
        headers: { 'Content-Length': '9', 'Content-Type': 'image/png' },
      }),
    );

    await act(async () => {
      root.render(
        <Probe
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          entry={makeEntry('slow.txt', 'text/plain', 50, 'ref-slow')}
        />,
      );
      await Promise.resolve();
    });
    expect(latest.status).toBe('loading');

    await renderPreview(makeEntry('fast.txt', 'text/plain', 9, 'ref-fast'));
    expect(latest.status).toBe('ready');
    expect(latest.text).toBe('fast text');

    slowTicket.resolve({
      ok: false,
      status: 500,
      errorCode: 'INTERNAL_ERROR',
      networkError: false,
    });
    await flushAsync();
    expect(latest.status).toBe('ready');
    expect(latest.text).toBe('fast text');
  });

  it('keeps blob URL stable on identical-metadata re-renders and reloads cleanly after visiting an unpreviewable entry', async () => {
    const createUrlSpy = vi.spyOn(URL, 'createObjectURL')
      .mockReturnValueOnce('blob:https://api.example.test/img-a-1')
      .mockReturnValueOnce('blob:https://api.example.test/img-a-2');
    const revokeUrlSpy = vi.spyOn(URL, 'revokeObjectURL');

    vi.spyOn(globalThis, 'fetch').mockImplementation(async () =>
      new Response('img', {
        status: 200,
        headers: { 'Content-Length': '3', 'Content-Type': 'image/png' },
      }),
    );

    const imgEntry = makeEntry('photo.png', 'image/png', 100, 'ref-img-a');
    await renderPreview(imgEntry);
    expect(latest.status).toBe('ready');
    expect(latest.blobUrl).toBe('blob:https://api.example.test/img-a-1');
    expect(createDownloadTicket).toHaveBeenCalledTimes(1);

    // Re-render with a new entry object carrying identical metadata -> should not revoke or re-fetch
    await renderPreview({ ...imgEntry });
    expect(revokeUrlSpy).not.toHaveBeenCalled();
    expect(createDownloadTicket).toHaveBeenCalledTimes(1);
    expect(latest.blobUrl).toBe('blob:https://api.example.test/img-a-1');

    // Switch to an unpreviewable file, then back to photo.png -> must reload a fresh blob URL
    await renderPreview(makeEntry('archive.zip', 'application/zip', 200, 'ref-zip'));
    expect(latest.status).toBe('fallback');
    expect(revokeUrlSpy).toHaveBeenCalledWith('blob:https://api.example.test/img-a-1');

    await renderPreview(makeEntry('photo.png', 'image/png', 100, 'ref-img-a'));
    expect(createUrlSpy).toHaveBeenCalledTimes(2);
    expect(latest.status).toBe('ready');
    expect(latest.blobUrl).toBe('blob:https://api.example.test/img-a-2');
  });

  it('supports manual fallback transition when media or document viewer errors', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(
      new Response('img', {
        status: 200,
        headers: { 'Content-Length': '3', 'Content-Type': 'image/png' },
      }),
    );

    await renderPreview(makeEntry('broken.png', 'image/png', 100));
    expect(latest.status).toBe('ready');

    act(() => {
      latest.fallback();
    });
    expect(latest.status).toBe('fallback');
  });
});
