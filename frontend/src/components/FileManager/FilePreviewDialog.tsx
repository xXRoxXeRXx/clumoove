import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ArrowDownTrayIcon, ChevronLeftIcon, ChevronRightIcon, MagnifyingGlassMinusIcon, MagnifyingGlassPlusIcon, XMarkIcon } from '@heroicons/react/24/outline';
import DOMPurify from 'dompurify';
import { Document, Page, pdfjs } from 'react-pdf';
import { useTranslation } from 'react-i18next';
import { createDownloadTicket, type FileEntry } from '../../api/files';
import { useFocusTrap } from '../../hooks/useFocusTrap';
import { useApiError } from '../../utils/apiError';
import { canPreview, isGenericPreviewMime, normalizedPreviewMime, previewKindFor, previewLimit } from './filePreview';

import 'react-pdf/dist/Page/AnnotationLayer.css';
import 'react-pdf/dist/Page/TextLayer.css';

pdfjs.GlobalWorkerOptions.workerSrc = new URL('pdfjs-dist/build/pdf.worker.min.mjs', import.meta.url).toString();

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
    worker.onerror = () => {
      fail(new Error('preview parser failed'));
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

type FilePreviewDialogProps = {
  apiUrl: string;
  token: string;
  profileId: string;
  entry: FileEntry;
  onClose: () => void;
  onDownload: (entry: FileEntry) => void;
};

export function FilePreviewDialog({ apiUrl, token, profileId, entry, onClose, onDownload }: FilePreviewDialogProps) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const [blobUrl, setBlobUrl] = useState<string | null>(null);
  const [text, setText] = useState('');
  const [docxHtml, setDocxHtml] = useState('');
  const [sheets, setSheets] = useState<Sheet[]>([]);
  const [sheetIndex, setSheetIndex] = useState(0);
  const [pages, setPages] = useState(0);
  const [page, setPage] = useState(1);
  const [zoom, setZoom] = useState(1);
  const [state, setState] = useState<'loading' | 'ready' | 'fallback'>('loading');
  const [error, setError] = useState('');
  const kind = previewKindFor(entry);
  useFocusTrap(dialogRef, closeRef, onClose);

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
        const declaredMime = normalizedPreviewMime(entry.mime_type);
        const receivedMime = normalizedPreviewMime(response.headers.get('Content-Type'));
        if (!isGenericPreviewMime(declaredMime) && !isGenericPreviewMime(receivedMime) && declaredMime !== receivedMime) {
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
        if (kind === 'docx' || kind === 'xlsx') {
          worker = new Worker(new URL(kind === 'docx' ? './docxPreview.worker.ts' : './xlsxPreview.worker.ts', import.meta.url), { type: 'module' });
          const parsed = await parseWorker<{ html?: string; sheets?: Sheet[]; ok: boolean }>(worker, await blob.arrayBuffer(), controller.signal);
          if (kind === 'docx') setDocxHtml(sanitizeDocx(parsed.html ?? ''));
          else setSheets(parsed.sheets ?? []);
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

  return createPortal(
    <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-0 sm:p-5" role="presentation">
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby="file-preview-title" tabIndex={-1} className="flex h-full w-full flex-col bg-[var(--color-bg-primary)] sm:rounded-lg overflow-hidden border border-[var(--color-border)] shadow-2xl">
        <header className="flex items-center justify-between gap-3 border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-4 py-3 shrink-0 sm:rounded-t-lg">
          <h2 id="file-preview-title" className="min-w-0 truncate text-base font-semibold text-[var(--color-text-primary)]">{t('files.previewTitle', { name: entry.name })}</h2>
          <div className="flex items-center gap-1">
            <button type="button" onClick={() => onDownload(entry)} className="ui-icon-button p-2 hover:bg-[var(--color-hover)]" aria-label={t('files.download', { name: entry.name })} title={t('files.download', { name: entry.name })}><ArrowDownTrayIcon className="h-5 w-5" aria-hidden="true" /></button>
            <button ref={closeRef} type="button" onClick={onClose} className="ui-icon-button p-2 hover:bg-[var(--color-hover)]" aria-label={t('common.close')} title={t('common.close')}><XMarkIcon className="h-5 w-5" aria-hidden="true" /></button>
          </div>
        </header>
        <div className="min-h-0 flex-1 overflow-auto p-4 flex flex-col items-center justify-center">
          {state === 'loading' && <p className="ui-empty py-12 text-center">{t('files.previewLoading')}</p>}
          {state === 'fallback' && <div className="ui-empty mx-auto max-w-lg py-12 text-center"><p>{error || t('files.previewUnavailable')}</p><button type="button" onClick={() => onDownload(entry)} className="ui-button-secondary mt-4 inline-flex items-center gap-2 px-3 py-2 text-sm"><ArrowDownTrayIcon className="h-4 w-4" aria-hidden="true" />{t('files.download', { name: entry.name })}</button></div>}
          {state === 'ready' && kind === 'image' && blobUrl && <img src={blobUrl} alt={entry.name} className="max-h-full max-w-full object-contain" onError={() => setState('fallback')} />}
          {state === 'ready' && kind === 'audio' && blobUrl && <audio controls src={blobUrl} className="w-full max-w-2xl" onError={() => setState('fallback')} />}
          {state === 'ready' && kind === 'video' && blobUrl && <video controls src={blobUrl} className="max-h-full max-w-full" onError={() => setState('fallback')} />}
          {state === 'ready' && kind === 'text' && <div className="w-full h-full overflow-auto"><pre className="whitespace-pre-wrap break-words font-mono text-sm">{text}</pre></div>}
          {state === 'ready' && kind === 'docx' && <div className="w-full h-full overflow-auto"><article className="prose max-w-none dark:prose-invert" dangerouslySetInnerHTML={{ __html: docxHtml }} /></div>}
          {state === 'ready' && kind === 'xlsx' && <div className="w-full h-full overflow-auto space-y-3"><div className="flex flex-wrap gap-2">{sheets.map((sheet, index) => <button key={sheet.name} type="button" onClick={() => setSheetIndex(index)} className={index === sheetIndex ? 'ui-button-primary px-3 py-2 text-sm' : 'ui-button-secondary px-3 py-2 text-sm'}>{sheet.name}</button>)}</div><div className="overflow-auto"><table className="ui-table text-sm"><tbody>{(sheets[sheetIndex]?.rows ?? []).map((row, rowIndex) => <tr key={rowIndex}>{row.map((value, cellIndex) => <td key={cellIndex} className="border border-[var(--color-border)] px-2 py-1">{value}</td>)}</tr>)}</tbody></table></div></div>}
          {state === 'ready' && kind === 'pdf' && blobUrl && <div className="w-full h-full overflow-auto flex flex-col items-center space-y-3"><div className="flex items-center justify-center gap-2 shrink-0"><button type="button" onClick={() => setPage((current) => Math.max(1, current - 1))} disabled={page <= 1} className="ui-icon-button p-2"><ChevronLeftIcon className="h-4 w-4" aria-hidden="true" /></button><span className="text-sm">{page} / {pages || '?'}</span><button type="button" onClick={() => setPage((current) => Math.min(pages || current, current + 1))} disabled={pages === 0 || page >= pages} className="ui-icon-button p-2"><ChevronRightIcon className="h-4 w-4" aria-hidden="true" /></button><button type="button" onClick={() => setZoom((current) => Math.max(0.5, current - 0.25))} className="ui-icon-button p-2" aria-label={t('files.previewZoomOut')}><MagnifyingGlassMinusIcon className="h-4 w-4" aria-hidden="true" /></button><button type="button" onClick={() => setZoom((current) => Math.min(2, current + 0.25))} className="ui-icon-button p-2" aria-label={t('files.previewZoomIn')}><MagnifyingGlassPlusIcon className="h-4 w-4" aria-hidden="true" /></button></div><div className="overflow-auto max-h-full"><Document file={blobUrl} onLoadSuccess={({ numPages }) => { setPages(numPages); setPage(1); }} onLoadError={() => setState('fallback')}><Page pageNumber={page} scale={zoom} className="mx-auto w-fit max-w-full" /></Document></div></div>}
        </div>
      </div>
    </div>,
    document.body
  );
}
