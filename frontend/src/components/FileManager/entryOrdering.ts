import type { FileEntry } from '../../api/files';
import type { SortDirection, SortField } from './FileManagerTypes';

const compareNames = (a: FileEntry, b: FileEntry) =>
  a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' });

/** Keeps list and grid ordering identical while the entry presentation evolves separately. */
export function sortFileManagerEntries(entries: FileEntry[], field: SortField, direction: SortDirection): FileEntry[] {
  return [...entries].sort((a, b) => {
    if (a.kind !== b.kind) return a.kind === 'directory' ? -1 : 1;
    if (field === 'name') {
      const comparison = compareNames(a, b);
      return direction === 'asc' ? comparison : -comparison;
    }
    if (field === 'size') {
      if (a.kind === 'directory') return compareNames(a, b);
      if (a.size !== b.size) return direction === 'asc' ? a.size - b.size : b.size - a.size;
      return compareNames(a, b);
    }
    const timeA = a.modified_at ? new Date(a.modified_at).getTime() : null;
    const timeB = b.modified_at ? new Date(b.modified_at).getTime() : null;
    if (timeA === null && timeB === null) {
      const comparison = compareNames(a, b);
      return direction === 'asc' ? comparison : -comparison;
    }
    if (timeA === null) return 1;
    if (timeB === null) return -1;
    if (timeA !== timeB) return direction === 'asc' ? timeA - timeB : timeB - timeA;
    return compareNames(a, b);
  });
}
