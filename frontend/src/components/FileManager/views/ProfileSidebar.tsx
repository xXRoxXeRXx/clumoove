import { useTranslation } from 'react-i18next';
import { ProviderIcon, WrenchScrewdriverIcon } from '../../icons';
import { LoadingIndicator } from '../../LoadingIndicator';

type ProfileSidebarProps = {
  profiles: Array<{ id: string; name: string; provider: string }>;
  activeId: string;
  loading: boolean;
  onSelect: (id: string) => void;
  onOpenManager?: () => void;
};

/** Connection picker with an optional shortcut to the connection manager. */
export function ProfileSidebar({ profiles, activeId, loading, onSelect, onOpenManager }: ProfileSidebarProps) {
  const { t } = useTranslation();
  return (
    <aside className="ui-card flex flex-col justify-between p-3 min-h-[160px] self-start lg:sticky lg:top-20 z-10" aria-label={t('files.profiles')}>
      <div>
        <div className="mb-2 px-2">
          <h2 className="text-sm font-semibold">{t('files.profiles')}</h2>
        </div>
        {loading ? (
          <div className="px-2 py-4"><LoadingIndicator label={t('common.loading')} size="sm" /></div>
        ) : profiles.length === 0 ? (
          <p className="ui-empty px-2 py-4 text-sm">{t('files.noProfiles')}</p>
        ) : (
          <ul className="space-y-1" aria-label={t('files.profiles')}>
            {profiles.map((profile) => (
              <li key={profile.id}>
                <button
                  type="button"
                  onClick={() => onSelect(profile.id)}
                  aria-current={profile.id === activeId ? 'page' : undefined}
                  className={`flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm transition-colors ${profile.id === activeId ? 'bg-[var(--color-selection-bg)] text-[var(--color-selection-text)]' : 'text-[var(--color-text-secondary)] hover:bg-[var(--color-hover)]'}`}
                >
                  <ProviderIcon provider={profile.provider} className="h-4 w-4 shrink-0" />
                  <span className="truncate font-medium">{profile.name}</span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>

      {onOpenManager && (
        <div className="mt-3 border-t border-[var(--color-border)] pt-2">
          <button
            type="button"
            onClick={onOpenManager}
            className="flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-hover)] hover:text-[var(--color-text-primary)]"
          >
            <WrenchScrewdriverIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span className="truncate">{t('files.manageProfiles')}</span>
          </button>
        </div>
      )}
    </aside>
  );
}
