import { describe, expect, it, vi } from 'vitest';
import type { FileEntry } from '../../../api/files';
import { listingState, toEntryRow, type EntryRowContext } from './entryRows';

const file = { ref: 'f', name: 'a.txt', kind: 'file', size: 2048, modified_at: '2026-01-01T00:00:00Z', allowed_actions: [] } as unknown as FileEntry;
const dir = { ref: 'd', name: 'docs', kind: 'directory', size: 0, modified_at: null, allowed_actions: [] } as unknown as FileEntry;

const ctx = (overrides: Partial<EntryRowContext> = {}): EntryRowContext => ({
  selectionActive: false,
  isSelected: () => false,
  isInteractive: () => true,
  canBrowse: true,
  loading: false,
  hasEntryActions: () => true,
  menuRef: null,
  formatBytes: (n) => `${n}B`,
  formatDateTime: (s) => `at ${s}`,
  renderThumbnail: vi.fn(() => 'thumb'),
  ...overrides,
});

describe('toEntryRow', () => {
  it('formats a file and leaves the size empty for folders', () => {
    expect(toEntryRow(file, ctx())).toMatchObject({ sizeLabel: '2048B', modifiedLabel: 'at 2026-01-01T00:00:00Z', isDirectory: false, thumbnail: 'thumb' });
    expect(toEntryRow(dir, ctx())).toMatchObject({ sizeLabel: null, modifiedLabel: null, isDirectory: true });
  });

  it('is clickable when interactive or while a selection is active', () => {
    expect(toEntryRow(file, ctx({ isInteractive: () => false })).clickable).toBe(false);
    expect(toEntryRow(file, ctx({ isInteractive: () => false, selectionActive: true })).clickable).toBe(true);
  });

  it('disables only the name of a folder that cannot be browsed into, never during selection', () => {
    expect(toEntryRow(dir, ctx({ canBrowse: false })).nameDisabled).toBe(true);
    expect(toEntryRow(dir, ctx({ loading: true })).nameDisabled).toBe(true);
    expect(toEntryRow(dir, ctx({ loading: true, selectionActive: true })).nameDisabled).toBe(false);
    expect(toEntryRow(file, ctx({ canBrowse: false })).nameDisabled).toBe(false);
  });

  it('reflects selection, available actions and the open menu', () => {
    const row = toEntryRow(file, ctx({ isSelected: (ref) => ref === 'f', menuRef: 'f', hasEntryActions: () => false }));
    expect(row).toMatchObject({ selected: true, menuOpen: true, hasActions: false });
  });
});

describe('listingState', () => {
  it('asks for a profile before anything else', () => {
    expect(listingState({ hasProfile: false, canBrowse: false, loading: true })).toBe('no-profile');
  });

  it('reports an unavailable listing only once loading has finished', () => {
    expect(listingState({ hasProfile: true, canBrowse: false, loading: false })).toBe('unavailable');
    expect(listingState({ hasProfile: true, canBrowse: false, loading: true })).toBe('loading');
  });

  it('is loading, then ready', () => {
    expect(listingState({ hasProfile: true, canBrowse: true, loading: true })).toBe('loading');
    expect(listingState({ hasProfile: true, canBrowse: true, loading: false })).toBe('ready');
  });
});
