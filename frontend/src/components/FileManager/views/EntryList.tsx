import { useEffect, useRef, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ChevronDownIcon, ChevronUpDownIcon, ChevronUpIcon, EllipsisVerticalIcon } from '../../icons';
import type { SortDirection, SortField } from '../useFileManagerBrowsing';
import type { EntryCollectionIntents, EntryRowModel } from './entryRows';

type EntryListProps = EntryCollectionIntents & {
  rows: EntryRowModel[];
  selection: {
    active: boolean;
    allSelected: boolean;
    partiallySelected: boolean;
    onToggleAll: () => void;
  };
  sort: {
    field: SortField;
    direction: SortDirection;
    onSort: (field: SortField) => void;
  };
};

function SortHeader({ field, label, sortLabel, sort }: { field: SortField; label: string; sortLabel: string; sort: EntryListProps['sort'] }) {
  const { t } = useTranslation();
  const isActive = sort.field === field;
  const ascending = sort.direction === 'asc';
  return (
    <button
      type="button"
      onClick={() => sort.onSort(field)}
      className="group -mx-1 -my-1 px-1 py-1 inline-flex items-center gap-1.5 font-medium hover:text-[var(--color-text-primary)] transition-colors select-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-focus)] rounded text-left"
      title={isActive ? (ascending ? t('files.sortDescending') : t('files.sortAscending')) : sortLabel}
      aria-label={`${label}: ${isActive ? (ascending ? t('files.sortAscending') : t('files.sortDescending')) : sortLabel}`}
    >
      <span className={isActive ? 'text-[var(--color-text-primary)] font-semibold' : 'text-[var(--color-text-secondary)] group-hover:text-[var(--color-text-primary)] transition-colors'}>
        {label}
      </span>
      {isActive ? (
        <span className="text-[var(--color-text-primary)] flex items-center shrink-0">
          {ascending ? (
            <ChevronUpIcon className="h-3.5 w-3.5 stroke-[2.5]" aria-hidden="true" />
          ) : (
            <ChevronDownIcon className="h-3.5 w-3.5 stroke-[2.5]" aria-hidden="true" />
          )}
        </span>
      ) : (
        <span className="opacity-0 group-hover:opacity-100 group-focus-visible:opacity-100 transition-opacity text-[var(--color-text-secondary)] flex items-center shrink-0">
          <ChevronUpDownIcon className="h-3.5 w-3.5" aria-hidden="true" />
        </span>
      )}
    </button>
  );
}

