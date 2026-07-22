import React from 'react';
import { useTranslation } from 'react-i18next';

export interface SaveProfileRowProps {
  idPrefix: string;
  checked: boolean;
  saveName: string;
  onSaveChange: (v: boolean) => void;
  onNameChange: (v: string) => void;
}

export const SaveProfileRow: React.FC<SaveProfileRowProps> = ({
  idPrefix,
  checked,
  saveName,
  onSaveChange,
  onNameChange,
}) => {
  const { t } = useTranslation();
  return (
    <div className="pt-1">
      <label className="flex items-center gap-2 cursor-pointer select-none">
        <input
          type="checkbox"
          id={`saveProfile-${idPrefix}`}
          checked={checked}
          onChange={(e) => onSaveChange(e.target.checked)}
          className="w-4 h-4 rounded border-[var(--color-border)] text-portal-orange focus:ring-portal-orange/30 cursor-pointer"
        />
        <span className="text-xs text-[var(--color-text-secondary)] font-medium">
          {t('settings.connections.saveAsProfile')}
        </span>
      </label>
      {checked && (
        <div className="mt-2 pl-6">
          <input
            type="text"
            id={`profileName-${idPrefix}`}
            placeholder={t('settings.connections.profileNamePlaceholder')}
            value={saveName}
            onChange={(e) => onNameChange(e.target.value)}
            className="w-full px-3 py-1.5 text-xs bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange transition-all font-sans"
          />
        </div>
      )}
    </div>
  );
};
