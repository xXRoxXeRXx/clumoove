import { useTranslation } from 'react-i18next';
import { XMarkIcon } from '../../icons';

type BatchSummaryBannerProps = {
  summary: { success: number; failed: number };
  onDismiss: () => void;
};

/** Dismissible status line with the outcome of the last batch operation. */
export function BatchSummaryBanner({ summary, onDismiss }: BatchSummaryBannerProps) {
  const { t } = useTranslation();
  return (
    <div className="mx-3 mt-3 flex items-center justify-between gap-2 ui-alert px-3 py-2 text-sm" role="status">
      <span>{t('files.batchResultSummary', summary)}</span>
      <button
        type="button"
        onClick={onDismiss}
        className="ui-icon-button p-0.5 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] shrink-0"
        aria-label={t('common.cancel')}
        title={t('common.cancel')}
      >
        <XMarkIcon className="h-4 w-4" aria-hidden="true" />
      </button>
    </div>
  );
}
