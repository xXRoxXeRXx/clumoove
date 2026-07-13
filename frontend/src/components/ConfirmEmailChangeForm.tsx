import { useState, useEffect } from 'react';
import { CheckCircle2, XCircle } from 'lucide-react';
interface ConfirmEmailChangeFormProps {
  apiUrl: string;
  token: string;
  onSuccess: () => void;
}

export function ConfirmEmailChangeForm({ apiUrl, token, onSuccess }: ConfirmEmailChangeFormProps) {
  const [error, setError] = useState<string>('');
  const [loading, setLoading] = useState<boolean>(true);
  const [success, setSuccess] = useState<boolean>(false);

  useEffect(() => {
    const confirm = async () => {
      setLoading(true);
      setError('');
      try {
        const response = await fetch(`${apiUrl}/api/auth/confirm-email-change`, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({ token }),
        });

        if (!response.ok) {
          const text = await response.text();
          throw new Error(text || 'Die E-Mail-Änderung ist fehlgeschlagen.');
        }

        setSuccess(true);
        setTimeout(() => onSuccess(), 1800);
      } catch (err: unknown) {
        setError(err instanceof Error ? err.message : 'Verbindung zum Server fehlgeschlagen.');
      } finally {
        setLoading(false);
      }
    };

    confirm();
  }, [apiUrl, token, onSuccess]);

  if (loading) {
    return (
      <div className="max-w-md w-full mx-auto my-8 px-4 relative">
        <div className="absolute -top-10 -left-10 w-40 h-40 bg-portal-orange/10 rounded-full blur-3xl pointer-events-none" />
        <div className="absolute -bottom-10 -right-10 w-40 h-40 bg-portal-navy/10 rounded-full blur-3xl pointer-events-none" />

        <div className="relative glass-panel rounded-3xl p-8 shadow-portal border border-[var(--color-glass-border)] transition-all duration-500 text-center">
          <div className="absolute top-0 left-0 w-full h-1.5 bg-gradient-to-r from-portal-orange via-orange-500 to-portal-navy" />
          <div className="flex flex-col items-center gap-4 py-4">
            <span className="animate-spin rounded-full h-12 w-12 border-2 border-portal-orange border-t-transparent" />
            <h2 className="font-display font-extrabold text-xl text-[var(--color-portal-navy-themed)] tracking-tight">
              E-Mail-Adresse wird geändert
            </h2>
            <p className="text-xs text-[var(--color-text-muted)] font-mono leading-relaxed">
              Bitte einen Moment Geduld...
            </p>
          </div>
        </div>
      </div>
    );
  }

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
              E-Mail-Adresse geändert
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
      <div className="absolute -top-10 -left-10 w-40 h-40 bg-rose-500/10 rounded-full blur-3xl pointer-events-none" />
      <div className="absolute -bottom-10 -right-10 w-40 h-40 bg-portal-orange/10 rounded-full blur-3xl pointer-events-none" />

      <div className="relative glass-panel rounded-3xl p-8 shadow-portal border border-[var(--color-glass-border)] transition-all duration-500 text-center">
        <div className="absolute top-0 left-0 w-full h-1.5 bg-gradient-to-r from-rose-500 via-red-500 to-portal-orange" />
        <div className="flex flex-col items-center gap-4 py-4">
          <div className="p-4 bg-rose-500/10 rounded-2xl text-rose-600">
            <XCircle className="w-12 h-12" />
          </div>
          <h2 className="font-display font-extrabold text-xl text-[var(--color-portal-navy-themed)] tracking-tight">
            Link ungültig
          </h2>
          <p className="text-xs text-[var(--color-text-muted)] font-mono leading-relaxed">
            {error || 'Dieser Bestätigungslink ist ungültig oder abgelaufen.'}
          </p>
        </div>
      </div>
    </div>
  );
}
