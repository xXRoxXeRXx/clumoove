import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { EyeIcon as Eye, EyeSlashIcon as EyeOff } from '@heroicons/react/24/outline';
import type { User as UserType } from '../types';
import { useApiError } from '../utils/apiError';
import { apiFetch } from '../utils/apiClient';

interface AuthFormProps {
  apiUrl: string;
  onAuthSuccess: (token: string, user: UserType) => void;
}

const authPanelClass = 'ui-section p-8';
const authInputClass = 'ui-input w-full px-4 py-2.5 text-sm transition-colors';
const authButtonClass = 'ui-button-primary w-full py-3 px-4 text-xs font-bold uppercase tracking-wider font-mono cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed';
const authErrorClass = 'ui-alert ui-alert-error p-3.5 text-xs mb-6 text-center font-mono leading-relaxed';
const authSuccessClass = 'ui-alert ui-alert-success p-3.5 text-xs mb-6 text-center font-mono leading-relaxed';
const authLinkClass = 'ui-link hover:underline cursor-pointer';
const spinnerClass = 'animate-spin rounded-full h-4 w-4 border-2 border-current/30 border-t-current';

export function AuthForm({ apiUrl, onAuthSuccess }: AuthFormProps) {
	const { t, i18n } = useTranslation();
	const language = i18n.language?.startsWith('de') ? 'de' : 'en';
  const translateApiError = useApiError();
  const [isLogin, setIsLogin] = useState<boolean>(true);
  const [email, setEmail] = useState<string>('');
  const [password, setPassword] = useState<string>('');
  const [displayName, setDisplayName] = useState<string>('');
  const [showPassword, setShowPassword] = useState<boolean>(false);
  const [error, setError] = useState<string>('');
  const [successMessage, setSuccessMessage] = useState<string>('');
  const [loading, setLoading] = useState<boolean>(false);
  const [registrationsEnabled, setRegistrationsEnabled] = useState<boolean>(false);
  const [passwordResetAvailable, setPasswordResetAvailable] = useState<boolean>(false);
  const [forgotMode, setForgotMode] = useState<boolean>(false);
  const [resetEmailSent, setResetEmailSent] = useState<boolean>(false);
  const [totpSession, setTotpSession] = useState<string>('');
  const [otpCode, setOtpCode] = useState<string>('');
  const [otpError, setOtpError] = useState<string>('');
  const [lockSeconds, setLockSeconds] = useState<number>(0);

  const [mustChangeSession, setMustChangeSession] = useState<string>('');
  const [newPassword, setNewPassword] = useState<string>('');
  const [confirmNewPassword, setConfirmNewPassword] = useState<string>('');
  const [mustChangeError, setMustChangeError] = useState<string>('');
  const [needsSetup, setNeedsSetup] = useState<boolean>(false);

  useEffect(() => {
    let cancelled = false;
    apiFetch(`${apiUrl}/api/settings`)
      .then((res) => res.json())
      .then((data) => {
        if (cancelled) return;
        if (data) {
          setRegistrationsEnabled(data.registrations_enabled === 'true');
          if (data.needs_setup === true) {
            setNeedsSetup(true);
          }
        }
      })
      .catch((err) => {
        console.error('Failed to fetch settings:', err);
      });
    return () => { cancelled = true; };
  }, [apiUrl]);

  useEffect(() => {
    let cancelled = false;
    apiFetch(`${apiUrl}/api/auth/password-reset-available`)
      .then((res) => res.json())
      .then((data) => {
        if (cancelled) return;
        if (data && data.available === true) {
          setPasswordResetAvailable(true);
        }
      })
      .catch((err) => {
        console.error('Failed to fetch password reset availability:', err);
      });
    return () => { cancelled = true; };
  }, [apiUrl]);

  useEffect(() => {
    if (lockSeconds <= 0) return;
    const timer = setInterval(() => {
      setLockSeconds((s) => (s > 0 ? s - 1 : 0));
    }, 1000);
    return () => clearInterval(timer);
  }, [lockSeconds]);

  const handleForgotPassword = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);

    const trimmedEmail = email.trim();
    if (!trimmedEmail) {
      setError(t('auth.enterEmail'));
      setLoading(false);
      return;
    }

    try {
      const response = await apiFetch(`${apiUrl}/api/auth/forgot-password`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ email: trimmedEmail }),
      });

      if (!response.ok) {
        const data = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(data.error_code));
      }

      setResetEmailSent(true);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('reset.networkError'));
    } finally {
      setLoading(false);
    }
  };

  const handleOTPSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setOtpError('');
    setLoading(true);

    const code = otpCode.trim();
    if (!code) {
      setOtpError(t('errors.TOTP_CODE_REQUIRED'));
      setLoading(false);
      return;
    }

    try {
      const response = await apiFetch(`${apiUrl}/api/auth/totp`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        credentials: 'include',
        body: JSON.stringify({ temp_session: totpSession, code }),
      });

      if (response.status === 429) {
        const retryAfter = Number(response.headers.get('Retry-After') || '900');
        setLockSeconds(retryAfter);
        setOtpError(t('auth.totpLocked'));
        setLoading(false);
        return;
      }

      if (!response.ok) {
        const data = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(data.error_code));
      }

      const data = await response.json();
      onAuthSuccess(data.access_token, data.user);
    } catch (err: unknown) {
      setOtpError(err instanceof Error ? err.message : t('reset.networkError'));
    } finally {
      setLoading(false);
    }
  };

  const handleMustChangeSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setMustChangeError('');
    setLoading(true);
    if (newPassword.length < 12) {
      setMustChangeError(t('reset.tooShort'));
      setLoading(false);
      return;
    }
    if (newPassword !== confirmNewPassword) {
      setMustChangeError(t('reset.mismatch'));
      setLoading(false);
      return;
    }
    try {
      const response = await apiFetch(`${apiUrl}/api/auth/change-password`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${mustChangeSession}`,
        },
        credentials: 'include',
        body: JSON.stringify({ new_password: newPassword, confirm_password: confirmNewPassword }),
      });
      if (!response.ok) {
        const data = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(data.error_code));
      }
      const data = await response.json();
      if (data.totp_required && data.temp_session) {
        setMustChangeSession('');
        setTotpSession(data.temp_session);
        setOtpCode('');
        setOtpError('');
        return;
      }
      onAuthSuccess(data.access_token, data.user);
    } catch (err: unknown) {
      setMustChangeError(err instanceof Error ? err.message : t('reset.networkError'));
    } finally {
      setLoading(false);
    }
  };

  if (mustChangeSession) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4 relative">
        <div className={authPanelClass}>

          <div className="flex flex-col items-center mb-8">
            <div className="mb-4 text-sm font-semibold text-[var(--color-text-secondary)]">
              <span className="text-sm font-semibold" aria-hidden="true">Clumoove</span>
            </div>
            <h1 className="font-display font-extrabold text-2xl text-[var(--color-text-primary)] tracking-tight">
              {t('auth.mustChangePassword')}
            </h1>
            <p className="text-[9px] text-[var(--color-text-muted)] font-mono tracking-widest uppercase mt-1">
              {t('auth.setNewPassword')}
            </p>
          </div>

          {mustChangeError && (
            <div role="alert" className={authErrorClass}>
              {mustChangeError}
            </div>
          )}

          <form onSubmit={handleMustChangeSubmit} className="space-y-5">
            <div className="space-y-1.5">
              <label htmlFor="must-change-new-password" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                {t('auth.newPassword')}
              </label>
              <input
                id="must-change-new-password"
                type="password"
                autoComplete="new-password"
                name="new_password"
                autoFocus
                required
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                className={`${authInputClass} font-mono`}
              />
            </div>
            <div className="space-y-1.5">
              <label htmlFor="must-change-confirm-password" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                {t('auth.confirmPassword')}
              </label>
              <input
                id="must-change-confirm-password"
                type="password"
                autoComplete="new-password"
                name="confirm_password"
                required
                value={confirmNewPassword}
                onChange={(e) => setConfirmNewPassword(e.target.value)}
                className={`${authInputClass} font-mono`}
              />
            </div>

            <button
              type="submit"
              disabled={loading}
              className={`${authButtonClass} mt-2`}
            >
              {loading ? (
                <span className="flex items-center justify-center gap-2">
                  <span className={spinnerClass}></span>
                  {t('common.processing')}
                </span>
              ) : (
                t('auth.changePassword')
              )}
            </button>
          </form>
        </div>
      </div>
    );
  }

  if (totpSession) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4 relative">
        <div className={authPanelClass}>

          <div className="flex flex-col items-center mb-8">
            <div className="mb-4 text-sm font-semibold text-[var(--color-text-secondary)]">
              <span className="text-sm font-semibold" aria-hidden="true">Clumoove</span>
            </div>
            <h1 className="font-display font-extrabold text-2xl text-[var(--color-text-primary)] tracking-tight">
              {t('auth.totpTitle')}
            </h1>
            <p className="text-[9px] text-[var(--color-text-muted)] font-mono tracking-widest uppercase mt-1">
              {t('auth.totpEnterCode')}
            </p>
          </div>

          {otpError && (
            <div role="alert" className={authErrorClass}>
              {otpError}
            </div>
          )}

          <form onSubmit={handleOTPSubmit} className="space-y-5">
            <div className="space-y-1.5">
              <label htmlFor="totp-code" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                {t('auth.authenticatorCode')}
              </label>
              <div className="relative group">
                <input
                  id="totp-code"
                  type="text"
                  inputMode="numeric"
                  autoFocus
                  required
                  placeholder="123456"
                  value={otpCode}
                  onChange={(e) => setOtpCode(e.target.value)}
                  className={`${authInputClass} tracking-[0.5em] text-center font-mono`}
                />
              </div>
            </div>

            {lockSeconds > 0 && (
              <p className="text-center text-xs font-mono text-[var(--color-error-text)]">
                {t('auth.lockedRetry', { time: `${Math.floor(lockSeconds / 60)}:${(lockSeconds % 60).toString().padStart(2, '0')}` })}
              </p>
            )}

            <button
              type="submit"
              disabled={loading || lockSeconds > 0}
              className={`${authButtonClass} mt-2`}
            >
              {loading ? (
                <span className="flex items-center justify-center gap-2">
                  <span className={spinnerClass}></span>
                  {t('common.processing')}
                </span>
              ) : (
                t('auth.verify')
              )}
            </button>
          </form>

          <div className="mt-6 text-center text-xs font-mono text-[var(--color-text-muted)] border-t border-[var(--color-border)] pt-5">
            <button
              type="button"
              onClick={() => {
                setTotpSession('');
                setOtpCode('');
                setOtpError('');
                setLockSeconds(0);
              }}
              className={authLinkClass}
            >
               {t('common.cancel')}
            </button>
          </div>
        </div>
      </div>
    );
  }

  if (forgotMode) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4 relative">
        <div className={authPanelClass}>

          <div className="flex flex-col items-center mb-8">
            <div className="mb-4 text-sm font-semibold text-[var(--color-text-secondary)]">
              <span className="text-sm font-semibold" aria-hidden="true">Clumoove</span>
            </div>
            <h1 className="font-display font-extrabold text-2xl text-[var(--color-text-primary)] tracking-tight">
              {t('auth.forgotTitle')}
            </h1>
            <p className="text-[9px] text-[var(--color-text-muted)] font-mono tracking-widest uppercase mt-1">
              {t('auth.portalLogin')}
            </p>
          </div>

          {!resetEmailSent ? (
            <>
              {error && (
                <div role="alert" className={authErrorClass}>
                  {error}
                </div>
              )}

              <form onSubmit={handleForgotPassword} className="space-y-5">
                <div className="space-y-1.5">
                  <label htmlFor="forgot-email" className="ui-field-label">
                     {t('auth.email')}
                  </label>
                  <div className="relative group">
                    <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-[var(--color-text-muted)]">
                      <span aria-hidden="true">@</span>
                    </span>
                    <input
                      id="forgot-email"
                      type="email"
                      required
                      autoComplete="email"
                      placeholder={t('auth.emailPlaceholder')}
                      value={email}
                      onChange={(e) => setEmail(e.target.value)}
                      className={`${authInputClass} pl-10 pr-4 font-sans`}
                    />
                  </div>
                </div>

                <button
                  type="submit"
                  disabled={loading}
                  className={`${authButtonClass} mt-2`}
                >
                  {loading ? (
                    <span className="flex items-center justify-center gap-2">
                      <span className={spinnerClass}></span>
                  {t('common.processing')}
                </span>
              ) : (
                t('auth.sendLink')
              )}
                </button>
              </form>
            </>
          ) : (
            <div className="ui-alert ui-alert-success p-4 text-xs text-center font-mono leading-relaxed mb-6">
               {t('auth.forgotSent')}
            </div>
          )}

          <div className="mt-6 text-center text-xs font-mono text-[var(--color-text-muted)] border-t border-[var(--color-border)] pt-5">
            <button
              type="button"
              onClick={() => {
                setForgotMode(false);
                setResetEmailSent(false);
                setError('');
              }}
              className={authLinkClass}
            >
                   {t('auth.backToLogin')}
                 </button>
          </div>
        </div>
      </div>
    );
  }

  const handleSetupAdminSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSuccessMessage('');
    setLoading(true);

    const trimmedEmail = email.trim();
    const trimmedName = displayName.trim();

    if (!trimmedEmail || !password || !trimmedName) {
      setError(t('auth.fillAllFields'));
      setLoading(false);
      return;
    }

    if (password.length < 12) {
      setError(translateApiError('PASSWORD_TOO_SHORT'));
      setLoading(false);
      return;
    }

    try {
      const response = await apiFetch(`${apiUrl}/api/auth/setup-admin`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        credentials: 'include',
        body: JSON.stringify({
          email: trimmedEmail,
          password,
          display_name: trimmedName,
			language,
        }),
      });

      if (!response.ok) {
        const data = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(data.error_code));
      }

      const data = await response.json() as { access_token?: string; user?: UserType };
      if (!data.access_token || !data.user) {
        throw new Error(translateApiError('UNKNOWN'));
      }
      onAuthSuccess(data.access_token, data.user);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('reset.networkError'));
    } finally {
      setLoading(false);
    }
  };

  if (needsSetup) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4 relative">
        <div className={authPanelClass}>
          <div className="flex flex-col items-center mb-8">
            <div className="mb-4 text-sm font-semibold text-[var(--color-text-secondary)]">
              <span className="text-sm font-semibold" aria-hidden="true">Clumoove</span>
            </div>
            <h1 className="font-display font-extrabold text-2xl text-[var(--color-text-primary)] tracking-tight text-center">
              {t('auth.setupAdminTitle')}
            </h1>
            <p className="text-xs text-[var(--color-text-muted)] font-mono tracking-wide text-center mt-2 leading-relaxed">
              {t('auth.setupAdminSubtitle')}
            </p>
          </div>

          {error && (
            <div role="alert" className={authErrorClass}>
              {error}
            </div>
          )}

          <form onSubmit={handleSetupAdminSubmit} className="space-y-5">
            <div className="space-y-1.5">
              <label htmlFor="setup-name" className="ui-field-label">
                {t('auth.name')}
              </label>
              <div>
                <input
                  id="setup-name"
                  type="text"
                  required
                  placeholder={t('auth.namePlaceholder')}
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                  className={`${authInputClass} font-sans`}
                />
              </div>
            </div>

            <div className="space-y-1.5">
              <label htmlFor="setup-email" className="ui-field-label">
                {t('auth.email')}
              </label>
              <div className="relative group">
                <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-[var(--color-text-muted)]">
                  <span aria-hidden="true">@</span>
                </span>
                <input
                  id="setup-email"
                  type="email"
                  required
                  autoComplete="email"
                  placeholder={t('auth.emailPlaceholder')}
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  className={`${authInputClass} pl-10 pr-4 font-sans`}
                />
              </div>
            </div>

            <div className="space-y-1.5">
              <label htmlFor="setup-password" className="ui-field-label">
                {t('auth.password')}
              </label>
              <div className="relative group">
                <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-[var(--color-text-muted)]">
                  <span aria-hidden="true">•</span>
                </span>
                <input
                  id="setup-password"
                  type={showPassword ? 'text' : 'password'}
                  required
                  placeholder={t('auth.passwordPlaceholder')}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className={`${authInputClass} pl-10 pr-10 font-sans`}
                />
                <button
                  type="button"
                  onClick={() => setShowPassword(!showPassword)}
                  className="absolute inset-y-0 right-0 pr-3 flex items-center text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors"
                  aria-label={showPassword ? t('auth.hidePassword') : t('auth.showPassword')}
                >
                  <span className="text-[10px]">{showPassword ? t('auth.hidePassword') : t('auth.showPassword')}</span>
                </button>
              </div>
            </div>

            <button
              type="submit"
              disabled={loading}
              className={`${authButtonClass} text-sm mt-6`}
            >
              {loading ? (
                <span className="inline-flex items-center justify-center gap-2">
                  <span className={spinnerClass} />
                  <span>{t('auth.createAdminAccount')}...</span>
                </span>
              ) : (
                t('auth.createAdminAccount')
              )}
            </button>
          </form>
        </div>
      </div>
    );
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSuccessMessage('');
    setLoading(true);

    const trimmedEmail = email.trim();
    if (!trimmedEmail || !password || (!isLogin && !displayName.trim())) {
      setError(t('auth.fillAllFields'));
      setLoading(false);
      return;
    }

    const endpoint = isLogin ? '/api/auth/login' : '/api/auth/register';
    const payload = isLogin
      ? { email: trimmedEmail, password }
		: { email: trimmedEmail, password, display_name: displayName.trim(), language };

    try {
      const response = await apiFetch(`${apiUrl}${endpoint}`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        credentials: 'include',
        body: JSON.stringify(payload),
      });

      if (!response.ok) {
        const data = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(data.error_code));
      }

      if (isLogin) {
        const data = await response.json();
        if (data.must_change_password && data.temp_session) {
          setMustChangeSession(data.temp_session);
          setNewPassword('');
          setConfirmNewPassword('');
          setMustChangeError('');
          setError('');
        } else if (data.totp_required && data.temp_session) {
          setTotpSession(data.temp_session);
          setOtpCode('');
          setOtpError('');
          setError('');
        } else {
          onAuthSuccess(data.access_token, data.user);
        }
      } else {
        // Registration success: switch to login and show success message
        setIsLogin(true);
        setPassword('');
        setSuccessMessage(t('auth.registrationSuccess'));
      }
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('reset.networkError'));
      setSuccessMessage('');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="max-w-md w-full mx-auto my-8 px-4 relative">
      <div className={authPanelClass}>
        
        {/* Brand header */}
        <div className="flex flex-col items-center mb-8">
          <div className="mb-4 text-sm font-semibold text-[var(--color-text-secondary)]">
            <span className="text-sm font-semibold" aria-hidden="true">Clumoove</span>
          </div>
          <h1 className="font-display font-extrabold text-2xl text-[var(--color-text-primary)] tracking-tight">
            {isLogin ? t('auth.welcomeBack') : t('auth.createAccount')}
          </h1>
          <p className="text-[9px] text-[var(--color-text-muted)] font-mono tracking-widest uppercase mt-1">
            {isLogin ? t('auth.portalLogin') : t('auth.portalRegister')}
          </p>
        </div>

        {error && (
          <div role="alert" className={authErrorClass}>
            {error}
          </div>
        )}

        {successMessage && (
          <div className={authSuccessClass}>
            {successMessage}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-5">
          {/* Display Name - only for registration */}
          {!isLogin && (
            <div className="space-y-1.5">
              <label htmlFor="auth-name" className="ui-field-label">
                {t('auth.name')}
              </label>
              <div>
                <input
                  id="auth-name"
                  type="text"
                  required
                  placeholder={t('auth.namePlaceholder')}
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                  className={`${authInputClass} font-sans`}
                />
              </div>
            </div>
          )}

          {/* Email input */}
          <div className="space-y-1.5">
            <label htmlFor="auth-email" className="ui-field-label">
              {t('auth.email')}
            </label>
            <div>
              <input
                id="auth-email"
                type="email"
                required
                autoComplete="email"
                placeholder={t('auth.emailPlaceholder')}
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className={`${authInputClass} font-sans`}
              />
            </div>
          </div>

          {/* Password input */}
          <div className="space-y-1.5">
            <label htmlFor="auth-password" className="ui-field-label">
                {t('auth.password')}
              </label>
            <div className="relative group">
              <input
                id="auth-password"
                type={showPassword ? 'text' : 'password'}
                required
                autoComplete={isLogin ? 'current-password' : 'new-password'}
                placeholder={t('auth.passwordPlaceholder')}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className={`${authInputClass} pr-10 font-mono`}
              />
                <button
                  type="button"
                  onClick={() => setShowPassword(!showPassword)}
                  aria-label={showPassword ? t('auth.hidePassword') : t('auth.showPassword')}
                  className="absolute inset-y-0 right-0 pr-3.5 flex items-center text-[var(--color-text-muted)] hover:text-[var(--color-text-secondary)] transition-colors"
                >
                  {showPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                </button>
            </div>
          </div>

          {/* Forgot password link - only if system SMTP is configured */}
          {isLogin && passwordResetAvailable && (
            <div className="mt-3 text-center">
              <button
                type="button"
                onClick={() => {
                  setForgotMode(true);
                  setError('');
                }}
                className="ui-link text-xs font-mono underline-offset-2 hover:underline"
              >
                 {t('auth.forgotPassword')}
               </button>
            </div>
          )}

          {/* Submit Button */}
          <button
            type="submit"
            disabled={loading}
            className={`${authButtonClass} mt-2`}
          >
            {loading ? (
              <span className="flex items-center justify-center gap-2">
                <span className={spinnerClass}></span>
                 {t('common.processing')}
               </span>
             ) : isLogin ? (
               t('auth.login')
             ) : (
               t('auth.register')
             )}
          </button>
        </form>

        {/* Toggle between login and registration */}
        <div className="mt-6 text-center text-xs font-mono text-[var(--color-text-muted)] border-t border-[var(--color-border)] pt-5">
          {isLogin ? (
            registrationsEnabled ? (
              <p>
                 {t('auth.noAccount')}{' '}
                <button
                  type="button"
                  onClick={() => {
                    setIsLogin(false);
                    setError('');
                  }}
                  className={authLinkClass}
                >
                   {t('auth.register')}
                 </button>
              </p>
            ) : (
              <p className="text-[var(--color-text-muted)]">
                {t('auth.registrationsDisabled')}
              </p>
            )
          ) : (
            <p>
               {t('auth.hasAccount')}{' '}
              <button
                type="button"
                onClick={() => {
                  setIsLogin(true);
                  setError('');
                }}
                className={authLinkClass}
              >
                   {t('auth.login')}
                 </button>
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
