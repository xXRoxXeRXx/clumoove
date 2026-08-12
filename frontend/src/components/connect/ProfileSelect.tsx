import React from 'react';
import { useTranslation } from 'react-i18next';

export interface ProfileSelectProps {
  idPrefix: string;
  profiles: { id: string; name: string; provider: string }[];
  selectedId: string;
  onSelect: (id: string) => void;
  onClear: () => void;
}

export const ProfileSelect: React.FC<ProfileSelectProps> = ({
  idPrefix,
  profiles,
  selectedId,
  onSelect,
  onClear,
}) => {
  const { t } = useTranslation();
  if (profiles.length === 0) return null;

  return (
    <div className="space-y-1.5">
      <label htmlFor={`connection-profile-${idPrefix}`} className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
        {t('settings.connections.useProfile')}
      </label>
      <div className="flex gap-2">
        <select
          id={`connection-profile-${idPrefix}`}
          value={selectedId}
          onChange={(e) => {
            onSelect(e.target.value);
          }}
          className="ui-input flex-1 px-3 py-2 text-xs font-sans"
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
            className="ui-button-secondary px-3 py-2 text-[10px] font-mono hover:bg-[var(--color-bg-tertiary)]"
          >
            {t('common.clear')}
          </button>
        )}
      </div>
    </div>
  );
};
