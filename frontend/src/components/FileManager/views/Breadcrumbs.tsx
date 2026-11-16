import { useCallback, useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ChevronRightIcon, EllipsisHorizontalIcon, FolderIcon } from '../../icons';
import type { FileManagerBreadcrumb } from '../fileManagerTypes';
import { nextMenuIndex } from './menuKeyboard';

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

/** Path navigation; deep paths collapse their middle into a keyboard-navigable menu. */
export function Breadcrumbs({ breadcrumbs, onNavigate, onUp }: BreadcrumbsProps) {
  const { t } = useTranslation();
  const [menuOpen, setMenuOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  const menuRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const itemRefs = useRef<Array<HTMLButtonElement | null>>([]);

  const dismissToTrigger = useCallback(() => {
    setMenuOpen(false);
    triggerRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!menuOpen) return;
    const handlePointerDown = (event: PointerEvent | MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) setMenuOpen(false);
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        dismissToTrigger();
      }
    };
    window.addEventListener('pointerdown', handlePointerDown);
    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('pointerdown', handlePointerDown);
      window.removeEventListener('keydown', handleKeyDown);
    };
  }, [dismissToTrigger, menuOpen]);

  useEffect(() => {
    if (menuOpen) itemRefs.current[activeIndex]?.focus();
  }, [activeIndex, menuOpen]);

  const navigate = (index: number) => {
    setMenuOpen(false);
    onNavigate(index);
  };

  const collapse = breadcrumbs.length > COLLAPSE_AFTER;
  const root = breadcrumbs[0];
  const collapsed = collapse ? breadcrumbs.slice(1, -2) : [];
  const parent = collapse ? breadcrumbs[breadcrumbs.length - 2] : null;
  const current = collapse ? breadcrumbs[breadcrumbs.length - 1] : null;

  const handleMenuKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Tab') {
      event.preventDefault();
      dismissToTrigger();
      return;
    }
    const next = nextMenuIndex(event.key, activeIndex, collapsed.length);
    if (next !== null) {
      event.preventDefault();
      setActiveIndex(next);
    }
  };

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
              ref={triggerRef}
              type="button"
              onClick={() => {
                setActiveIndex(0);
                setMenuOpen((open) => !open);
              }}
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
                onKeyDown={handleMenuKeyDown}
                className="ui-section absolute left-0 top-full mt-1 z-[var(--layer-menu)] min-w-48 max-w-64 p-1"
              >
                {collapsed.map((crumb, idx) => (
                  <button
                    key={crumb.ref ?? idx + 1}
                    ref={(element) => {
                      itemRefs.current[idx] = element;
                    }}
                    type="button"
                    role="menuitem"
                    tabIndex={idx === activeIndex ? 0 : -1}
                    onFocus={() => setActiveIndex(idx)}
                    onClick={() => navigate(idx + 1)}
                    className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-hover)] hover:text-[var(--color-text-primary)] transition-colors focus-visible:outline-2 focus-visible:outline-[var(--color-focus)]"
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
