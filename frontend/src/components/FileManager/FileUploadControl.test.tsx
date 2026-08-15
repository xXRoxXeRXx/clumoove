import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import { FileUploadControl } from './FileUploadControl';
import { uploadFile, type FileCapabilities } from '../../api/files';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../../api/files', () => ({
  uploadFile: vi.fn(),
}));

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (err: unknown) => void;
};

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (err: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const mockFullCapabilities: FileCapabilities = {
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

function makeFile(name: string, size = 1024): File {
  const blob = new Blob(['x'.repeat(size)], { type: 'text/plain' });
  return new File([blob], name, { type: 'text/plain', lastModified: Date.now() });
}

describe('FileUploadControl component', () => {
  let container: HTMLDivElement;
  let root: Root;
  const onCompleted = vi.fn();

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    onCompleted.mockReset();
    vi.mocked(uploadFile).mockReset();
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    // Clean up portals attached to document.body
    document.querySelectorAll('[role="dialog"]').forEach((el) => el.parentElement?.remove());
    vi.restoreAllMocks();
  });

  it('selects files, configures conflict strategy, and enqueues uploads', async () => {
    vi.mocked(uploadFile).mockResolvedValue({
      ok: true,
      status: 201,
      data: { status: 'uploaded', name: 'test.txt' },
    });

    await act(async () => {
      root.render(
        <FileUploadControl
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          parentRef="ref-root"
          capabilities={mockFullCapabilities}
          onCompleted={onCompleted}
        />
      );
      await Promise.resolve();
    });

    const fileInput = container.querySelector<HTMLInputElement>('input[type="file"]');
    expect(fileInput).not.toBeNull();

    // Trigger file selection
    const testFile = makeFile('test.txt');
    await act(async () => {
      Object.defineProperty(fileInput, 'files', {
        value: [testFile],
        writable: true,
      });
      fileInput?.dispatchEvent(new Event('change', { bubbles: true }));
      await Promise.resolve();
    });

    // Conflict strategy dialog opens in portal
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.textContent).toContain('Upload conflict handling');

    // Change conflict strategy to RENAME
    const select = dialog?.querySelector('select');
    expect(select).toBeDefined();
    await act(async () => {
      select?.focus();
      if (select) {
        select.value = 'RENAME';
        select.dispatchEvent(new Event('change', { bubbles: true }));
      }
      await Promise.resolve();
    });

    // Click "Start upload" button
    const startBtn = Array.from(dialog!.querySelectorAll('button')).find((b) => b.textContent?.includes('Start upload') || b.textContent?.includes('Upload starten'));
    expect(startBtn).toBeDefined();

    await act(async () => {
      startBtn?.click();
      await Promise.resolve();
      await new Promise((r) => setTimeout(r, 50));
    });

    expect(uploadFile).toHaveBeenCalledWith(
      'https://api.example.test',
      'jwt-token',
      'profile-1',
      testFile,
      'ref-root',
      'RENAME',
      expect.any(Function),
      expect.any(AbortSignal)
    );
    expect(onCompleted).toHaveBeenCalledWith('profile-1');
  });

  it('limits concurrent uploads to 4 slots simultaneously', async () => {
    const deferredList: Deferred<{ ok: true; status: number; data: { status: 'uploaded'; name: string } }>[] = [];
    for (let i = 0; i < 6; i++) {
      deferredList.push(deferred());
    }

    let callCount = 0;
    vi.mocked(uploadFile).mockImplementation(() => {
      const idx = callCount++;
      return deferredList[idx].promise;
    });

    await act(async () => {
      root.render(
        <FileUploadControl
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          parentRef="ref-root"
          capabilities={mockFullCapabilities}
          onCompleted={onCompleted}
        />
      );
      await Promise.resolve();
    });

    const fileInput = container.querySelector<HTMLInputElement>('input[type="file"]');
    const files = [
      makeFile('file1.txt'),
      makeFile('file2.txt'),
      makeFile('file3.txt'),
      makeFile('file4.txt'),
      makeFile('file5.txt'),
      makeFile('file6.txt'),
    ];

    await act(async () => {
      Object.defineProperty(fileInput, 'files', { value: files, writable: true });
      fileInput?.dispatchEvent(new Event('change', { bubbles: true }));
      await Promise.resolve();
    });

    // Start uploads
    const dialog = document.querySelector('[role="dialog"]');
    const startBtn = Array.from(dialog!.querySelectorAll('button')).find((b) => b.textContent?.includes('Start upload') || b.textContent?.includes('Upload starten'));
    await act(async () => {
      startBtn?.click();
      await Promise.resolve();
      await new Promise((r) => setTimeout(r, 50));
    });

    // Exactly 4 uploads should have started
    expect(uploadFile).toHaveBeenCalledTimes(4);

    // Resolve file 1 -> slot 5 should start
    await act(async () => {
      deferredList[0].resolve({ ok: true, status: 201, data: { status: 'uploaded', name: 'file1.txt' } });
      await Promise.resolve();
      await new Promise((r) => setTimeout(r, 50));
    });

    expect(uploadFile).toHaveBeenCalledTimes(5);

    // Resolve file 2 -> slot 6 should start
    await act(async () => {
      deferredList[1].resolve({ ok: true, status: 201, data: { status: 'uploaded', name: 'file2.txt' } });
      await Promise.resolve();
      await new Promise((r) => setTimeout(r, 50));
    });

    expect(uploadFile).toHaveBeenCalledTimes(6);

    // Resolve remaining files
    await act(async () => {
      deferredList[2].resolve({ ok: true, status: 201, data: { status: 'uploaded', name: 'file3.txt' } });
      deferredList[3].resolve({ ok: true, status: 201, data: { status: 'uploaded', name: 'file4.txt' } });
      deferredList[4].resolve({ ok: true, status: 201, data: { status: 'uploaded', name: 'file5.txt' } });
      deferredList[5].resolve({ ok: true, status: 201, data: { status: 'uploaded', name: 'file6.txt' } });
      await Promise.resolve();
      await new Promise((r) => setTimeout(r, 50));
    });

    expect(onCompleted).toHaveBeenCalledTimes(6);
  });

  it('handles cancellation and retry of uploads', async () => {
    let capturedSignal: AbortSignal | undefined;

    vi.mocked(uploadFile).mockImplementation((_url, _token, _prof, _file, _parent, _strat, _prog, signal) => {
      capturedSignal = signal;
      return new Promise((resolve) => {
        signal?.addEventListener('abort', () => {
          resolve({ ok: false as const, status: 0, errorCode: 'ABORTED', networkError: true });
        });
      });
    });

    await act(async () => {
      root.render(
        <FileUploadControl
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          parentRef="ref-root"
          capabilities={mockFullCapabilities}
          onCompleted={onCompleted}
        />
      );
      await Promise.resolve();
    });

    const fileInput = container.querySelector<HTMLInputElement>('input[type="file"]');
    const testFile = makeFile('cancel-test.txt');

    await act(async () => {
      Object.defineProperty(fileInput, 'files', { value: [testFile], writable: true });
      fileInput?.dispatchEvent(new Event('change', { bubbles: true }));
      await Promise.resolve();
    });

    const dialog = document.querySelector('[role="dialog"]');
    const startBtn = Array.from(dialog!.querySelectorAll('button')).find((b) => b.textContent?.includes('Start upload') || b.textContent?.includes('Upload starten'));
    await act(async () => {
      startBtn?.click();
      await Promise.resolve();
      await new Promise((r) => setTimeout(r, 50));
    });

    expect(capturedSignal?.aborted).toBe(false);

    // Cancel the uploading item
    const cancelBtn = container.querySelector('button[aria-label*="cancel-test.txt"]');
    expect(cancelBtn).toBeDefined();

    await act(async () => {
      (cancelBtn as HTMLButtonElement)?.click();
      await Promise.resolve();
    });

    expect(capturedSignal?.aborted).toBe(true);
    expect(container.textContent).toContain('Cancelled');

    // Retry the cancelled item
    vi.mocked(uploadFile).mockResolvedValueOnce({
      ok: true,
      status: 201,
      data: { status: 'uploaded', name: 'cancel-test.txt' },
    });

    const retryBtn = container.querySelector('button[aria-label="Retry"], button[aria-label="Wiederholen"]');
    expect(retryBtn).toBeDefined();

    await act(async () => {
      (retryBtn as HTMLButtonElement)?.click();
      await Promise.resolve();
      await new Promise((r) => setTimeout(r, 50));
    });

    expect(container.textContent).toContain('Uploaded');
    expect(onCompleted).toHaveBeenCalledWith('profile-1');
  });

  it('aborts active upload controllers on unmount', async () => {
    let capturedSignal: AbortSignal | undefined;
    const uploadDeferred = deferred<{ ok: true; status: number; data: { status: 'uploaded'; name: string } }>();

    vi.mocked(uploadFile).mockImplementation((_url, _token, _prof, _file, _parent, _strat, _prog, signal) => {
      capturedSignal = signal;
      return uploadDeferred.promise;
    });

    await act(async () => {
      root.render(
        <FileUploadControl
          apiUrl="https://api.example.test"
          token="jwt-token"
          profileId="profile-1"
          parentRef="ref-root"
          capabilities={mockFullCapabilities}
          onCompleted={onCompleted}
        />
      );
      await Promise.resolve();
    });

    const fileInput = container.querySelector<HTMLInputElement>('input[type="file"]');
    const testFile = makeFile('unmount-test.txt');

    await act(async () => {
      Object.defineProperty(fileInput, 'files', { value: [testFile], writable: true });
      fileInput?.dispatchEvent(new Event('change', { bubbles: true }));
      await Promise.resolve();
    });

    const dialog = document.querySelector('[role="dialog"]');
    const startBtn = Array.from(dialog!.querySelectorAll('button')).find((b) => b.textContent?.includes('Start upload') || b.textContent?.includes('Upload starten'));
    await act(async () => {
      startBtn?.click();
      await Promise.resolve();
      await new Promise((r) => setTimeout(r, 50));
    });

    expect(capturedSignal?.aborted).toBe(false);

    // Unmount component
    await act(async () => {
      root.unmount();
      await Promise.resolve();
    });

    // The signal must now be aborted
    expect(capturedSignal?.aborted).toBe(true);
  });
});
