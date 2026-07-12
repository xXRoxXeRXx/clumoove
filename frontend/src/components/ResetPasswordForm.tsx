import { useState } from 'react';
import { CloudSync, Lock, Eye, EyeOff, CheckCircle2, XCircle } from 'lucide-react';

interface ResetPasswordFormProps {
  apiUrl: string;
  token: string;
  onSuccess: () => void;
}

function getPasswordStrength(password: string): { score: number; label: string; color: string } {
  if (password.length === 0) return { score: 0, label: '', color: '' };
  let score = 0;
  if (password.length >= 8) score++;
  if (password.length >= 12) score++;
  if (/[A-Z]/.test(password) && /[a-z]/.test(password)) score++;
  if (/\d/.test(password)) score++;
  if (/[^A-Za-z0-9]/.test(password)) score++;

  if (score <= 1) return { score, label: 'Schwach', color: 'bg-rose-500' };
  if (score <= 3) return { score, label: 'Mittel', color: 'bg-amber-500' };
  return { score, label: 'Stark', color: 'bg-emerald-500' };
}

export function ResetPasswordForm({ apiUrl, token, onSuccess }: ResetPasswordFormProps) {
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

    if (password.length < 8) {
      setError('Das Passwort muss mindestens 8 Zeichen lang sein.');
      return;
    }

    if (password !== confirmPassword) {
      setError('Die Passwörter stimmen nicht überein.');
      return;
    }

    setLoading(true);

    try {
      const response = await fetch(`${apiUrl}/api/auth/reset-password`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ token, new_password: password }),
      });

      if (!response.ok) {
        const text = await response.text();
        throw new Error(text || 'Das Zurücksetzen des Passworts ist fehlgeschlagen.');
      }

      setSuccess(true);
      setTimeout(() => onSuccess(), 1500);
    } catch (err: any) {
      setError(err.message || 'Verbindung zum Server fehlgeschlagen.');
    } finally {
      setLoading(false);
    }
  };

  if (success) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4 relative">
        <div className="absolute -top-10 -left-10 w-40 h-40 bg-emerald-500/10 rounded-full blur-3xl pointer-events-none" />
        <div className="absolute -bottom-10 -right-10 w-40 h-40 bg-portal-orange/10 rounded-full blur-3xl pointer-events-none" />

        <div className="relative glass-panel rounded-3xl p-8 shadow-portal border border-[var(--color-glass-border)] transition-all duration-500 text-center">
          <div className="absolute top-0 left-0 w-full h-1.5 bg-gradient-to-r from-emerald-500 via-green-500 to-portal-orange" />
          <div className="flex flex-col items-center gap-4 py-4">
            <div className="p-4 bg-emerald-500/10 rounded-2xl text-emerald-600">
              <CheckCircle2 className="w-12 h-12" />
            </div>
            <h2 className="font-display font-extrabold text-xl text-[var(--color-portal-navy-themed)] tracking-tight">
              Passwort geändert
            </h2>
            <p className="text-xs text-[var(--color-text-muted)] font-mono leading-relaxed">
              Du wirst zur Anmeldung weitergeleitet...
            </p>
          </div>
        </div>
      </div>
    );
  }

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
            Neues Passwort
          </h2>
          <p className="text-[9px] text-[var(--color-text-muted)] font-mono tracking-widest uppercase mt-1">
            // CLUMOVE SAAS PORTAL
          </p>
        </div>

        {error && (
          <div className="p-3.5 rounded-xl border text-xs mb-6 text-center font-mono leading-relaxed animate-fade-in bg-rose-50/80 border-rose-250 text-rose-800 flex items-center justify-center gap-2">
            <XCircle className="w-4 h-4 shrink-0" />
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-5">
          <div className="space-y-1.5">
            <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
              Neues Passwort
            </label>
            <div className="relative group">
              <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-[var(--color-text-muted)] group-focus-within:text-portal-orange transition-colors">
                <Lock className="w-4 h-4" />
              </span>
              <input
                type={showPassword ? 'text' : 'password'}
                required
                autoComplete="new-password"
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

            {password.length > 0 && (
              <div className="flex items-center gap-2 mt-2">
                <div className="flex-1 h-1.5 bg-[var(--color-border)] rounded-full overflow-hidden">
                  <div
                    className={`h-full ${strength.color} transition-all duration-300`}
                    style={{ width: `${(strength.score / 5) * 100}%` }}
                  />
                </div>
                <span className="text-[9px] font-mono text-[var(--color-text-muted)] uppercase">{strength.label}</span>
              </div>
            )}
          </div>

          <div className="space-y-1.5">
            <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
              Passwort bestätigen
            </label>
            <div className="relative group">
              <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-[var(--color-text-muted)] group-focus-within:text-portal-orange transition-colors">
                <Lock className="w-4 h-4" />
              </span>
              <input
                type={showConfirmPassword ? 'text' : 'password'}
                required
                autoComplete="new-password"
                placeholder="••••••••"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                className="w-full pl-10 pr-10 py-2.5 bg-[var(--color-bg-secondary)]/50 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
              />
              <button
                type="button"
                onClick={() => setShowConfirmPassword(!showConfirmPassword)}
                className="absolute inset-y-0 right-0 pr-3.5 flex items-center text-[var(--color-text-muted)] hover:text-[var(--color-text-secondary)] transition-colors"
              >
                {showConfirmPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
              </button>
            </div>
          </div>

          <button
            type="submit"
            disabled={loading || password.length < 8 || password !== confirmPassword}
            className="w-full bg-gradient-to-r from-portal-orange to-orange-500 text-white hover:shadow-md hover:scale-[1.01] active:scale-[0.99] py-3 px-4 rounded-xl text-xs font-bold transition-all duration-300 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-portal-orange disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider font-mono cursor-pointer mt-2"
          >
            {loading ? (
              <span className="flex items-center justify-center gap-2">
                <span className="animate-spin rounded-full h-4 w-4 border-2 border-white border-t-transparent"></span>
                Wird verarbeitet...
              </span>
            ) : (
              'Passwort zurücksetzen'
            )}
          </button>
        </form>
      </div>
    </div>
  );
}
