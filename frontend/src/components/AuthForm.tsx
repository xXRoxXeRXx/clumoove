import { useState, useEffect } from 'react';
import { CloudLightning, Lock, Mail, User, Eye, EyeOff } from 'lucide-react';

interface AuthFormProps {
  apiUrl: string;
  onAuthSuccess: (token: string, user: any) => void;
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
        onAuthSuccess(data.access_token, data.user);
      } else {
        // Registration success: switch to login and show success message
        setIsLogin(true);
        setPassword('');
        setError('Registrierung erfolgreich! Bitte logge dich ein.');
      }
    } catch (err: any) {
      setError(err.message || 'Verbindung zum Server fehlgeschlagen.');
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
      <div className="relative glass-panel rounded-3xl p-8 shadow-portal hover:shadow-portal-hover border border-white/50 transition-all duration-500 overflow-hidden">
        <div className="absolute top-0 left-0 w-full h-1.5 bg-gradient-to-r from-portal-orange via-orange-500 to-portal-navy" />
        
        {/* Brand header */}
        <div className="flex flex-col items-center mb-8">
          <div className="p-3 bg-gradient-to-tr from-portal-orange to-orange-500 rounded-2xl text-white shadow-sm mb-4 transition-transform hover:scale-105 duration-300">
            <CloudLightning className="w-6 h-6 stroke-[2.5]" />
          </div>
          <h2 className="font-display font-extrabold text-2xl text-portal-navy tracking-tight">
            {isLogin ? 'Willkommen zurück' : 'Account erstellen'}
          </h2>
          <p className="text-[9px] text-slate-400 font-mono tracking-widest uppercase mt-1">
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
              <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">
                Name
              </label>
              <div className="relative group">
                <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-slate-400 group-focus-within:text-portal-orange transition-colors">
                  <User className="w-4 h-4" />
                </span>
                <input
                  type="text"
                  required
                  placeholder="Max Mustermann"
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                  className="w-full pl-10 pr-4 py-2.5 bg-white/50 border border-slate-200/80 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                />
              </div>
            </div>
          )}

          {/* Email input */}
          <div className="space-y-1.5">
            <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">
              E-Mail Adresse
            </label>
            <div className="relative group">
              <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-slate-400 group-focus-within:text-portal-orange transition-colors">
                <Mail className="w-4 h-4" />
              </span>
              <input
                type="email"
                required
                autoComplete="email"
                placeholder="name@beispiel.de"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="w-full pl-10 pr-4 py-2.5 bg-white/50 border border-slate-200/80 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
              />
            </div>
          </div>

          {/* Password input */}
          <div className="space-y-1.5">
            <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">
              Passwort
            </label>
            <div className="relative group">
              <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-slate-400 group-focus-within:text-portal-orange transition-colors">
                <Lock className="w-4 h-4" />
              </span>
              <input
                type={showPassword ? 'text' : 'password'}
                required
                autoComplete={isLogin ? 'current-password' : 'new-password'}
                placeholder="••••••••"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full pl-10 pr-10 py-2.5 bg-white/50 border border-slate-200/80 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans font-mono"
              />
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
                className="absolute inset-y-0 right-0 pr-3.5 flex items-center text-slate-450 hover:text-slate-600 transition-colors"
              >
                {showPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
              </button>
            </div>
          </div>

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
        <div className="mt-6 text-center text-xs font-mono text-slate-450 border-t border-slate-200/40 pt-5">
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
              <p className="text-slate-400">
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
