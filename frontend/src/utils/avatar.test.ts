import { describe, expect, it } from 'vitest';
import { safeAvatarUrl } from './avatar';

describe('safeAvatarUrl', () => {
  it('allows only HTTPS URLs and accepted image data URLs', () => {
    expect(safeAvatarUrl('https://cdn.example.test/avatar.png')).toBe('https://cdn.example.test/avatar.png');
    expect(safeAvatarUrl('data:image/png;base64,iVBORw0KGgo=')).toBe('data:image/png;base64,iVBORw0KGgo=');
  });

  it.each([
    'http://cdn.example.test/avatar.png',
    'javascript:alert(1)',
    'data:image/svg+xml;base64,PHN2Zy8+',
    'data:text/html;base64,PHNjcmlwdD4=',
    'not a URL',
  ])('rejects an unsafe avatar URL: %s', (value) => {
    expect(safeAvatarUrl(value)).toBeUndefined();
  });
});
