import React, { useState } from 'react';
import { Server, ArrowRight, ShieldCheck, RefreshCw, AlertCircle, HelpCircle } from 'lucide-react';

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
        throw new Error(`Die Verbindung konnte nicht hergestellt werden. HTTP-Status ${response.status}`);
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
        setError(data.error || 'Verbindung fehlgeschlagen. Bitte prüfe deine Zugangsdaten.');
      }
    } catch (err: any) {
      setError(err.message || 'Ein Netzwerkfehler ist aufgetreten. Bitte überprüfe deine Internetverbindung.');
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
