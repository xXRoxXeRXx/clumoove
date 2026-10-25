import { describe, expect, it } from 'vitest';
import type { CloudFile } from '../../types';
import { buildTargetTree } from './targetTree';

const folder = (path: string): CloudFile => ({ path, name: path.split('/').pop() || '/', is_dir: true, size: 0, hash: '', last_modified: '' });

describe('buildTargetTree', () => {
  it('includes only expanded loaded descendants in keyboard order', () => {
    expect(buildTargetTree({ '/': true, '/A': true }, {
      '/': [folder('/A'), folder('/B')],
      '/A': [folder('/A/child')],
    }, 'Root')).toEqual([
      { path: '/', name: 'Root', depth: 0 },
      { path: '/A', name: 'A', depth: 1 },
      { path: '/A/child', name: 'child', depth: 2 },
      { path: '/B', name: 'B', depth: 1 },
    ]);
  });
});
