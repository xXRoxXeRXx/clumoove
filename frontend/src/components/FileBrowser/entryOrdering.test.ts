import { describe, expect, it } from 'vitest';
import type { CloudFile } from '../../types';
import { sortFileBrowserEntries } from './entryOrdering';

const file = (name: string, is_dir: boolean): CloudFile => ({ path: `/${name}`, name, is_dir, size: 0, hash: '', last_modified: '' });

describe('sortFileBrowserEntries', () => {
  it('keeps its input unchanged while placing folders before case-insensitive names', () => {
    const entries = [file('zeta', false), file('beta', true), file('Alpha', true)];
    expect(sortFileBrowserEntries(entries).map(({ name }) => name)).toEqual(['Alpha', 'beta', 'zeta']);
    expect(entries.map(({ name }) => name)).toEqual(['zeta', 'beta', 'Alpha']);
  });
});
