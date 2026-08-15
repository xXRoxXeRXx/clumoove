import { describe, expect, it } from 'vitest';
import { getFileCategory } from './fileIcons';

describe('fileIcons utility', () => {
  describe('getFileCategory', () => {
    it('detects folders correctly', () => {
      expect(getFileCategory('Documents', undefined, true)).toBe('folder');
      expect(getFileCategory('Folder/', undefined, false)).toBe('folder');
      expect(getFileCategory('path/to/folder/', undefined, false)).toBe('folder');
    });

    it('detects categories by MIME type when provided', () => {
      expect(getFileCategory('image_without_ext', 'image/png')).toBe('image');
      expect(getFileCategory('video_without_ext', 'video/mp4; charset=binary')).toBe('video');
      expect(getFileCategory('audio_without_ext', 'audio/mpeg')).toBe('audio');
      expect(getFileCategory('doc_without_ext', 'application/pdf')).toBe('document');
      expect(getFileCategory('sheet_without_ext', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet')).toBe('document');
      expect(getFileCategory('text_without_ext', 'text/plain')).toBe('document');
      expect(getFileCategory('zip_without_ext', 'application/zip')).toBe('archive');
      expect(getFileCategory('tar_without_ext', 'application/x-tar')).toBe('archive');
      expect(getFileCategory('code_without_ext', 'application/json')).toBe('code');
      expect(getFileCategory('script_without_ext', 'application/javascript')).toBe('code');
    });

    it('falls back to file extension when MIME type is generic or empty', () => {
      expect(getFileCategory('photo.jpg', 'application/octet-stream')).toBe('image');
      expect(getFileCategory('clip.mp4', '')).toBe('video');
      expect(getFileCategory('song.mp3', null)).toBe('audio');
      expect(getFileCategory('paper.pdf', undefined)).toBe('document');
      expect(getFileCategory('notes.txt', '')).toBe('document');
      expect(getFileCategory('bundle.zip', 'application/octet-stream')).toBe('archive');
      expect(getFileCategory('main.ts', '')).toBe('code');
      expect(getFileCategory('index.html', '')).toBe('code');
    });

    it('returns file for unknown extensions without MIME type', () => {
      expect(getFileCategory('data.unknown', undefined)).toBe('file');
      expect(getFileCategory('noextension', undefined)).toBe('file');
    });
  });
});
