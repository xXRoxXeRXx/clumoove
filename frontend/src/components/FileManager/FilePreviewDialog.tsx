import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  ArrowDownTrayIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  FileIcon,
  MagnifyingGlassMinusIcon,
  MagnifyingGlassPlusIcon,
  XMarkIcon,
} from '../icons';
import DOMPurify from 'dompurify';
import { Document, Page, pdfjs } from 'react-pdf';
import { useTranslation } from 'react-i18next';
import { createDownloadTicket, type FileEntry } from '../../api/files';
import { useFocusTrap } from '../../hooks/useFocusTrap';
import { useApiError } from '../../utils/apiError';
import { useFormat } from '../../utils/format';
import { canPreview, previewKindFor, previewLimit } from './filePreview';
import DocxWorker from './docxPreview.worker.ts?worker';
import XlsxWorker from './xlsxPreview.worker.ts?worker';
import pdfWorkerSrc from 'pdfjs-dist/build/pdf.worker.min.mjs?url';

import 'react-pdf/dist/Page/AnnotationLayer.css';
import 'react-pdf/dist/Page/TextLayer.css';

if (typeof window !== 'undefined') {
  pdfjs.GlobalWorkerOptions.workerSrc = pdfWorkerSrc;
}

type Sheet = { name: string; rows: string[][] };

function parseWorker<T>(worker: Worker, buffer: ArrayBuffer, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    const cleanup = () => {
      window.clearTimeout(timeout);
      signal.removeEventListener('abort', abort);
      worker.terminate();
    };
    const fail = (error: Error) => {
      cleanup();
      reject(error);
    };
    const timeout = window.setTimeout(() => fail(new Error('preview timeout')), 15_000);
    const abort = () => fail(new DOMException('Aborted', 'AbortError'));
    signal.addEventListener('abort', abort, { once: true });
    worker.onmessage = (event: MessageEvent<T & { ok: boolean }>) => {
      if (!event.data.ok) fail(new Error('preview parser failed'));
      else {
        cleanup();
        resolve(event.data);
      }
    };
    worker.onerror = (err) => {
      fail(err instanceof Error ? err : new Error('preview parser failed'));
    };
    worker.postMessage({ type: 'parse', buffer }, [buffer]);
  });
}

function sanitizeDocx(html: string): string {
  const clean = DOMPurify.sanitize(html, {
    ALLOWED_TAGS: ['p', 'br', 'strong', 'em', 'b', 'i', 'u', 's', 'h1', 'h2', 'h3', 'h4', 'ul', 'ol', 'li', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'blockquote', 'code', 'pre', 'img'],
    ALLOWED_ATTR: ['src', 'alt', 'colspan', 'rowspan'],
    ALLOW_DATA_ATTR: false,
  });
  const document = new DOMParser().parseFromString(clean, 'text/html');
  document.querySelectorAll('img').forEach((image) => {
    if (!image.getAttribute('src')?.startsWith('data:image/')) image.remove();
  });
  return document.body.innerHTML;
}

type FilePreviewContentProps = {
  apiUrl: string;
  token: string;
  profileId: string;
  entry: FileEntry;
  onDownload: (entry: FileEntry) => void;
};

