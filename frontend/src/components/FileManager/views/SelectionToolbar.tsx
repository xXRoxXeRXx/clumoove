import type { ComponentType, SVGProps } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowDownTrayIcon, ArrowsRightLeftIcon, ClipboardDocumentIcon, TrashIcon, XMarkIcon } from '../../icons';
import { STICKY_TOOLBAR_CLASS } from './toolbarStyles';

type SelectionToolbarProps = {
  count: number;
  /** A batch operation is running; every batch button waits for it. */
  busy: boolean;
  canDownloadArchive: boolean;
  canCopy: boolean;
  canMove: boolean;
  canDelete: boolean;
  onClear: () => void;
  onDownloadArchive: () => void;
  onCopy: () => void;
  onMove: () => void;
  onDelete: () => void;
};

function BatchButton({
  label,
  Icon,
  disabled,
  danger,
  onClick,
}: {
  label: string;
  Icon: ComponentType<SVGProps<SVGSVGElement>>;
  disabled: boolean;
  danger?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className={`${danger ? 'ui-button-danger' : 'ui-button-secondary'} inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0`}
      disabled={disabled}
      onClick={onClick}
      title={label}
      aria-label={label}
    >
      <Icon className="h-4 w-4 shrink-0" aria-hidden="true" />
      <span className="hidden md:inline">{label}</span>
    </button>
  );
}

/** Toolbar shown instead of the browse toolbar while entries are selected. */
export function SelectionToolbar({ count, busy, canDownloadArchive, canCopy, canMove, canDelete, onClear, onDownloadArchive, onCopy, onMove, onDelete }: SelectionToolbarProps) {
  const { t } = useTranslation();
  return (
    <div
      className={`${STICKY_TOOLBAR_CLASS} gap-2 sm:gap-3 px-2.5 sm:px-3 py-2 sm:py-2.5`}
      role="toolbar"
      aria-label={t('files.selectionActions')}
    >
      <div className="absolute inset-0 bg-[var(--color-selection-bg)]/15 pointer-events-none rounded-t-[inherit]" />
      <div className="flex items-center gap-1.5 sm:gap-2 min-w-0 relative z-10">
        <button
          type="button"
          onClick={onClear}
          className="ui-icon-button p-1.5 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] shrink-0"
          title={t('files.clearSelection')}
          aria-label={t('files.clearSelection')}
        >
          <XMarkIcon className="h-5 w-5" aria-hidden="true" />
        </button>
        <span className="text-sm font-semibold text-[var(--color-text-primary)] truncate" title={t('files.selectedCount', { count })}>
          <span className="hidden sm:inline">{t('files.selectedCount', { count })}</span>
          <span className="sm:hidden">{t('files.selectedCountShort', { count })}</span>
        </span>
      </div>

      <div className="flex items-center gap-1 sm:gap-2 shrink-0 relative z-10">
        <BatchButton label={t('files.downloadArchive')} Icon={ArrowDownTrayIcon} disabled={!canDownloadArchive || busy} onClick={onDownloadArchive} />
        <BatchButton label={t('files.copy')} Icon={ClipboardDocumentIcon} disabled={!canCopy || busy} onClick={onCopy} />
        <BatchButton label={t('files.move')} Icon={ArrowsRightLeftIcon} disabled={!canMove || busy} onClick={onMove} />
        <BatchButton label={t('files.deleteAction')} Icon={TrashIcon} disabled={!canDelete || busy} danger onClick={onDelete} />
      </div>
    </div>
  );
}
