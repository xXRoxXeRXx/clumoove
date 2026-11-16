import type { ComponentProps } from 'react';
import { useTranslation } from 'react-i18next';
import { LoadingIndicator } from '../../LoadingIndicator';
import { EntryGrid } from './EntryGrid';
import { EntryList } from './EntryList';
import type { EntryCollectionIntents, EntryRowModel, ListingState } from './entryRows';
import type { ViewMode } from './useViewMode';

type EntryCollectionProps = EntryCollectionIntents & {
  state: ListingState;
  rows: EntryRowModel[];
  viewMode: ViewMode;
  selection: { active: boolean; allSelected: boolean; partiallySelected: boolean; onToggleAll: () => void };
  sort: ComponentProps<typeof EntryList>['sort'];
};

const EMPTY_CLASS = 'ui-empty p-8 text-sm flex-1 flex items-center justify-center';

/** The browsing area: a message or spinner while there are no entries to show, else the list or grid. */
export function EntryCollection({ state, rows, viewMode, selection, sort, ...intents }: EntryCollectionProps) {
  const { t } = useTranslation();

  if (state === 'no-profile') return <div className={EMPTY_CLASS}>{t('files.selectProfile')}</div>;
  if (state === 'unavailable') return <p className={EMPTY_CLASS}>{t('files.listUnavailable')}</p>;
  if (state === 'loading') {
    return <div className="p-8 flex-1 flex items-center justify-center"><LoadingIndicator label={t('common.loading')} /></div>;
  }
  if (rows.length === 0) return <p className={EMPTY_CLASS}>{t('files.emptyDirectory')}</p>;
  if (viewMode === 'list') return <EntryList rows={rows} selection={selection} sort={sort} {...intents} />;
  return <EntryGrid rows={rows} selectionActive={selection.active} {...intents} />;
}
