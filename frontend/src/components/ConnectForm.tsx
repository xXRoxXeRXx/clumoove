import React, { useState } from 'react';
import { Server, User, Key, ArrowRight, ShieldCheck, RefreshCw, AlertCircle } from 'lucide-react';

interface ConnectFormProps {
  onConnectSuccess: (config: any, initialFiles: any[]) => void;
  apiUrl: string;
}

export const ConnectForm: React.FC<ConnectFormProps> = ({ onConnectSuccess, apiUrl }) => {
  const [sourceUrl, setSourceUrl] = useState('');
  const [sourceUser, setSourceUser] = useState('');
  const [sourcePass, setSourcePass] = useState('');

  const [targetUrl, setTargetUrl] = useState('');
  const [targetUser, setTargetUser] = useState('');
  const [targetPass, setTargetPass] = useState('');

  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!sourceUrl || !sourceUser || !sourcePass || !targetUrl || !targetUser || !targetPass) {
      setError('Bitte füllen Sie alle Felder aus.');
      return;
    }

    setLoading(true);
    setError(null);

    try {
      const response = await fetch(`${apiUrl}/api/migration/connect`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          source_url: sourceUrl,
          source_username: sourceUser,
          source_password: sourcePass,
          target_url: targetUrl,
          target_username: targetUser,
          target_password: targetPass,
        }),
      });

      if (!response.ok) {
        throw new Error(`Server antwortete mit Status ${response.status}`);
      }

      const data = await response.json();
      if (data.success) {
        onConnectSuccess(
          {
            source_url: sourceUrl,
            source_username: sourceUser,
            source_password: sourcePass,
            target_url: targetUrl,
            target_username: targetUser,
            target_password: targetPass,
          },
          data.files || []
        );
      } else {
        setError(data.error || 'Verbindung fehlgeschlagen. Bitte überprüfen Sie Ihre Daten.');
      }
    } catch (err: any) {
      setError(err.message || 'Ein Netzwerkfehler ist aufgetreten.');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="w-full max-w-4xl mx-auto py-8 px-4">
      <div className="text-center mb-10">
        <div className="inline-flex items-center gap-2 px-3 py-1 rounded-full bg-blue-500/10 border border-blue-500/20 text-blue-400 text-sm mb-4">
          <ShieldCheck className="w-4 h-4" /> Zero-Data-Retention & Ende-zu-Ende-verschlüsselt
        </div>
        <h1 className="text-4xl font-extrabold tracking-tight bg-gradient-to-r from-blue-400 via-indigo-200 to-cyan-400 bg-clip-text text-transparent mb-3">
          Verbinden Sie Ihre Instanzen
        </h1>
        <p className="text-slate-400 max-w-md mx-auto">
          Geben Sie die Zugangsdaten der Quell- und Ziel-Nextcloud an. Verwenden Sie aus Sicherheitsgründen App-Passwörter.
        </p>
      </div>

      <form onSubmit={handleSubmit} className="space-y-6">
        <div className="grid md:grid-cols-2 gap-6">
          {/* Source Cloud Card */}
          <div className="glass shadow-glow p-6 rounded-2xl transition-all duration-300 hover:border-blue-500/30">
            <div className="flex items-center gap-3 mb-6">
              <div className="p-3 bg-blue-500/10 rounded-xl text-blue-400 border border-blue-500/20">
                <Server className="w-6 h-6" />
              </div>
              <div>
                <h2 className="text-xl font-bold text-slate-100">Quelle (Source)</h2>
                <p className="text-xs text-slate-400">Hier liegen Ihre Daten</p>
              </div>
            </div>

            <div className="space-y-4">
              <div>
                <label className="block text-xs font-semibold text-slate-400 uppercase tracking-wider mb-2">Nextcloud URL</label>
                <div className="relative">
                  <Server className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-500" />
                  <input
                    type="url"
                    placeholder="https://nextcloud.source-domain.com"
                    value={sourceUrl}
                    onChange={(e) => setSourceUrl(e.target.value)}
                    className="w-full bg-slate-900/50 border border-slate-800 rounded-xl py-3 pl-10 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-blue-500/50 focus:ring-2 focus:ring-blue-500/20 transition-all"
                    required
                  />
                </div>
              </div>

              <div>
                <label className="block text-xs font-semibold text-slate-400 uppercase tracking-wider mb-2">Benutzername</label>
                <div className="relative">
                  <User className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-500" />
                  <input
                    type="text"
                    placeholder="max.mustermann"
                    value={sourceUser}
                    onChange={(e) => setSourceUser(e.target.value)}
                    className="w-full bg-slate-900/50 border border-slate-800 rounded-xl py-3 pl-10 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-blue-500/50 focus:ring-2 focus:ring-blue-500/20 transition-all"
                    required
                  />
                </div>
              </div>

              <div>
                <label className="block text-xs font-semibold text-slate-400 uppercase tracking-wider mb-2">App-Passwort</label>
                <div className="relative">
                  <Key className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-500" />
                  <input
                    type="password"
                    placeholder="•••• •••• •••• ••••"
                    value={sourcePass}
                    onChange={(e) => setSourcePass(e.target.value)}
                    className="w-full bg-slate-900/50 border border-slate-800 rounded-xl py-3 pl-10 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-blue-500/50 focus:ring-2 focus:ring-blue-500/20 transition-all"
                    required
                  />
                </div>
              </div>
            </div>
          </div>

          {/* Target Cloud Card */}
          <div className="glass shadow-glow p-6 rounded-2xl transition-all duration-300 hover:border-indigo-500/30">
            <div className="flex items-center gap-3 mb-6">
              <div className="p-3 bg-indigo-500/10 rounded-xl text-indigo-400 border border-indigo-500/20">
                <Server className="w-6 h-6" />
              </div>
              <div>
                <h2 className="text-xl font-bold text-slate-100">Ziel (Target)</h2>
                <p className="text-xs text-slate-400">Hierhin sollen Ihre Daten</p>
              </div>
            </div>

            <div className="space-y-4">
              <div>
                <label className="block text-xs font-semibold text-slate-400 uppercase tracking-wider mb-2">Nextcloud URL</label>
                <div className="relative">
                  <Server className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-500" />
                  <input
                    type="url"
                    placeholder="https://nextcloud.target-domain.com"
                    value={targetUrl}
                    onChange={(e) => setTargetUrl(e.target.value)}
                    className="w-full bg-slate-900/50 border border-slate-800 rounded-xl py-3 pl-10 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-indigo-500/50 focus:ring-2 focus:ring-indigo-500/20 transition-all"
                    required
                  />
                </div>
              </div>

              <div>
                <label className="block text-xs font-semibold text-slate-400 uppercase tracking-wider mb-2">Benutzername</label>
                <div className="relative">
                  <User className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-500" />
                  <input
                    type="text"
                    placeholder="max.mustermann"
                    value={targetUser}
                    onChange={(e) => setTargetUser(e.target.value)}
                    className="w-full bg-slate-900/50 border border-slate-800 rounded-xl py-3 pl-10 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-indigo-500/50 focus:ring-2 focus:ring-indigo-500/20 transition-all"
                    required
                  />
                </div>
              </div>

              <div>
                <label className="block text-xs font-semibold text-slate-400 uppercase tracking-wider mb-2">App-Passwort</label>
                <div className="relative">
                  <Key className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-500" />
                  <input
                    type="password"
                    placeholder="•••• •••• •••• ••••"
                    value={targetPass}
                    onChange={(e) => setTargetPass(e.target.value)}
                    className="w-full bg-slate-900/50 border border-slate-800 rounded-xl py-3 pl-10 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-indigo-500/50 focus:ring-2 focus:ring-indigo-500/20 transition-all"
                    required
                  />
                </div>
              </div>
            </div>
          </div>
        </div>

        {error && (
          <div className="p-4 bg-rose-500/10 border border-rose-500/20 rounded-xl flex items-start gap-3 max-w-xl mx-auto">
            <AlertCircle className="w-5 h-5 text-rose-400 shrink-0 mt-0.5" />
            <div className="text-sm text-rose-300 font-medium">{error}</div>
          </div>
        )}

        <div className="flex justify-center pt-4">
          <button
            type="submit"
            disabled={loading}
            className="flex items-center gap-2 px-8 py-4 bg-gradient-to-r from-blue-500 to-indigo-600 hover:from-blue-600 hover:to-indigo-700 text-slate-100 rounded-xl font-semibold shadow-lg hover:shadow-indigo-500/10 disabled:opacity-50 disabled:cursor-not-allowed group transition-all duration-300"
          >
            {loading ? (
              <>
                <RefreshCw className="w-5 h-5 animate-spin" />
                Verbindung wird geprüft...
              </>
            ) : (
              <>
                Instanzen verbinden
                <ArrowRight className="w-5 h-5 group-hover:translate-x-1 transition-transform" />
              </>
            )}
          </button>
        </div>
      </form>
    </div>
  );
};
