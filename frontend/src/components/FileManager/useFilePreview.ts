import { useCallback, useEffect, useMemo, useState } from 'react';
import DOMPurify from 'dompurify';
import { createDownloadTicket, getFileThumbnailResult, type FileEntry } from '../../api/files';
import { useApiError } from '../../utils/apiError';
import { canPreview, getPreviewThumbnailDimensions, previewKindFor, previewLimit, type PreviewKind } from './filePreview';
import DocxWorker from './docxPreview.worker.ts?worker';
import XlsxWorker from './xlsxPreview.worker.ts?worker';

export type PreviewSheet = {
  name: string;
  rows: string[][];
};

export type PreviewStatus = 'loading' | 'ready' | 'fallback';

export type UseFilePreviewOptions = {
  apiUrl: string;
  token: string;
  profileId: string;
  entry: FileEntry;
  supportsThumbnails?: boolean;
};

export type FilePreviewState = {
  kind: PreviewKind | null;
  status: PreviewStatus;
  error: string;
  blobUrl: string | null;
  pdfData: Uint8Array | null;
  text: string;
  docxHtml: string;
  sheets: PreviewSheet[];
  fallback: () => void;
};

type PreviewTarget = {
  apiUrl: string;
  token: string;
  profileId: string;
  entryRef: string;
  entrySize: number;
  mimeType: string;
  supportsThumbnails: boolean;
};

type PreviewPayload = {
  error: string;
  blobUrl: string | null;
  pdfData: Uint8Array | null;
  text: string;
  docxHtml: string;
  sheets: PreviewSheet[];
};

type LoadedPreviewData = PreviewPayload & {
  target: PreviewTarget;
  status: PreviewStatus;
};

const EMPTY_PREVIEW_PAYLOAD: PreviewPayload = {
  error: '',
  blobUrl: null,
  pdfData: null,
  text: '',
  docxHtml: '',
  sheets: [],
};

function matchesPreviewTarget(a: PreviewTarget, b: PreviewTarget): boolean {
  return (Object.keys(a) as (keyof PreviewTarget)[]).every((key) => a[key] === b[key]);
}

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
  const doc = new DOMParser().parseFromString(clean, 'text/html');
  doc.querySelectorAll('img').forEach((image) => {
    if (!image.getAttribute('src')?.startsWith('data:image/')) image.remove();
  });
  return doc.body.innerHTML;
}

function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError';
}

