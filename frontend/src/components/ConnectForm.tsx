import React, { useState } from 'react';
import { Server, ArrowRight, ShieldCheck, RefreshCw, AlertCircle, HelpCircle } from 'lucide-react';

interface CloudFile {
  path: string;
  name: string;
  size: number;
  is_dir: boolean;
  hash: string;
  last_modified: string;
}

interface MigrationConfig {
  source_url: string;
  source_username: string;
  source_password: string;
  target_url: string;
  target_username: string;
  target_password: string;
  source_provider: 'nextcloud' | 'dropbox';
  target_provider: 'nextcloud' | 'dropbox';
}

interface ConnectFormProps {
  onConnectSuccess: (config: MigrationConfig, initialFiles: CloudFile[]) => void;
  apiUrl: string;
}

export const ConnectForm: React.FC<ConnectFormProps> = ({ onConnectSuccess, apiUrl }) => {
  const [sourceUrl, setSourceUrl] = useState('');
  const [sourceUser, setSourceUser] = useState('');
  const [sourcePass, setSourcePass] = useState('');

  const [targetUrl, setTargetUrl] = useState('');
  const [targetUser, setTargetUser] = useState('');
  const [targetPass, setTargetPass] = useState('');

  const [sourceProvider, setSourceProvider] = useState<'nextcloud' | 'dropbox'>('nextcloud');
  const [targetProvider, setTargetProvider] = useState<'nextcloud' | 'dropbox'>('nextcloud');
  const [sourceOAuthUser, setSourceOAuthUser] = useState('');
  const [targetOAuthUser, setTargetOAuthUser] = useState('');

  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showHelp, setShowHelp] = useState(false);

  const openOAuthPopup = (provider: string, type: 'source' | 'target') => {
    const width = 600;
    const height = 700;
    const left = window.screen.width / 2 - width / 2;
    const top = window.screen.height / 2 - height / 2;

    const targetOrigin = new URL(apiUrl, window.location.origin).origin;

    window.open(
      `${apiUrl}/api/oauth/auth?provider=${provider}&origin=${encodeURIComponent(window.location.origin)}`,
      'OAuth',
      `width=${width},height=${height},left=${left},top=${top}`
    );

    const handleMessage = (event: MessageEvent) => {
      if (event.origin !== targetOrigin) return;
      if (event.data && event.data.type === 'oauth-success' && event.data.provider === provider) {
        if (type === 'source') {
          setSourceOAuthUser(event.data.username || 'dropbox');
          setSourceUrl('https://api.dropboxapi.com');
          setSourceUser(event.data.username || 'dropbox');
          setSourcePass(event.data.token);
        } else {
          setTargetOAuthUser(event.data.username || 'dropbox');
          setTargetUrl('https://api.dropboxapi.com');
          setTargetUser(event.data.username || 'dropbox');
          setTargetPass(event.data.token);
        }
        window.removeEventListener('message', handleMessage);
      } else if (event.data && event.data.type === 'oauth-error') {
        setError(`OAuth Fehler: ${event.data.error}`);
        window.removeEventListener('message', handleMessage);
      }
    };

    window.addEventListener('message', handleMessage);
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const finalSourceUrl = sourceProvider === 'dropbox' ? 'https://api.dropboxapi.com' : sourceUrl;
    const finalSourceUser = sourceProvider === 'dropbox' ? (sourceOAuthUser || 'dropbox') : sourceUser;
    const finalTargetUrl = targetProvider === 'dropbox' ? 'https://api.dropboxapi.com' : targetUrl;
    const finalTargetUser = targetProvider === 'dropbox' ? (targetOAuthUser || 'dropbox') : targetUser;

    if (!finalSourceUrl || !finalSourceUser || !sourcePass || !finalTargetUrl || !finalTargetUser || !targetPass) {
      setError('Bitte fülle alle Eingabefelder aus bzw. autorisiere Dropbox.');
      return;
    }

    setLoading(true);
    setError(null);

    try {
      const response = await fetch(`${apiUrl}/api/migration/connect`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          source_url: finalSourceUrl,
          source_username: finalSourceUser,
          source_password: sourcePass,
          target_url: finalTargetUrl,
          target_username: finalTargetUser,
          target_password: targetPass,
          source_provider: sourceProvider,
          target_provider: targetProvider,
        }),
      });

      if (!response.ok) {
        throw new Error(`Die Verbindung konnte nicht hergestellt werden. HTTP-Status ${response.status}`);
      }

      const data = await response.json();
      if (data.success) {
        onConnectSuccess(
          {
            source_url: finalSourceUrl,
            source_username: finalSourceUser,
            source_password: sourcePass,
            target_url: finalTargetUrl,
            target_username: finalTargetUser,
            target_password: targetPass,
            source_provider: sourceProvider,
            target_provider: targetProvider,
          },
          data.files || []
        );
      } else {
        setError(data.error || 'Verbindung fehlgeschlagen. Bitte prüfe deine Zugangsdaten.');
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Ein Netzwerkfehler ist aufgetreten. Bitte überprüfe deine Internetverbindung.');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="w-full max-w-4xl mx-auto py-2">
      
      {/* Title & Info */}
      <div className="text-center mb-10">
        <div className="inline-flex items-center gap-2 px-3.5 py-1.5 rounded-full bg-emerald-50 text-emerald-700 border border-emerald-200 text-xs font-semibold mb-4 shadow-sm">
          <ShieldCheck className="w-4 h-4 text-emerald-650" /> 
          <span>Verschlüsselte Verbindung (SSL)</span>
        </div>
        
        <h1 className="font-display font-extrabold text-3xl md:text-4xl text-portal-navy tracking-tight mb-2">
          Verbinde deine Instanzen
        </h1>
        
        <p className="text-sm text-slate-550 max-w-lg mx-auto leading-relaxed">
          Gibe hier die Serveradressen und Zugangsdaten deiner Nextcloud-Instanzen an. Für die Sicherheit deines Haupt-Accounts empfehlen wir App-Passwörter.
        </p>
      </div>

      {/* Gateway schematic pipeline */}
      <div className="hidden md:flex justify-between items-center max-w-2xl mx-auto mb-10 px-8">
        <div className="px-5 py-2.5 bg-portal-navy text-white text-xs font-bold rounded-lg shadow-sm">
          Quelle (Egress)
        </div>
        
        <div className="flex-grow mx-4 h-0.5 border-t-2 border-dashed border-slate-300 relative">
          <div className="absolute top-1/2 -translate-y-1/2 w-3.5 h-3.5 bg-portal-orange rounded-full shadow-sm animate-pulse" style={{ left: '48%' }}></div>
        </div>
        
        <div className="px-5 py-2.5 bg-portal-navy text-white text-xs font-bold rounded-lg shadow-sm">
          Ziel (Ingress)
        </div>
      </div>

      <form onSubmit={handleSubmit} className="space-y-6">
        <div className="grid md:grid-cols-2 gap-8">
          
          {/* Source Host Card (White with Indigo Border indicator) */}
          <div className="bg-white border border-portal-border hover:border-portal-navy/40 rounded-lg p-6 shadow-portal transition-all duration-200 group">
            <div className="flex items-center gap-3 mb-6 border-b border-portal-border pb-4">
              <div className="p-2.5 bg-slate-50 border border-portal-border text-portal-navy rounded-lg group-hover:bg-portal-navy/5 transition-colors">
                <Server className="w-5 h-5" />
              </div>
              <div>
                <h2 className="font-display font-bold text-lg text-portal-navy">Quelle (Source)</h2>
                <p className="text-xs text-slate-450">Verzeichnis mit den Quelldaten</p>
              </div>
            </div>

            <div className="space-y-4 text-xs">
              <div>
                <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Anbieter (Provider)</label>
                <select
                  value={sourceProvider}
                  onChange={(e) => {
                    const val = e.target.value as 'nextcloud' | 'dropbox';
                    setSourceProvider(val);
                    if (val === 'dropbox') {
                      setSourceUrl('https://api.dropboxapi.com');
                      setSourceUser('dropbox');
                      setSourcePass('');
                      setSourceOAuthUser('');
                    } else {
                      setSourceUrl('');
                      setSourceUser('');
                      setSourcePass('');
                    }
                  }}
                  className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                >
                  <option value="nextcloud">Nextcloud (WebDAV)</option>
                  <option value="dropbox">Dropbox (OAuth2)</option>
                </select>
              </div>

              {sourceProvider === 'nextcloud' ? (
                <>
                  <div>
                    <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Nextcloud WebDAV-URL</label>
                    <input
                      type="url"
                      placeholder="https://nextcloud.source-domain.de"
                      value={sourceUrl}
                      onChange={(e) => setSourceUrl(e.target.value)}
                      className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                      required
                    />
                  </div>

                  <div>
                    <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Benutzername</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                      required
                    />
                  </div>

                  <div>
                    <div className="flex justify-between items-center mb-1.5">
                      <label className="block font-display font-bold text-slate-500 uppercase tracking-wider">App-Passwort</label>
                      <button
                        type="button"
                        onClick={() => setShowHelp(!showHelp)}
                        className="text-[10.5px] text-portal-orange hover:text-portal-orange-hover hover:underline font-bold uppercase tracking-wider flex items-center gap-1 cursor-pointer"
                      >
                        <HelpCircle className="w-3.5 h-3.5" /> Hilfe-Anleitung
                      </button>
                    </div>
                    <input
                      type="password"
                      placeholder="•••• •••• •••• ••••"
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                      required
                    />
                  </div>
                </>
              ) : (
                <div className="py-2">
                  <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-2">Dropbox Verbindung</label>
                  {sourcePass ? (
                    <div className="bg-emerald-50 border border-emerald-200 text-emerald-800 rounded-lg p-4 flex items-center justify-between shadow-sm">
                      <div className="truncate pr-2">
                        <p className="font-bold text-[10.5px] uppercase tracking-wider text-emerald-650">Verbunden als</p>
                        <p className="text-xs font-bold text-slate-700 truncate">{sourceOAuthUser || 'Dropbox Account'}</p>
                      </div>
                      <button
                        type="button"
                        onClick={() => {
                          setSourcePass('');
                          setSourceOAuthUser('');
                        }}
                        className="px-3.5 py-1.5 bg-white border border-emerald-250 text-emerald-700 text-xs font-bold rounded shadow-sm hover:bg-emerald-100 active:scale-97 transition-all cursor-pointer"
                      >
                        Trennen
                      </button>
                    </div>
                  ) : (
                    <button
                      type="button"
                      onClick={() => openOAuthPopup('dropbox', 'source')}
                      className="w-full py-3.5 px-4 bg-portal-navy text-white font-display font-bold text-xs uppercase tracking-wider rounded-lg shadow-sm hover:bg-portal-navy/90 hover:scale-101 active:scale-99 transition-all cursor-pointer flex items-center justify-center gap-2"
                    >
                      <RefreshCw className="w-4 h-4" /> Mit Dropbox verbinden
                    </button>
                  )}
                </div>
              )}
            </div>
          </div>

          {/* Target Host Card */}
          <div className="bg-white border border-portal-border hover:border-portal-navy/40 rounded-lg p-6 shadow-portal transition-all duration-200 group">
            <div className="flex items-center gap-3 mb-6 border-b border-portal-border pb-4">
              <div className="p-2.5 bg-slate-50 border border-portal-border text-portal-navy rounded-lg group-hover:bg-portal-navy/5 transition-colors">
                <Server className="w-5 h-5" />
              </div>
              <div>
                <h2 className="font-display font-bold text-lg text-portal-navy">Ziel (Target)</h2>
                <p className="text-xs text-slate-455">Zielverzeichnis für die Migration</p>
              </div>
            </div>

            <div className="space-y-4 text-xs">
              <div>
                <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Anbieter (Provider)</label>
                <select
                  value={targetProvider}
                  onChange={(e) => {
                    const val = e.target.value as 'nextcloud' | 'dropbox';
                    setTargetProvider(val);
                    if (val === 'dropbox') {
                      setTargetUrl('https://api.dropboxapi.com');
                      setTargetUser('dropbox');
                      setTargetPass('');
                      setTargetOAuthUser('');
                    } else {
                      setTargetUrl('');
                      setTargetUser('');
                      setTargetPass('');
                    }
                  }}
                  className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                >
                  <option value="nextcloud">Nextcloud (WebDAV)</option>
                  <option value="dropbox">Dropbox (OAuth2)</option>
                </select>
              </div>

              {targetProvider === 'nextcloud' ? (
                <>
                  <div>
                    <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Nextcloud WebDAV-URL</label>
                    <input
                      type="url"
                      placeholder="https://nextcloud.target-domain.de"
                      value={targetUrl}
                      onChange={(e) => setTargetUrl(e.target.value)}
                      className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                      required
                    />
                  </div>

                  <div>
                    <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Benutzername</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                      required
                    />
                  </div>

                  <div>
                    <div className="flex justify-between items-center mb-1.5">
                      <label className="block font-display font-bold text-slate-500 uppercase tracking-wider">App-Passwort</label>
                      <button
                        type="button"
                        onClick={() => setShowHelp(!showHelp)}
                        className="text-[10.5px] text-portal-orange hover:text-portal-orange-hover hover:underline font-bold uppercase tracking-wider flex items-center gap-1 cursor-pointer"
                      >
                        <HelpCircle className="w-3.5 h-3.5" /> Hilfe-Anleitung
                      </button>
                    </div>
                    <input
                      type="password"
                      placeholder="•••• •••• •••• ••••"
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                      required
                    />
                  </div>
                </>
              ) : (
                <div className="py-2">
                  <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-2">Dropbox Verbindung</label>
                  {targetPass ? (
                    <div className="bg-emerald-50 border border-emerald-200 text-emerald-800 rounded-lg p-4 flex items-center justify-between shadow-sm">
                      <div className="truncate pr-2">
                        <p className="font-bold text-[10.5px] uppercase tracking-wider text-emerald-650">Verbunden als</p>
                        <p className="text-xs font-bold text-slate-700 truncate">{targetOAuthUser || 'Dropbox Account'}</p>
                      </div>
                      <button
                        type="button"
                        onClick={() => {
                          setTargetPass('');
                          setTargetOAuthUser('');
                        }}
                        className="px-3.5 py-1.5 bg-white border border-emerald-250 text-emerald-700 text-xs font-bold rounded shadow-sm hover:bg-emerald-100 active:scale-97 transition-all cursor-pointer"
                      >
                        Trennen
                      </button>
                    </div>
                  ) : (
                    <button
                      type="button"
                      onClick={() => openOAuthPopup('dropbox', 'target')}
                      className="w-full py-3.5 px-4 bg-portal-navy text-white font-display font-bold text-xs uppercase tracking-wider rounded-lg shadow-sm hover:bg-portal-navy/90 hover:scale-101 active:scale-99 transition-all cursor-pointer flex items-center justify-center gap-2"
                    >
                      <RefreshCw className="w-4 h-4" /> Mit Dropbox verbinden
                    </button>
                  )}
                </div>
              )}
            </div>
          </div>
        </div>

        {/* Helpful Info Guide Box */}
        {showHelp && (
          <div className="bg-slate-50 border border-portal-border p-6 rounded-lg max-w-2xl mx-auto shadow-sm animate-pulse text-xs leading-relaxed text-slate-650">
            <h4 className="font-display font-bold text-sm text-portal-navy mb-3">
              💡 Anleitung zur App-Passwort-Erstellung in deiner Nextcloud:
            </h4>
            <ol className="list-decimal list-inside space-y-2 text-slate-600 pl-1">
              <li>Melde dich in deiner Nextcloud über den Webbrowser an.</li>
              <li>Klicke oben rechts auf dein Profilbild und wähle <strong className="text-slate-800">Einstellungen</strong>.</li>
              <li>Klicke im linken Menü auf <strong className="text-slate-800">Sicherheit</strong>.</li>
              <li>Scrolle ganz nach unten zu <strong className="text-slate-800">Geräte & Clients</strong>.</li>
              <li>Gib links einen App-Namen ein (z. B. <code className="bg-slate-200/50 border border-slate-300 px-1.5 py-0.5 rounded font-mono text-[10px] font-bold">CloudMove</code>) und klicke auf <strong className="text-slate-800">Neues App-Passwort erstellen</strong>.</li>
              <li>Kopiere das generierte Passwort und füge es oben ein (dein Hauptpasswort funktioniert oft nicht!).</li>
            </ol>
          </div>
        )}

        {error && (
          <div className="p-4 bg-rose-50 border border-rose-200 rounded-lg flex items-start gap-3 max-w-xl mx-auto">
            <AlertCircle className="w-5 h-5 text-rose-600 shrink-0 mt-0.5" />
            <div className="text-xs font-semibold text-rose-700 leading-normal">{error}</div>
          </div>
        )}

        {/* Action Button */}
        <div className="flex justify-center pt-4">
          <button
            type="submit"
            disabled={loading}
            className="flex items-center gap-2.5 px-10 py-4 bg-portal-orange text-white font-display text-base font-bold rounded-lg shadow-sm hover:bg-portal-orange-hover hover:scale-101 active:scale-99 transition-all cursor-pointer disabled:opacity-50"
          >
            {loading ? (
              <>
                <RefreshCw className="w-5 h-5 animate-spin" />
                <span>Verbindung wird geprüft...</span>
              </>
            ) : (
              <>
                <span>Instanzen verbinden</span>
                <ArrowRight className="w-5 h-5 stroke-[2.5]" />
              </>
            )}
          </button>
        </div>
      </form>
    </div>
  );
};
