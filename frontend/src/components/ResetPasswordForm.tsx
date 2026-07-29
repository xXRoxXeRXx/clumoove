import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useApiError } from '../utils/apiError';
import { apiFetch } from '../utils/apiClient';
import { ProgressBar } from './ProgressBar';

interface ResetPasswordFormProps {
  apiUrl: string;
  token: string;
  onSuccess: () => void;
}

function getPasswordStrength(password: string): { score: number; label: string; color: string } {
  if (password.length === 0) return { score: 0, label: '', color: '' };
  let score = 0;
  if (password.length >= 12) score++;
  if (password.length >= 16) score++;
  if (/[A-Z]/.test(password) && /[a-z]/.test(password)) score++;
  if (/\d/.test(password)) score++;
  if (/[^A-Za-z0-9]/.test(password)) score++;

  if (score <= 1) return { score, label: 'weak', color: 'ui-progress-error' };
  if (score <= 3) return { score, label: 'medium', color: 'ui-progress-warning' };
  return { score, label: 'strong', color: 'ui-progress-success' };
}

export function ResetPasswordForm({ apiUrl, token, onSuccess }: ResetPasswordFormProps) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const [password, setPassword] = useState<string>('');
  const [confirmPassword, setConfirmPassword] = useState<string>('');
  const [showPassword, setShowPassword] = useState<boolean>(false);
  const [showConfirmPassword, setShowConfirmPassword] = useState<boolean>(false);
  const [error, setError] = useState<string>('');
  const [loading, setLoading] = useState<boolean>(false);
  const [success, setSuccess] = useState<boolean>(false);

  const strength = getPasswordStrength(password);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');

    if (password.length < 12) {
      setError(t('reset.tooShort'));
      return;
    }

    if (password !== confirmPassword) {
      setError(t('reset.mismatch'));
      return;
    }

    setLoading(true);

    try {
      const response = await apiFetch(`${apiUrl}/api/auth/reset-password`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ token, new_password: password }),
      });

      if (!response.ok) {
        const data = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(data.error_code));
      }

      setSuccess(true);
      setTimeout(() => onSuccess(), 1500);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('reset.networkError'));
    } finally {
      setLoading(false);
    }
  };

  if (success) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4">
        <div className="ui-section p-8 text-center">
          <div className="flex flex-col items-center gap-4 py-4">
            <h1 className="font-display font-extrabold text-xl text-[var(--color-text-primary)] tracking-tight">
               {t('reset.changed')}
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
      <div className="ui-section p-8">

        <div className="flex flex-col items-center mb-8">
          <h1 className="font-display font-extrabold text-2xl text-[var(--color-text-primary)] tracking-tight">
            {t('reset.title')}
          </h1>
          <p className="text-[9px] text-[var(--color-text-muted)] font-mono tracking-widest uppercase mt-1">
             {t('reset.portal')}
          </p>
        </div>

        {error && (
          <div role="alert" className="ui-alert ui-alert-error p-3.5 text-xs mb-6 text-center font-mono leading-relaxed flex items-center justify-center gap-2">
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-5">
          <div className="space-y-1.5">
            <label htmlFor="reset-password" className="ui-field-label">
              {t('reset.title')}
            </label>
            <div className="relative group">
              <input
                id="reset-password"
                type={showPassword ? 'text' : 'password'}
                required
                autoComplete="new-password"
                placeholder="••••••••"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="ui-input w-full px-3 py-2 text-sm font-mono"
              />
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
                className="absolute inset-y-0 right-0 pr-3.5 flex items-center text-[var(--color-text-muted)] hover:text-[var(--color-text-secondary)] transition-colors"
                aria-label={showPassword ? t('auth.hidePassword') : t('auth.showPassword')}
              >
                <span className="text-[10px]">{showPassword ? t('auth.hidePassword') : t('auth.showPassword')}</span>
              </button>
            </div>

            {password.length > 0 && (
              <div className="flex items-center gap-2 mt-2">
                <ProgressBar label={t('reset.title')} value={(strength.score / 5) * 100} className="flex-1 h-1.5" indicatorClassName={strength.color} />
                 <span className="text-[9px] font-mono text-[var(--color-text-muted)] uppercase">{t(`reset.strength.${strength.label}`)}</span>
              </div>
            )}
          </div>

          <div className="space-y-1.5">
            <label htmlFor="reset-confirm-password" className="ui-field-label">
              {t('settings.confirmPassword')}
            </label>
            <div className="relative group">
              <input
                id="reset-confirm-password"
                type={showConfirmPassword ? 'text' : 'password'}
                required
                autoComplete="new-password"
                placeholder="••••••••"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                className="ui-input w-full px-3 py-2 text-sm font-mono"
              />
              <button
                type="button"
                onClick={() => setShowConfirmPassword(!showConfirmPassword)}
                className="absolute inset-y-0 right-0 pr-3.5 flex items-center text-[var(--color-text-muted)] hover:text-[var(--color-text-secondary)] transition-colors"
                aria-label={showConfirmPassword ? t('auth.hidePassword') : t('auth.showPassword')}
              >
                <span className="text-[10px]">{showConfirmPassword ? t('auth.hidePassword') : t('auth.showPassword')}</span>
              </button>
            </div>
          </div>

          <button
            type="submit"
            disabled={loading || password.length < 12 || password !== confirmPassword}
            className="ui-button-primary mt-2 w-full px-4 py-2.5 text-sm font-medium hover:opacity-90 disabled:opacity-50"
          >
              {loading ? (
                <span className="flex items-center justify-center gap-2">
                  <span className="animate-spin rounded-full h-4 w-4 border-2 border-current/30 border-t-current"></span>
                  {t('common.processing')}
                </span>
              ) : (
                t('reset.submit')
              )}
          </button>
        </form>
      </div>
    </div>
  );
}
