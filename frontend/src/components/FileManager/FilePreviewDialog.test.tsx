import { act } from 'react';
import React from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import { FilePreviewDialog } from './FilePreviewDialog';
import { createDownloadTicket, type FileEntry } from '../../api/files';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../../api/files', () => ({
  createDownloadTicket: vi.fn(),
}));

vi.mock('react-pdf', () => ({
  Document: ({
    children,
    onLoadSuccess,
  }: {
    children: React.ReactNode;
    onLoadSuccess?: (pdf: { numPages: number }) => void;
  }) => {
    React.useEffect(() => {
      const timer = setTimeout(() => onLoadSuccess?.({ numPages: 5 }), 0);
      return () => clearTimeout(timer);
    }, [onLoadSuccess]);
    return <div data-testid="mock-pdf-document">{children}</div>;
  },
  Page: ({ pageNumber, scale }: { pageNumber: number; scale?: number }) => (
    <div data-testid="mock-pdf-page" data-page={pageNumber} data-scale={scale}>
      Page {pageNumber}
    </div>
  ),
  pdfjs: {
    GlobalWorkerOptions: {
      workerSrc: '',
    },
  },
}));

function makeEntry(name: string, mimeType?: string, size = 1024): FileEntry {
  return {
    ref: 'safe-ref',
    name,
    display_path: '/files/' + name,
    kind: 'file',
    size,
    mime_type: mimeType,
    allowed_actions: ['download'],
  };
}

type MockWorkerInstance = {
  postMessage: ReturnType<typeof vi.fn>;
  terminate: ReturnType<typeof vi.fn>;
  onmessage: ((event: MessageEvent) => void) | null;
  onerror: ((error: ErrorEvent) => void) | null;
};

const mockXlsxWorkerInstances: MockWorkerInstance[] = [];

vi.mock('./xlsxPreview.worker.ts?worker', () => {
  return {
    default: class MockXlsxWorker {
      postMessage = vi.fn();
      terminate = vi.fn();
      onmessage = null;
      onerror = null;
      constructor() {
        mockXlsxWorkerInstances.push(this);
      }
    },
  };
});