export function useFilePreview({
  apiUrl,
  token,
  profileId,
  entry,
  supportsThumbnails,
}: UseFilePreviewOptions): FilePreviewState {
  const translateApiError = useApiError();
  const kind = previewKindFor(entry);
  const previewable = Boolean(kind && canPreview(entry));
  const entryRef = entry.ref;
  const entrySize = entry.size;
  const entryMimeType = entry.mime_type ?? '';
  const thumbnailsEnabled = Boolean(supportsThumbnails);

  const target = useMemo<PreviewTarget>(
    () => ({
      apiUrl,
      token,
      profileId,
      entryRef,
      entrySize,
      mimeType: entryMimeType,
      supportsThumbnails: thumbnailsEnabled,
    }),
    [apiUrl, entryMimeType, entryRef, entrySize, profileId, thumbnailsEnabled, token],
  );

  const [loaded, setLoaded] = useState<LoadedPreviewData | null>(null);

  useEffect(() => {
    if (!kind || !previewable) return;

    const controller = new AbortController();
    let objectUrl: string | null = null;
    let worker: Worker | null = null;

    const commit = (status: PreviewStatus, partial?: Partial<PreviewPayload>) => {
      if (controller.signal.aborted) return;
      setLoaded({
        target,
        status,
        ...EMPTY_PREVIEW_PAYLOAD,
        ...partial,
      });
    };

    const load = async () => {
      if (kind === 'image' && target.supportsThumbnails) {
        const { width, height } = getPreviewThumbnailDimensions();
        try {
          const thumbResult = await getFileThumbnailResult(
            target.apiUrl,
            target.token,
            target.profileId,
            target.entryRef,
            width,
            height,
            controller.signal,
          );
          if (controller.signal.aborted) return;
          if (thumbResult.blob && thumbResult.status === 200) {
            objectUrl = URL.createObjectURL(thumbResult.blob);
            commit('ready', { blobUrl: objectUrl });
            return;
          }
        } catch (thumbErr) {
          if (controller.signal.aborted || isAbortError(thumbErr)) return;
        }
      }

      const ticket = await createDownloadTicket(target.apiUrl, target.token, target.profileId, target.entryRef, controller.signal);
      if (controller.signal.aborted) return;
      if (ticket.ok === false) {
        commit('fallback', { error: translateApiError(ticket.errorCode) });
        return;
      }

      try {
        // Raw fetch is intentional: download_url may be a pre-signed absolute URL
        // and must omit credentials/Authorization headers rather than routing through apiFetch.
        const response = await fetch(new URL(ticket.data.download_url, target.apiUrl), {
          cache: 'no-store',
          credentials: 'omit',
          signal: controller.signal,
        });
        if (!response.ok) {
          const body = (await response.json().catch(() => ({}))) as { error_code?: string };
          commit('fallback', { error: translateApiError(body.error_code) });
          return;
        }

        const contentLength = Number(response.headers.get('Content-Length') || 0);
        if (contentLength > previewLimit(kind)) {
          commit('fallback');
          return;
        }

        const blob = await response.blob();
        if (controller.signal.aborted || blob.size > previewLimit(kind)) {
          commit('fallback');
          return;
        }

        objectUrl = URL.createObjectURL(blob);
        let text = '';
        let pdfData: Uint8Array | null = null;
        let docxHtml = '';
        let sheets: PreviewSheet[] = [];

        if (kind === 'text') {
          text = await blob.text();
        } else if (kind === 'pdf') {
          pdfData = new Uint8Array(await blob.arrayBuffer());
        } else if (kind === 'docx') {
          worker = new DocxWorker();
          const parsed = await parseWorker<{ html?: string; ok: boolean }>(worker, await blob.arrayBuffer(), controller.signal);
          docxHtml = sanitizeDocx(parsed.html ?? '');
        } else if (kind === 'xlsx') {
          worker = new XlsxWorker();
          const parsed = await parseWorker<{ sheets?: PreviewSheet[]; ok: boolean }>(worker, await blob.arrayBuffer(), controller.signal);
          sheets = parsed.sheets ?? [];
        }

        commit('ready', {
          blobUrl: objectUrl,
          text,
          pdfData,
          docxHtml,
          sheets,
        });
      } catch (loadError) {
        if (controller.signal.aborted || isAbortError(loadError)) return;
        commit('fallback');
      }
    };

    void load();

    return () => {
      controller.abort();
      worker?.terminate();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
      setLoaded((current) => (current && matchesPreviewTarget(current.target, target) ? null : current));
    };
  }, [kind, previewable, target, translateApiError]);

  const fallback = useCallback(() => {
    setLoaded((current) => ({
      target,
      ...EMPTY_PREVIEW_PAYLOAD,
      error: current && matchesPreviewTarget(current.target, target) ? current.error : '',
      status: 'fallback',
    }));
  }, [target]);

  if (!kind || !previewable) {
    return {
      kind,
      status: 'fallback',
      ...EMPTY_PREVIEW_PAYLOAD,
      fallback,
    };
  }

  const active = loaded && matchesPreviewTarget(loaded.target, target) ? loaded : null;
  const { error, blobUrl, pdfData, text, docxHtml, sheets } = active ?? EMPTY_PREVIEW_PAYLOAD;
  return {
    kind,
    status: active?.status ?? 'loading',
    error,
    blobUrl,
    pdfData,
    text,
    docxHtml,
    sheets,
    fallback,
  };
}
