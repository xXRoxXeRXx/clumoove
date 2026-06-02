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
        throw new Error(`Verbindung gescheitert. HTTP-Status ${response.status}`);
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
        setError(data.error || 'Verbindung fehlgeschlagen. Bitte überprüfe deine Angaben.');
      }
    } catch (err: any) {
      setError(err.message || 'Netzwerkfehler aufgetreten. Bitte prüfe die Instanz-Erreichbarkeit.');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="w-full max-w-4xl mx-auto py-2">
      
      {/* Title block */}
      <div className="text-left border-l-4 border-bauhaus-rust pl-6 mb-10">
        <div className="inline-flex items-center gap-2 font-mono text-[10px] font-extrabold uppercase tracking-widest text-slate-500 mb-2">
          <ShieldCheck className="w-4 h-4 text-bauhaus-moss" /> 
          <span>Protokoll: 256-Bit SSL-Pufferung</span>
        </div>
        
        <h1 className="font-serif font-black text-4xl md:text-5xl uppercase tracking-tight text-bauhaus-ink leading-tight mb-2">
          Instanzen verbinden
        </h1>
        
        <p className="text-sm font-medium text-slate-650 max-w-xl leading-relaxed">
          Kopplung von Quelle und Ziel zur verschlüsselten Direktübertragungs-Pufferung im Arbeitsspeicher des Gateways.
        </p>
      </div>

      {/* Structural Schema Pipeline */}
      <div className="hidden md:flex justify-between items-center max-w-2xl mx-auto mb-10 px-8">
        <div className="px-4 py-2 border-2 border-bauhaus-ink font-mono text-xs font-bold bg-bauhaus-rust text-white shadow-flat">
          [ QUELLE_HOST ]
        </div>
        
        {/* Architectural layout lines */}
        <div className="flex-grow mx-4 h-0.5 border-t-2 border-dashed border-bauhaus-ink relative">
          <div className="absolute top-1/2 -translate-y-1/2 w-4 h-4 bg-bauhaus-ink rotate-45" style={{ left: '48%' }}></div>
        </div>
        
        <div className="px-4 py-2 border-2 border-bauhaus-ink font-mono text-xs font-bold bg-bauhaus-moss text-white shadow-flat">
          [ ZIEL_HOST ]
        </div>
      </div>

      <form onSubmit={handleSubmit} className="space-y-8">
        <div className="grid md:grid-cols-2 gap-8">
          
          {/* Source Host Card (Rust theme) */}
          <div className="border-2 border-bauhaus-ink bg-bauhaus-sand p-6 shadow-flat hover:translate-x-[-1px] hover:translate-y-[-1px] hover:shadow-flat-lg transition-all duration-150 relative">
            <div className="absolute top-0 right-0 border-b-2 border-l-2 border-bauhaus-ink px-3 py-1 font-mono text-[9px] font-black uppercase text-bauhaus-rust">
              Egress
            </div>
            
            <div className="flex items-center gap-3 mb-6 border-b border-bauhaus-ink pb-4">
              <Server className="w-5 h-5 text-bauhaus-rust" />
              <h2 className="font-serif font-black text-lg uppercase tracking-tight">Quelle (Source)</h2>
            </div>

            <div className="space-y-4 font-mono text-xs">
              <div>
                <label className="block font-bold text-slate-500 uppercase tracking-widest mb-1.5">Nextcloud WebDAV-URL</label>
                <input
                  type="url"
                  placeholder="https://nextcloud.source.com"
                  value={sourceUrl}
                  onChange={(e) => setSourceUrl(e.target.value)}
                  className="w-full bg-white border-1.5 border-bauhaus-ink rounded-none py-3 px-4 text-bauhaus-ink placeholder-slate-400 focus:outline-none focus:border-bauhaus-rust focus:bg-white transition-colors"
                  required
                />
              </div>

              <div>
                <label className="block font-bold text-slate-500 uppercase tracking-widest mb-1.5">Benutzername</label>
                <input
                  type="text"
                  placeholder="max.mustermann"
                  value={sourceUser}
                  onChange={(e) => setSourceUser(e.target.value)}
                  className="w-full bg-white border-1.5 border-bauhaus-ink rounded-none py-3 px-4 text-bauhaus-ink placeholder-slate-400 focus:outline-none focus:border-bauhaus-rust focus:bg-white transition-colors"
                  required
                />
              </div>

              <div>
                <div className="flex justify-between items-center mb-1.5">
                  <label className="block font-bold text-slate-500 uppercase tracking-widest">App-Passwort</label>
                  <button
                    type="button"
                    onClick={() => setShowHelp(!showHelp)}
                    className="text-[10px] text-bauhaus-rust hover:underline font-extrabold uppercase tracking-wider flex items-center gap-1 cursor-pointer"
                  >
                    <HelpCircle className="w-3.5 h-3.5" /> Anleitung
                  </button>
                </div>
                <input
                  type="password"
                  placeholder="•••• •••• •••• ••••"
                  value={sourcePass}
                  onChange={(e) => setSourcePass(e.target.value)}
                  className="w-full bg-white border-1.5 border-bauhaus-ink rounded-none py-3 px-4 text-bauhaus-ink placeholder-slate-400 focus:outline-none focus:border-bauhaus-rust focus:bg-white transition-colors"
                  required
                />
              </div>
            </div>
          </div>

          {/* Target Host Card (Moss theme) */}
          <div className="border-2 border-bauhaus-ink bg-bauhaus-sand p-6 shadow-flat hover:translate-x-[-1px] hover:translate-y-[-1px] hover:shadow-flat-lg transition-all duration-150 relative">
            <div className="absolute top-0 right-0 border-b-2 border-l-2 border-bauhaus-ink px-3 py-1 font-mono text-[9px] font-black uppercase text-bauhaus-moss">
              Ingress
            </div>
            
            <div className="flex items-center gap-3 mb-6 border-b border-bauhaus-ink pb-4">
              <Server className="w-5 h-5 text-bauhaus-moss" />
              <h2 className="font-serif font-black text-lg uppercase tracking-tight">Ziel (Target)</h2>
            </div>

            <div className="space-y-4 font-mono text-xs">
              <div>
                <label className="block font-bold text-slate-500 uppercase tracking-widest mb-1.5">Nextcloud WebDAV-URL</label>
                <input
                  type="url"
                  placeholder="https://nextcloud.target.com"
                  value={targetUrl}
                  onChange={(e) => setTargetUrl(e.target.value)}
                  className="w-full bg-white border-1.5 border-bauhaus-ink rounded-none py-3 px-4 text-bauhaus-ink placeholder-slate-400 focus:outline-none focus:border-bauhaus-moss focus:bg-white transition-colors"
                  required
                />
              </div>

              <div>
                <label className="block font-bold text-slate-500 uppercase tracking-widest mb-1.5">Benutzername</label>
                <input
                  type="text"
                  placeholder="max.mustermann"
                  value={targetUser}
                  onChange={(e) => setTargetUser(e.target.value)}
                  className="w-full bg-white border-1.5 border-bauhaus-ink rounded-none py-3 px-4 text-bauhaus-ink placeholder-slate-400 focus:outline-none focus:border-bauhaus-moss focus:bg-white transition-colors"
                  required
                />
              </div>

              <div>
                <div className="flex justify-between items-center mb-1.5">
                  <label className="block font-bold text-slate-500 uppercase tracking-widest">App-Passwort</label>
                  <button
                    type="button"
                    onClick={() => setShowHelp(!showHelp)}
                    className="text-[10px] text-bauhaus-moss hover:underline font-extrabold uppercase tracking-wider flex items-center gap-1 cursor-pointer"
                  >
                    <HelpCircle className="w-3.5 h-3.5" /> Anleitung
                  </button>
                </div>
                <input
                  type="password"
                  placeholder="•••• •••• •••• ••••"
                  value={targetPass}
                  onChange={(e) => setTargetPass(e.target.value)}
                  className="w-full bg-white border-1.5 border-bauhaus-ink rounded-none py-3 px-4 text-bauhaus-ink placeholder-slate-400 focus:outline-none focus:border-bauhaus-moss focus:bg-white transition-colors"
                  required
                />
              </div>
            </div>
          </div>
        </div>

        {/* Instructive Pamphlet (App Password Guide) */}
        {showHelp && (
          <div className="border-2 border-bauhaus-ink bg-white p-6 shadow-flat max-w-2xl mx-auto font-mono text-[11px] leading-relaxed relative">
            <div className="absolute top-0 right-0 bg-bauhaus-yellow border-b-2 border-l-2 border-bauhaus-ink px-3 py-1 font-bold text-[9px]">
              GUIDE_REF.01
            </div>
            
            <h4 className="font-serif font-black text-sm uppercase tracking-tight text-bauhaus-ink mb-3">
              Anleitung: App-Passwort in Nextcloud erstellen
            </h4>
            
            <ol className="list-decimal list-inside space-y-2 text-slate-700 pl-1">
              <li>Öffne deine Nextcloud im Browser und logge dich ein.</li>
              <li>Klicke oben rechts auf dein Profilbild und gehe auf <strong className="text-bauhaus-ink">Einstellungen</strong>.</li>
              <li>Wähle im linken Seitenmenü den Punkt <strong className="text-bauhaus-ink">Sicherheit</strong>.</li>
              <li>Scrolle nach ganz unten zur Tabelle <strong className="text-bauhaus-ink">Geräte & Clients</strong>.</li>
              <li>Gib links einen App-Namen ein (z.B. <code className="bg-bauhaus-sand border border-bauhaus-ink px-1.5 py-0.5 font-bold">CloudMove</code>) und klicke auf den Button daneben.</li>
              <li>Kopiere das generierte Passwort und füge es oben ein (dein Hauptpasswort funktioniert oft nicht!).</li>
            </ol>
          </div>
        )}

        {error && (
          <div className="p-4 bg-white border-2 border-bauhaus-rust shadow-flat-rust max-w-xl mx-auto flex items-start gap-3">
            <AlertCircle className="w-5 h-5 text-bauhaus-rust shrink-0 mt-0.5" />
            <div className="text-xs font-mono font-bold text-bauhaus-rust uppercase leading-relaxed">{error}</div>
          </div>
        )}

        {/* Submit block-button */}
        <div className="flex justify-center pt-4">
          <button
            type="submit"
            disabled={loading}
            className="flex items-center gap-3 px-10 py-5 bg-bauhaus-rust text-white border-2 border-bauhaus-ink shadow-flat hover:translate-x-[2px] hover:translate-y-[2px] hover:shadow-flat-active active:translate-x-[4px] active:translate-y-[4px] active:shadow-none transition-all duration-150 font-serif text-lg font-black uppercase tracking-tight cursor-pointer disabled:opacity-50"
          >
            {loading ? (
              <>
                <RefreshCw className="w-5 h-5 animate-spin" />
                <span>Verbindung wird geprüft...</span>
              </>
            ) : (
              <>
                <span>Instanzen koppeln</span>
                <ArrowRight className="w-5 h-5 stroke-[3]" />
              </>
            )}
          </button>
        </div>
      </form>
    </div>
  );
};
