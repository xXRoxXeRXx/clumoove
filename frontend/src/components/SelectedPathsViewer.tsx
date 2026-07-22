import React, { useState, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { 
  Folder, 
  FolderOpen,
  File, 
  FileText, 
  Image as ImageIcon, 
  Film, 
  Search, 
  Copy, 
  Check, 
  X, 
  Eye, 
  Layers,
  ChevronRight,
  ChevronDown,
  List,
  FolderTree
} from 'lucide-react';

interface SelectedPathsViewerProps {
  paths?: string[];
  maxVisible?: number;
}

type PathType = 'folder' | 'image' | 'video' | 'document' | 'file';

interface TreeNode {
  name: string;
  path: string;
  isDir: boolean;
  children: TreeNode[];
}

const getPathType = (path: string): PathType => {
  if (!path) return 'file';
  if (path.endsWith('/')) return 'folder';
  
  const lastSegment = path.split('/').pop() || '';
  if (!lastSegment.includes('.')) return 'folder';

  const ext = lastSegment.split('.').pop()?.toLowerCase() || '';

  if (['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'ico'].includes(ext)) {
    return 'image';
  }
  if (['mp4', 'mkv', 'avi', 'mov', 'webm', 'm4v'].includes(ext)) {
    return 'video';
  }
  if (['pdf', 'docx', 'doc', 'pptx', 'ppt', 'xlsx', 'xls', 'csv', 'md', 'txt', 'odt', 'ods'].includes(ext)) {
    return 'document';
  }
  return 'file';
};

const getPathIcon = (type: PathType, className = "w-3.5 h-3.5 shrink-0") => {
  switch (type) {
    case 'folder':
      return <Folder className={`${className} text-amber-500`} />;
    case 'image':
      return <ImageIcon className={`${className} text-purple-500`} />;
    case 'video':
      return <Film className={`${className} text-blue-500`} />;
    case 'document':
      return <FileText className={`${className} text-emerald-500`} />;
    default:
      return <File className={`${className} text-slate-400`} />;
  }
};

const buildTreeFromPaths = (paths: string[]): TreeNode[] => {
  const rootNodes: TreeNode[] = [];
  const nodeMap = new Map<string, TreeNode>();

  paths.forEach((rawPath) => {
    if (!rawPath) return;
    const cleanPath = rawPath.startsWith('/') ? rawPath : `/${rawPath}`;
    const parts = cleanPath.split('/').filter(Boolean);
    const isDir = rawPath.endsWith('/') || getPathType(rawPath) === 'folder';

    let currentPath = '';
    let parentChildren = rootNodes;

    parts.forEach((part, idx) => {
      const isLast = idx === parts.length - 1;
      currentPath += `/${part}`;
      const nodeIsDir = isLast ? isDir : true;

      let existingNode = nodeMap.get(currentPath);
      if (!existingNode) {
        existingNode = {
          name: part,
          path: currentPath + (nodeIsDir ? '/' : ''),
          isDir: nodeIsDir,
          children: [],
        };
        nodeMap.set(currentPath, existingNode);
        parentChildren.push(existingNode);
      } else if (nodeIsDir && !existingNode.isDir) {
        existingNode.isDir = true;
      }
      parentChildren = existingNode.children;
    });
  });

  const sortNodes = (nodes: TreeNode[]) => {
    nodes.sort((a, b) => {
      if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
      return a.name.localeCompare(b.name, 'de', { sensitivity: 'base' });
    });
    nodes.forEach((n) => {
      if (n.children.length > 0) sortNodes(n.children);
    });
  };

  sortNodes(rootNodes);
  return rootNodes;
};

const TreeItem: React.FC<{ node: TreeNode; depth: number }> = ({ node, depth }) => {
  const [isExpanded, setIsExpanded] = useState(true);
  const type = getPathType(node.path);

  return (
    <div className="select-none font-sans text-xs">
      <div
        className="flex items-center gap-2.5 py-1.5 px-2 hover:bg-[var(--color-bg-tertiary)] cursor-pointer transition-colors duration-150 rounded-lg group"
        style={{ paddingLeft: `${depth * 16 + 8}px` }}
        onClick={() => {
          if (node.isDir) setIsExpanded(!isExpanded);
        }}
      >
        {node.isDir ? (
          <span className="w-4 h-4 flex items-center justify-center text-[var(--color-text-muted)] group-hover:text-[var(--color-portal-navy-themed)] transition-colors">
            {isExpanded ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
          </span>
        ) : (
          <span className="w-4 h-4" />
        )}

        <span className="shrink-0">
          {node.isDir ? (
            isExpanded ? <FolderOpen className="w-4 h-4 text-amber-500" /> : <Folder className="w-4 h-4 text-amber-500" />
          ) : (
            getPathIcon(type, "w-4 h-4")
          )}
        </span>

        <span className="text-[11.5px] font-mono text-[var(--color-text-primary)] truncate flex-grow">
          {node.name}
        </span>
      </div>

      {node.isDir && isExpanded && node.children.length > 0 && (
        <div className="relative">
          <div className="absolute left-[15px] top-0 bottom-2.5 border-l border-[var(--color-border)]/60" />
          {node.children.map((child) => (
            <TreeItem key={child.path} node={child} depth={depth + 1} />
          ))}
        </div>
      )}
    </div>
  );
};

export const SelectedPathsViewer: React.FC<SelectedPathsViewerProps> = ({
  paths,
  maxVisible = 3,
}) => {
  const { t } = useTranslation();
  const [isModalOpen, setIsModalOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [filterType, setFilterType] = useState<'all' | 'folders' | 'files'>('all');
  const [viewMode, setViewMode] = useState<'tree' | 'list'>('tree');
  const [copied, setCopied] = useState(false);

  const pathList = useMemo(() => paths || [], [paths]);
  const hasPaths = pathList.length > 0;

  const treeNodes = useMemo(() => buildTreeFromPaths(pathList), [pathList]);

  const visiblePaths = useMemo(() => {
    return hasPaths ? pathList.slice(0, maxVisible) : [];
  }, [pathList, hasPaths, maxVisible]);

  const hiddenCount = hasPaths ? Math.max(0, pathList.length - maxVisible) : 0;

  const stats = useMemo(() => {
    let folders = 0;
    let files = 0;
    pathList.forEach(p => {
      if (getPathType(p) === 'folder') {
        folders++;
      } else {
        files++;
      }
    });
    return { folders, files, total: pathList.length };
  }, [pathList]);

  const filteredPaths = useMemo(() => {
    return pathList.filter(p => {
      const type = getPathType(p);
      if (filterType === 'folders' && type !== 'folder') return false;
      if (filterType === 'files' && type === 'folder') return false;
      
      if (!searchQuery.trim()) return true;
      return p.toLowerCase().includes(searchQuery.toLowerCase().trim());
    });
  }, [pathList, filterType, searchQuery]);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(pathList.join('\n'));
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Fallback if clipboard API fails
    }
  };

  return (
    <>
      <div className="flex flex-wrap items-center gap-1.5 pt-1">
        {hasPaths ? (
          <>
            {visiblePaths.map((p, idx) => {
              const type = getPathType(p);
              return (
                <span
                  key={idx}
                  className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-lg bg-white dark:bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] text-[10px] font-mono text-portal-navy shadow-2xs max-w-[200px] truncate"
                  title={p}
                >
                  {getPathIcon(type)}
                  <span className="truncate">{p}</span>
                </span>
              );
            })}

            {hiddenCount > 0 && (
              <button
                type="button"
                onClick={() => setIsModalOpen(true)}
                className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-lg bg-portal-navy/5 hover:bg-portal-navy/10 border border-portal-navy/20 text-[10px] font-medium text-portal-navy transition-colors cursor-pointer shadow-2xs group"
              >
                <Eye className="w-3 h-3 text-portal-navy/70 group-hover:scale-110 transition-transform" />
                <span>{t('paths.moreItems', { count: hiddenCount })}</span>
              </button>
            )}
          </>
        ) : (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-lg bg-white dark:bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] text-[10px] font-mono text-portal-navy shadow-2xs">
            <Folder className="w-3.5 h-3.5 text-amber-500 shrink-0" />
            <span>/</span>
          </span>
        )}
      </div>

      {/* Modal Dialog */}
      {isModalOpen && (
        <div 
          className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-xs animate-in fade-in duration-200"
          onClick={(e) => {
            if (e.target === e.currentTarget) setIsModalOpen(false);
          }}
        >
          <div className="bg-[var(--color-bg-primary)] border border-[var(--color-border)] rounded-2xl shadow-2xl max-w-xl w-full flex flex-col max-h-[85vh] overflow-hidden animate-in zoom-in-95 duration-150">
            {/* Modal Header */}
            <div className="flex items-center justify-between px-5 py-4 border-b border-[var(--color-border-light)] bg-[var(--color-bg-secondary)]">
              <div className="flex items-center gap-2.5">
                <div className="p-2 rounded-xl bg-portal-navy/10 text-portal-navy">
                  <Layers className="w-5 h-5" />
                </div>
                <div>
                  <h3 className="font-bold text-sm text-[var(--color-text-primary)]">
                    {t('paths.modalTitle', { count: stats.total })}
                  </h3>
                  <div className="flex items-center gap-2 text-xs text-[var(--color-text-muted)] mt-0.5 font-mono">
                    <span>{stats.folders} {t('paths.filterFolders', { count: stats.folders }).split(' ')[0]}</span>
                    <span>•</span>
                    <span>{stats.files} {t('paths.filterFiles', { count: stats.files }).split(' ')[0]}</span>
                  </div>
                </div>
              </div>

              <button
                type="button"
                onClick={() => setIsModalOpen(false)}
                className="p-1.5 rounded-lg text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] hover:bg-[var(--color-bg-tertiary)] transition-colors cursor-pointer"
                aria-label={t('paths.close')}
              >
                <X className="w-5 h-5" />
              </button>
            </div>

            {/* Controls: Search & View Switcher & Tabs */}
            <div className="p-4 border-b border-[var(--color-border-light)] space-y-3 bg-[var(--color-bg-primary)]">
              <div className="flex items-center gap-2">
                {/* Search input */}
                <div className="relative flex-grow">
                  <Search className="w-4 h-4 text-[var(--color-text-muted)] absolute left-3 top-1/2 -translate-y-1/2" />
                  <input
                    type="text"
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                    placeholder={t('paths.searchPlaceholder')}
                    className="w-full pl-9 pr-8 py-2 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] text-xs text-[var(--color-text-primary)] focus:outline-none focus:ring-2 focus:ring-portal-navy/30 transition-all placeholder:text-[var(--color-text-muted)]"
                  />
                  {searchQuery && (
                    <button
                      type="button"
                      onClick={() => setSearchQuery('')}
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] cursor-pointer"
                    >
                      <X className="w-3.5 h-3.5" />
                    </button>
                  )}
                </div>

                {/* View Mode Toggle: Tree vs List */}
                <div className="flex bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] p-1 rounded-xl shrink-0">
                  <button
                    type="button"
                    onClick={() => setViewMode('tree')}
                    className={`p-1.5 rounded-lg transition-colors cursor-pointer ${
                      viewMode === 'tree'
                        ? 'bg-portal-navy text-white shadow-2xs'
                        : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
                    }`}
                    title="Baumansicht"
                  >
                    <FolderTree className="w-4 h-4" />
                  </button>
                  <button
                    type="button"
                    onClick={() => setViewMode('list')}
                    className={`p-1.5 rounded-lg transition-colors cursor-pointer ${
                      viewMode === 'list'
                        ? 'bg-portal-navy text-white shadow-2xs'
                        : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
                    }`}
                    title="Listenansicht"
                  >
                    <List className="w-4 h-4" />
                  </button>
                </div>
              </div>

              {/* Filter Tabs (Visible in List Mode or when searching) */}
              {viewMode === 'list' && (
                <div className="flex items-center gap-1.5 text-xs">
                  <button
                    type="button"
                    onClick={() => setFilterType('all')}
                    className={`px-3 py-1.5 rounded-lg font-medium transition-colors cursor-pointer ${
                      filterType === 'all'
                        ? 'bg-portal-navy text-white shadow-2xs'
                        : 'bg-[var(--color-bg-secondary)] text-[var(--color-text-muted)] hover:bg-[var(--color-bg-tertiary)]'
                    }`}
                  >
                    {t('paths.filterAll', { count: stats.total })}
                  </button>
                  <button
                    type="button"
                    onClick={() => setFilterType('folders')}
                    className={`px-3 py-1.5 rounded-lg font-medium transition-colors cursor-pointer ${
                      filterType === 'folders'
                        ? 'bg-portal-navy text-white shadow-2xs'
                        : 'bg-[var(--color-bg-secondary)] text-[var(--color-text-muted)] hover:bg-[var(--color-bg-tertiary)]'
                    }`}
                  >
                    {t('paths.filterFolders', { count: stats.folders })}
                  </button>
                  <button
                    type="button"
                    onClick={() => setFilterType('files')}
                    className={`px-3 py-1.5 rounded-lg font-medium transition-colors cursor-pointer ${
                      filterType === 'files'
                        ? 'bg-portal-navy text-white shadow-2xs'
                        : 'bg-[var(--color-bg-secondary)] text-[var(--color-text-muted)] hover:bg-[var(--color-bg-tertiary)]'
                    }`}
                  >
                    {t('paths.filterFiles', { count: stats.files })}
                  </button>
                </div>
              )}
            </div>

            {/* List / Tree Body */}
            <div className="flex-1 overflow-y-auto p-4 space-y-1.5 max-h-[50vh] scrollbar-portal">
              {viewMode === 'tree' && !searchQuery.trim() ? (
                treeNodes.length > 0 ? (
                  treeNodes.map((node) => (
                    <TreeItem key={node.path} node={node} depth={0} />
                  ))
                ) : (
                  <div className="py-12 text-center text-xs text-[var(--color-text-muted)] space-y-2">
                    <Folder className="w-8 h-8 mx-auto opacity-30 text-amber-500" />
                    <p>{t('paths.noResults')}</p>
                  </div>
                )
              ) : filteredPaths.length > 0 ? (
                filteredPaths.map((p, idx) => {
                  const type = getPathType(p);
                  const isFold = type === 'folder';
                  const ext = p.includes('.') ? p.split('.').pop()?.toUpperCase() : null;

                  return (
                    <div
                      key={idx}
                      className="flex items-center justify-between gap-3 px-3 py-2 rounded-xl border border-[var(--color-border-light)] bg-[var(--color-bg-secondary)]/50 hover:bg-[var(--color-bg-secondary)] transition-colors group"
                    >
                      <div className="flex items-center gap-2.5 min-w-0 flex-1">
                        {getPathIcon(type, "w-4 h-4 shrink-0")}
                        <span className="text-xs font-mono text-[var(--color-text-primary)] truncate break-all select-all">
                          {p}
                        </span>
                      </div>

                      <span className="text-[10px] font-mono font-semibold px-2 py-0.5 rounded-md bg-black/5 dark:bg-white/10 text-[var(--color-text-muted)] shrink-0">
                        {isFold ? t('paths.folderType') : (ext || t('paths.fileType'))}
                      </span>
                    </div>
                  );
                })
              ) : (
                <div className="py-12 text-center text-xs text-[var(--color-text-muted)] space-y-2">
                  <Search className="w-8 h-8 mx-auto opacity-30" />
                  <p>{t('paths.noResults')}</p>
                </div>
              )}
            </div>

            {/* Modal Footer */}
            <div className="flex items-center justify-between px-5 py-3.5 border-t border-[var(--color-border-light)] bg-[var(--color-bg-secondary)]">
              <button
                type="button"
                onClick={handleCopy}
                className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-primary)] hover:bg-[var(--color-bg-tertiary)] text-xs font-medium text-[var(--color-text-primary)] transition-colors cursor-pointer shadow-2xs"
              >
                {copied ? (
                  <>
                    <Check className="w-3.5 h-3.5 text-emerald-500" />
                    <span className="text-emerald-600 font-semibold">{t('paths.copied')}</span>
                  </>
                ) : (
                  <>
                    <Copy className="w-3.5 h-3.5 text-[var(--color-text-muted)]" />
                    <span>{t('paths.copyAll')}</span>
                  </>
                )}
              </button>

              <button
                type="button"
                onClick={() => setIsModalOpen(false)}
                className="px-4 py-1.5 rounded-xl bg-portal-navy hover:bg-portal-navy/90 text-white text-xs font-medium transition-colors cursor-pointer shadow-2xs"
              >
                {t('paths.close')}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
};