function FilePreviewContent({ apiUrl, token, profileId, entry, onDownload }: FilePreviewContentProps) {
  const { t } = useTranslation();
  const { formatBytes, formatDateTime } = useFormat();
  const translateApiError = useApiError();
  const kind = previewKindFor(entry);
  const [blobUrl, setBlobUrl] = useState<string | null>(null);
  const [pdfData, setPdfData] = useState<Uint8Array | null>(null);
  const [text, setText] = useState('');
  const [docxHtml, setDocxHtml] = useState('');
  const [sheets, setSheets] = useState<Sheet[]>([]);
  const [sheetIndex, setSheetIndex] = useState(0);
  const [pages, setPages] = useState(0);
  const [page, setPage] = useState(1);
  const [zoom, setZoom] = useState(1);
  const [state, setState] = useState<'loading' | 'ready' | 'fallback'>(() => (!kind || !canPreview(entry) ? 'fallback' : 'loading'));
  const [error, setError] = useState('');

  const pdfFile = useMemo(() => {
    if (pdfData) return { data: pdfData };
    if (blobUrl) return blobUrl;
    return null;
  }, [pdfData, blobUrl]);

  useEffect(() => {
    if (!kind || !canPreview(entry)) return;

    const controller = new AbortController();
    let objectUrl: string | null = null;
    let worker: Worker | null = null;
    const load = async () => {
      const ticket = await createDownloadTicket(apiUrl, token, profileId, entry.ref, controller.signal);
      if (controller.signal.aborted) return;
      if (ticket.ok === false) {
        setError(translateApiError(ticket.errorCode));
        setState('fallback');
        return;
      }
      try {
        const response = await fetch(new URL(ticket.data.download_url, apiUrl), { cache: 'no-store', credentials: 'omit', signal: controller.signal });
        if (!response.ok) {
          const body = await response.json().catch(() => ({})) as { error_code?: string };
          setError(translateApiError(body.error_code));
          setState('fallback');
          return;
        }
        const contentLength = Number(response.headers.get('Content-Length') || 0);
        if (contentLength > previewLimit(kind)) {
          setState('fallback');
          return;
        }
        const blob = await response.blob();
        if (controller.signal.aborted || blob.size > previewLimit(kind)) return setState('fallback');
        objectUrl = URL.createObjectURL(blob);
        setBlobUrl(objectUrl);
        if (kind === 'text') setText(await blob.text());
        if (kind === 'pdf') {
          const buffer = await blob.arrayBuffer();
          setPdfData(new Uint8Array(buffer));
        }
        if (kind === 'docx') {
          worker = new DocxWorker();
          const parsed = await parseWorker<{ html?: string; ok: boolean }>(worker, await blob.arrayBuffer(), controller.signal);
          setDocxHtml(sanitizeDocx(parsed.html ?? ''));
        } else if (kind === 'xlsx') {
          worker = new XlsxWorker();
          const parsed = await parseWorker<{ sheets?: Sheet[]; ok: boolean }>(worker, await blob.arrayBuffer(), controller.signal);
          setSheets(parsed.sheets ?? []);
        }
        if (!controller.signal.aborted) setState('ready');
      } catch (loadError) {
        if (controller.signal.aborted || (loadError instanceof DOMException && loadError.name === 'AbortError')) return;
        setState('fallback');
      }
    };
    void load();
    return () => {
      controller.abort();
      worker?.terminate();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [apiUrl, entry, kind, profileId, token, translateApiError]);

  if (state === 'loading') {
    return <p className="ui-empty py-12 text-center">{t('files.previewLoading')}</p>;
  }

  if (state === 'fallback') {
    return (
      <div className="mx-auto flex flex-col items-center justify-center p-6 text-center max-w-lg z-10">
        <div className="relative mb-5 flex h-24 w-24 items-center justify-center rounded-2xl bg-[var(--color-bg-secondary)] border border-[var(--color-border)] shadow-xs">
          <FileIcon
            name={entry.name}
            mimeType={entry.mime_type}
            isDir={false}
            className="h-14 w-14 drop-shadow-xs"
          />
        </div>
        <h3 className="text-lg font-semibold text-[var(--color-text-primary)] break-all max-w-md">
          {entry.name}
        </h3>
        <div className="mt-2 flex items-center justify-center gap-2 text-xs text-[var(--color-text-secondary)]">
          {entry.modified_at && (
            <span>{formatDateTime(entry.modified_at)}</span>
          )}
          {entry.modified_at && entry.size >= 0 && (
            <span className="text-[var(--color-text-muted)]" aria-hidden="true">•</span>
          )}
          <span>{formatBytes(entry.size)}</span>
        </div>
        <p className="mt-4 text-sm text-[var(--color-text-muted)] max-w-sm">
          {error || t('files.previewUnavailable')}
        </p>
        {entry.allowed_actions.includes('download') && (
          <button
            type="button"
            onClick={() => onDownload(entry)}
            className="ui-button-primary mt-5 inline-flex items-center gap-2 px-4 py-2 text-sm shadow-xs"
          >
            <ArrowDownTrayIcon className="h-4 w-4" aria-hidden="true" />
            {t('files.download', { name: entry.name })}
          </button>
        )}
      </div>
    );
  }

  return (
    <>
      {kind === 'image' && blobUrl && (
        <img src={blobUrl} alt={entry.name} className="max-h-full max-w-full object-contain" onError={() => setState('fallback')} />
      )}
      {kind === 'audio' && blobUrl && (
        <audio controls src={blobUrl} className="w-full max-w-2xl" onError={() => setState('fallback')} />
      )}
      {kind === 'video' && blobUrl && (
        <video controls src={blobUrl} className="max-h-full max-w-full" onError={() => setState('fallback')} />
      )}
      {kind === 'text' && (
        <div className="w-full h-full overflow-auto">
          <pre className="whitespace-pre-wrap break-words font-mono text-sm">{text}</pre>
        </div>
      )}
      {kind === 'docx' && (
        <div className="w-full h-full overflow-auto">
          <article className="prose max-w-none dark:prose-invert" dangerouslySetInnerHTML={{ __html: docxHtml }} />
        </div>
      )}
      {kind === 'xlsx' && (
        <div className="w-full h-full overflow-auto space-y-3">
          <div className="flex flex-wrap gap-2">
            {sheets.map((sheet, index) => (
              <button
                key={sheet.name}
                type="button"
                onClick={() => setSheetIndex(index)}
                className={index === sheetIndex ? 'ui-button-primary px-3 py-2 text-sm' : 'ui-button-secondary px-3 py-2 text-sm'}
              >
                {sheet.name}
              </button>
            ))}
          </div>
          <div className="overflow-auto">
            <table className="ui-table text-sm">
              <tbody>
                {(sheets[sheetIndex]?.rows ?? []).map((row, rowIndex) => (
                  <tr key={rowIndex}>
                    {row.map((value, cellIndex) => (
                      <td key={cellIndex} className="border border-[var(--color-border)] px-2 py-1">
                        {value}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
      {kind === 'pdf' && pdfFile && (
        <div className="w-full h-full overflow-auto flex flex-col items-center space-y-3">
          <div className="flex items-center justify-center gap-2 shrink-0">
            <button
              type="button"
              onClick={() => setPage((current) => Math.max(1, current - 1))}
              disabled={page <= 1}
              className="ui-icon-button p-2"
            >
              <ChevronLeftIcon className="h-4 w-4" aria-hidden="true" />
            </button>
            <span className="text-sm">{page} / {pages || '?'}</span>
            <button
              type="button"
              onClick={() => setPage((current) => Math.min(pages || current, current + 1))}
              disabled={pages === 0 || page >= pages}
              className="ui-icon-button p-2"
            >
              <ChevronRightIcon className="h-4 w-4" aria-hidden="true" />
            </button>
            <button
              type="button"
              onClick={() => setZoom((current) => Math.max(0.5, current - 0.25))}
              className="ui-icon-button p-2"
              aria-label={t('files.previewZoomOut')}
            >
              <MagnifyingGlassMinusIcon className="h-4 w-4" aria-hidden="true" />
            </button>
            <button
              type="button"
              onClick={() => setZoom((current) => Math.min(2, current + 0.25))}
              className="ui-icon-button p-2"
              aria-label={t('files.previewZoomIn')}
            >
              <MagnifyingGlassPlusIcon className="h-4 w-4" aria-hidden="true" />
            </button>
          </div>
          <div className="overflow-auto max-h-full">
            <Document file={pdfFile} onLoadSuccess={({ numPages }) => { setPages(numPages); setPage(1); }} onLoadError={() => setState('fallback')}>
              <Page pageNumber={page} scale={zoom} className="mx-auto w-fit max-w-full" />
            </Document>
          </div>
        </div>
      )}
    </>
  );
}

type FilePreviewDialogProps = {
  apiUrl: string;
  token: string;
  profileId: string;
  entry: FileEntry;
  entries?: FileEntry[];
  onNavigate?: (entry: FileEntry) => void;
  onClose: () => void;
  onDownload: (entry: FileEntry) => void;
};

export function FilePreviewDialog({ apiUrl, token, profileId, entry, entries, onNavigate, onClose, onDownload }: FilePreviewDialogProps) {
  const { t } = useTranslation();
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  useFocusTrap(dialogRef, closeRef, onClose);

  const fileList = useMemo(() => {
    if (!entries || entries.length === 0) return [entry];
    const files = entries.filter((e) => e.kind === 'file');
    return files.length > 0 ? files : [entry];
  }, [entries, entry]);

  const currentIndex = fileList.findIndex((item) => item.ref === entry.ref);
  const hasMultipleFiles = fileList.length > 1;
  const hasPrevious = currentIndex > 0;
  const hasNext = currentIndex >= 0 && currentIndex < fileList.length - 1;

  const handlePrevious = useCallback(() => {
    if (hasPrevious && onNavigate) {
      onNavigate(fileList[currentIndex - 1]);
    }
  }, [currentIndex, fileList, hasPrevious, onNavigate]);

  const handleNext = useCallback(() => {
    if (hasNext && onNavigate) {
      onNavigate(fileList[currentIndex + 1]);
    }
  }, [currentIndex, fileList, hasNext, onNavigate]);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (
        e.target instanceof HTMLInputElement ||
        e.target instanceof HTMLTextAreaElement ||
        e.target instanceof HTMLSelectElement
      ) {
        return;
      }
      if (e.key === 'ArrowLeft' && hasPrevious) {
        e.preventDefault();
        handlePrevious();
      } else if (e.key === 'ArrowRight' && hasNext) {
        e.preventDefault();
        handleNext();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [handleNext, handlePrevious, hasNext, hasPrevious]);

  return createPortal(
    <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-0 sm:p-5" role="presentation">
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby="file-preview-title" tabIndex={-1} className="flex h-full w-full flex-col bg-[var(--color-bg-primary)] sm:rounded-lg overflow-hidden border border-[var(--color-border)] shadow-2xl">
        <header className="flex items-center justify-between gap-3 border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-4 py-3 shrink-0 sm:rounded-t-lg">
          <div className="flex min-w-0 items-center gap-2.5">
            {hasMultipleFiles && currentIndex >= 0 && (
              <span className="inline-flex shrink-0 items-center rounded-md bg-[var(--color-bg-tertiary)] px-2 py-0.5 text-xs font-medium text-[var(--color-text-secondary)] border border-[var(--color-border)]">
                {currentIndex + 1} / {fileList.length}
              </span>
            )}
            <FileIcon name={entry.name} mimeType={entry.mime_type} isDir={entry.kind === 'directory'} className="h-5 w-5 shrink-0" />
            <h2 id="file-preview-title" className="min-w-0 truncate text-base text-[var(--color-text-primary)]">
              {t('files.previewTitle', { name: entry.name })}
            </h2>
          </div>
          <div className="flex items-center gap-1">
            <button
              type="button"
              onClick={() => onDownload(entry)}
              disabled={!entry.allowed_actions.includes('download')}
              className="ui-icon-button p-2 hover:bg-[var(--color-hover)] disabled:opacity-30 disabled:cursor-not-allowed"
              aria-label={t('files.download', { name: entry.name })}
              title={t('files.download', { name: entry.name })}
            >
              <ArrowDownTrayIcon className="h-5 w-5" aria-hidden="true" />
            </button>
            <button
              ref={closeRef}
              type="button"
              onClick={onClose}
              className="ui-icon-button p-2 hover:bg-[var(--color-hover)]"
              aria-label={t('common.close')}
              title={t('common.close')}
            >
              <XMarkIcon className="h-5 w-5" aria-hidden="true" />
            </button>
          </div>
        </header>

        <div className="relative min-h-0 flex-1 overflow-auto p-4 flex flex-col items-center justify-center">
          {hasMultipleFiles && (
            <>
              <button
                type="button"
                onClick={handlePrevious}
                disabled={!hasPrevious}
                className="absolute left-3 top-1/2 -translate-y-1/2 z-20 flex h-10 w-10 items-center justify-center rounded-full bg-[var(--color-bg-primary)]/90 hover:bg-[var(--color-bg-primary)] text-[var(--color-text-primary)] border border-[var(--color-border)] shadow-lg backdrop-blur-xs transition-all disabled:opacity-0 disabled:pointer-events-none hover:scale-105 active:scale-95 focus-visible:outline-2 focus-visible:outline-[var(--color-focus)]"
                aria-label={t('files.previousFile')}
                title={t('files.previousFile')}
              >
                <ChevronLeftIcon className="h-6 w-6" aria-hidden="true" />
              </button>

              <button
                type="button"
                onClick={handleNext}
                disabled={!hasNext}
                className="absolute right-3 top-1/2 -translate-y-1/2 z-20 flex h-10 w-10 items-center justify-center rounded-full bg-[var(--color-bg-primary)]/90 hover:bg-[var(--color-bg-primary)] text-[var(--color-text-primary)] border border-[var(--color-border)] shadow-lg backdrop-blur-xs transition-all disabled:opacity-0 disabled:pointer-events-none hover:scale-105 active:scale-95 focus-visible:outline-2 focus-visible:outline-[var(--color-focus)]"
                aria-label={t('files.nextFile')}
                title={t('files.nextFile')}
              >
                <ChevronRightIcon className="h-6 w-6" aria-hidden="true" />
              </button>
            </>
          )}

          <FilePreviewContent
            key={entry.ref}
            apiUrl={apiUrl}
            token={token}
            profileId={profileId}
            entry={entry}
            onDownload={onDownload}
          />
        </div>
      </div>
    </div>,
    document.body
  );
}
