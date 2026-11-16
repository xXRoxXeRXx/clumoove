import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowPathIcon } from '../../icons';
import { LoadingIndicator } from '../../LoadingIndicator';

type LoadMoreFooterProps = {
  loading: boolean;
  /** Request the next page by itself once the footer scrolls near the viewport. */
  autoLoad: boolean;
  onLoadMore: () => void;
};

/** Pagination footer: a manual "load more" button that also fires when scrolled into view. */
export function LoadMoreFooter({ loading, autoLoad, onLoadMore }: LoadMoreFooterProps) {
  const { t } = useTranslation();
  const sentinelRef = useRef<HTMLDivElement>(null);
  const loadMoreRef = useRef(onLoadMore);

  useEffect(() => {
    loadMoreRef.current = onLoadMore;
  }, [onLoadMore]);

  useEffect(() => {
    if (!autoLoad) return;
    const target = sentinelRef.current;
    if (!target || typeof IntersectionObserver === 'undefined') return;
    const observer = new IntersectionObserver(
      (observed) => {
        if (observed[0]?.isIntersecting) loadMoreRef.current();
      },
      { rootMargin: '250px' },
    );
    observer.observe(target);
    return () => observer.disconnect();
  }, [autoLoad]);

  return (
    <div ref={sentinelRef} className="flex justify-center p-4 border-t border-[var(--color-border)]">
      {loading ? (
        <LoadingIndicator label={t('common.loading')} size="sm" />
      ) : (
        <button
          type="button"
          onClick={onLoadMore}
          className="ui-button-secondary inline-flex items-center gap-1.5 px-3 py-1.5 text-xs text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
        >
          <ArrowPathIcon className="h-3.5 w-3.5" aria-hidden="true" />
          {t('files.loadMore')}
        </button>
      )}
    </div>
  );
}
