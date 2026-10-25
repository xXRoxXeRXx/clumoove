import type { CloudFile } from '../../types';

export const isOneDrivePersonalVault = (file: CloudFile) =>
  file.metadata?.custom_props?.onedrive_special_folder === 'vault';

export const canExpandSourceNode = (file: CloudFile, isImmich: boolean): boolean =>
  file.is_dir && (!isImmich || file.path === '/Albums');

export const resolveImmichSelectionState = (prev: Record<string, boolean>, filePath: string): Record<string, boolean> => {
  const willBeSelected = !prev[filePath];
  const next = { ...prev, [filePath]: willBeSelected };
  if (filePath === '/Library' && willBeSelected) {
    next['/Albums'] = false;
    Object.keys(next).filter((key) => key.startsWith('/Albums/')).forEach((key) => { next[key] = false; });
  } else if (filePath === '/Albums') {
    if (willBeSelected) next['/Library'] = false;
    Object.keys(next).filter((key) => key.startsWith('/Albums/')).forEach((key) => { next[key] = willBeSelected; });
  } else if (filePath.startsWith('/Albums/')) {
    if (willBeSelected) next['/Library'] = false;
    else next['/Albums'] = false;
  }
  return next;
};

const parentPath = (path: string): string | null => {
  const separator = path.lastIndexOf('/');
  return separator <= 0 ? null : path.slice(0, separator);
};

/** Preserves the API's inclusive-directory contract when a descendant is excluded. */
export const resolveHierarchicalSelectionState = (prev: Record<string, boolean>, filePath: string, directoryContents: Record<string, CloudFile[]>): Record<string, boolean> => {
  const next = { ...prev };
  const setLoadedSubtree = (path: string, selected: boolean) => {
    next[path] = selected;
    for (const child of directoryContents[path] || []) {
      if (isOneDrivePersonalVault(child)) next[child.path] = false;
      else setLoadedSubtree(child.path, selected);
    }
  };
  if (!prev[filePath]) { setLoadedSubtree(filePath, true); return next; }
  setLoadedSubtree(filePath, false);
  let excludedPath = filePath;
  let ancestorPath = parentPath(filePath);
  while (ancestorPath !== null) {
    if (prev[ancestorPath]) for (const sibling of directoryContents[ancestorPath] || []) {
      if (sibling.path !== excludedPath && !isOneDrivePersonalVault(sibling)) setLoadedSubtree(sibling.path, true);
    }
    next[ancestorPath] = false;
    excludedPath = ancestorPath;
    ancestorPath = parentPath(ancestorPath);
  }
  return next;
};
