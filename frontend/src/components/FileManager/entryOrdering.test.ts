import { describe, expect, it } from 'vitest';
import type { FileEntry } from '../../api/files';
import { sortFileManagerEntries } from './entryOrdering';

const entry = (name: string, kind: FileEntry['kind'], size = 0, modified_at?: string): FileEntry => ({
  ref: name, name, display_path: `/${name}`, kind, size, modified_at, allowed_actions: [],
});

describe('sortFileManagerEntries', () => {
  it('keeps directories first for every entry presentation', () => {
    expect(sortFileManagerEntries([entry('z', 'file'), entry('alpha', 'directory')], 'name', 'desc').map(({ name }) => name)).toEqual(['alpha', 'z']);
  });

  it('uses deterministic name fallbacks for equal metadata', () => {
    expect(sortFileManagerEntries([entry('z', 'file', 1), entry('a', 'file', 1)], 'size', 'desc').map(({ name }) => name)).toEqual(['a', 'z']);
  });
});
