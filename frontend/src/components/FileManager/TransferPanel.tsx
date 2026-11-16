import { useState } from 'react';
import {
  ArrowPathIcon,
  ArrowRightIcon,
  ArrowUpTrayIcon,
  CheckCircleIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  ClipboardDocumentIcon,
  FileIcon,
  XMarkIcon,
} from '../icons';
import { useTranslation } from 'react-i18next';
import { useFormat } from '../../utils/format';
import { AUTO_DISMISS_MS } from './useTransferCenter';
import { summarizeTransfers, type TransferItem } from './transferItems';

type TransferPanelProps = {
  items: TransferItem[];
  onCancel: (itemId: string) => void;
  onDismiss: (itemId: string) => void;
  onRetry: (itemId: string) => void;
};

const iconButtonClass = 'ui-icon-button p-1 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)]';

function percentOf(item: TransferItem): number {
  const { done, total } = item.progress;
  return total > 0 ? Math.min(100, Math.round((done / total) * 100)) : 0;
}

function TransferRow({
  item,
  onCancel,
  onDismiss,
  onRetry,
}: { item: TransferItem } & Omit<TransferPanelProps, 'items'>) {
  const { t } = useTranslation();
  const { formatBytes } = useFormat();
  const isActive = item.state === 'active' || item.state === 'queued';
  const isCompleted = item.state === 'completed';
  const { subject, progress } = item;
  const operationLabel = item.operation === 'move' ? t('files.move') : t('files.copy');
  const RouteIcon = item.operation === 'move' ? ArrowRightIcon : item.origin === 'cross-profile' ? ArrowPathIcon : ClipboardDocumentIcon;
  const title = subject.type === 'file' ? subject.name : `${operationLabel}: ${subject.source} → ${subject.destination}`;
  const cancelLabel = subject.type === 'file' ? t('files.cancelUpload', { name: subject.name }) : t('files.cancelTransfer');
  const statusTone = isCompleted
    ? 'text-[var(--color-success-text)] font-medium'
    : item.state === 'failed'
      ? 'text-[var(--color-error-text)] font-semibold'
      : item.state === 'active'
        ? 'text-[var(--color-info-text)]'
        : 'text-[var(--color-text-secondary)]';

  return (
    <li className="flex items-center gap-2.5 px-2 py-2 text-sm rounded-md hover:bg-[var(--color-hover)] transition-colors">
      {isCompleted ? (
        <CheckCircleIcon className="h-5 w-5 shrink-0 text-[var(--color-success-text)]" aria-hidden="true" />
      ) : subject.type === 'file' ? (
        <FileIcon name={subject.name} mimeType={subject.mimeType} className="h-5 w-5 shrink-0" />
      ) : (
        <RouteIcon
          className={`h-5 w-5 shrink-0 ${item.state === 'active' ? `${progress.mode === 'pulse' ? 'animate-pulse' : 'animate-spin'} text-[var(--color-info-text)]` : 'text-[var(--color-text-secondary)]'}`}
          aria-hidden="true"
        />
      )}
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-2">
          <span className="truncate font-medium text-[var(--color-text-primary)] text-xs sm:text-sm" title={title}>
            {title}
          </span>
          <div className="flex items-center gap-2 shrink-0">
            {subject.type === 'file' && (
              <span className="text-xs text-[var(--color-text-secondary)]">{formatBytes(subject.size)}</span>
            )}
            {progress.mode === 'count' && (
              <span className="text-xs text-[var(--color-text-secondary)]">{progress.done}/{progress.total}</span>
            )}
            {item.state === 'active' && progress.mode === 'bytes' && (
              <span className="text-xs font-semibold text-[var(--color-info-text)]">{percentOf(item)}%</span>
            )}
            <span className={`text-xs ${statusTone}`}>{t(`files.uploadStatus.${item.statusKey}`)}</span>
          </div>
        </div>
        {item.state === 'active' && (
          <div className="h-1 w-full rounded-full bg-[var(--color-progress-track)] overflow-hidden mt-1.5">
            {progress.mode === 'pulse' ? (
              <div className="h-full bg-[var(--color-info-text)] animate-pulse w-full" />
            ) : (
              <div className="h-full bg-[var(--color-info-text)] transition-all duration-200" style={{ width: `${percentOf(item)}%` }} />
            )}
          </div>
        )}
        {isCompleted && (
          <div className="h-0.5 w-full rounded-full bg-[var(--color-progress-track)] overflow-hidden mt-1.5" aria-hidden="true">
            <div
              className="h-full bg-[var(--color-progress-success)]"
              style={{ animation: `transferCountdownDrain ${AUTO_DISMISS_MS}ms linear forwards` }}
            />
          </div>
        )}
        {item.state === 'failed' && item.error && (
          <span role="alert" className="text-xs text-[var(--color-error-text)] mt-0.5 truncate block">
            {item.error}
          </span>
        )}
      </div>
      <div className="shrink-0 flex items-center gap-1">
        {isActive && (
          <button
            type="button"
            onClick={() => onCancel(item.id)}
            className={`${iconButtonClass} hover:text-[var(--color-error-text)]`}
            aria-label={cancelLabel}
            title={cancelLabel}
          >
            <XMarkIcon className="h-4 w-4" aria-hidden="true" />
          </button>
        )}
        {item.retryable && (
          <button
            type="button"
            onClick={() => onRetry(item.id)}
            className={`${iconButtonClass} hover:text-[var(--color-text-primary)]`}
            aria-label={t('common.retry')}
            title={t('common.retry')}
          >
            <ArrowPathIcon className="h-4 w-4" aria-hidden="true" />
          </button>
        )}
        {!isActive && (
          <button
            type="button"
            onClick={() => onDismiss(item.id)}
            className={`${iconButtonClass} hover:text-[var(--color-text-primary)]`}
            aria-label={t('files.dismissTransfer')}
            title={t('files.dismissTransfer')}
          >
            <XMarkIcon className="h-4 w-4" aria-hidden="true" />
          </button>
        )}
      </div>
    </li>
  );
}

