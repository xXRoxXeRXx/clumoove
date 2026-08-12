import { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { LoadingIndicator } from './LoadingIndicator';
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
  const [errorCode, setErrorCode] = useState<string>('');
  const [loading, setLoading] = useState<boolean>(true);
  const [success, setSuccess] = useState<boolean>(false);
  const onSuccessRef = useRef(onSuccess);

  useEffect(() => {
    onSuccessRef.current = onSuccess;
  }, [onSuccess]);

  useEffect(() => {
    let cancelled = false;
    let redirectTimer: number | undefined;

    const confirm = async () => {
      setLoading(true);
      setErrorCode('');
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
          if (!cancelled) setErrorCode(data.error_code || 'UNKNOWN');
          return;
        }

        if (!cancelled) {
          setSuccess(true);
          redirectTimer = window.setTimeout(() => onSuccessRef.current(), 1800);
        }
      } catch {
        if (!cancelled) setErrorCode('NETWORK');
      } finally {
        if (!cancelled) setLoading(false);
      }
    };

    void confirm();
    return () => {
      cancelled = true;
      if (redirectTimer !== undefined) window.clearTimeout(redirectTimer);
    };
  }, [apiUrl, token]);

  if (loading) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4">
        <div className="ui-section-elevated p-8 text-center">
          <div className="flex flex-col items-center gap-4 py-4">
            <LoadingIndicator label={t('confirmEmail.pleaseWait')} />
            <h1 className="font-display font-extrabold text-xl text-[var(--color-text-primary)] tracking-tight">
               {t('confirmEmail.changing')}
            </h1>
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
        <div className="ui-section-elevated p-8 text-center">
          <div className="flex flex-col items-center gap-4 py-4">
            <h1 className="font-display font-extrabold text-xl text-[var(--color-text-primary)] tracking-tight">
               {t('confirmEmail.changed')}
            </h1>
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
      <div className="ui-section-elevated p-8 text-center">
        <div className="flex flex-col items-center gap-4 py-4">
          <h1 className="font-display font-extrabold text-xl text-[var(--color-text-primary)] tracking-tight">
             {t('confirmEmail.invalid')}
          </h1>
          <p className="text-xs text-[var(--color-text-muted)] font-mono leading-relaxed">
              {errorCode === 'NETWORK'
                ? t('confirmEmail.networkError')
                : errorCode
                  ? translateApiError(errorCode)
                  : t('confirmEmail.invalidDefault')}
          </p>
        </div>
      </div>
    </div>
  );
}
