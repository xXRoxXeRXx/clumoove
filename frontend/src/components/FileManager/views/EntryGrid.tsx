import { useTranslation } from 'react-i18next';
import { EllipsisVerticalIcon } from '../../icons';
import { entryCollectionHandlers, type EntryCollectionIntents, type EntryRowModel } from './entryRows';

type EntryGridProps = EntryCollectionIntents & {
  rows: EntryRowModel[];
  selectionActive: boolean;
};

/** Card grid of entries with selection checkboxes, native activation buttons and an actions button. */
export function EntryGrid({ rows, selectionActive, onActivate, onToggleSelect, onOpenMenu }: EntryGridProps) {
  const { t } = useTranslation();
  const { activate, openMenuFromButton, openContextMenu } = entryCollectionHandlers({ onActivate, onOpenMenu });

  return (
    <div className="p-4 grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-3 flex-1 auto-rows-max" role="grid" aria-label={t('files.title')}>
      {rows.map((row) => (
        <div key={row.ref} role="row" className="contents">
          <div
            role="gridcell"
            aria-selected={selectionActive ? row.selected : undefined}
            onClick={(event) => {
              if (event.target === event.currentTarget) activate(row.ref, event);
            }}
            onContextMenu={(event) => openContextMenu(row, event)}
            onKeyDown={(event) => {
              if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) {
                event.preventDefault();
                activate(row.ref, event);
              }
            }}
            className={`group isolate relative flex flex-col justify-between rounded-xl border transition-colors ${
              row.selected
                ? 'border-[var(--color-text-primary)] ring-2 ring-[var(--color-text-primary)]/20 bg-[var(--color-selection-bg)]/20'
                : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)] hover:bg-[var(--color-hover)] hover:border-[var(--color-border-hover,var(--color-border))]'
            } ${row.clickable ? '' : 'opacity-70'}`}
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
                  className="ui-icon-button p-1.5 rounded-lg bg-[var(--color-bg-primary)] border border-[var(--color-border)] hover:bg-[var(--color-hover)]"
                  aria-label={t('files.actionsFor', { name: row.name })}
                  title={t('files.actionsFor', { name: row.name })}
                  aria-haspopup="menu"
                  aria-expanded={row.menuOpen}
                  onClick={(event) => openMenuFromButton(row.ref, event)}
                >
                  <EllipsisVerticalIcon className="h-4 w-4" aria-hidden="true" />
                </button>
              </div>
            )}
            <button
              type="button"
              disabled={!row.clickable}
              onClick={(event) => {
                event.stopPropagation();
                activate(row.ref, event);
              }}
              className="flex w-full flex-1 flex-col text-left rounded-xl overflow-hidden cursor-pointer disabled:cursor-default focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-focus)]"
            >
              <div className="relative w-full aspect-[4/3] flex items-center justify-center bg-[var(--color-bg-tertiary)]/40 overflow-hidden">
                {row.thumbnail}
              </div>
              <div className="w-full min-w-0 p-2.5 flex flex-col text-left border-t border-[var(--color-border)]/60 bg-[var(--color-bg-secondary)]">
                <span className="w-full truncate text-xs font-medium text-[var(--color-text-primary)]">{row.name}</span>
                <span className="text-[11px] text-[var(--color-text-secondary)] mt-0.5 truncate">{row.sizeLabel ?? t('files.directory')}</span>
              </div>
            </button>
          </div>
        </div>
      ))}
    </div>
  );
}
