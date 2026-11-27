import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useFormat } from '../utils/format';
import { listErrors, type ErrorListItem } from '../api/errors';
import { ExclamationTriangleIcon } from './icons';

interface ErrorOverviewProps {
  endpoint: string;
  token: string;
  refreshKey: number | string | null | undefined;
  onDownloadReport?: () => void;
}

const PAGE_SIZE = 20;

export function ErrorOverview({ endpoint, token, refreshKey, onDownloadReport }: ErrorOverviewProps) {
  const [items, setItems] = useState<ErrorListItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadMoreError, setLoadMoreError] = useState(false);
  const requestGenerationRef = useRef(0);
  const { t } = useTranslation();
  const { formatDateTime, formatNumber } = useFormat();

  useEffect(() => {
    let cancelled = false;
    const requestGeneration = ++requestGenerationRef.current;
    void listErrors({ endpoint, token, limit: PAGE_SIZE, offset: 0 })
      .then((result) => {
        if (cancelled || requestGeneration !== requestGenerationRef.current) return;
        if (!result.ok) {
          setItems([]);
          setTotal(0);
          return;
        }
        setItems(result.data?.errors ?? []);
        setTotal(result.data?.total ?? 0);
      })
      .finally(() => {
        if (!cancelled && requestGeneration === requestGenerationRef.current) setLoading(false);
      });
    return () => {
      cancelled = true;
      requestGenerationRef.current += 1;
    };
  }, [endpoint, token, refreshKey]);

  const loadMore = async () => {
    const requestGeneration = requestGenerationRef.current;
    setLoadingMore(true);
    setLoadMoreError(false);
    try {
      const result = await listErrors({ endpoint, token, limit: PAGE_SIZE, offset: items.length });
      if (requestGeneration !== requestGenerationRef.current) return;
      if (!result.ok) {
        setLoadMoreError(true);
        return;
      }
      setItems((current) => [...current, ...(result.data?.errors ?? [])]);
      setTotal(result.data?.total ?? 0);
    } finally {
      if (requestGeneration === requestGenerationRef.current) setLoadingMore(false);
    }
  };

  if (loading || total === 0) return null;

  return (
    <section className="ui-card overflow-hidden bg-[var(--color-error-bg)] border-[var(--color-error-border)]">
      <div className="flex items-center justify-between gap-3 border-b border-[var(--color-error-border)] px-5 py-3.5">
        <div className="flex items-center gap-2">
          <ExclamationTriangleIcon className="h-4 w-4 text-[var(--color-error-text)]" aria-hidden="true" />
          <h3 className="font-display font-bold text-xs text-[var(--color-error-text)] uppercase tracking-wider font-mono">
            {t('common.errorOverview')}
          </h3>
        </div>
        <div className="flex items-center gap-2">
          {onDownloadReport && (
            <button
              type="button"
              onClick={onDownloadReport}
              className="ui-button-secondary border-[var(--color-error-border)] px-2.5 py-1 text-xs font-bold text-[var(--color-error-text)] hover:bg-[var(--color-bg-tertiary)]"
            >
              {t('sync.downloadReport')}
            </button>
          )}
          <span aria-live="polite" className="ui-card border-[var(--color-error-border)] px-2.5 py-1 text-xs font-bold text-[var(--color-error-text)] font-mono">
            {formatNumber(total)}
          </span>
        </div>
      </div>

        <div className="m-4 overflow-x-auto border border-[var(--color-error-border)] bg-[var(--color-error-bg)]">
        <table className="ui-responsive-table w-full text-left text-xs">
          <thead className="bg-[var(--color-error-bg)] text-[11px] font-mono uppercase tracking-wider text-[var(--color-error-text)]">
            <tr>
              <th className="px-3 py-2.5 font-bold">{t('common.errorPath')}</th>
              <th className="px-3 py-2.5 font-bold">{t('common.errorType')}</th>
              <th className="px-3 py-2.5 font-bold">{t('common.errorMessage')}</th>
              <th className="px-3 py-2.5 font-bold whitespace-nowrap">{t('common.errorOccurredAt')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--color-border-light)]">
            {items.map((item) => (
              <tr key={`${item.kind}-${item.id}`} className="transition-colors hover:bg-[var(--color-bg-secondary)]/40">
                <td data-label={t('common.errorPath')} className="max-w-[220px] px-3 py-2.5 font-mono text-[var(--color-text-primary)] break-all">{item.path}</td>
                <td data-label={t('common.errorType')} className="px-3 py-2.5 whitespace-nowrap text-[var(--color-text-secondary)]">
                  {item.kind === 'indexing' ? t('common.errorTypeIndexing') : t('common.errorTypeTransfer')}
                  {item.attempts > 0 ? ` / ${t('common.errorAttempts', { count: item.attempts })}` : ''}
                </td>
                <td data-label={t('common.errorMessage')} className="max-w-[360px] px-3 py-2.5 text-[var(--color-error-text)] break-words">{item.error_message || t('common.errorNoMessage')}</td>
                <td data-label={t('common.errorOccurredAt')} className="px-3 py-2.5 whitespace-nowrap text-[var(--color-text-muted)]">{formatDateTime(item.occurred_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {items.length < total && (
        <button
          type="button"
          onClick={loadMore}
          disabled={loadingMore}
          className="ui-button-secondary mb-4 ml-4 px-3 py-2 text-sm text-[var(--color-error-text)] border-[var(--color-error-border)] hover:bg-[var(--color-bg-tertiary)] disabled:opacity-50"
        >
          {loadingMore && `${t('common.loading')} `}
          {t('common.loadMoreErrors')}
        </button>
      )}
      {loadMoreError && <p className="mx-4 mb-4 text-xs text-[var(--color-error-text)]">{t('common.loadMoreErrorsFailed')}</p>}
    </section>
  );
}
