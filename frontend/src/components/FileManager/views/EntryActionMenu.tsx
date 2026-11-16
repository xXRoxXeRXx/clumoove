import { useEffect, useRef, type ComponentType, type SVGProps } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { ArrowDownTrayIcon, ArrowRightIcon, ClipboardDocumentIcon, PencilIcon, TrashIcon } from '../../icons';
import type { EntryMenuAction } from './entryMenu';

export type EntryMenuPosition = {
  x: number;
  y: number;
  /** `right` places the menu's right edge at `x` (opened from a button), `left` its left edge (context menu). */
  align: 'left' | 'right';
};

type EntryActionMenuProps = {
  name: string;
  position: EntryMenuPosition;
  actions: EntryMenuAction[];
  onSelect: (action: EntryMenuAction) => void;
  onDismiss: () => void;
};

const MENU_WIDTH = 180;
const MENU_HEIGHT = 220;
const VIEWPORT_MARGIN = 8;

const ITEMS: Record<EntryMenuAction, { labelKey: string; Icon: ComponentType<SVGProps<SVGSVGElement>>; danger?: boolean }> = {
  rename: { labelKey: 'files.rename', Icon: PencilIcon },
  copy: { labelKey: 'files.copy', Icon: ClipboardDocumentIcon },
  move: { labelKey: 'files.move', Icon: ArrowRightIcon },
  download: { labelKey: 'files.downloadAction', Icon: ArrowDownTrayIcon },
  delete: { labelKey: 'files.deleteAction', Icon: TrashIcon, danger: true },
};

function clampToViewport({ x, y, align }: EntryMenuPosition) {
  let left = align === 'right' ? x - MENU_WIDTH : x;
  let top = y;
  if (left + MENU_WIDTH > window.innerWidth - VIEWPORT_MARGIN) left = window.innerWidth - MENU_WIDTH - VIEWPORT_MARGIN;
  if (left < VIEWPORT_MARGIN) left = VIEWPORT_MARGIN;
  if (top + MENU_HEIGHT > window.innerHeight - VIEWPORT_MARGIN) top = Math.max(VIEWPORT_MARGIN, top - MENU_HEIGHT - VIEWPORT_MARGIN);
  return { left, top };
}

/** Floating per-entry action menu, portalled to the body; dismisses on Escape, outside press and scroll. */
export function EntryActionMenu({ name, position, actions, onSelect, onDismiss }: EntryActionMenuProps) {
  const { t } = useTranslation();
  const menuRef = useRef<HTMLDivElement>(null);
  const dismissRef = useRef(onDismiss);

  useEffect(() => {
    dismissRef.current = onDismiss;
  }, [onDismiss]);

  useEffect(() => {
    const dismiss = () => dismissRef.current();
    const handlePointerDown = (event: PointerEvent | MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) dismiss();
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') dismiss();
    };
    window.addEventListener('pointerdown', handlePointerDown);
    window.addEventListener('keydown', handleKeyDown);
    window.addEventListener('scroll', dismiss, true);
    return () => {
      window.removeEventListener('pointerdown', handlePointerDown);
      window.removeEventListener('keydown', handleKeyDown);
      window.removeEventListener('scroll', dismiss, true);
    };
  }, []);

  const { left, top } = clampToViewport(position);

  return createPortal(
    <div
      ref={menuRef}
      role="menu"
      aria-label={t('files.actionsFor', { name })}
      style={{ position: 'fixed', top: `${top}px`, left: `${left}px`, zIndex: 50 }}
      className="w-44 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-primary)] p-1.5 shadow-xl ring-1 ring-black/5 dark:ring-white/10 animate-in fade-in zoom-in-95 duration-100"
      onClick={(event) => event.stopPropagation()}
    >
      {actions.map((action) => {
        const { labelKey, Icon, danger } = ITEMS[action];
        const tone = danger ? 'text-[var(--color-danger)]' : 'text-[var(--color-text-primary)]';
        return (
          <button
            key={action}
            type="button"
            role="menuitem"
            className={`flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm ${tone} hover:bg-[var(--color-hover)] transition-colors`}
            onClick={() => onSelect(action)}
          >
            <Icon className={`h-4 w-4 shrink-0 ${danger ? 'text-[var(--color-danger)]' : 'text-[var(--color-text-secondary)]'}`} aria-hidden="true" />
            <span>{t(labelKey)}</span>
          </button>
        );
      })}
    </div>,
    document.body,
  );
}