/** Sortable table of entries with selection checkboxes and a per-row actions button. */
export function EntryList({ rows, selection, sort, onActivate, onToggleSelect, onOpenMenu }: EntryListProps) {
  const { t } = useTranslation();
  const selectAllRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = selection.partiallySelected;
  }, [selection.partiallySelected]);

  const ariaSortFor = (field: SortField) => {
    if (sort.field !== field) return 'none';
    return sort.direction === 'asc' ? 'ascending' : 'descending';
  };

  const activate = (ref: string, event: MouseEvent) => onActivate(ref, { toggleSelection: event.ctrlKey || event.metaKey });

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
    <div className="overflow-x-auto flex-1">
      <table className="ui-table w-full table-fixed text-sm">
        <thead className="bg-[var(--color-bg-tertiary)] text-left text-xs text-[var(--color-text-secondary)]">
          <tr>
            <th scope="col" className="w-10 px-3 py-2 font-medium"><input ref={selectAllRef} type="checkbox" checked={selection.allSelected} onChange={selection.onToggleAll} aria-label={t('files.selectAll')} /></th>
            <th scope="col" aria-sort={ariaSortFor('name')} className="px-3 py-2 font-medium">
              <SortHeader field="name" label={t('files.name')} sortLabel={t('files.sortByName')} sort={sort} />
            </th>
            <th scope="col" aria-sort={ariaSortFor('size')} className="hidden sm:table-cell w-28 sm:w-32 px-3 py-2 font-medium whitespace-nowrap shrink-0">
              <SortHeader field="size" label={t('files.size')} sortLabel={t('files.sortBySize')} sort={sort} />
            </th>
            <th scope="col" aria-sort={ariaSortFor('modified')} className="hidden sm:table-cell w-36 sm:w-44 px-3 py-2 font-medium whitespace-nowrap shrink-0">
              <SortHeader field="modified" label={t('files.modified')} sortLabel={t('files.sortByModified')} sort={sort} />
            </th>
            <th scope="col" className="w-12 sm:w-16 px-2 sm:px-3 py-2 font-medium text-right shrink-0"><span className="sr-only">{t('files.actions')}</span></th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const sizeText = row.sizeLabel ?? t('files.directory');
            return (
              <tr
                key={row.ref}
                aria-selected={selection.active ? row.selected : undefined}
                onClick={(event) => activate(row.ref, event)}
                onContextMenu={(event) => openContextMenu(row, event)}
                className={`border-t border-[var(--color-border)] transition-colors ${
                  row.selected ? 'bg-[var(--color-selection-bg)]/20 hover:bg-[var(--color-selection-bg)]/30' : 'hover:bg-[var(--color-hover)]'
                } ${row.clickable ? 'cursor-pointer' : ''}`}
              >
                <td className="w-10 px-3 py-2"><input type="checkbox" checked={row.selected} onClick={(event) => event.stopPropagation()} onChange={() => onToggleSelect(row.ref)} aria-label={t('files.selectEntry', { name: row.name })} /></td>
                <td data-label={t('files.name')} className="px-3 py-2 min-w-0 max-w-0">
                  <button
                    type="button"
                    onClick={(event) => {
                      event.stopPropagation();
                      activate(row.ref, event);
                    }}
                    disabled={row.nameDisabled}
                    className="inline-flex w-full items-center gap-2.5 sm:gap-3 min-w-0 text-left ui-link disabled:cursor-not-allowed disabled:opacity-55"
                    title={row.name}
                  >
                    <div className="relative w-9 h-9 sm:w-10 sm:h-10 shrink-0 rounded-lg overflow-hidden bg-[var(--color-bg-tertiary)]/40 border border-[var(--color-border)]/60 flex items-center justify-center">
                      {row.thumbnail}
                    </div>
                    <div className="min-w-0 flex-1">
                      <span className="truncate block font-medium text-sm text-[var(--color-text-primary)]">{row.name}</span>
                      <div className="flex items-center gap-1.5 text-[11px] text-[var(--color-text-secondary)] sm:hidden mt-0.5 truncate font-normal">
                        <span>{sizeText}</span>
                        {row.modifiedLabel && (
                          <>
                            <span aria-hidden="true">•</span>
                            <span className="truncate">{row.modifiedLabel}</span>
                          </>
                        )}
                      </div>
                    </div>
                  </button>
                </td>
                <td data-label={t('files.size')} className="hidden sm:table-cell w-28 sm:w-32 px-3 py-2 text-[var(--color-text-secondary)] whitespace-nowrap shrink-0">
                  {sizeText}
                </td>
                <td data-label={t('files.modified')} className="hidden sm:table-cell w-36 sm:w-44 px-3 py-2 text-[var(--color-text-secondary)] whitespace-nowrap shrink-0">
                  {row.modifiedLabel ?? t('common.unspecified')}
                </td>
                <td data-label={t('files.actions')} className="w-12 sm:w-16 px-2 sm:px-3 py-2 text-right whitespace-nowrap shrink-0">
                  {row.hasActions && (
                    <button
                      type="button"
                      className="ui-icon-button p-2 hover:bg-[var(--color-hover)]"
                      aria-label={t('files.actionsFor', { name: row.name })}
                      aria-haspopup="menu"
                      aria-expanded={row.menuOpen}
                      onClick={(event) => openMenuFromButton(row.ref, event)}
                    >
                      <EllipsisVerticalIcon className="h-5 w-5" aria-hidden="true" />
                    </button>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
