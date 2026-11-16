import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import { TransferPanel } from './TransferPanel';
import type { TransferItem } from './transferItems';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

function item(overrides: Partial<TransferItem> & Pick<TransferItem, 'id' | 'origin'>): TransferItem {
  const base: TransferItem = {
    id: overrides.id,
    origin: overrides.origin,
    operation: overrides.origin === 'upload' ? 'upload' : 'copy',
    state: 'active',
    statusKey: 'running',
    subject: overrides.origin === 'upload'
      ? { type: 'file', name: 'report.txt', mimeType: 'text/plain', size: 2048 }
      : { type: 'route', source: 'Source', destination: 'Target' },
    progress: { mode: 'count', done: 1, total: 4 },
    retryable: false,
  };
  return { ...base, ...overrides };
}

const upload = (overrides: Partial<TransferItem> = {}) =>
  item({ id: 'up', origin: 'upload', statusKey: 'uploading', progress: { mode: 'bytes', done: 512, total: 2048 }, ...overrides });
const same = (overrides: Partial<TransferItem> = {}) =>
  item({ id: 'same', origin: 'same-profile', statusKey: 'copying', progress: { mode: 'pulse', done: 0, total: 1 }, ...overrides });
const cross = (overrides: Partial<TransferItem> = {}) => item({ id: 'cross', origin: 'cross-profile', ...overrides });

describe('TransferPanel', () => {
  let container: HTMLDivElement;
  let root: Root;
  const onCancel = vi.fn();
  const onDismiss = vi.fn();
  const onRetry = vi.fn();

  async function render(items: TransferItem[]) {
    await act(async () => {
      root.render(<TransferPanel items={items} onCancel={onCancel} onDismiss={onDismiss} onRetry={onRetry} />);
    });
  }

  async function expand() {
    await act(async () => {
      container.querySelector<HTMLButtonElement>('button[aria-expanded="false"]')?.click();
    });
  }

  const byLabel = (label: string) => container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
  const click = async (button: HTMLButtonElement | null) => {
    expect(button).not.toBeNull();
    await act(async () => button?.click());
  };

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    onCancel.mockReset();
    onDismiss.mockReset();
    onRetry.mockReset();
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it('renders nothing without transfers', async () => {
    await render([]);
    expect(container.querySelector('aside')).toBeNull();
  });

  it('shows a compact summary that expands into one row per transfer of every origin', async () => {
    await render([same(), cross(), upload(), upload({ id: 'done', statusKey: 'uploaded', state: 'completed' })]);
    expect(container.textContent).toContain('1/4');
    expect(container.querySelectorAll('li')).toHaveLength(0);

    await expand();
    const rows = Array.from(container.querySelectorAll('li'));
    expect(rows).toHaveLength(4);
    expect(rows[0].textContent).toContain('Copy: Source → Target');
    expect(rows[0].textContent).toContain('Copying');
    expect(rows[1].textContent).toContain('Transferring');
    expect(rows[1].textContent).toContain('1/4');
    expect(rows[2].textContent).toContain('report.txt');
    expect(rows[2].textContent).toContain('25%');
    expect(rows[2].textContent).toContain('2 KB');
  });

  it('shows the same status vocabulary for finished transfers of every origin', async () => {
    await render([
      same({ id: 'a', state: 'completed', statusKey: 'copied' }),
      cross({ id: 'b', state: 'completed', statusKey: 'completed' }),
      upload({ id: 'c', state: 'completed', statusKey: 'renamed' }),
      cross({ id: 'd', state: 'failed', statusKey: 'failed' }),
    ]);
    await expand();
    const text = Array.from(container.querySelectorAll('li')).map((li) => li.textContent);
    expect(text[0]).toContain('Copied');
    expect(text[1]).toContain('Completed');
    expect(text[2]).toContain('Uploaded with a new name');
    expect(text[3]).toContain('Failed');
  });

  it.each([
    ['upload', upload(), 'Cancel upload of report.txt'],
    ['same-profile', same(), 'Cancel transfer'],
    ['cross-profile', cross(), 'Cancel transfer'],
  ] as const)('cancels an active %s transfer by id', async (_origin, transferItem, label) => {
    await render([transferItem]);
    await expand();
    await click(byLabel(label));
    expect(onCancel).toHaveBeenCalledWith(transferItem.id);
    expect(byLabel('Dismiss item from list')).toBeNull();
  });

  it.each([
    ['upload', upload({ state: 'completed', statusKey: 'uploaded' })],
    ['same-profile', same({ state: 'completed', statusKey: 'copied' })],
    ['cross-profile', cross({ state: 'completed', statusKey: 'completed' })],
    ['failed cross-profile', cross({ state: 'failed', statusKey: 'failed' })],
    ['cancelled same-profile', same({ state: 'cancelled', statusKey: 'cancelled' })],
  ] as const)('dismisses a finished %s transfer by id', async (_name, transferItem) => {
    await render([transferItem]);
    await expand();
    await click(byLabel('Dismiss item from list'));
    expect(onDismiss).toHaveBeenCalledWith(transferItem.id);
  });

  it('offers retry only for retryable transfers', async () => {
    await render([
      upload({ id: 'bad', state: 'failed', statusKey: 'failed', retryable: true, error: 'Nope' }),
      same({ id: 'other', state: 'failed', statusKey: 'failed', error: 'Denied' }),
    ]);
    await expand();
    expect(container.querySelectorAll('button[aria-label="Retry"]')).toHaveLength(1);
    expect(container.textContent).toContain('Nope');
    expect(container.textContent).toContain('Denied');
    await click(byLabel('Retry'));
    expect(onRetry).toHaveBeenCalledWith('bad');
  });

  it.each([
    ['same-profile', same],
    ['cross-profile', cross],
  ] as const)('shows no retry button for failed or cancelled %s transfers', async (_origin, make) => {
    await render([
      make({ id: 'bad', state: 'failed', statusKey: 'failed', error: 'Denied' }),
      make({ id: 'stopped', state: 'cancelled', statusKey: 'cancelled' }),
    ]);
    await expand();
    expect(container.querySelectorAll('button[aria-label="Dismiss item from list"]')).toHaveLength(2);
    expect(byLabel('Retry')).toBeNull();
  });

  it('clears every finished transfer once nothing is in progress', async () => {
    await render([
      upload({ id: 'a', state: 'completed', statusKey: 'uploaded' }),
      same({ id: 'b', state: 'failed', statusKey: 'failed' }),
      cross({ id: 'c', state: 'completed', statusKey: 'completed' }),
    ]);
    await click(byLabel('Clear transfers'));
    expect(onDismiss.mock.calls.map((call) => call[0]).sort()).toEqual(['a', 'b', 'c']);
  });

  it('does not offer clearing while something is in progress and clears only completed transfers on request', async () => {
    await render([upload({ id: 'a', state: 'completed', statusKey: 'uploaded' }), same({ id: 'b' })]);
    expect(byLabel('Clear transfers')).toBeNull();
    await expand();
    await click(container.querySelector<HTMLButtonElement>('button[title="Clear completed"]'));
    expect(onDismiss.mock.calls.map((call) => call[0])).toEqual(['a']);
  });
});
