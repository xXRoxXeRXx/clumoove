import { useCallback } from 'react';
import { useTranslation } from 'react-i18next';

// translateApiError maps a backend error_code to a localized message.
// The backend sends ONLY the machine-readable code (no curated text); all
// localization happens here. Unknown codes fall back to a generic message.
export const useApiError = () => {
  const { t } = useTranslation();

  // Memoize so the returned function keeps a stable identity across renders.
  // Passing it as a useEffect dependency otherwise re-runs the effect on every
  // render, which for long-lived connections (SSE/WebSocket) opens a new
  // connection each time and floods the rate limiter.
  return useCallback((code?: string | null): string => {
    if (code) {
      const key = `errors.${code}`;
      const translated = t(key);
      if (translated !== key) return translated;
    }
    return t('errors.UNKNOWN');
  }, [t]);
};
