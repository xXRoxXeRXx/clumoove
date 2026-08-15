import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { FileIcon } from './FileIcon';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

describe('FileIcon component', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
  });

  it('renders folder icon with ui-file-folder class', () => {
    act(() => {
      root.render(<FileIcon name="Photos" isDir={true} />);
    });
    const svg = container.querySelector('svg');
    expect(svg).toBeTruthy();
    expect(svg?.classList.contains('ui-file-folder')).toBe(true);
  });

  it('renders image icon with ui-file-image class', () => {
    act(() => {
      root.render(<FileIcon name="photo.png" />);
    });
    const svg = container.querySelector('svg');
    expect(svg).toBeTruthy();
    expect(svg?.classList.contains('ui-file-image')).toBe(true);
  });

  it('renders video icon with ui-file-video class', () => {
    act(() => {
      root.render(<FileIcon name="movie.mkv" />);
    });
    const svg = container.querySelector('svg');
    expect(svg).toBeTruthy();
    expect(svg?.classList.contains('ui-file-video')).toBe(true);
  });

  it('renders audio icon with ui-file-audio class', () => {
    act(() => {
      root.render(<FileIcon name="track.flac" />);
    });
    const svg = container.querySelector('svg');
    expect(svg).toBeTruthy();
    expect(svg?.classList.contains('ui-file-audio')).toBe(true);
  });

  it('renders document icon with ui-file-document class', () => {
    act(() => {
      root.render(<FileIcon name="document.docx" />);
    });
    const svg = container.querySelector('svg');
    expect(svg).toBeTruthy();
    expect(svg?.classList.contains('ui-file-document')).toBe(true);
  });

  it('renders code icon with ui-file-folder class', () => {
    act(() => {
      root.render(<FileIcon name="app.tsx" />);
    });
    const svg = container.querySelector('svg');
    expect(svg).toBeTruthy();
    expect(svg?.classList.contains('ui-file-folder')).toBe(true);
  });

  it('renders archive icon with ui-file-archive class', () => {
    act(() => {
      root.render(<FileIcon name="backup.tar.gz" />);
    });
    const svg = container.querySelector('svg');
    expect(svg).toBeTruthy();
    expect(svg?.classList.contains('ui-file-archive')).toBe(true);
  });

  it('renders default file icon with ui-file-default class', () => {
    act(() => {
      root.render(<FileIcon name="mysteryfile" />);
    });
    const svg = container.querySelector('svg');
    expect(svg).toBeTruthy();
    expect(svg?.classList.contains('ui-file-default')).toBe(true);
  });
});
