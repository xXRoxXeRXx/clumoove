import { act } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { FileCapabilities } from '../../api/files';
import { FileUploadControl } from './FileUploadControl';
import { useRenderHarness } from './views/renderHarness';

const fullCapabilities: FileCapabilities = {
  browse: true,
  native_pagination: false,
  download: true,
  upload: true,
  mkdir: false,
  rename: false,
  move: false,
  delete_file: false,
  delete_empty_directory: false,
  delete_recursive_directory: false,
  conflict_skip: true,
  conflict_overwrite: true,
  conflict_overwrite_atomic: false,
  conflict_rename: true,
  native_copy: false,
  range_download: false,
  thumbnails: false,
};

const makeFile = (name: string) => new File(['x'], name, { type: 'text/plain' });

describe('FileUploadControl picker', () => {
  const view = useRenderHarness();
  const onUpload = vi.fn();

  const renderControl = (props: Partial<{ capabilities: FileCapabilities; disabled: boolean }> = {}) =>
    view.render(<FileUploadControl capabilities={props.capabilities ?? fullCapabilities} disabled={props.disabled} onUpload={onUpload} />);

  const fileInput = () => view.container.querySelector<HTMLInputElement>('input[type="file"]')!;
  const dialog = () => document.querySelector('[role="dialog"]');

  async function choose(files: File[]) {
    await act(async () => {
      Object.defineProperty(fileInput(), 'files', { value: files, configurable: true });
      fileInput().dispatchEvent(new Event('change', { bubbles: true }));
    });
  }

  const button = (label: string) =>
    Array.from(dialog()!.querySelectorAll('button')).find((b) => b.textContent?.includes(label)) ?? null;

  beforeEach(() => onUpload.mockReset());

  it('asks for a conflict strategy and hands the chosen files and strategy to the caller', async () => {
    await renderControl();
    const file = makeFile('test.txt');
    await choose([file]);
    expect(dialog()?.textContent).toContain('Upload conflict handling');

    const select = dialog()!.querySelector('select')!;
    await act(async () => {
      select.value = 'RENAME';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await view.click(button('Start upload'));

    expect(onUpload).toHaveBeenCalledWith([file], 'RENAME');
    expect(dialog()).toBeNull();
  });

  it('defaults to SKIP and offers only the strategies the connection supports', async () => {
    await renderControl({ capabilities: { ...fullCapabilities, conflict_overwrite: false } });
    await choose([makeFile('a.txt')]);
    expect(Array.from(dialog()!.querySelectorAll('option')).map((o) => o.value)).toEqual(['SKIP', 'RENAME']);
    await view.click(button('Start upload'));
    expect(onUpload).toHaveBeenCalledWith(expect.any(Array), 'SKIP');
  });

  it('warns when the connection cannot overwrite atomically', async () => {
    await renderControl();
    await choose([makeFile('a.txt')]);
    const select = dialog()!.querySelector('select')!;
    await act(async () => {
      select.value = 'OVERWRITE';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    expect(dialog()!.textContent).toContain('cannot overwrite files atomically');
  });

  it('cancels the pending selection with the Cancel button and with Escape', async () => {
    await renderControl();
    await choose([makeFile('a.txt')]);
    await view.click(button('Cancel'));
    expect(dialog()).toBeNull();

    await choose([makeFile('b.txt')]);
    expect(dialog()).not.toBeNull();
    act(() => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(dialog()).toBeNull();
    expect(onUpload).not.toHaveBeenCalled();
  });

  it('exposes a modal dialog and moves focus into it', async () => {
    await renderControl();
    await choose([makeFile('a.txt')]);
    expect(dialog()!.getAttribute('aria-modal')).toBe('true');
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 5));
    });
    expect(dialog()!.contains(document.activeElement)).toBe(true);
  });

  it('accepts dropped files', async () => {
    await renderControl();
    const file = makeFile('dropped.txt');
    const drop = new Event('drop', { bubbles: true, cancelable: true });
    Object.defineProperty(drop, 'dataTransfer', { value: { files: [file] } });
    await act(async () => {
      fileInput().parentElement!.dispatchEvent(drop);
    });
    expect(dialog()?.textContent).toContain('dropped.txt');
  });

  it('ignores selection and drops when uploads are unavailable or disabled', async () => {
    await renderControl({ capabilities: { ...fullCapabilities, upload: false } });
    expect(fileInput().disabled).toBe(true);
    expect(view.container.querySelector<HTMLButtonElement>('button')!.disabled).toBe(true);
    await choose([makeFile('a.txt')]);
    expect(dialog()).toBeNull();

    await renderControl({ disabled: true });
    await choose([makeFile('a.txt')]);
    expect(dialog()).toBeNull();
  });
});
