import type { CloudFile } from '../../types';

export type TargetTreeNode = { path: string; name: string; depth: number };

/** Projects loaded target folders into the keyboard-navigation order. */
export const buildTargetTree = (
  expanded: Record<string, boolean>,
  contents: Record<string, CloudFile[]>,
  rootName: string,
): TargetTreeNode[] => {
  const nodes: TargetTreeNode[] = [{ path: '/', name: rootName, depth: 0 }];
  const addChildren = (entries: CloudFile[], depth: number) => {
    entries.forEach((file) => {
      nodes.push({ path: file.path, name: file.name, depth });
      if (expanded[file.path]) addChildren(contents[file.path] || [], depth + 1);
    });
  };
  if (expanded['/']) addChildren(contents['/'] || [], 1);
  return nodes;
};
