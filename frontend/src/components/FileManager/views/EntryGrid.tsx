import type { KeyboardEvent, MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { EllipsisVerticalIcon } from '../../icons';
import type { EntryCollectionIntents, EntryRowModel } from './entryRows';

type EntryGridProps = EntryCollectionIntents & {
  rows: EntryRowModel[];
  selectionActive: boolean;
};

/** Card grid of entries with selection checkboxes and a hover/focus actions button. */
export function EntryGrid({ rows, selectionActive, onActivate, onToggleSelect, onOpenMenu }: EntryGridProps) {
  const { t } = useTranslation();

  const activate = (ref: string, event: MouseEvent | KeyboardEvent) =>
    onActivate(ref, { toggleSelection: event.ctrlKey || event.metaKey });

  const openMenuFromButton = (ref: string, event: MouseEvent) => {
    event.stopPropagation();
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
    onOpenMenu(ref, { type: 'button', right: rect.right, bottom: rect.bottom });
  };

  const openContextMenu = (row: EntryRowModel, event: MouseEvent) => {
    if (!row.hasActions) return;
    event.preventDefault();
    event.stopPropagation();
    onOpenMenu(row.ref, { type: 'point', x: event.clientX, y: event.clientY });
  };

  return (
    <div className="p-4 grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-3 flex-1 auto-rows-max" role="grid" aria-label={t('files.title')}>
      {rows.map((row) => (
        <div
          key={row.ref}
          role="gridcell"
          aria-selected={selectionActive ? row.selected : undefined}
          tabIndex={row.clickable ? 0 : undefined}
          onClick={(event) => activate(row.ref, event)}
          onContextMenu={(event) => openContextMenu(row, event)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' || event.key === ' ') {
              event.preventDefault();
              activate(row.ref, event);
            }
          }}
          className={`group isolate relative flex flex-col justify-between rounded-xl border transition-all ${
            row.selected
              ? 'border-[var(--color-text-primary)] ring-2 ring-[var(--color-text-primary)]/20 bg-[var(--color-selection-bg)]/20 shadow-xs'
              : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)] hover:bg-[var(--color-hover)] hover:border-[var(--color-border-hover,var(--color-border))] hover:shadow-xs'
          } ${row.clickable ? 'cursor-pointer focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-focus)]' : 'opacity-70'}`}
          title={row.name}
        >
          <input
            type="checkbox"
            className="absolute left-2 top-2 z-10"
            checked={row.selected}
            onClick={(event) => event.stopPropagation()}
            onKeyDown={(event) => event.stopPropagation()}
            onChange={() => onToggleSelect(row.ref)}
            aria-label={t('files.selectEntry', { name: row.name })}
          />
          {row.hasActions && (
            <div className="absolute right-2 top-2 z-10 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
              <button
                type="button"
                className="ui-icon-button p-1.5 rounded-lg bg-[var(--color-bg-primary)]/80 backdrop-blur-xs border border-[var(--color-border)]/60 shadow-xs hover:bg-[var(--color-hover)]"
                aria-label={t('files.actionsFor', { name: row.name })}
                aria-haspopup="menu"
                aria-expanded={row.menuOpen}
                onClick={(event) => openMenuFromButton(row.ref, event)}
              >
                <EllipsisVerticalIcon className="h-4 w-4" aria-hidden="true" />
              </button>
            </div>
          )}
          <div className="relative w-full aspect-[4/3] flex items-center justify-center bg-[var(--color-bg-tertiary)]/40 overflow-hidden">
            {row.thumbnail}
          </div>
          <div className="w-full min-w-0 p-2.5 flex flex-col text-left border-t border-[var(--color-border)]/60 bg-[var(--color-bg-secondary)]">
            <span className="w-full truncate text-xs font-medium text-[var(--color-text-primary)]">{row.name}</span>
            <span className="text-[11px] text-[var(--color-text-secondary)] mt-0.5 truncate">{row.sizeLabel ?? t('files.directory')}</span>
          </div>
        </div>
      ))}
    </div>
  );
}
