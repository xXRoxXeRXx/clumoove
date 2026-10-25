import type { CloudFile } from '../../types';

/** Orders setup-browser entries predictably: folders first, then names. */
export const sortFileBrowserEntries = (entries: CloudFile[]): CloudFile[] =>
  [...entries].sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
    return a.name.localeCompare(b.name, undefined, { sensitivity: 'base' });
  });
