import { useState } from 'react';
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
    <div className="max-w-md w-full mx-auto my-10 px-4">
      {/* Container Card with Premium Glassmorphism and Drop Navy Border */}
      <div className="bg-white/80 backdrop-blur-md rounded-2xl border border-portal-border p-8 shadow-portal hover:shadow-portal-hover transition-all duration-300">
        
        {/* Brand header */}
        <div className="flex flex-col items-center mb-8">
          <div className="p-3 bg-portal-orange rounded-xl text-white shadow-sm mb-4">
            <CloudLightning className="w-6 h-6 stroke-[2.5] animate-pulse" />
          </div>
          <h2 className="font-display font-extrabold text-2xl text-portal-navy tracking-tight">
            {isLogin ? 'Willkommen zurück' : 'Account erstellen'}
          </h2>
          <p className="text-xs text-slate-500 font-mono tracking-wider uppercase mt-1">
            {isLogin ? '// CLUMOVE SAAS PORTAL' : '// REGISTRIERUNG'}
          </p>
        </div>

        {error && (
          <div className={`p-3.5 rounded-lg border text-xs mb-6 text-center font-mono leading-relaxed ${
            error.includes('erfolgreich')
              ? 'bg-emerald-50 border-emerald-200 text-emerald-800'
              : 'bg-rose-50 border-rose-150 text-rose-800'
          }`}>
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-5">
          {/* Display Name - only for registration */}
          {!isLogin && (
            <div>
              <label className="block text-xs font-bold text-portal-navy uppercase tracking-wider mb-2 font-mono">
                Name
              </label>
              <div className="relative">
                <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-slate-400">
                  <User className="w-4 h-4" />
                </span>
                <input
                  type="text"
                  required
                  placeholder="Max Mustermann"
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                  className="w-full pl-10 pr-4 py-2.5 bg-slate-50/50 border border-portal-border rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange focus:bg-white transition-all font-sans"
                />
              </div>
            </div>
          )}

          {/* Email input */}
          <div>
            <label className="block text-xs font-bold text-portal-navy uppercase tracking-wider mb-2 font-mono">
              E-Mail Adresse
            </label>
            <div className="relative">
              <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-slate-400">
                <Mail className="w-4 h-4" />
              </span>
              <input
                type="email"
                required
                placeholder="name@beispiel.de"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="w-full pl-10 pr-4 py-2.5 bg-slate-50/50 border border-portal-border rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange focus:bg-white transition-all font-sans"
              />
            </div>
          </div>

          {/* Password input */}
          <div>
            <label className="block text-xs font-bold text-portal-navy uppercase tracking-wider mb-2 font-mono">
              Passwort
            </label>
            <div className="relative">
              <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-slate-400">
                <Lock className="w-4 h-4" />
              </span>
              <input
                type={showPassword ? 'text' : 'password'}
                required
                placeholder="••••••••"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full pl-10 pr-10 py-2.5 bg-slate-50/50 border border-portal-border rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange focus:bg-white transition-all font-sans font-mono"
              />
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
                className="absolute inset-y-0 right-0 pr-3 flex items-center text-slate-400 hover:text-slate-600 transition-colors"
              >
                {showPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
              </button>
            </div>
          </div>

          {/* Submit Button */}
          <button
            type="submit"
            disabled={loading}
            className="w-full bg-portal-orange text-white hover:bg-portal-orange-hover py-3 px-4 rounded-xl text-sm font-bold shadow-sm transition-all focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-portal-orange disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider font-mono"
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
        <div className="mt-6 text-center text-xs font-mono text-slate-500 border-t border-portal-border/60 pt-5">
          {isLogin ? (
            <p>
              Noch keinen Account?{' '}
              <button
                type="button"
                onClick={() => {
                  setIsLogin(false);
                  setError('');
                }}
                className="text-portal-orange font-bold hover:underline transition-all"
              >
                Registrieren
              </button>
            </p>
          ) : (
            <p>
              Bereits registriert?{' '}
              <button
                type="button"
                onClick={() => {
                  setIsLogin(true);
                  setError('');
                }}
                className="text-portal-orange font-bold hover:underline transition-all"
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
