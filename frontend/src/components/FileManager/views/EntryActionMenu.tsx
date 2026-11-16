import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { entryMenuItem, type EntryMenuAction } from './entryMenu';

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

function clampToViewport({ x, y, align }: EntryMenuPosition) {
  let left = align === 'right' ? x - MENU_WIDTH : x;
  let top = y;
  if (left + MENU_WIDTH > window.innerWidth - VIEWPORT_MARGIN) left = window.innerWidth - MENU_WIDTH - VIEWPORT_MARGIN;
  if (left < VIEWPORT_MARGIN) left = VIEWPORT_MARGIN;
  if (top + MENU_HEIGHT > window.innerHeight - VIEWPORT_MARGIN) top = Math.max(VIEWPORT_MARGIN, top - MENU_HEIGHT - VIEWPORT_MARGIN);
  return { left, top };
}

/**
 * Floating per-entry action menu, portalled to the body. Focus moves to the first item on open and
 * back to the opener on close; arrow keys, Home and End move between items. Dismisses on Escape,
 * Tab, outside press and scroll.
 */
export function EntryActionMenu({ name, position, actions, onSelect, onDismiss }: EntryActionMenuProps) {
  const { t } = useTranslation();
  const menuRef = useRef<HTMLDivElement>(null);
  const dismissRef = useRef(onDismiss);
  const itemRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const [activeIndex, setActiveIndex] = useState(0);

  useEffect(() => {
    dismissRef.current = onDismiss;
  }, [onDismiss]);

  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    return () => {
      if (opener && opener !== document.body && document.contains(opener)) opener.focus();
    };
  }, []);

  useEffect(() => {
    itemRefs.current[activeIndex]?.focus();
  }, [activeIndex]);

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

  const handleMenuKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const last = actions.length - 1;
    const target: Record<string, number> = {
      ArrowDown: activeIndex === last ? 0 : activeIndex + 1,
      ArrowUp: activeIndex === 0 ? last : activeIndex - 1,
      Home: 0,
      End: last,
    };
    if (event.key === 'Tab') {
      event.preventDefault();
      onDismiss();
    } else if (event.key in target) {
      event.preventDefault();
      setActiveIndex(target[event.key]);
    }
  };

  return createPortal(
    <div
      ref={menuRef}
      role="menu"
      aria-label={t('files.actionsFor', { name })}
      style={{ position: 'fixed', top: `${top}px`, left: `${left}px`, zIndex: 'var(--layer-menu)' }}
      className="ui-section w-44 p-1.5"
      onClick={(event) => event.stopPropagation()}
      onKeyDown={handleMenuKeyDown}
    >
      {actions.map((action, index) => {
        const { labelKey, Icon, danger } = entryMenuItem(action);
        const tone = danger ? 'text-[var(--color-danger)]' : 'text-[var(--color-text-primary)]';
        return (
          <button
            key={action}
            ref={(element) => {
              itemRefs.current[index] = element;
            }}
            type="button"
            role="menuitem"
            tabIndex={index === activeIndex ? 0 : -1}
            onFocus={() => setActiveIndex(index)}
            className={`flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm ${tone} hover:bg-[var(--color-hover)] transition-colors focus-visible:outline-2 focus-visible:outline-[var(--color-focus)]`}
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
