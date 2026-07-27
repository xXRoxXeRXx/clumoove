import { useEffect, useState } from 'react';
import { AlertTriangle, Loader2 } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useFormat } from '../utils/format';
import { apiFetch } from '../utils/apiClient';

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
    <section className="p-5 rounded-2xl border border-rose-200 bg-rose-50/40 space-y-4">
      <div className="flex items-center justify-between gap-3 border-b border-rose-100 pb-3">
        <div className="flex items-center gap-2">
          <AlertTriangle className="w-4 h-4 shrink-0 text-rose-600" />
          <h3 className="font-display font-bold text-xs text-rose-800 uppercase tracking-wider font-mono">
            {t('common.errorOverview')}
          </h3>
        </div>
        <span className="rounded-full bg-rose-100 px-2 py-0.5 text-xs font-bold text-rose-700 font-mono">
          {total}
        </span>
      </div>

      <div className="overflow-x-auto rounded-xl border border-rose-100 bg-[var(--color-bg-primary)]">
        <table className="w-full min-w-[680px] text-left text-xs">
          <thead className="bg-rose-50 text-[10px] font-mono uppercase tracking-wider text-rose-700">
            <tr>
              <th className="px-3 py-2.5 font-bold">{t('common.errorPath')}</th>
              <th className="px-3 py-2.5 font-bold">{t('common.errorType')}</th>
              <th className="px-3 py-2.5 font-bold">{t('common.errorMessage')}</th>
              <th className="px-3 py-2.5 font-bold whitespace-nowrap">{t('common.errorOccurredAt')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--color-border-light)]">
            {items.map((item) => (
              <tr key={`${item.kind}-${item.id}`}>
                <td className="max-w-[220px] px-3 py-2.5 font-mono text-[var(--color-text-primary)] break-all">{item.path}</td>
                <td className="px-3 py-2.5 whitespace-nowrap text-[var(--color-text-secondary)]">
                  {item.kind === 'indexing' ? t('common.errorTypeIndexing') : t('common.errorTypeTransfer')}
                  {item.attempts > 0 ? ` / ${t('common.errorAttempts', { count: item.attempts })}` : ''}
                </td>
                <td className="max-w-[360px] px-3 py-2.5 text-rose-700 break-words">{item.error_message || t('common.errorNoMessage')}</td>
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
          className="flex items-center gap-2 px-3 py-2 rounded-xl border border-rose-200 bg-white text-xs font-bold text-rose-700 hover:bg-rose-100 disabled:opacity-50 cursor-pointer"
        >
          {loadingMore && <Loader2 className="w-3.5 h-3.5 animate-spin" />}
          {t('common.loadMoreErrors')}
        </button>
      )}
      {loadMoreError && <p className="text-xs text-rose-700">{t('common.loadMoreErrorsFailed')}</p>}
    </section>
  );
}
