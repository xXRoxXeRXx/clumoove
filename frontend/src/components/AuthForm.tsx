import { useState, useEffect } from 'react';
import { CloudSync, Lock, Mail, User, Eye, EyeOff } from 'lucide-react';
import type { User as UserType } from '../types';

interface AuthFormProps {
  apiUrl: string;
  onAuthSuccess: (token: string, user: UserType) => void;
}

export function AuthForm({ apiUrl, onAuthSuccess }: AuthFormProps) {
  const [isLogin, setIsLogin] = useState<boolean>(true);
  const [email, setEmail] = useState<string>('');
  const [password, setPassword] = useState<string>('');
  const [displayName, setDisplayName] = useState<string>('');
  const [showPassword, setShowPassword] = useState<boolean>(false);
  const [error, setError] = useState<string>('');
  const [loading, setLoading] = useState<boolean>(false);
  const [registrationsEnabled, setRegistrationsEnabled] = useState<boolean>(true);
  const [passwordResetAvailable, setPasswordResetAvailable] = useState<boolean>(false);
  const [forgotMode, setForgotMode] = useState<boolean>(false);
  const [resetEmailSent, setResetEmailSent] = useState<boolean>(false);
  const [totpSession, setTotpSession] = useState<string>('');
  const [otpCode, setOtpCode] = useState<string>('');
  const [otpError, setOtpError] = useState<string>('');
  const [lockSeconds, setLockSeconds] = useState<number>(0);

  useEffect(() => {
    fetch(`${apiUrl}/api/settings`)
      .then((res) => res.json())
      .then((data) => {
        if (data && data.registrations_enabled === 'false') {
          setRegistrationsEnabled(false);
        }
      })
      .catch((err) => {
        console.error('Failed to fetch settings:', err);
      });
  }, [apiUrl]);

  useEffect(() => {
    fetch(`${apiUrl}/api/auth/password-reset-available`)
      .then((res) => res.json())
      .then((data) => {
        if (data && data.available === true) {
          setPasswordResetAvailable(true);
        }
      })
      .catch((err) => {
        console.error('Failed to fetch password reset availability:', err);
      });
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
      setError('Bitte gib deine E-Mail-Adresse ein.');
      setLoading(false);
      return;
    }

    try {
      const response = await fetch(`${apiUrl}/api/auth/forgot-password`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ email: trimmedEmail }),
      });

      if (!response.ok) {
        const text = await response.text();
        throw new Error(text || 'Ein Fehler ist aufgetreten.');
      }

      setResetEmailSent(true);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Verbindung zum Server fehlgeschlagen.');
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
      setOtpError('Bitte gib deinen Code ein.');
      setLoading(false);
      return;
    }

    try {
      const response = await fetch(`${apiUrl}/api/auth/totp`, {
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
        setOtpError('Zu viele Fehlversuche. Konto für 15 Minuten gesperrt.');
        setLoading(false);
        return;
      }

      if (!response.ok) {
        const text = await response.text();
        throw new Error(text || 'Ungültiger Code.');
      }

      const data = await response.json();
      onAuthSuccess(data.access_token, data.user);
    } catch (err: unknown) {
      setOtpError(err instanceof Error ? err.message : 'Verbindung zum Server fehlgeschlagen.');
    } finally {
      setLoading(false);
    }
  };

  if (totpSession) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4 relative">
        <div className="absolute -top-10 -left-10 w-40 h-40 bg-portal-orange/10 rounded-full blur-3xl pointer-events-none" />
        <div className="absolute -bottom-10 -right-10 w-40 h-40 bg-portal-navy/10 rounded-full blur-3xl pointer-events-none" />

        <div className="relative glass-panel rounded-3xl p-8 shadow-portal hover:shadow-portal-hover border border-[var(--color-glass-border)] transition-all duration-500 overflow-hidden">
          <div className="absolute top-0 left-0 w-full h-1.5 bg-gradient-to-r from-portal-orange via-orange-500 to-portal-navy" />

          <div className="flex flex-col items-center mb-8">
            <div className="p-3 bg-gradient-to-tr from-portal-orange to-orange-500 rounded-2xl text-white shadow-sm mb-4 transition-transform hover:scale-105 duration-300">
              <Lock className="w-6 h-6 stroke-[2.5]" />
            </div>
            <h2 className="font-display font-extrabold text-2xl text-[var(--color-portal-navy-themed)] tracking-tight">
              Zwei-Faktor-Authentifizierung
            </h2>
            <p className="text-[9px] text-[var(--color-text-muted)] font-mono tracking-widest uppercase mt-1">
              // GIB DEINEN CODE EIN
            </p>
          </div>

          {otpError && (
            <div className="p-3.5 rounded-xl border text-xs mb-6 text-center font-mono leading-relaxed animate-fade-in bg-rose-50/80 border-rose-250 text-rose-800">
              {otpError}
            </div>
          )}

          <form onSubmit={handleOTPSubmit} className="space-y-5">
            <div className="space-y-1.5">
              <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                Authenticator-Code
              </label>
              <div className="relative group">
                <input
                  type="text"
                  inputMode="numeric"
                  autoFocus
                  required
                  placeholder="123456"
                  value={otpCode}
                  onChange={(e) => setOtpCode(e.target.value)}
                  className="w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/50 border border-[var(--color-border)] rounded-xl text-sm tracking-[0.5em] text-center focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-mono"
                />
              </div>
            </div>

            {lockSeconds > 0 && (
              <p className="text-center text-xs font-mono text-rose-700">
                Gesperrt. Erneut in {Math.floor(lockSeconds / 60)}:{(lockSeconds % 60).toString().padStart(2, '0')}
              </p>
            )}

            <button
              type="submit"
              disabled={loading || lockSeconds > 0}
              className="w-full bg-gradient-to-r from-portal-orange to-orange-500 text-white hover:shadow-md hover:scale-[1.01] active:scale-[0.99] py-3 px-4 rounded-xl text-xs font-bold transition-all duration-300 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-portal-orange disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider font-mono cursor-pointer mt-2"
            >
              {loading ? (
                <span className="flex items-center justify-center gap-2">
                  <span className="animate-spin rounded-full h-4 w-4 border-2 border-white border-t-transparent"></span>
                  Wird verarbeitet...
                </span>
              ) : (
                'Verifizieren'
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
              className="text-portal-orange font-bold hover:underline transition-all cursor-pointer"
            >
              Abbrechen
            </button>
          </div>
        </div>
      </div>
    );
  }

  if (forgotMode) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4 relative">
        <div className="absolute -top-10 -left-10 w-40 h-40 bg-portal-orange/10 rounded-full blur-3xl pointer-events-none" />
        <div className="absolute -bottom-10 -right-10 w-40 h-40 bg-portal-navy/10 rounded-full blur-3xl pointer-events-none" />

        <div className="relative glass-panel rounded-3xl p-8 shadow-portal hover:shadow-portal-hover border border-[var(--color-glass-border)] transition-all duration-500 overflow-hidden">
          <div className="absolute top-0 left-0 w-full h-1.5 bg-gradient-to-r from-portal-orange via-orange-500 to-portal-navy" />

          <div className="flex flex-col items-center mb-8">
            <div className="p-3 bg-gradient-to-tr from-portal-orange to-orange-500 rounded-2xl text-white shadow-sm mb-4 transition-transform hover:scale-105 duration-300">
              <CloudSync className="w-6 h-6 stroke-[2.5]" />
            </div>
            <h2 className="font-display font-extrabold text-2xl text-[var(--color-portal-navy-themed)] tracking-tight">
              Passwort zurücksetzen
            </h2>
            <p className="text-[9px] text-[var(--color-text-muted)] font-mono tracking-widest uppercase mt-1">
              // CLUMOVE SAAS PORTAL
            </p>
          </div>

          {!resetEmailSent ? (
            <>
              {error && (
                <div className="p-3.5 rounded-xl border text-xs mb-6 text-center font-mono leading-relaxed animate-fade-in bg-rose-50/80 border-rose-250 text-rose-800">
                  {error}
                </div>
              )}

              <form onSubmit={handleForgotPassword} className="space-y-5">
                <div className="space-y-1.5">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                    E-Mail Adresse
                  </label>
                  <div className="relative group">
                    <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-[var(--color-text-muted)] group-focus-within:text-portal-orange transition-colors">
                      <Mail className="w-4 h-4" />
                    </span>
                    <input
                      type="email"
                      required
                      autoComplete="email"
                      placeholder="name@beispiel.de"
                      value={email}
                      onChange={(e) => setEmail(e.target.value)}
                      className="w-full pl-10 pr-4 py-2.5 bg-[var(--color-bg-secondary)]/50 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                    />
                  </div>
                </div>

                <button
                  type="submit"
                  disabled={loading}
                  className="w-full bg-gradient-to-r from-portal-orange to-orange-500 text-white hover:shadow-md hover:scale-[1.01] active:scale-[0.99] py-3 px-4 rounded-xl text-xs font-bold transition-all duration-300 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-portal-orange disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider font-mono cursor-pointer mt-2"
                >
                  {loading ? (
                    <span className="flex items-center justify-center gap-2">
                      <span className="animate-spin rounded-full h-4 w-4 border-2 border-white border-t-transparent"></span>
                      Wird verarbeitet...
                    </span>
                  ) : (
                    'Link senden'
                  )}
                </button>
              </form>
            </>
          ) : (
            <div className="p-4 rounded-xl border text-xs text-center font-mono leading-relaxed bg-emerald-50/80 border-emerald-200 text-emerald-800 mb-6">
              Falls ein Account mit dieser E-Mail existiert, wurde ein Link gesendet.
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
              className="text-portal-orange font-bold hover:underline transition-all cursor-pointer"
            >
              Zurück zum Login
            </button>
          </div>
        </div>
      </div>
    );
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);

    const trimmedEmail = email.trim();
    if (!trimmedEmail || !password || (!isLogin && !displayName.trim())) {
      setError('Bitte alle Felder ausfüllen.');
      setLoading(false);
      return;
    }

    const endpoint = isLogin ? '/api/auth/login' : '/api/auth/register';
    const payload = isLogin
      ? { email: trimmedEmail, password }
      : { email: trimmedEmail, password, display_name: displayName.trim() };

    try {
      const response = await fetch(`${apiUrl}${endpoint}`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        credentials: 'include',
        body: JSON.stringify(payload),
      });

      if (!response.ok) {
        const text = await response.text();
        throw new Error(text || 'Ein Fehler ist aufgetreten.');
      }

      if (isLogin) {
        const data = await response.json();
        if (data.totp_required && data.temp_session) {
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
        setError('Registrierung erfolgreich! Bitte logge dich ein.');
      }
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Verbindung zum Server fehlgeschlagen.');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="max-w-md w-full mx-auto my-8 px-4 relative">
      {/* Ambient background glow */}
      <div className="absolute -top-10 -left-10 w-40 h-40 bg-portal-orange/10 rounded-full blur-3xl pointer-events-none" />
      <div className="absolute -bottom-10 -right-10 w-40 h-40 bg-portal-navy/10 rounded-full blur-3xl pointer-events-none" />

      {/* Container Card with Premium Glassmorphism */}
      <div className="relative glass-panel rounded-3xl p-8 shadow-portal hover:shadow-portal-hover border border-[var(--color-glass-border)] transition-all duration-500 overflow-hidden">
        <div className="absolute top-0 left-0 w-full h-1.5 bg-gradient-to-r from-portal-orange via-orange-500 to-portal-navy" />
        
        {/* Brand header */}
        <div className="flex flex-col items-center mb-8">
          <div className="p-3 bg-gradient-to-tr from-portal-orange to-orange-500 rounded-2xl text-white shadow-sm mb-4 transition-transform hover:scale-105 duration-300">
            <CloudSync className="w-6 h-6 stroke-[2.5]" />
          </div>
          <h2 className="font-display font-extrabold text-2xl text-[var(--color-portal-navy-themed)] tracking-tight">
            {isLogin ? 'Willkommen zurück' : 'Account erstellen'}
          </h2>
          <p className="text-[9px] text-[var(--color-text-muted)] font-mono tracking-widest uppercase mt-1">
            {isLogin ? '// CLUMOVE SAAS PORTAL' : '// ACCOUNT REGISTRIERUNG'}
          </p>
        </div>

        {error && (
          <div className={`p-3.5 rounded-xl border text-xs mb-6 text-center font-mono leading-relaxed animate-fade-in ${
            error.includes('erfolgreich')
              ? 'bg-emerald-50/80 border-emerald-200 text-emerald-800'
              : 'bg-rose-50/80 border-rose-250 text-rose-800'
          }`}>
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-5">
          {/* Display Name - only for registration */}
          {!isLogin && (
            <div className="space-y-1.5">
              <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                Name
              </label>
              <div className="relative group">
                <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-[var(--color-text-muted)] group-focus-within:text-portal-orange transition-colors">
                  <User className="w-4 h-4" />
                </span>
                <input
                  type="text"
                  required
                  placeholder="Max Mustermann"
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                  className="w-full pl-10 pr-4 py-2.5 bg-[var(--color-bg-secondary)]/50 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                />
              </div>
            </div>
          )}

          {/* Email input */}
          <div className="space-y-1.5">
            <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
              E-Mail Adresse
            </label>
            <div className="relative group">
              <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-[var(--color-text-muted)] group-focus-within:text-portal-orange transition-colors">
                <Mail className="w-4 h-4" />
              </span>
              <input
                type="email"
                required
                autoComplete="email"
                placeholder="name@beispiel.de"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="w-full pl-10 pr-4 py-2.5 bg-[var(--color-bg-secondary)]/50 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
              />
            </div>
          </div>

          {/* Password input */}
          <div className="space-y-1.5">
            <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
              Passwort
            </label>
            <div className="relative group">
              <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-[var(--color-text-muted)] group-focus-within:text-portal-orange transition-colors">
                <Lock className="w-4 h-4" />
              </span>
              <input
                type={showPassword ? 'text' : 'password'}
                required
                autoComplete={isLogin ? 'current-password' : 'new-password'}
                placeholder="••••••••"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full pl-10 pr-10 py-2.5 bg-[var(--color-bg-secondary)]/50 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
              />
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
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
                className="text-xs font-mono text-[var(--color-text-muted)] hover:text-portal-orange transition-colors cursor-pointer underline-offset-2 hover:underline"
              >
                Passwort vergessen?
              </button>
            </div>
          )}

          {/* Submit Button */}
          <button
            type="submit"
            disabled={loading}
            className="w-full bg-gradient-to-r from-portal-orange to-orange-500 text-white hover:shadow-md hover:scale-[1.01] active:scale-[0.99] py-3 px-4 rounded-xl text-xs font-bold transition-all duration-300 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-portal-orange disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider font-mono cursor-pointer mt-2"
          >
            {loading ? (
              <span className="flex items-center justify-center gap-2">
                <span className="animate-spin rounded-full h-4 w-4 border-2 border-white border-t-transparent"></span>
                Wird verarbeitet...
              </span>
            ) : isLogin ? (
              'Anmelden'
            ) : (
              'Registrieren'
            )}
          </button>
        </form>

        {/* Toggle between login and registration */}
        <div className="mt-6 text-center text-xs font-mono text-[var(--color-text-muted)] border-t border-[var(--color-border)] pt-5">
          {isLogin ? (
            registrationsEnabled ? (
              <p>
                Noch keinen Account?{' '}
                <button
                  type="button"
                  onClick={() => {
                    setIsLogin(false);
                    setError('');
                  }}
                  className="text-portal-orange font-bold hover:underline transition-all cursor-pointer"
                >
                  Registrieren
                </button>
              </p>
            ) : (
              <p className="text-[var(--color-text-muted)]">
                Registrierungen sind derzeit deaktiviert.
              </p>
            )
          ) : (
            <p>
              Bereits registriert?{' '}
              <button
                type="button"
                onClick={() => {
                  setIsLogin(true);
                  setError('');
                }}
                className="text-portal-orange font-bold hover:underline transition-all cursor-pointer"
              >
                Anmelden
              </button>
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
