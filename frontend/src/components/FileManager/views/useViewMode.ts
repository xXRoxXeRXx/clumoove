import { useCallback, useState } from 'react';

export type ViewMode = 'list' | 'grid';

const STORAGE_KEY = 'clumoove_file_manager_view_mode';

function readStoredMode(): ViewMode {
  try {
    return localStorage.getItem(STORAGE_KEY) === 'grid' ? 'grid' : 'list';
  } catch {
    return 'list';
  }
}

/** List/grid preference, remembered in the browser when storage is available. */
export function useViewMode(): [ViewMode, (mode: ViewMode) => void] {
  const [mode, setMode] = useState<ViewMode>(readStoredMode);

  const update = useCallback((next: ViewMode) => {
    setMode(next);
    try {
      localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // ignore storage errors
    }
  }, []);

  return [mode, update];
}
