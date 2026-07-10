import React, { useState } from 'react';
import { Server, ArrowRight, RefreshCw, AlertCircle, HelpCircle } from 'lucide-react';

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
  source_refresh_token: string;
  source_token_expires_in: number;
  target_url: string;
  target_username: string;
  target_password: string;
  target_refresh_token: string;
  target_token_expires_in: number;
  source_provider: 'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb';
  target_provider: 'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb';
}

interface ConnectFormProps {
  onConnectSuccess: (config: MigrationConfig, initialFiles: CloudFile[]) => void;
  apiUrl: string;
  token: string;
}

export const ConnectForm: React.FC<ConnectFormProps> = ({ onConnectSuccess, apiUrl, token }) => {
  const [sourceUrl, setSourceUrl] = useState('');
  const [sourceUser, setSourceUser] = useState('');
  const [sourcePass, setSourcePass] = useState('');
  const [sourceRefreshToken, setSourceRefreshToken] = useState('');
  const [sourceTokenExpiresIn, setSourceTokenExpiresIn] = useState(0);

  const [targetUrl, setTargetUrl] = useState('');
  const [targetUser, setTargetUser] = useState('');
  const [targetPass, setTargetPass] = useState('');
  const [targetRefreshToken, setTargetRefreshToken] = useState('');
  const [targetTokenExpiresIn, setTargetTokenExpiresIn] = useState(0);
  const [sourceProvider, setSourceProvider] = useState<'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb'>('nextcloud');
  const [targetProvider, setTargetProvider] = useState<'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb'>('nextcloud');
  const [sourceOAuthUser, setSourceOAuthUser] = useState('');
  const [targetOAuthUser, setTargetOAuthUser] = useState('');

  const [sourceSmbHost, setSourceSmbHost] = useState('');
  const [sourceSmbPort, setSourceSmbPort] = useState('445');
  const [sourceSmbShare, setSourceSmbShare] = useState('');
  const [sourceSmbDomain, setSourceSmbDomain] = useState('');

  const [targetSmbHost, setTargetSmbHost] = useState('');
  const [targetSmbPort, setTargetSmbPort] = useState('445');
  const [targetSmbShare, setTargetSmbShare] = useState('');
  const [targetSmbDomain, setTargetSmbDomain] = useState('');

  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showHelp, setShowHelp] = useState(false);

  const openOAuthPopup = (provider: string, type: 'source' | 'target') => {
    const width = 600;
    const height = 700;
    const left = window.screen.width / 2 - width / 2;
    const top = window.screen.height / 2 - height / 2;

    const targetOrigin = new URL(apiUrl, window.location.origin).origin;

    const popup = window.open(
      `${apiUrl}/api/oauth/auth?provider=${provider}&origin=${encodeURIComponent(window.location.origin)}`,
      'OAuth',
      `width=${width},height=${height},left=${left},top=${top}`
    );

    const cleanup = () => {
      window.removeEventListener('message', handleMessage);
      clearInterval(checkClosedInterval);
    };

    const handleMessage = (event: MessageEvent) => {
      if (event.origin !== targetOrigin) {
        return;
      }
      if (event.source !== popup) {
        return; // Ensure the event was posted from our specific popup window (I7 fix)
      }
      if (event.data && event.data.type === 'oauth-success' && event.data.provider === provider) {
        if (type === 'source') {
          setSourceOAuthUser(event.data.username || provider);
          setSourceUrl(`https://api.${provider}.com`);
          setSourceUser(event.data.username || provider);
          setSourcePass(event.data.token);
          setSourceRefreshToken(event.data.refreshToken || '');
          setSourceTokenExpiresIn(event.data.expiresIn || 3600);
        } else {
          setTargetOAuthUser(event.data.username || provider);
          setTargetUrl(`https://api.${provider}.com`);
          setTargetUser(event.data.username || provider);
          setTargetPass(event.data.token);
          setTargetRefreshToken(event.data.refreshToken || '');
          setTargetTokenExpiresIn(event.data.expiresIn || 3600);
        }
        cleanup();
      } else if (event.data && event.data.type === 'oauth-error') {
        setError(`OAuth Fehler: ${event.data.error}`);
        cleanup();
      }
    };

    // Periodically check if user closed the popup manually to clean up listener leaks (I7 fix)
    const checkClosedInterval = setInterval(() => {
      if (!popup || popup.closed) {
        cleanup();
      }
    }, 1000);

    window.addEventListener('message', handleMessage);
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const finalSourceUrl = sourceProvider === 'smb'
      ? `smb://${sourceSmbHost}:${sourceSmbPort}/${sourceSmbShare.replace(/^\//, '')}${sourceSmbDomain ? '?domain=' + encodeURIComponent(sourceSmbDomain) : ''}`
      : ((sourceProvider === 'dropbox' || sourceProvider === 'google') ? `https://api.${sourceProvider}.com` : sourceUrl);
    const finalSourceUser = (sourceProvider === 'dropbox' || sourceProvider === 'google') ? (sourceOAuthUser || sourceProvider) : sourceUser;
    const finalTargetUrl = targetProvider === 'smb'
      ? `smb://${targetSmbHost}:${targetSmbPort}/${targetSmbShare.replace(/^\//, '')}${targetSmbDomain ? '?domain=' + encodeURIComponent(targetSmbDomain) : ''}`
      : ((targetProvider === 'dropbox' || targetProvider === 'google') ? `https://api.${targetProvider}.com` : targetUrl);
    const finalTargetUser = (targetProvider === 'dropbox' || targetProvider === 'google') ? (targetOAuthUser || targetProvider) : targetUser;

    if (sourceProvider === 'smb') {
      if (!sourceSmbHost.trim() || !sourceSmbShare.trim()) {
        setError('Bitte gib einen Server Host und einen Freigabe-Namen für die Quelle an.');
        return;
      }
    }
    if (targetProvider === 'smb') {
      if (!targetSmbHost.trim() || !targetSmbShare.trim()) {
        setError('Bitte gib einen Server Host und einen Freigabe-Namen für das Ziel an.');
        return;
      }
    }

    if (!finalSourceUrl || !finalSourceUser || !sourcePass || !finalTargetUrl || !finalTargetUser || !targetPass) {
      setError('Bitte fülle alle Eingabefelder aus bzw. autorisiere die Anbieter.');
      return;
    }

    setLoading(true);
    setError(null);

    try {
      const response = await fetch(`${apiUrl}/api/migration/connect`, {
        method: 'POST',
        headers: { 
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify({
          source_url: finalSourceUrl,
          source_username: finalSourceUser,
          source_password: sourcePass,
          source_refresh_token: sourceRefreshToken,
          source_token_expires_in: sourceTokenExpiresIn,
          target_url: finalTargetUrl,
          target_username: finalTargetUser,
          target_password: targetPass,
          target_refresh_token: targetRefreshToken,
          target_token_expires_in: targetTokenExpiresIn,
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
            source_refresh_token: sourceRefreshToken,
            source_token_expires_in: sourceTokenExpiresIn,
            target_url: finalTargetUrl,
            target_username: finalTargetUser,
            target_password: targetPass,
            target_refresh_token: targetRefreshToken,
            target_token_expires_in: targetTokenExpiresIn,
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
                    const val = e.target.value as 'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb';
                    setSourceProvider(val);
                    if (val === 'dropbox' || val === 'google') {
                      setSourceUrl(`https://api.${val}.com`);
                      setSourceUser(val);
                      setSourcePass('');
                      setSourceOAuthUser('');
                    } else if (val === 'smb') {
                      setSourceUrl('');
                      setSourceUser('');
                      setSourcePass('');
                      setSourceSmbHost('');
                      setSourceSmbPort('445');
                      setSourceSmbShare('');
                      setSourceSmbDomain('');
                    } else {
                      setSourceUrl('');
                      setSourceUser('');
                      setSourcePass('');
                    }
                  }}
                  className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                >
                  <option value="nextcloud">Nextcloud (WebDAV)</option>
                  <option value="webdav">Generischer WebDAV-Server</option>
                  <option value="smb">SMB/CIFS Freigabe</option>
                  <option value="dropbox">Dropbox (OAuth2)</option>
                  <option value="google">Google (OAuth2)</option>
                </select>
              </div>

              {sourceProvider === 'smb' ? (
                <>
                  <div className="grid grid-cols-3 gap-4">
                    <div className="col-span-2">
                      <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Server Host / IP</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={sourceSmbHost}
                        onChange={(e) => setSourceSmbHost(e.target.value)}
                        className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                        required
                      />
                    </div>
                    <div>
                      <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Port</label>
                      <input
                        type="text"
                        placeholder="445"
                        value={sourceSmbPort}
                        onChange={(e) => setSourceSmbPort(e.target.value)}
                        className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="grid grid-cols-2 gap-4">
                    <div>
                      <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Freigabe-Name (Share)</label>
                      <input
                        type="text"
                        placeholder="projekte"
                        value={sourceSmbShare}
                        onChange={(e) => setSourceSmbShare(e.target.value)}
                        className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                        required
                      />
                    </div>
                    <div>
                      <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Domain (Optional)</label>
                      <input
                        type="text"
                        placeholder="WORKGROUP"
                        value={sourceSmbDomain}
                        onChange={(e) => setSourceSmbDomain(e.target.value)}
                        className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                      />
                    </div>
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
                    <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Kennwort / Passwort</label>
                    <input
                      type="password"
                      placeholder="passwort"
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                      required
                    />
                  </div>
                </>
              ) : sourceProvider === 'nextcloud' || sourceProvider === 'webdav' ? (
                <>
                  <div>
                    <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">
                      {sourceProvider === 'nextcloud' ? 'Nextcloud WebDAV-URL' : 'WebDAV-URL'}
                    </label>
                    <input
                      type="url"
                      placeholder={sourceProvider === 'nextcloud' ? 'https://nextcloud.source-domain.de' : 'https://webdav.domain.de/dav'}
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
                  <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-2">
                    {sourceProvider === 'google' ? 'Google Verbindung' : 'Dropbox Verbindung'}
                  </label>
                  {sourcePass ? (
                    <div className="bg-emerald-50 border border-emerald-200 text-emerald-800 rounded-lg p-4 flex items-center justify-between shadow-sm">
                      <div className="truncate pr-2">
                        <p className="font-bold text-[10.5px] uppercase tracking-wider text-emerald-650">Verbunden als</p>
                        <p className="text-xs font-bold text-slate-700 truncate">{sourceOAuthUser || (sourceProvider === 'google' ? 'Google Account' : 'Dropbox Account')}</p>
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
                      onClick={() => openOAuthPopup(sourceProvider, 'source')}
                      className="w-full py-3.5 px-4 bg-portal-navy text-white font-display font-bold text-xs uppercase tracking-wider rounded-lg shadow-sm hover:bg-portal-navy/90 hover:scale-101 active:scale-99 transition-all cursor-pointer flex items-center justify-center gap-2"
                    >
                      <RefreshCw className="w-4 h-4" /> Mit {sourceProvider === 'google' ? 'Google' : 'Dropbox'} verbinden
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
                    const val = e.target.value as 'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb';
                    setTargetProvider(val);
                    if (val === 'dropbox' || val === 'google') {
                      setTargetUrl(`https://api.${val}.com`);
                      setTargetUser(val);
                      setTargetPass('');
                      setTargetOAuthUser('');
                    } else if (val === 'smb') {
                      setTargetUrl('');
                      setTargetUser('');
                      setTargetPass('');
                      setTargetSmbHost('');
                      setTargetSmbPort('445');
                      setTargetSmbShare('');
                      setTargetSmbDomain('');
                    } else {
                      setTargetUrl('');
                      setTargetUser('');
                      setTargetPass('');
                    }
                  }}
                  className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                >
                  <option value="nextcloud">Nextcloud (WebDAV)</option>
                  <option value="webdav">Generischer WebDAV-Server</option>
                  <option value="smb">SMB/CIFS Freigabe</option>
                  <option value="dropbox">Dropbox (OAuth2)</option>
                  <option value="google">Google (OAuth2)</option>
                </select>
              </div>

              {targetProvider === 'smb' ? (
                <>
                  <div className="grid grid-cols-3 gap-4">
                    <div className="col-span-2">
                      <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Server Host / IP</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={targetSmbHost}
                        onChange={(e) => setTargetSmbHost(e.target.value)}
                        className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                        required
                      />
                    </div>
                    <div>
                      <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Port</label>
                      <input
                        type="text"
                        placeholder="445"
                        value={targetSmbPort}
                        onChange={(e) => setTargetSmbPort(e.target.value)}
                        className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="grid grid-cols-2 gap-4">
                    <div>
                      <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Freigabe-Name (Share)</label>
                      <input
                        type="text"
                        placeholder="projekte"
                        value={targetSmbShare}
                        onChange={(e) => setTargetSmbShare(e.target.value)}
                        className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                        required
                      />
                    </div>
                    <div>
                      <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Domain (Optional)</label>
                      <input
                        type="text"
                        placeholder="WORKGROUP"
                        value={targetSmbDomain}
                        onChange={(e) => setTargetSmbDomain(e.target.value)}
                        className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                      />
                    </div>
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
                    <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">Kennwort / Passwort</label>
                    <input
                      type="password"
                      placeholder="passwort"
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className="w-full bg-white border border-portal-border rounded-lg py-3 px-4 text-slate-800 placeholder-slate-400 focus:outline-none focus:border-portal-orange/60 focus:ring-4 focus:ring-portal-orange/10 transition-all font-sans"
                      required
                    />
                  </div>
                </>
              ) : targetProvider === 'nextcloud' || targetProvider === 'webdav' ? (
                <>
                  <div>
                    <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-1.5">
                      {targetProvider === 'nextcloud' ? 'Nextcloud WebDAV-URL' : 'WebDAV-URL'}
                    </label>
                    <input
                      type="url"
                      placeholder={targetProvider === 'nextcloud' ? 'https://nextcloud.target-domain.de' : 'https://webdav.domain.de/dav'}
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
                  <label className="block font-display font-bold text-slate-500 uppercase tracking-wider mb-2">
                    {targetProvider === 'google' ? 'Google Verbindung' : 'Dropbox Verbindung'}
                  </label>
                  {targetPass ? (
                    <div className="bg-emerald-50 border border-emerald-200 text-emerald-800 rounded-lg p-4 flex items-center justify-between shadow-sm">
                      <div className="truncate pr-2">
                        <p className="font-bold text-[10.5px] uppercase tracking-wider text-emerald-650">Verbunden als</p>
                        <p className="text-xs font-bold text-slate-700 truncate">{targetOAuthUser || (targetProvider === 'google' ? 'Google Account' : 'Dropbox Account')}</p>
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
                      onClick={() => openOAuthPopup(targetProvider, 'target')}
                      className="w-full py-3.5 px-4 bg-portal-navy text-white font-display font-bold text-xs uppercase tracking-wider rounded-lg shadow-sm hover:bg-portal-navy/90 hover:scale-101 active:scale-99 transition-all cursor-pointer flex items-center justify-center gap-2"
                    >
                      <RefreshCw className="w-4 h-4" /> Mit {targetProvider === 'google' ? 'Google' : 'Dropbox'} verbinden
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
              <li>Gib links einen App-Namen ein (z. B. <code className="bg-slate-200/50 border border-slate-300 px-1.5 py-0.5 rounded font-mono text-[10px] font-bold">Clumove</code>) und klicke auf <strong className="text-slate-800">Neues App-Passwort erstellen</strong>.</li>
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
