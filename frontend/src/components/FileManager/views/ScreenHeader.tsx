import { useTranslation } from 'react-i18next';
import { ArrowLeftIcon, FolderIcon } from '../../icons';

type ScreenHeaderProps = {
  /** Shows the back button when provided. */
  onBack?: () => void;
};

/** Page heading of the file manager with an optional back button. */
export function ScreenHeader({ onBack }: ScreenHeaderProps) {
  const { t } = useTranslation();
  return (
    <div className="flex items-center justify-between pb-4 border-b border-[var(--color-border)]/50">
      {onBack ? (
        <button
          type="button"
          onClick={onBack}
          className="ui-button-secondary flex items-center gap-2 px-3 py-2 text-sm font-medium hover:bg-[var(--color-bg-tertiary)]"
        >
          <ArrowLeftIcon className="w-4 h-4" aria-hidden="true" />
          {t('common.back')}
        </button>
      ) : <div />}
      <div className="flex items-center gap-2">
        <FolderIcon className="w-5 h-5 text-[var(--color-text-primary)]" aria-hidden="true" />
        <h1 id="file-manager-title" className="font-display font-semibold text-xl text-[var(--color-text-primary)] leading-none">{t('files.title')}</h1>
      </div>
    </div>
  );
}
