import React, { useEffect, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  ArchiveBoxIcon as Archive,
  CheckIcon as Check,
  ChevronDownIcon as ChevronDown,
  ChevronRightIcon as ChevronRight,
  CodeBracketIcon as FileCode,
  DocumentIcon as File,
  DocumentTextIcon as FileText,
  EyeIcon as Eye,
  FolderIcon as Folder,
  FolderOpenIcon as FolderOpen,
  RectangleStackIcon as FolderTree,
  MagnifyingGlassIcon as Search,
  PhotoIcon as ImageIcon,
  FilmIcon as Film,
  MusicalNoteIcon as Music,
  ClipboardDocumentIcon as Copy,
  Squares2X2Icon as Layers,
  ListBulletIcon as List,
  XMarkIcon as X,
} from '@heroicons/react/24/outline';

interface SelectedPathsViewerProps {
  paths?: string[];
  maxVisible?: number;
}

type PathType = 'folder' | 'image' | 'video' | 'audio' | 'document' | 'code' | 'archive' | 'file';

interface TreeNode {
  name: string;
  path: string;
  isDir: boolean;
  children: TreeNode[];
}

const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

const getPathType = (path: string): PathType => {
  if (!path) return 'file';
  if (path.endsWith('/')) return 'folder';
  
  const lastSegment = path.split('/').pop() || '';
  if (!lastSegment.includes('.')) return 'folder';

  const ext = lastSegment.split('.').pop()?.toLowerCase() || '';

  if (['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'ico', 'tiff', 'heic', 'raw', 'psd', 'ai'].includes(ext)) {
    return 'image';
  }
  if (['mp4', 'mkv', 'avi', 'mov', 'webm', 'm4v', 'flv', 'wmv', 'mpeg', '3gp'].includes(ext)) {
    return 'video';
  }
  if (['mp3', 'wav', 'flac', 'aac', 'ogg', 'm4a', 'wma', 'alac'].includes(ext)) {
    return 'audio';
  }
  if (['pdf', 'docx', 'doc', 'pptx', 'ppt', 'xlsx', 'xls', 'csv', 'md', 'txt', 'odt', 'ods', 'odp', 'rtf'].includes(ext)) {
    return 'document';
  }
  if (['js', 'ts', 'jsx', 'tsx', 'json', 'xml', 'html', 'css', 'scss', 'py', 'go', 'rs', 'java', 'c', 'cpp', 'h', 'sh', 'yaml', 'yml', 'sql', 'env'].includes(ext)) {
    return 'code';
  }
  if (['zip', 'tar', 'gz', '7z', 'rar', 'bz2', 'xz', 'iso', 'dmg'].includes(ext)) {
    return 'archive';
  }
  return 'file';
};

const getPathIcon = (type: PathType, className = "w-3.5 h-3.5 shrink-0") => {
  switch (type) {
    case 'folder':
      return <Folder className={`${className} ui-file-folder`} />;
    case 'image':
      return <ImageIcon className={`${className} ui-file-image`} />;
    case 'video':
      return <Film className={`${className} ui-file-video`} />;
    case 'audio':
      return <Music className={`${className} ui-file-audio`} />;
    case 'document':
      return <FileText className={`${className} ui-file-document`} />;
    case 'code':
      return <FileCode className={`${className} ui-file-folder`} />;
    case 'archive':
      return <Archive className={`${className} ui-file-archive`} />;
    default:
      return <File className={`${className} ui-file-default`} />;
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
        role={node.isDir ? 'button' : undefined}
        tabIndex={node.isDir ? 0 : undefined}
        className="flex items-center gap-2.5 py-1.5 px-2 hover:bg-[var(--color-bg-tertiary)] cursor-pointer transition-colors duration-150 rounded-lg group"
        style={{ paddingLeft: `${depth * 16 + 8}px` }}
        onClick={() => {
          if (node.isDir) setIsExpanded(!isExpanded);
        }}
        onKeyDown={(event) => {
          if (node.isDir && (event.key === 'Enter' || event.key === ' ')) {
            event.preventDefault();
            setIsExpanded(!isExpanded);
          }
        }}
      >
        {node.isDir ? (
          <span className="w-4 h-4 flex items-center justify-center text-[var(--color-text-muted)] group-hover:text-[var(--color-text-primary)] transition-colors">
            {isExpanded ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
          </span>
        ) : (
          <span className="w-4 h-4" />
        )}

        <span className="shrink-0">
          {node.isDir ? (
            isExpanded ? <FolderOpen className="w-4 h-4 text-[var(--color-text-secondary)]" /> : <Folder className="w-4 h-4 text-[var(--color-text-secondary)]" />
          ) : (
            getPathIcon(type, "w-4 h-4")
          )}
        </span>

        <span className="text-[11.5px] font-mono text-[var(--color-text-primary)] truncate flex-grow leading-normal py-0.5">
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
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const previousFocusRef = useRef<HTMLElement | null>(null);
  const titleId = useId();

  useEffect(() => {
    if (!isModalOpen) return;
    previousFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const focusTimer = window.setTimeout(() => closeButtonRef.current?.focus(), 0);
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setIsModalOpen(false);
        return;
      }
      if (event.key !== 'Tab' || !dialogRef.current) return;
      const focusable = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(FOCUSABLE))
        .filter((element) => !element.hasAttribute('disabled') && element.tabIndex !== -1);
      if (focusable.length === 0) {
        event.preventDefault();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      const active = document.activeElement as HTMLElement | null;
      if (event.shiftKey ? active === first || !dialogRef.current.contains(active) : active === last || !dialogRef.current.contains(active)) {
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
      }
    };
    document.addEventListener('keydown', onKeyDown, true);
    return () => {
      window.clearTimeout(focusTimer);
      document.removeEventListener('keydown', onKeyDown, true);
      if (previousFocusRef.current && document.contains(previousFocusRef.current)) previousFocusRef.current.focus();
      previousFocusRef.current = null;
    };
  }, [isModalOpen]);

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
                  className="ui-card inline-flex max-w-[200px] items-center gap-1.5 px-2.5 py-1 text-[10px] font-mono text-[var(--color-text-primary)] truncate"
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
                aria-haspopup="dialog"
                className="ui-button-secondary inline-flex items-center gap-1.5 px-2.5 py-1 text-[10px] font-medium hover:bg-[var(--color-bg-tertiary)] group"
              >
                <Eye className="w-3 h-3 text-[var(--color-text-secondary)]" />
                <span>{t('paths.moreItems', { count: hiddenCount })}</span>
              </button>
            )}
          </>
        ) : (
          <span className="ui-card inline-flex items-center gap-1.5 px-2.5 py-1 text-[10px] font-mono text-[var(--color-text-primary)]">
            <Folder className="w-3.5 h-3.5 text-[var(--color-text-secondary)] shrink-0" />
            <span>/</span>
          </span>
        )}
      </div>

      {/* Modal Dialog */}
      {isModalOpen && (
        <div 
          className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4"
          onClick={(e) => {
            if (e.target === e.currentTarget) setIsModalOpen(false);
          }}
        >
          <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby={titleId} className="ui-card flex max-h-[85vh] w-full max-w-xl flex-col overflow-hidden bg-[var(--color-bg-primary)]">
            {/* Modal Header */}
            <div className="flex items-center justify-between px-5 py-4 border-b border-[var(--color-border-light)] bg-[var(--color-bg-secondary)]">
              <div className="flex items-center gap-2.5">
                <div className="p-2 rounded-xl bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)]">
                  <Layers className="w-5 h-5" />
                </div>
                <div>
                  <h3 id={titleId} className="font-bold text-sm text-[var(--color-text-primary)]">
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
                ref={closeButtonRef}
                onClick={() => setIsModalOpen(false)}
                className="ui-button-secondary p-1.5 hover:bg-[var(--color-bg-tertiary)]"
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
                    aria-label={t('paths.searchLabel')}
                    className="ui-input w-full py-2 pl-9 pr-8 text-sm text-[var(--color-text-primary)] placeholder:text-[var(--color-text-muted)]"
                  />
                  {searchQuery && (
                    <button
                      type="button"
                      onClick={() => setSearchQuery('')}
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]"
                      aria-label={t('paths.close')}
                    >
                      <X className="w-3.5 h-3.5" />
                    </button>
                  )}
                </div>

                {/* View Mode Toggle: Tree vs List */}
                <div className="flex bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] p-1 rounded-xl shrink-0" role="group" aria-label={t('paths.viewModeLabel')}>
                  <button
                    type="button"
                    onClick={() => setViewMode('tree')}
                    aria-pressed={viewMode === 'tree'}
                    className={`p-1.5 rounded-lg transition-colors cursor-pointer ${
                      viewMode === 'tree'
                        ? 'ui-button-primary text-[var(--color-text-inverse)]'
                        : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
                    }`}
                    title={t('paths.treeView')}
                    aria-label={t('paths.treeView')}
                  >
                    <FolderTree className="w-4 h-4" />
                  </button>
                  <button
                    type="button"
                    onClick={() => setViewMode('list')}
                    aria-pressed={viewMode === 'list'}
                    className={`p-1.5 rounded-lg transition-colors cursor-pointer ${
                      viewMode === 'list'
                        ? 'ui-button-primary text-[var(--color-text-inverse)]'
                        : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
                    }`}
                    title={t('paths.listView')}
                    aria-label={t('paths.listView')}
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
                    aria-pressed={filterType === 'all'}
                    className={`px-3 py-1.5 rounded-lg font-medium transition-colors cursor-pointer ${
                      filterType === 'all'
                        ? 'ui-button-primary text-[var(--color-text-inverse)]'
                        : 'bg-[var(--color-bg-secondary)] text-[var(--color-text-muted)] hover:bg-[var(--color-bg-tertiary)]'
                    }`}
                  >
                    {t('paths.filterAll', { count: stats.total })}
                  </button>
                  <button
                    type="button"
                    onClick={() => setFilterType('folders')}
                    aria-pressed={filterType === 'folders'}
                    className={`px-3 py-1.5 rounded-lg font-medium transition-colors cursor-pointer ${
                      filterType === 'folders'
                        ? 'ui-button-primary text-[var(--color-text-inverse)]'
                        : 'bg-[var(--color-bg-secondary)] text-[var(--color-text-muted)] hover:bg-[var(--color-bg-tertiary)]'
                    }`}
                  >
                    {t('paths.filterFolders', { count: stats.folders })}
                  </button>
                  <button
                    type="button"
                    onClick={() => setFilterType('files')}
                    aria-pressed={filterType === 'files'}
                    className={`px-3 py-1.5 rounded-lg font-medium transition-colors cursor-pointer ${
                      filterType === 'files'
                        ? 'ui-button-primary text-[var(--color-text-inverse)]'
                        : 'bg-[var(--color-bg-secondary)] text-[var(--color-text-muted)] hover:bg-[var(--color-bg-tertiary)]'
                    }`}
                  >
                    {t('paths.filterFiles', { count: stats.files })}
                  </button>
                </div>
              )}
            </div>

            {/* List / Tree Body */}
            <div className="flex-1 overflow-y-auto p-4 space-y-1.5 max-h-[50vh]">
              {viewMode === 'tree' && !searchQuery.trim() ? (
                treeNodes.length > 0 ? (
                  treeNodes.map((node) => (
                    <TreeItem key={node.path} node={node} depth={0} />
                  ))
                ) : (
                  <div className="py-12 text-center text-xs text-[var(--color-text-muted)] space-y-2">
                    <Folder className="w-8 h-8 mx-auto opacity-30 text-[var(--color-text-muted)]" />
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

                      <span className="ui-badge ui-badge-muted text-[10px] font-mono font-semibold px-2 py-0.5 shrink-0">
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
                className="ui-button-secondary inline-flex items-center gap-1.5 px-3 py-2 text-sm hover:bg-[var(--color-bg-tertiary)]"
              >
                {copied ? (
                  <>
                    <Check className="w-3.5 h-3.5 text-[var(--color-success-text)]" />
                    <span className="text-[var(--color-success-text)] font-semibold">{t('paths.copied')}</span>
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
                className="ui-button-primary px-4 py-2 text-sm font-medium hover:opacity-90"
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

