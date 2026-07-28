import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useFormat } from '../utils/format';
import { apiFetch } from '../utils/apiClient';
import { ExclamationTriangleIcon } from '@heroicons/react/24/outline';

interface ErrorListItem {
  id: string;
  kind: 'transfer' | 'indexing';
  resource_type: string;
  path: string;
  status: string;
  attempts: number;
  error_message: string;
  occurred_at: string;
}

interface ErrorListResponse {
  errors: ErrorListItem[];
  total: number;
}

interface ErrorOverviewProps {
  endpoint: string;
  token: string;
  refreshKey: number | string | null | undefined;
}

const PAGE_SIZE = 20;

export function ErrorOverview({ endpoint, token, refreshKey }: ErrorOverviewProps) {
  const [items, setItems] = useState<ErrorListItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadMoreError, setLoadMoreError] = useState(false);
  const { t } = useTranslation();
  const { formatDateTime } = useFormat();

  useEffect(() => {
    let cancelled = false;
    void apiFetch(`${endpoint}?limit=${PAGE_SIZE}&offset=0`, {
      headers: { Authorization: `Bearer ${token}` },
    })
      .then(async (response) => {
        if (!response.ok) throw new Error('error list request failed');
        return response.json() as Promise<ErrorListResponse>;
      })
      .then((data) => {
        if (cancelled) return;
        setItems(data.errors);
        setTotal(data.total);
      })
      .catch(() => {
        if (!cancelled) {
          setItems([]);
          setTotal(0);
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [endpoint, token, refreshKey]);

  const loadMore = async () => {
    setLoadingMore(true);
    setLoadMoreError(false);
    try {
      const response = await apiFetch(`${endpoint}?limit=${PAGE_SIZE}&offset=${items.length}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!response.ok) {
        setLoadMoreError(true);
        return;
      }
      const data = await response.json() as ErrorListResponse;
      setItems((current) => [...current, ...data.errors]);
      setTotal(data.total);
    } catch {
      setLoadMoreError(true);
    } finally {
      setLoadingMore(false);
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
        <span className="ui-card border-[var(--color-error-border)] px-2.5 py-1 text-xs font-bold text-[var(--color-error-text)] font-mono">
          {total}
        </span>
      </div>

        <div className="ui-card m-4 overflow-x-auto border-[var(--color-error-border)]">
        <table className="w-full min-w-[680px] text-left text-xs">
          <thead className="bg-[var(--color-bg-tertiary)] text-[10px] font-mono uppercase tracking-wider text-[var(--color-error-text)]">
            <tr>
              <th className="px-3 py-2.5 font-bold">{t('common.errorPath')}</th>
              <th className="px-3 py-2.5 font-bold">{t('common.errorType')}</th>
              <th className="px-3 py-2.5 font-bold">{t('common.errorMessage')}</th>
              <th className="px-3 py-2.5 font-bold whitespace-nowrap">{t('common.errorOccurredAt')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--color-border-light)]">
            {items.map((item) => (
              <tr key={`${item.kind}-${item.id}`} className="transition-colors hover:bg-[var(--color-bg-tertiary)]">
                <td className="max-w-[220px] px-3 py-2.5 font-mono text-[var(--color-text-primary)] break-all">{item.path}</td>
                <td className="px-3 py-2.5 whitespace-nowrap text-[var(--color-text-secondary)]">
                  {item.kind === 'indexing' ? t('common.errorTypeIndexing') : t('common.errorTypeTransfer')}
                  {item.attempts > 0 ? ` / ${t('common.errorAttempts', { count: item.attempts })}` : ''}
                </td>
                <td className="max-w-[360px] px-3 py-2.5 text-[var(--color-error-text)] break-words">{item.error_message || t('common.errorNoMessage')}</td>
                <td className="px-3 py-2.5 whitespace-nowrap text-[var(--color-text-muted)]">{formatDateTime(item.occurred_at)}</td>
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