describe('FilePreviewDialog', () => {
  let container: HTMLDivElement;
  let root: Root;
  const onDownload = vi.fn();
  const onClose = vi.fn();

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    vi.mocked(createDownloadTicket).mockReset();
    vi.mocked(createDownloadTicket).mockResolvedValue({
      ok: true,
      status: 200,
      data: {
        download_url: '/api/files/download/mock-ticket',
      },
    });
    onDownload.mockReset();
    onClose.mockReset();
    mockXlsxWorkerInstances.length = 0;
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    vi.useRealTimers();
    act(() => root?.unmount());
    container?.remove();
  });

  it('renders fallback state for unsupported file types', async () => {
    const unsupported = makeEntry('archive.zip', 'application/zip', 2048);
    await act(async () => {
      root.render(
        <FilePreviewDialog
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          entry={unsupported}
          onClose={onClose}
          onDownload={onDownload}
        />
      );
      await Promise.resolve();
    });

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.textContent).toContain('archive.zip');
    expect(dialog?.textContent).toContain('2 KB');
    expect(dialog?.textContent).toContain('This file cannot be safely previewed.');

    const downloadBtn = dialog?.querySelector('button.ui-button-primary');
    expect(downloadBtn).not.toBeNull();
    act(() => {
      (downloadBtn as HTMLButtonElement)?.click();
    });
    expect(onDownload).toHaveBeenCalledWith(unsupported);
  });

  it('renders navigation buttons and counter when multiple files are provided', async () => {
    const file1 = { ...makeEntry('photo1.jpg', 'image/jpeg'), ref: 'ref-1' };
    const file2 = { ...makeEntry('doc.pdf', 'application/pdf'), ref: 'ref-2' };
    const file3 = { ...makeEntry('archive.zip', 'application/zip'), ref: 'ref-3' };
    const dir = { ...makeEntry('Folder', undefined), ref: 'ref-dir', kind: 'directory' as const };
    const onNavigate = vi.fn();

    await act(async () => {
      root.render(
        <FilePreviewDialog
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          entry={file2}
          entries={[file1, dir, file2, file3]}
          onNavigate={onNavigate}
          onClose={onClose}
          onDownload={onDownload}
        />
      );
      await Promise.resolve();
    });

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain('2 / 3');

    const prevBtn = dialog?.querySelector<HTMLButtonElement>('button[aria-label="Previous file"]');
    const nextBtn = dialog?.querySelector<HTMLButtonElement>('button[aria-label="Next file"]');
    expect(prevBtn).not.toBeNull();
    expect(nextBtn).not.toBeNull();
    expect(prevBtn?.disabled).toBe(false);
    expect(nextBtn?.disabled).toBe(false);

    act(() => {
      prevBtn?.click();
    });
    expect(onNavigate).toHaveBeenCalledWith(file1);

    act(() => {
      nextBtn?.click();
    });
    expect(onNavigate).toHaveBeenCalledWith(file3);
  });

  it('disables previous button on first file and next button on last file', async () => {
    const file1 = { ...makeEntry('first.txt', 'text/plain'), ref: 'ref-1' };
    const file2 = { ...makeEntry('second.txt', 'text/plain'), ref: 'ref-2' };
    const onNavigate = vi.fn();

    await act(async () => {
      root.render(
        <FilePreviewDialog
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          entry={file1}
          entries={[file1, file2]}
          onNavigate={onNavigate}
          onClose={onClose}
          onDownload={onDownload}
        />
      );
      await Promise.resolve();
    });

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain('1 / 2');

    const prevBtn = dialog?.querySelector<HTMLButtonElement>('button[aria-label="Previous file"]');
    const nextBtn = dialog?.querySelector<HTMLButtonElement>('button[aria-label="Next file"]');
    expect(prevBtn?.disabled).toBe(true);
    expect(nextBtn?.disabled).toBe(false);
  });

  it('handles keyboard navigation with arrow keys', async () => {
    const file1 = { ...makeEntry('first.txt', 'text/plain'), ref: 'ref-1' };
    const file2 = { ...makeEntry('second.txt', 'text/plain'), ref: 'ref-2' };
    const file3 = { ...makeEntry('third.txt', 'text/plain'), ref: 'ref-3' };
    const onNavigate = vi.fn();

    await act(async () => {
      root.render(
        <FilePreviewDialog
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          entry={file2}
          entries={[file1, file2, file3]}
          onNavigate={onNavigate}
          onClose={onClose}
          onDownload={onDownload}
        />
      );
      await Promise.resolve();
    });

    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft' }));
    });
    expect(onNavigate).toHaveBeenCalledWith(file1);

    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }));
    });
    expect(onNavigate).toHaveBeenCalledWith(file3);
  });

  it('loads and renders a PDF with page controls', async () => {
    vi.mocked(createDownloadTicket).mockResolvedValue({
      ok: true,
      status: 200,
      data: {
        download_url: '/api/files/download/ticket-123',
      },
    });

    const mockBlob = new Blob(['%PDF-1.4 dummy pdf content'], { type: 'application/pdf' });
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(mockBlob, {
        status: 200,
        headers: { 'Content-Length': String(mockBlob.size) },
      })
    );

    const pdfEntry = makeEntry('document.pdf', 'application/pdf', 100);

    await act(async () => {
      root.render(
        <FilePreviewDialog
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          entry={pdfEntry}
          onClose={onClose}
          onDownload={onDownload}
        />
      );
      await Promise.resolve();
    });

    // Wait for the state to transition to ready and document to load
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });

    const pdfDoc = document.querySelector('[data-testid="mock-pdf-document"]');
    expect(pdfDoc).not.toBeNull();
    expect(document.body.textContent).toContain('1 / 5');

    fetchSpy.mockRestore();
  });

  it('calls onDownload when download button in header is clicked', async () => {
    vi.mocked(createDownloadTicket).mockReturnValue(new Promise(() => {}));
    const entry = makeEntry('document.pdf', 'application/pdf');
    await act(async () => {
      root.render(
        <FilePreviewDialog
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          entry={entry}
          onClose={onClose}
          onDownload={onDownload}
        />
      );
      await Promise.resolve();
    });

    const downloadBtn = document.querySelector<HTMLButtonElement>('header button[aria-label^="Download"]');
    expect(downloadBtn).not.toBeNull();
    act(() => {
      downloadBtn?.click();
    });
    expect(onDownload).toHaveBeenCalledWith(entry);
  });

  it('loads and renders an XLSX spreadsheet preview with sheets and table cells', async () => {
    vi.mocked(createDownloadTicket).mockResolvedValue({
      ok: true,
      status: 200,
      data: {
        download_url: '/api/files/download/ticket-456',
      },
    });

    const mockBlob = new Blob(['mock xlsx bytes'], {
      type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    });
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(mockBlob, {
        status: 200,
        headers: { 'Content-Length': String(mockBlob.size) },
      })
    );

    const xlsxEntry = makeEntry(
      'budget.xlsx',
      'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
      100
    );

    await act(async () => {
      root.render(
        <FilePreviewDialog
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          entry={xlsxEntry}
          onClose={onClose}
          onDownload={onDownload}
        />
      );
      await Promise.resolve();
    });

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 10));
    });

    expect(mockXlsxWorkerInstances).toHaveLength(1);
    const worker = mockXlsxWorkerInstances[0];

    await act(async () => {
      worker.onmessage?.({
        data: {
          ok: true,
          sheets: [
            {
              name: 'Q1',
              rows: [
                ['Category', 'Amount'],
                ['Hardware', '1200'],
              ],
            },
            {
              name: 'Q2',
              rows: [['Category', 'Amount']],
            },
          ],
        },
      } as MessageEvent);
      await Promise.resolve();
    });

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain('Q1');
    expect(dialog?.textContent).toContain('Hardware');
    expect(dialog?.textContent).toContain('1200');

    fetchSpy.mockRestore();
  });

  it('terminates unresponsive XLSX worker after 15s timeout and presents download fallback', async () => {
    vi.useFakeTimers();

    vi.mocked(createDownloadTicket).mockResolvedValue({
      ok: true,
      status: 200,
      data: {
        download_url: '/api/files/download/ticket-789',
      },
    });

    const mockBlob = new Blob(['mock xlsx bytes'], {
      type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    });
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(mockBlob, {
        status: 200,
        headers: { 'Content-Length': String(mockBlob.size) },
      })
    );

    const xlsxEntry = makeEntry(
      'huge.xlsx',
      'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
      100
    );

    await act(async () => {
      root.render(
        <FilePreviewDialog
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          entry={xlsxEntry}
          onClose={onClose}
          onDownload={onDownload}
        />
      );
      await Promise.resolve();
    });

    // Let the fetch and worker startup complete
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });

    expect(mockXlsxWorkerInstances).toHaveLength(1);
    const worker = mockXlsxWorkerInstances[0];

    // Fast-forward 15 seconds to trigger parseWorker timeout
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_000);
    });

    expect(worker.terminate).toHaveBeenCalled();
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain('This file cannot be safely previewed.');

    fetchSpy.mockRestore();
  });

  it('terminates worker and revokes Blob URL on dialog unmount or abort without error', async () => {
    vi.mocked(createDownloadTicket).mockResolvedValue({
      ok: true,
      status: 200,
      data: {
        download_url: '/api/files/download/ticket-abort',
      },
    });

    const mockBlob = new Blob(['mock xlsx bytes'], {
      type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    });
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(mockBlob, {
        status: 200,
        headers: { 'Content-Length': String(mockBlob.size) },
      })
    );

    const revokeObjectURLSpy = vi.spyOn(URL, 'revokeObjectURL');

    const xlsxEntry = makeEntry(
      'data.xlsx',
      'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
      100
    );

    await act(async () => {
      root.render(
        <FilePreviewDialog
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          entry={xlsxEntry}
          onClose={onClose}
          onDownload={onDownload}
        />
      );
      await Promise.resolve();
    });

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 10));
    });

    expect(mockXlsxWorkerInstances).toHaveLength(1);
    const worker = mockXlsxWorkerInstances[0];

    // Unmount before worker finishes
    act(() => {
      root.unmount();
    });

    expect(worker.terminate).toHaveBeenCalled();
    expect(revokeObjectURLSpy).toHaveBeenCalled();

    fetchSpy.mockRestore();
    revokeObjectURLSpy.mockRestore();
  });
});

