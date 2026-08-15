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

describe('FilePreviewDialog', () => {
  let container: HTMLDivElement;
  let root: Root;
  const onDownload = vi.fn();
  const onClose = vi.fn();

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    vi.mocked(createDownloadTicket).mockReset();
    onDownload.mockReset();
    onClose.mockReset();
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
  });

  it('renders fallback state for unsupported file types', async () => {
    const unsupported = makeEntry('archive.zip', 'application/zip');
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
    expect(dialog?.textContent).toContain('This file cannot be safely previewed.');
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
});
