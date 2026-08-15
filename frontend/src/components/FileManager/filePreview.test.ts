import { describe, expect, it } from 'vitest';
import type { FileEntry } from '../../api/files';
import { canPreview, previewKindFor } from './filePreview';

function entry(name: string, mimeType?: string, size = 1024): FileEntry {
  return {
    ref: 'opaque-ref',
    name,
    display_path: '/not-exposed',
    kind: 'file',
    size,
    mime_type: mimeType,
    allowed_actions: ['download'],
  };
}

describe('file preview policy', () => {
  it('uses an explicit supported MIME type', () => {
    expect(previewKindFor(entry('download.bin', 'application/pdf'))).toBe('pdf');
  });

  it('uses an extension only when MIME is absent or generic', () => {
    expect(previewKindFor(entry('report.pdf', 'application/octet-stream'))).toBe('pdf');
    expect(previewKindFor(entry('report.pdf', 'application/zip'))).toBeNull();
  });

  it('keeps executable document types download-only', () => {
    expect(previewKindFor(entry('page.html', 'text/html'))).toBeNull();
    expect(previewKindFor(entry('image.svg', 'image/svg+xml'))).toBeNull();
  });

  it('enforces the per-format byte limit before fetching', () => {
    expect(canPreview(entry('large.txt', 'text/plain', 2 * 1024 * 1024))).toBe(true);
    expect(canPreview(entry('large.txt', 'text/plain', 2 * 1024 * 1024 + 1))).toBe(false);
  });
});
