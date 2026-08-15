import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FileThumbnail } from './FileThumbnail';
import * as filesApi from '../../api/files';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const imageEntry: filesApi.FileEntry = {
  ref: 'ref-photo-1',
  name: 'photo.jpg',
  display_path: '/public/photo.jpg',
  kind: 'file',
  size: 2048,
  mime_type: 'image/jpeg',
  allowed_actions: ['download'],
};

const dirEntry: filesApi.FileEntry = {
  ref: 'ref-dir-1',
  name: 'photos',
  display_path: '/public/photos',
  kind: 'directory',
  size: 0,
  allowed_actions: [],
};

async function flushAsync(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20));
    });
  }
}

describe('FileThumbnail component', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    vi.stubGlobal('URL', {
      createObjectURL: vi.fn(() => 'blob:https://example.test/fake-blob-uuid'),
      revokeObjectURL: vi.fn(),
    });
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
    vi.restoreAllMocks();
  });

  it('renders standard FileIcon when thumbnails are disabled', async () => {
    act(() => {
      root.render(
        <FileThumbnail
          apiUrl="https://api.example.test"
          token="test-token"
          profileId="p1"
          entry={imageEntry}
          thumbnailsEnabled={false}
        />
      );
    });
    await flushAsync();

    expect(container.querySelector('svg')).not.toBeNull();
    expect(container.querySelector('img')).toBeNull();
  });

  it('renders standard FileIcon for directory entries regardless of capability', async () => {
    act(() => {
      root.render(
        <FileThumbnail
          apiUrl="https://api.example.test"
          token="test-token"
          profileId="p1"
          entry={dirEntry}
          thumbnailsEnabled={true}
        />
      );
    });
    await flushAsync();

    expect(container.querySelector('svg')).not.toBeNull();
    expect(container.querySelector('img')).toBeNull();
  });

  it('fetches thumbnail and displays img when thumbnails are enabled for image files', async () => {
    const mockBlob = new Blob(['image-bytes'], { type: 'image/jpeg' });
    const spy = vi.spyOn(filesApi, 'getFileThumbnail').mockResolvedValue(mockBlob);

    act(() => {
      root.render(
        <FileThumbnail
          apiUrl="https://api.example.test"
          token="test-token"
          profileId="p1"
          entry={imageEntry}
          thumbnailsEnabled={true}
          size="lg"
        />
      );
    });
    await flushAsync();

    expect(spy).toHaveBeenCalledWith(
      'https://api.example.test',
      'test-token',
      'p1',
      'ref-photo-1',
      256,
      256,
      expect.any(AbortSignal)
    );
    const img = container.querySelector('img');
    expect(img).not.toBeNull();
    expect(img?.getAttribute('src')).toBe('blob:https://example.test/fake-blob-uuid');
    expect(img?.getAttribute('alt')).toBe('photo.jpg');
  });

  it('falls back gracefully to FileIcon when thumbnail request fails', async () => {
    vi.spyOn(filesApi, 'getFileThumbnail').mockResolvedValue(null);

    act(() => {
      root.render(
        <FileThumbnail
          apiUrl="https://api.example.test"
          token="test-token"
          profileId="p1"
          entry={{ ...imageEntry, ref: 'ref-photo-failed' }}
          thumbnailsEnabled={true}
        />
      );
    });
    await flushAsync();

    expect(container.querySelector('svg')).not.toBeNull();
    expect(container.querySelector('img')).toBeNull();
  });
});