/** Floating transfer list showing uploads and same/cross-profile copy and move jobs with one row layout. */
export function TransferPanel({ items, onCancel, onDismiss, onRetry }: TransferPanelProps) {
  const { t } = useTranslation();
  const [isExpanded, setIsExpanded] = useState(false);
  if (items.length === 0) return null;

  const summary = summarizeTransfers(items);
  const canClear = summary.inProgress === 0;
  const barClass = summary.failed > 0 && summary.inProgress === 0 ? 'bg-[var(--color-progress-error)]' : 'bg-[var(--color-progress-success)]';

  const clearCompleted = () => items.filter((item) => item.state === 'completed').forEach((item) => onDismiss(item.id));
  const clearAll = () => items.forEach((item) => onDismiss(item.id));

  const clearButton = (
    <button
      type="button"
      onClick={clearAll}
      className={`${iconButtonClass} hover:text-[var(--color-text-primary)]`}
      aria-label={t('files.clearQueue')}
      title={t('files.clearQueue')}
    >
      <XMarkIcon className="h-4 w-4" aria-hidden="true" />
    </button>
  );

  return (
    <aside aria-label={t('files.uploadQueue')}>
      <style>{`
        @keyframes transferCountdownDrain {
          from { width: 100%; }
          to { width: 0%; }
        }
      `}</style>
      {!isExpanded ? (
        <div className="fixed bottom-6 left-1/2 -translate-x-1/2 z-[var(--layer-toast)] flex items-center gap-3 rounded-full border border-[var(--color-border)] bg-[var(--color-bg-secondary)]/95 px-4 py-2.5 shadow-xl backdrop-blur-md transition-all duration-200 hover:border-[var(--color-text-secondary)] select-none">
          <button
            type="button"
            onClick={() => setIsExpanded(true)}
            className="flex items-center gap-3 text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-focus)] rounded-full"
            aria-expanded={false}
            aria-label={t('files.expandQueue')}
          >
            <div className="relative flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)]">
              <ArrowUpTrayIcon className={`h-4 w-4 ${summary.inProgress > 0 ? 'animate-pulse text-[var(--color-focus)]' : ''}`} aria-hidden="true" />
            </div>
            <div className="flex flex-col gap-0.5">
              <div className="flex items-center gap-2 text-xs font-semibold text-[var(--color-text-primary)]">
                <span>{summary.completed}/{summary.total}</span>
                <span className="text-[var(--color-text-secondary)] font-normal hidden sm:inline">
                  {t('files.uploadProgressSummary', { completed: summary.completed, total: summary.total })}
                </span>
                <span className="font-mono text-[var(--color-text-secondary)]">({summary.percent}%)</span>
              </div>
              <div className="h-1.5 w-28 sm:w-36 rounded-full bg-[var(--color-progress-track)] overflow-hidden">
                <div className={`h-full transition-all duration-300 ${barClass}`} style={{ width: `${summary.percent}%` }} />
              </div>
            </div>
            <ChevronUpIcon className="h-4 w-4 shrink-0 text-[var(--color-text-secondary)]" aria-hidden="true" />
          </button>
          {canClear && clearButton}
        </div>
      ) : (
        <div className="fixed bottom-6 left-1/2 -translate-x-1/2 z-[var(--layer-toast)] w-[calc(100vw-2rem)] max-w-lg md:max-w-xl rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] shadow-2xl overflow-hidden flex flex-col">
          <div className="flex items-center justify-between gap-2 border-b border-[var(--color-border)] px-4 py-3 bg-[var(--color-bg-secondary)]">
            <div className="flex items-center gap-2 min-w-0">
              <ArrowUpTrayIcon className="h-4 w-4 shrink-0 text-[var(--color-text-primary)]" aria-hidden="true" />
              <h2 className="text-sm font-semibold text-[var(--color-text-primary)] truncate">{t('files.uploadQueue')}</h2>
              <span className="ui-badge text-xs px-2 py-0.5 font-medium bg-[var(--color-bg-tertiary)] text-[var(--color-text-secondary)]">
                {summary.completed}/{summary.total}
              </span>
            </div>
            <div className="flex items-center gap-1">
              {summary.completed > 0 && summary.inProgress > 0 && (
                <button
                  type="button"
                  onClick={clearCompleted}
                  className="text-xs px-2 py-1 rounded text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
                  title={t('files.clearCompleted')}
                >
                  {t('files.clearCompleted')}
                </button>
              )}
              <button
                type="button"
                onClick={() => setIsExpanded(false)}
                className={`${iconButtonClass} hover:text-[var(--color-text-primary)]`}
                aria-label={t('files.minimizeQueue')}
                title={t('files.minimizeQueue')}
              >
                <ChevronDownIcon className="h-4 w-4" aria-hidden="true" />
              </button>
              {canClear && clearButton}
            </div>
          </div>
          <div className="h-1 w-full bg-[var(--color-progress-track)] overflow-hidden">
            <div className={`h-full transition-all duration-300 ${barClass}`} style={{ width: `${summary.percent}%` }} />
          </div>
          <ul className="max-h-[380px] overflow-y-auto divide-y divide-[var(--color-border)]/50 p-2">
            {items.map((item) => (
              <TransferRow key={item.id} item={item} onCancel={onCancel} onDismiss={onDismiss} onRetry={onRetry} />
            ))}
          </ul>
        </div>
      )}
    </aside>
  );
}
