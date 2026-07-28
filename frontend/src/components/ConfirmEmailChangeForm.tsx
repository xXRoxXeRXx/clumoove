import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useApiError } from '../utils/apiError';
import { apiFetch } from '../utils/apiClient';
interface ConfirmEmailChangeFormProps {
  apiUrl: string;
  token: string;
  onSuccess: () => void;
}

export function ConfirmEmailChangeForm({ apiUrl, token, onSuccess }: ConfirmEmailChangeFormProps) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const [error, setError] = useState<string>('');
  const [loading, setLoading] = useState<boolean>(true);
  const [success, setSuccess] = useState<boolean>(false);

  useEffect(() => {
    const confirm = async () => {
      setLoading(true);
      setError('');
      try {
        const response = await apiFetch(`${apiUrl}/api/auth/confirm-email-change`, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({ token }),
        });

        if (!response.ok) {
          const data = (await response.json().catch(() => ({}))) as { error_code?: string };
          throw new Error(translateApiError(data.error_code));
        }

        setSuccess(true);
        setTimeout(() => onSuccess(), 1800);
      } catch (err: unknown) {
        setError(err instanceof Error ? err.message : t('confirmEmail.networkError'));
      } finally {
        setLoading(false);
      }
    };

    confirm();
  }, [apiUrl, token, onSuccess, t, translateApiError]);

  if (loading) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4">
        <div className="glass-panel rounded-lg p-8 text-center">
          <div className="flex flex-col items-center gap-4 py-4">
            <span className="animate-spin rounded-full h-12 w-12 border-2 border-portal-orange border-t-transparent" />
            <h2 className="font-display font-extrabold text-xl text-[var(--color-portal-navy-themed)] tracking-tight">
               {t('confirmEmail.changing')}
            </h2>
            <p className="text-xs text-[var(--color-text-muted)] font-mono leading-relaxed">
               {t('confirmEmail.pleaseWait')}
            </p>
          </div>
        </div>
      </div>
    );
  }

  if (success) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4">
        <div className="glass-panel rounded-lg p-8 text-center">
          <div className="flex flex-col items-center gap-4 py-4">
            <h2 className="font-display font-extrabold text-xl text-[var(--color-portal-navy-themed)] tracking-tight">
               {t('confirmEmail.changed')}
            </h2>
            <p className="text-xs text-[var(--color-text-muted)] font-mono leading-relaxed">
               {t('reset.redirecting')}
            </p>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="max-w-md w-full mx-auto my-8 px-4">
      <div className="glass-panel rounded-lg p-8 text-center">
        <div className="flex flex-col items-center gap-4 py-4">
          <h2 className="font-display font-extrabold text-xl text-[var(--color-portal-navy-themed)] tracking-tight">
             {t('confirmEmail.invalid')}
          </h2>
          <p className="text-xs text-[var(--color-text-muted)] font-mono leading-relaxed">
              {error || t('confirmEmail.invalidDefault')}
          </p>
        </div>
      </div>
    </div>
  );
}
