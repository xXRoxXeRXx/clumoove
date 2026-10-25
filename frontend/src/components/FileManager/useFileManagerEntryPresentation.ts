import { useCallback, useMemo, useState } from 'react';
import type { FileEntry } from '../../api/files';
import type { SortDirection, SortField } from './FileManagerTypes';
import { sortFileManagerEntries } from './entryOrdering';

type ViewMode = 'list' | 'grid';

/** Keeps persisted grid/list and sort preferences with the entry presentation workflow. */
export function useFileManagerEntryPresentation(entries: FileEntry[]) {
  const [viewMode, setViewMode] = useState<ViewMode>(() => {
    try { return localStorage.getItem('clumoove_file_manager_view_mode') === 'grid' ? 'grid' : 'list'; } catch { return 'list'; }
  });
  const [sortField, setSortField] = useState<SortField>(() => {
    try { const value = localStorage.getItem('clumoove_file_manager_sort_field'); if (value === 'name' || value === 'size' || value === 'modified') return value; } catch { /* ignore */ }
    return 'name';
  });
  const [sortDirection, setSortDirection] = useState<SortDirection>(() => {
    try { const value = localStorage.getItem('clumoove_file_manager_sort_dir'); if (value === 'asc' || value === 'desc') return value; } catch { /* ignore */ }
    return 'asc';
  });
  const setView = useCallback((mode: ViewMode) => { setViewMode(mode); try { localStorage.setItem('clumoove_file_manager_view_mode', mode); } catch { /* ignore */ } }, []);
  const sortBy = useCallback((field: SortField) => {
    const direction = sortField === field ? (sortDirection === 'asc' ? 'desc' : 'asc') : (field === 'name' ? 'asc' : 'desc');
    setSortField(field); setSortDirection(direction);
    try { localStorage.setItem('clumoove_file_manager_sort_field', field); localStorage.setItem('clumoove_file_manager_sort_dir', direction); } catch { /* ignore */ }
  }, [sortDirection, sortField]);
  const sortedEntries = useMemo(() => sortFileManagerEntries(entries, sortField, sortDirection), [entries, sortDirection, sortField]);
  return { viewMode, setView, sortField, sortDirection, sortBy, sortedEntries };
}
