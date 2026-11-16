import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ChevronRightIcon, EllipsisHorizontalIcon, FolderIcon } from '../../icons';
import type { FileManagerBreadcrumb } from '../fileManagerTypes';

type BreadcrumbsProps = {
  breadcrumbs: FileManagerBreadcrumb[];
  onNavigate: (index: number) => void;
  onUp: () => void;
};

const CRUMB_CLASS = 'max-w-28 sm:max-w-36 md:max-w-44 truncate rounded px-1 py-0.5';
const COLLAPSE_AFTER = 3;

function Separator() {
  return <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />;
}

/** Path navigation; deep paths collapse their middle into a dismissible menu. */
export function Breadcrumbs({ breadcrumbs, onNavigate, onUp }: BreadcrumbsProps) {
  const { t } = useTranslation();
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!menuOpen) return;
    const handlePointerDown = (event: PointerEvent | MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) setMenuOpen(false);
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMenuOpen(false);
    };
    window.addEventListener('pointerdown', handlePointerDown);
    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('pointerdown', handlePointerDown);
      window.removeEventListener('keydown', handleKeyDown);
    };
  }, [menuOpen]);

  const navigate = (index: number) => {
    setMenuOpen(false);
    onNavigate(index);
  };

  const collapse = breadcrumbs.length > COLLAPSE_AFTER;
  const root = breadcrumbs[0];
  const collapsed = collapse ? breadcrumbs.slice(1, -2) : [];
  const parent = collapse ? breadcrumbs[breadcrumbs.length - 2] : null;
  const current = collapse ? breadcrumbs[breadcrumbs.length - 1] : null;

  return (
    <nav className="flex min-w-0 flex-nowrap items-center gap-1 text-sm overflow-hidden" aria-label={t('files.breadcrumb')}>
      {collapse ? (
        <>
          {root && (
            <span className="inline-flex min-w-0 items-center gap-1 shrink-0">
              <button type="button" onClick={() => navigate(0)} className={`${CRUMB_CLASS} hover:bg-[var(--color-hover)]`} title={root.name}>
                {root.name}
              </button>
            </span>
          )}
          <Separator />
          <div className="relative inline-flex items-center shrink-0" ref={menuRef}>
            <button
              type="button"
              onClick={() => setMenuOpen((open) => !open)}
              aria-expanded={menuOpen}
              aria-haspopup="menu"
              aria-label={t('files.moreFolders')}
              title={t('files.moreFolders')}
              className="rounded px-1.5 py-0.5 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] transition-colors"
            >
              <EllipsisHorizontalIcon className="h-4 w-4" aria-hidden="true" />
            </button>
            {menuOpen && (
              <div
                role="menu"
                aria-label={t('files.moreFolders')}
                className="absolute left-0 top-full mt-1 z-30 min-w-48 max-w-64 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-primary)] p-1 shadow-lg backdrop-blur-xs"
              >
                {collapsed.map((crumb, idx) => (
                  <button
                    key={crumb.ref ?? idx + 1}
                    type="button"
                    role="menuitem"
                    onClick={() => navigate(idx + 1)}
                    className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-hover)] hover:text-[var(--color-text-primary)] transition-colors"
                    title={crumb.name}
                  >
                    <FolderIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />
                    <span className="truncate">{crumb.name}</span>
                  </button>
                ))}
              </div>
            )}
          </div>
          <Separator />
          {parent && (
            <span className="inline-flex min-w-0 items-center gap-1 shrink-0">
              <button type="button" onClick={onUp} className={`${CRUMB_CLASS} hover:bg-[var(--color-hover)]`} title={parent.name}>
                {parent.name}
              </button>
            </span>
          )}
          <Separator />
          {current && (
            <span className="inline-flex min-w-0 items-center gap-1 shrink-0">
              <button type="button" disabled className={`${CRUMB_CLASS} font-semibold text-[var(--color-text-primary)]`} title={current.name}>
                {current.name}
              </button>
            </span>
          )}
        </>
      ) : (
        breadcrumbs.map((breadcrumb, index) => (
          <span key={breadcrumb.ref ?? 'root'} className="inline-flex min-w-0 items-center gap-1 shrink-0">
            {index > 0 && <Separator />}
            <button
              type="button"
              onClick={() => navigate(index)}
              disabled={index === breadcrumbs.length - 1}
              className={`${CRUMB_CLASS} disabled:text-[var(--color-text-primary)] disabled:font-semibold hover:bg-[var(--color-hover)]`}
              title={breadcrumb.name}
            >
              {breadcrumb.name}
            </button>
          </span>
        ))
      )}
    </nav>
  );
}
