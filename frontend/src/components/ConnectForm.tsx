import React, { useState } from 'react';
import { Server, User, Key, ArrowRight, ShieldCheck, RefreshCw, AlertCircle, HelpCircle } from 'lucide-react';

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
  const [showHelp, setShowHelp] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!sourceUrl || !sourceUser || !sourcePass || !targetUrl || !targetUser || !targetPass) {
      setError('Bitte fülle alle Eingabefelder aus.');
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
        throw new Error(`Der Server hat mit Status ${response.status} geantwortet.`);
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
        setError(data.error || 'Verbindung fehlgeschlagen. Bitte überprüfe deine Zugangsdaten.');
      }
    } catch (err: any) {
      setError(err.message || 'Ein Netzwerkfehler ist aufgetreten. Bitte prüfe deine Internetverbindung.');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="w-full max-w-4xl mx-auto py-4 px-2">
      {/* Welcome Banner */}
      <div className="text-center mb-8">
        <div className="inline-flex items-center gap-2 px-4 py-1.5 rounded-full bg-cozy-mint/10 border border-cozy-mint/20 text-cozy-mint-light text-xs font-semibold mb-4 shadow-sm">
          <ShieldCheck className="w-4 h-4 text-cozy-mint" /> 
          <span>Sichere Ende-zu-Ende-Verbindung</span>
        </div>
        <h1 className="text-4xl font-display font-extrabold tracking-tight bg-gradient-to-r from-cozy-indigo via-cozy-coral to-cozy-peach bg-clip-text text-transparent mb-3">
          Verbinde deine Instanzen
        </h1>
        <p className="text-slate-400 max-w-lg mx-auto text-sm leading-relaxed">
          Lass uns deine Quell- und Ziel-Nextcloud koppeln. Die Übertragung erfolgt direkt und sicher im RAM-Speicher unseres Servers.
        </p>
      </div>

      {/* Visual Connection Bridge Diagram */}
      <div className="hidden md:flex justify-between items-center max-w-2xl mx-auto mb-10 px-8 relative">
        <div className="w-12 h-12 rounded-2xl bg-cozy-indigo/10 border border-cozy-indigo/30 flex items-center justify-center text-cozy-indigo shadow-md shadow-cozy-indigo/5">
          <Server className="w-6 h-6" />
        </div>
        {/* Animated flow track */}
        <div className="flex-grow mx-4 h-2 bg-slate-900 border border-slate-850 rounded-full relative overflow-hidden">
          <div className="absolute top-0 bottom-0 left-0 bg-gradient-to-r from-cozy-indigo via-cozy-coral to-cozy-mint rounded-full w-2/3 animate-pulse-slow"></div>
          {/* Pulsing bubble indicator */}
          <div className="absolute top-1/2 -translate-y-1/2 w-3 h-3 bg-cozy-peach rounded-full shadow-cozy-coral animate-ping" style={{ left: '40%' }}></div>
        </div>
        <div className="w-12 h-12 rounded-2xl bg-cozy-coral/10 border border-cozy-coral/30 flex items-center justify-center text-cozy-peach shadow-md shadow-cozy-coral/5">
          <Server className="w-6 h-6" />
        </div>
      </div>

      <form onSubmit={handleSubmit} className="space-y-6">
        <div className="grid md:grid-cols-2 gap-6">
          {/* Source Cloud Card (Indigo Glow) */}
          <div className="cozy-glass p-6 rounded-3xl transition-all duration-300 hover:border-cozy-indigo/35 hover:shadow-cozy-indigo/5 group">
            <div className="flex items-center gap-3 mb-6">
              <div className="p-3 bg-cozy-indigo/10 rounded-2xl text-cozy-indigo border border-cozy-indigo/20 group-hover:scale-105 transition-all">
                <Server className="w-6 h-6" />
              </div>
              <div>
                <h2 className="text-lg font-display font-bold text-slate-100">Quelle (Source)</h2>
                <p className="text-xs text-slate-400">Hier liegen deine umzuziehenden Daten</p>
              </div>
            </div>

            <div className="space-y-4">
              <div>
                <label className="block text-[11px] font-display font-semibold text-slate-400 uppercase tracking-wider mb-2">Nextcloud URL</label>
                <div className="relative">
                  <Server className="absolute left-3.5 top-1/2 -translate-y-1/2 w-4.5 h-4.5 text-slate-500" />
                  <input
                    type="url"
                    placeholder="https://nextcloud.quell-domain.de"
                    value={sourceUrl}
                    onChange={(e) => setSourceUrl(e.target.value)}
                    className="w-full bg-slate-900/40 border border-slate-800 rounded-xl py-3 pl-11 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-cozy-indigo/60 focus:ring-4 focus:ring-cozy-indigo/10 transition-all font-sans"
                    required
                  />
                </div>
              </div>

              <div>
                <label className="block text-[11px] font-display font-semibold text-slate-400 uppercase tracking-wider mb-2">Benutzername</label>
                <div className="relative">
                  <User className="absolute left-3.5 top-1/2 -translate-y-1/2 w-4.5 h-4.5 text-slate-500" />
                  <input
                    type="text"
                    placeholder="benutzername"
                    value={sourceUser}
                    onChange={(e) => setSourceUser(e.target.value)}
                    className="w-full bg-slate-900/40 border border-slate-800 rounded-xl py-3 pl-11 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-cozy-indigo/60 focus:ring-4 focus:ring-cozy-indigo/10 transition-all font-sans"
                    required
                  />
                </div>
              </div>

              <div>
                <div className="flex justify-between items-center mb-2">
                  <label className="block text-[11px] font-display font-semibold text-slate-400 uppercase tracking-wider">App-Passwort</label>
                  <button
                    type="button"
                    onClick={() => setShowHelp(!showHelp)}
                    className="text-[11px] text-cozy-indigo hover:text-cozy-peach font-semibold flex items-center gap-1 transition-colors"
                  >
                    <HelpCircle className="w-3.5 h-3.5" /> Wie finde ich das?
                  </button>
                </div>
                <div className="relative">
                  <Key className="absolute left-3.5 top-1/2 -translate-y-1/2 w-4.5 h-4.5 text-slate-500" />
                  <input
                    type="password"
                    placeholder="•••• •••• •••• ••••"
                    value={sourcePass}
                    onChange={(e) => setSourcePass(e.target.value)}
                    className="w-full bg-slate-900/40 border border-slate-800 rounded-xl py-3 pl-11 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-cozy-indigo/60 focus:ring-4 focus:ring-cozy-indigo/10 transition-all font-sans"
                    required
                  />
                </div>
              </div>
            </div>
          </div>

          {/* Target Cloud Card (Coral/Peach Glow) */}
          <div className="cozy-glass p-6 rounded-3xl transition-all duration-300 hover:border-cozy-coral/35 hover:shadow-cozy-coral/5 group">
            <div className="flex items-center gap-3 mb-6">
              <div className="p-3 bg-cozy-coral/10 rounded-2xl text-cozy-peach border border-cozy-coral/20 group-hover:scale-105 transition-all">
                <Server className="w-6 h-6" />
              </div>
              <div>
                <h2 className="text-lg font-display font-bold text-slate-100">Ziel (Target)</h2>
                <p className="text-xs text-slate-400">Hierhin sollen deine Daten migriert werden</p>
              </div>
            </div>

            <div className="space-y-4">
              <div>
                <label className="block text-[11px] font-display font-semibold text-slate-400 uppercase tracking-wider mb-2">Nextcloud URL</label>
                <div className="relative">
                  <Server className="absolute left-3.5 top-1/2 -translate-y-1/2 w-4.5 h-4.5 text-slate-500" />
                  <input
                    type="url"
                    placeholder="https://nextcloud.ziel-domain.de"
                    value={targetUrl}
                    onChange={(e) => setTargetUrl(e.target.value)}
                    className="w-full bg-slate-900/40 border border-slate-800 rounded-xl py-3 pl-11 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-cozy-coral/60 focus:ring-4 focus:ring-cozy-coral/10 transition-all font-sans"
                    required
                  />
                </div>
              </div>

              <div>
                <label className="block text-[11px] font-display font-semibold text-slate-400 uppercase tracking-wider mb-2">Benutzername</label>
                <div className="relative">
                  <User className="absolute left-3.5 top-1/2 -translate-y-1/2 w-4.5 h-4.5 text-slate-500" />
                  <input
                    type="text"
                    placeholder="benutzername"
                    value={targetUser}
                    onChange={(e) => setTargetUser(e.target.value)}
                    className="w-full bg-slate-900/40 border border-slate-800 rounded-xl py-3 pl-11 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-cozy-coral/60 focus:ring-4 focus:ring-cozy-coral/10 transition-all font-sans"
                    required
                  />
                </div>
              </div>

              <div>
                <div className="flex justify-between items-center mb-2">
                  <label className="block text-[11px] font-display font-semibold text-slate-400 uppercase tracking-wider">App-Passwort</label>
                  <button
                    type="button"
                    onClick={() => setShowHelp(!showHelp)}
                    className="text-[11px] text-cozy-coral hover:text-cozy-peach font-semibold flex items-center gap-1 transition-colors"
                  >
                    <HelpCircle className="w-3.5 h-3.5" /> Wie finde ich das?
                  </button>
                </div>
                <div className="relative">
                  <Key className="absolute left-3.5 top-1/2 -translate-y-1/2 w-4.5 h-4.5 text-slate-500" />
                  <input
                    type="password"
                    placeholder="•••• •••• •••• ••••"
                    value={targetPass}
                    onChange={(e) => setTargetPass(e.target.value)}
                    className="w-full bg-slate-900/40 border border-slate-800 rounded-xl py-3 pl-11 pr-4 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-cozy-coral/60 focus:ring-4 focus:ring-cozy-coral/10 transition-all font-sans"
                    required
                  />
                </div>
              </div>
            </div>
          </div>
        </div>

        {/* Helpful Info Guide Panel */}
        {showHelp && (
          <div className="cozy-glass p-5 rounded-2xl border border-cozy-indigo/20 max-w-2xl mx-auto shadow-md animate-float">
            <h4 className="font-display font-bold text-sm text-slate-100 flex items-center gap-2 mb-2">
              <span className="text-cozy-peach text-base">💡</span>
              So erstellst du ein App-Passwort in deiner Nextcloud:
            </h4>
            <ol className="list-decimal list-inside text-xs text-slate-350 space-y-1.5 leading-relaxed pl-1">
              <li>Melde dich in deiner Nextcloud über den Webbrowser an.</li>
              <li>Klicke oben rechts auf dein Profilbild und wähle <strong className="text-slate-250">Einstellungen</strong>.</li>
              <li>Klicke im linken Menü auf <strong className="text-slate-250">Sicherheit</strong>.</li>
              <li>Scrolle ganz nach unten zum Bereich <strong className="text-slate-250">Geräte & Clients</strong>.</li>
              <li>Trage in das Eingabefeld einen Namen ein (z. B. <code className="bg-slate-900 px-1.5 py-0.5 rounded text-cozy-peach">CloudMove</code>) und klicke auf <strong className="text-slate-250">Neues App-Passwort erstellen</strong>.</li>
              <li>Kopiere das angezeigte Passwort und verwende es hier anstelle deines Hauptpassworts.</li>
            </ol>
          </div>
        )}

        {error && (
          <div className="p-4 bg-rose-500/10 border border-rose-550/20 rounded-2xl flex items-start gap-3 max-w-xl mx-auto animate-pulse">
            <AlertCircle className="w-5 h-5 text-rose-450 shrink-0 mt-0.5" />
            <div className="text-sm text-rose-250 font-medium">{error}</div>
          </div>
        )}

        <div className="flex justify-center pt-4">
          <button
            type="submit"
            disabled={loading}
            className="flex items-center gap-2.5 px-10 py-4.5 bg-gradient-to-r from-cozy-indigo via-cozy-coral to-cozy-peach text-white rounded-2xl font-display font-bold shadow-lg hover:shadow-cozy-coral/20 disabled:opacity-50 disabled:cursor-not-allowed group transition-all duration-300 hover:scale-102 cursor-pointer"
          >
            {loading ? (
              <>
                <RefreshCw className="w-5 h-5 animate-spin" />
                <span>Prüfe Verbindung...</span>
              </>
            ) : (
              <>
                <span>Instanzen verbinden</span>
                <ArrowRight className="w-5 h-5 group-hover:translate-x-1.5 transition-transform" />
              </>
            )}
          </button>
        </div>
      </form>
    </div>
  );
};
