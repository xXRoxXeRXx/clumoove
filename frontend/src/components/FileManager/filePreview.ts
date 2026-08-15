import type { FileEntry } from '../../api/files';

export type PreviewKind = 'image' | 'audio' | 'video' | 'text' | 'pdf' | 'docx' | 'xlsx';

const limits: Record<PreviewKind, number> = {
  text: 2 * 1024 * 1024,
  docx: 10 * 1024 * 1024,
  xlsx: 10 * 1024 * 1024,
  image: 25 * 1024 * 1024,
  pdf: 25 * 1024 * 1024,
  audio: 50 * 1024 * 1024,
  video: 50 * 1024 * 1024,
};

const extensionKinds: Record<string, PreviewKind> = {
  png: 'image', jpg: 'image', jpeg: 'image', webp: 'image', avif: 'image', gif: 'image',
  mp3: 'audio', wav: 'audio', ogg: 'audio', m4a: 'audio', aac: 'audio', flac: 'audio',
  mp4: 'video', webm: 'video', ogv: 'video',
  txt: 'text', log: 'text', md: 'text', csv: 'text', json: 'text', xml: 'text', yaml: 'text', yml: 'text',
  js: 'text', ts: 'text', tsx: 'text', jsx: 'text', css: 'text', rtf: 'text', sql: 'text', sh: 'text', env: 'text', ini: 'text', conf: 'text',
  pdf: 'pdf', docx: 'docx', xlsx: 'xlsx', xls: 'xlsx', ods: 'xlsx', tsv: 'xlsx',
};

function extension(name: string): string {
  const index = name.lastIndexOf('.');
  return index > 0 ? name.slice(index + 1).toLowerCase() : '';
}

export function normalizedPreviewMime(value: string | null | undefined): string {
  return value?.split(';', 1)[0].trim().toLowerCase() ?? '';
}

export function isGenericPreviewMime(value: string): boolean {
  return value === '' || value === 'application/octet-stream';
}

function mimeKind(value: string): PreviewKind | null {
  if (value === 'text/html' || value === 'application/xhtml+xml' || value === 'image/svg+xml') return null;
  if (value.startsWith('image/')) return 'image';
  if (value.startsWith('audio/')) return 'audio';
  if (value.startsWith('video/')) return 'video';
  if (value === 'application/pdf') return 'pdf';
  if (value === 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' || value === 'application/msword') return 'docx';
  if (value === 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' || value === 'application/vnd.ms-excel' || value === 'application/vnd.oasis.opendocument.spreadsheet') return 'xlsx';
  if (value.startsWith('text/') || value === 'application/json' || value === 'application/xml' || value === 'application/yaml' || value === 'application/x-yaml') return 'text';
  return null;
}

export function previewKindFor(entry: FileEntry): PreviewKind | null {
  const explicit = normalizedPreviewMime(entry.mime_type);
  if (!isGenericPreviewMime(explicit)) return mimeKind(explicit);
  return extensionKinds[extension(entry.name)] ?? null;
}

export function canPreview(entry: FileEntry): boolean {
  const kind = previewKindFor(entry);
  return kind !== null && entry.size >= 0 && entry.size <= limits[kind];
}

export function previewLimit(kind: PreviewKind): number {
  return limits[kind];
}
