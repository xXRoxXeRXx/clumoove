import React from 'react';
import { useTranslation } from 'react-i18next';

export interface ProfileSelectProps {
  profiles: { id: string; name: string; provider: string }[];
  selectedId: string;
  onSelect: (id: string) => void;
  onClear: () => void;
}

export const ProfileSelect: React.FC<ProfileSelectProps> = ({
  profiles,
  selectedId,
  onSelect,
  onClear,
}) => {
  const { t } = useTranslation();
  if (profiles.length === 0) return null;

  return (
    <div className="space-y-1.5">
      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
        {t('settings.connections.useProfile')}
      </label>
      <div className="flex gap-2">
        <select
          value={selectedId}
          onChange={(e) => onSelect(e.target.value)}
          className="flex-1 px-3 py-2 text-xs bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange transition-all font-sans"
        >
          <option value="">—</option>
          {profiles.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
        {selectedId && (
          <button
            type="button"
            onClick={onClear}
            className="px-3 py-2 rounded-xl text-[10px] font-mono border border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] transition-all cursor-pointer"
          >
            {t('common.cancel')}
          </button>
        )}
      </div>
    </div>
  );
};
