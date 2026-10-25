import { describe, expect, it } from 'vitest';
import type { CloudFile } from '../../types';
import { canExpandSourceNode, resolveHierarchicalSelectionState, resolveImmichSelectionState } from './sourceSelection';

const directory = (path: string): CloudFile => ({ path, name: path.split('/').pop() || '/', is_dir: true, size: 0, hash: '', last_modified: '' });

describe('source selection rules', () => {
  it('keeps Immich library and albums mutually exclusive', () => {
    expect(resolveImmichSelectionState({ '/Albums': true, '/Albums/holiday': true }, '/Library')).toEqual({
      '/Albums': false, '/Albums/holiday': false, '/Library': true,
    });
    expect(canExpandSourceNode(directory('/Albums'), true)).toBe(true);
    expect(canExpandSourceNode(directory('/Library'), true)).toBe(false);
  });

  it('replaces an inclusive ancestor with siblings when a descendant is excluded', () => {
    const contents = {
      '/': [directory('/Documents')],
      '/Documents': [directory('/Documents/keep'), directory('/Documents/exclude')],
    };
    expect(resolveHierarchicalSelectionState({ '/Documents': true, '/Documents/exclude': true }, '/Documents/exclude', contents)).toEqual({
      '/Documents': false,
      '/Documents/keep': true,
      '/Documents/exclude': false,
    });
  });
});
