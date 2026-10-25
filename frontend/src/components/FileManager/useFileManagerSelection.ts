import { useCallback, useMemo, useState } from 'react';
import type { FileEntry } from '../../api/files';

/** Keeps entry selection semantics independent from list and grid rendering. */
export function useFileManagerSelection(entries: FileEntry[]) {
  const [selectedRefs, setSelectedRefs] = useState<Set<string>>(() => new Set());
  const resetSelection = useCallback(() => setSelectedRefs(new Set()), []);
  const toggleSelection = useCallback((ref: string) => {
    setSelectedRefs((current) => { const next = new Set(current); if (next.has(ref)) next.delete(ref); else next.add(ref); return next; });
  }, []);
  const toggleAll = useCallback(() => {
    setSelectedRefs((current) => {
      const next = new Set(current);
      const allSelected = entries.length > 0 && entries.every((entry) => next.has(entry.ref));
      entries.forEach((entry) => allSelected ? next.delete(entry.ref) : next.add(entry.ref));
      return next;
    });
  }, [entries]);
  const selectedEntries = useMemo(() => entries.filter((entry) => selectedRefs.has(entry.ref)), [entries, selectedRefs]);
  const allLoadedSelected = entries.length > 0 && entries.every((entry) => selectedRefs.has(entry.ref));
  const someLoadedSelected = entries.some((entry) => selectedRefs.has(entry.ref));
  return { selectedRefs, selectedEntries, allLoadedSelected, someLoadedSelected, isSelectionMode: selectedRefs.size > 0, resetSelection, toggleSelection, toggleAll };
}
