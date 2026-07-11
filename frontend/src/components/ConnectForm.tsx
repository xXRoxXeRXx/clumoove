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
  source_provider: 'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb' | 's3';
  target_provider: 'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb' | 's3';
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
  const [sourceProvider, setSourceProvider] = useState<'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb' | 's3'>('nextcloud');
  const [targetProvider, setTargetProvider] = useState<'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb' | 's3'>('nextcloud');
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

  const [sourceS3Endpoint, setSourceS3Endpoint] = useState('');
  const [sourceS3Region, setSourceS3Region] = useState('us-east-1');
  const [sourceS3Bucket, setSourceS3Bucket] = useState('');
  const [sourceS3Insecure, setSourceS3Insecure] = useState(false);

  const [targetS3Endpoint, setTargetS3Endpoint] = useState('');
  const [targetS3Region, setTargetS3Region] = useState('us-east-1');
  const [targetS3Bucket, setTargetS3Bucket] = useState('');
  const [targetS3Insecure, setTargetS3Insecure] = useState(false);

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
      : sourceProvider === 's3'
      ? `s3://${sourceS3Bucket}?region=${encodeURIComponent(sourceS3Region)}${sourceS3Endpoint ? '&endpoint=' + encodeURIComponent(sourceS3Endpoint) : ''}${sourceS3Insecure ? '&insecure=true' : ''}`
      : ((sourceProvider === 'dropbox' || sourceProvider === 'google') ? `https://api.${sourceProvider}.com` : sourceUrl);
    const finalSourceUser = (sourceProvider === 'dropbox' || sourceProvider === 'google') ? (sourceOAuthUser || sourceProvider) : sourceUser;
    const finalTargetUrl = targetProvider === 'smb'
      ? `smb://${targetSmbHost}:${targetSmbPort}/${targetSmbShare.replace(/^\//, '')}${targetSmbDomain ? '?domain=' + encodeURIComponent(targetSmbDomain) : ''}`
      : targetProvider === 's3'
      ? `s3://${targetS3Bucket}?region=${encodeURIComponent(targetS3Region)}${targetS3Endpoint ? '&endpoint=' + encodeURIComponent(targetS3Endpoint) : ''}${targetS3Insecure ? '&insecure=true' : ''}`
      : ((targetProvider === 'dropbox' || targetProvider === 'google') ? `https://api.${targetProvider}.com` : targetUrl);
    const finalTargetUser = (targetProvider === 'dropbox' || targetProvider === 'google') ? (targetOAuthUser || targetProvider) : targetUser;

    if (sourceProvider === 'smb') {
      if (!sourceSmbHost.trim() || !sourceSmbShare.trim()) {
        setError('Bitte gib einen Server Host und einen Freigabe-Namen für die Quelle an.');
        return;
      }
    }
    if (sourceProvider === 's3') {
      if (!sourceS3Bucket.trim() || !sourceS3Region.trim()) {
        setError('Bitte gib einen Bucket-Namen und eine Region für die Quelle an.');
        return;
      }
    }
    if (targetProvider === 'smb') {
      if (!targetSmbHost.trim() || !targetSmbShare.trim()) {
        setError('Bitte gib einen Server Host und einen Freigabe-Namen für das Ziel an.');
        return;
      }
    }
    if (targetProvider === 's3') {
      if (!targetS3Bucket.trim() || !targetS3Region.trim()) {
        setError('Bitte gib einen Bucket-Namen und eine Region für das Ziel an.');
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
  const handleSourceProviderSelect = (val: 'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb' | 's3') => {
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
    } else if (val === 's3') {
      setSourceUrl('');
      setSourceUser('');
      setSourcePass('');
      setSourceS3Endpoint('');
      setSourceS3Region('us-east-1');
      setSourceS3Bucket('');
      setSourceS3Insecure(false);
    } else {
      setSourceUrl('');
      setSourceUser('');
      setSourcePass('');
    }
  };

  const handleTargetProviderSelect = (val: 'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb' | 's3') => {
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
    } else if (val === 's3') {
      setTargetUrl('');
      setTargetUser('');
      setTargetPass('');
      setTargetS3Endpoint('');
      setTargetS3Region('us-east-1');
      setTargetS3Bucket('');
      setTargetS3Insecure(false);
    } else {
      setTargetUrl('');
      setTargetUser('');
      setTargetPass('');
    }
  };

  const providerOptions: { id: 'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb' | 's3'; name: string }[] = [
    { id: 'nextcloud', name: 'Nextcloud' },
    { id: 'webdav', name: 'WebDAV' },
    { id: 'smb', name: 'SMB/CIFS' },
    { id: 's3', name: 'S3' },
    { id: 'dropbox', name: 'Dropbox' },
    { id: 'google', name: 'Google' }
  ];

  return (
    <div className="w-full max-w-4xl mx-auto py-2 animate-fade-in">
      
      <form onSubmit={handleSubmit} className="space-y-6">
        <div className="grid md:grid-cols-2 gap-8">
          
          {/* Source Host Card */}
          <div className="glass-panel border border-white/50 rounded-3xl p-6.5 shadow-portal hover:shadow-portal-hover transition-all duration-300 relative overflow-hidden flex flex-col group">
            <div className="absolute top-0 left-0 w-full h-1 bg-gradient-to-r from-portal-orange to-orange-500" />
            
            <div className="flex items-center gap-3.5 mb-6 border-b border-slate-100 pb-4.5">
              <div className="p-2.5 bg-slate-100 text-portal-navy rounded-xl group-hover:bg-portal-orange/10 group-hover:text-portal-orange transition-colors duration-300">
                <Server className="w-5 h-5" />
              </div>
              <div className="text-left">
                <h2 className="font-display font-extrabold text-lg text-portal-navy leading-none">Quelle (Source)</h2>
                <p className="text-[10px] font-mono text-slate-400 mt-1 uppercase tracking-wider">// VERZEICHNIS DER QUELLEDATEN</p>
              </div>
            </div>

            <div className="space-y-5 text-xs text-left">
              <div>
                <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono mb-2">Anbieter (Provider)</label>
                
                {/* Visual Provider Pills */}
                <div className="grid grid-cols-3 gap-2">
                  {providerOptions.map(opt => (
                    <button
                      key={opt.id}
                      type="button"
                      onClick={() => handleSourceProviderSelect(opt.id)}
                      className={`py-2 px-1 rounded-xl text-[11px] font-bold font-mono transition-all duration-200 border cursor-pointer ${
                        sourceProvider === opt.id
                          ? 'bg-gradient-to-tr from-portal-navy to-portal-navy-light border-portal-navy text-white shadow-xs'
                          : 'bg-slate-50/50 border-slate-200 text-slate-600 hover:bg-slate-100 hover:text-slate-900'
                      }`}
                    >
                      {opt.name}
                    </button>
                  ))}
                </div>
              </div>

              {sourceProvider === 'smb' ? (
                <>
                  <div className="grid grid-cols-3 gap-4">
                    <div className="col-span-2 space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Server Host / IP</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={sourceSmbHost}
                        onChange={(e) => setSourceSmbHost(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Port</label>
                      <input
                        type="text"
                        placeholder="445"
                        value={sourceSmbPort}
                        onChange={(e) => setSourceSmbPort(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="grid grid-cols-2 gap-4">
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Freigabe-Name (Share)</label>
                      <input
                        type="text"
                        placeholder="projekte"
                        value={sourceSmbShare}
                        onChange={(e) => setSourceSmbShare(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Domain (Optional)</label>
                      <input
                        type="text"
                        placeholder="WORKGROUP"
                        value={sourceSmbDomain}
                        onChange={(e) => setSourceSmbDomain(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Benutzername</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Kennwort / Passwort</label>
                    <input
                      type="password"
                      placeholder="passwort"
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans font-mono"
                      required
                    />
                  </div>
                </>
              ) : sourceProvider === 's3' ? (
                <>
                  <div className="grid grid-cols-2 gap-4">
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Bucket Name</label>
                      <input
                        type="text"
                        placeholder="mein-bucket"
                        value={sourceS3Bucket}
                        onChange={(e) => setSourceS3Bucket(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Region</label>
                      <input
                        type="text"
                        placeholder="us-east-1"
                        value={sourceS3Region}
                        onChange={(e) => setSourceS3Region(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Custom Endpoint URL (Optional)</label>
                    <input
                      type="url"
                      placeholder="https://s3.wasabisys.com oder http://localhost:9000"
                      value={sourceS3Endpoint}
                      onChange={(e) => setSourceS3Endpoint(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Access Key</label>
                    <input
                      type="text"
                      placeholder="AKIAIOSFODNN7EXAMPLE"
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Secret Key</label>
                    <input
                      type="password"
                      placeholder="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans font-mono"
                      required
                    />
                  </div>

                  <div className="flex items-center gap-2 pt-1">
                    <input
                      type="checkbox"
                      id="sourceS3Insecure"
                      checked={sourceS3Insecure}
                      onChange={(e) => setSourceS3Insecure(e.target.checked)}
                      className="rounded border-slate-350 text-portal-orange focus:ring-portal-orange"
                    />
                    <label htmlFor="sourceS3Insecure" className="text-slate-650 cursor-pointer font-sans select-none">
                      HTTP erlauben (nur für lokale/MinIO Entwicklungsendpunkte)
                    </label>
                  </div>
                </>
              ) : sourceProvider === 'nextcloud' || sourceProvider === 'webdav' ? (
                <>
                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">
                      {sourceProvider === 'nextcloud' ? 'Nextcloud WebDAV-URL' : 'WebDAV-URL'}
                    </label>
                    <input
                      type="url"
                      placeholder={sourceProvider === 'nextcloud' ? 'https://nextcloud.source-domain.de' : 'https://webdav.domain.de/dav'}
                      value={sourceUrl}
                      onChange={(e) => setSourceUrl(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Benutzername</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <div className="flex justify-between items-center mb-1.5">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">App-Passwort</label>
                      <button
                        type="button"
                        onClick={() => setShowHelp(!showHelp)}
                        className="text-[10px] text-portal-orange hover:text-portal-orange-hover hover:underline font-bold uppercase tracking-wider flex items-center gap-1 cursor-pointer font-mono"
                      >
                        <HelpCircle className="w-3.5 h-3.5" /> Hilfe-Anleitung
                      </button>
                    </div>
                    <input
                      type="password"
                      placeholder="•••• •••• •••• ••••"
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      required
                    />
                  </div>
                </>
              ) : (
                <div className="py-2 space-y-1">
                  <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono mb-2">
                    {sourceProvider === 'google' ? 'Google Verbindung' : 'Dropbox Verbindung'}
                  </label>
                  {sourcePass ? (
                    <div className="bg-emerald-50/80 border border-emerald-200 text-emerald-800 rounded-2xl p-4 flex items-center justify-between shadow-xs">
                      <div className="truncate pr-2">
                        <p className="font-bold text-[9px] uppercase tracking-wider text-emerald-650 font-mono">Verbunden als</p>
                        <p className="text-xs font-bold text-slate-700 truncate">{sourceOAuthUser || (sourceProvider === 'google' ? 'Google Account' : 'Dropbox Account')}</p>
                      </div>
                      <button
                        type="button"
                        onClick={() => {
                          setSourcePass('');
                          setSourceOAuthUser('');
                        }}
                        className="px-3 py-1.5 bg-white border border-emerald-250 text-emerald-750 text-[10px] font-mono font-bold rounded-xl shadow-xs hover:bg-emerald-100 active:scale-97 transition-all cursor-pointer"
                      >
                        Trennen
                      </button>
                    </div>
                  ) : (
                    <button
                      type="button"
                      onClick={() => openOAuthPopup(sourceProvider, 'source')}
                      className="w-full py-3 px-4 bg-portal-navy hover:bg-portal-navy-light text-white font-mono font-bold text-[11px] uppercase tracking-wider rounded-xl shadow-xs hover:shadow-sm hover:scale-[1.01] active:scale-[0.99] transition-all cursor-pointer flex items-center justify-center gap-2"
                    >
                      <RefreshCw className="w-4 h-4" /> Mit {sourceProvider === 'google' ? 'Google' : 'Dropbox'} verbinden
                    </button>
                  )}
                </div>
              )}
            </div>
          </div>

          {/* Target Host Card */}
          <div className="glass-panel border border-white/50 rounded-3xl p-6.5 shadow-portal hover:shadow-portal-hover transition-all duration-300 relative overflow-hidden flex flex-col group">
            <div className="absolute top-0 left-0 w-full h-1 bg-gradient-to-r from-portal-navy to-portal-navy-light" />
            
            <div className="flex items-center gap-3.5 mb-6 border-b border-slate-100 pb-4.5">
              <div className="p-2.5 bg-slate-100 text-portal-navy rounded-xl group-hover:bg-portal-navy/10 group-hover:text-portal-navy-light transition-colors duration-300">
                <Server className="w-5 h-5" />
              </div>
              <div className="text-left">
                <h2 className="font-display font-extrabold text-lg text-portal-navy leading-none">Ziel (Target)</h2>
                <p className="text-[10px] font-mono text-slate-400 mt-1 uppercase tracking-wider">// ZIELVERZEICHNIS FÜR DIE MIGRATION</p>
              </div>
            </div>

            <div className="space-y-5 text-xs text-left">
              <div>
                <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono mb-2">Anbieter (Provider)</label>
                
                {/* Visual Provider Pills */}
                <div className="grid grid-cols-3 gap-2">
                  {providerOptions.map(opt => (
                    <button
                      key={opt.id}
                      type="button"
                      onClick={() => handleTargetProviderSelect(opt.id)}
                      className={`py-2 px-1 rounded-xl text-[11px] font-bold font-mono transition-all duration-200 border cursor-pointer ${
                        targetProvider === opt.id
                          ? 'bg-gradient-to-tr from-portal-navy to-portal-navy-light border-portal-navy text-white shadow-xs'
                          : 'bg-slate-50/50 border-slate-200 text-slate-600 hover:bg-slate-100 hover:text-slate-900'
                      }`}
                    >
                      {opt.name}
                    </button>
                  ))}
                </div>
              </div>

              {targetProvider === 'smb' ? (
                <>
                  <div className="grid grid-cols-3 gap-4">
                    <div className="col-span-2 space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Server Host / IP</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={targetSmbHost}
                        onChange={(e) => setTargetSmbHost(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Port</label>
                      <input
                        type="text"
                        placeholder="445"
                        value={targetSmbPort}
                        onChange={(e) => setTargetSmbPort(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="grid grid-cols-2 gap-4">
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Freigabe-Name (Share)</label>
                      <input
                        type="text"
                        placeholder="projekte"
                        value={targetSmbShare}
                        onChange={(e) => setTargetSmbShare(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Domain (Optional)</label>
                      <input
                        type="text"
                        placeholder="WORKGROUP"
                        value={targetSmbDomain}
                        onChange={(e) => setTargetSmbDomain(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Benutzername</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Kennwort / Passwort</label>
                    <input
                      type="password"
                      placeholder="passwort"
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans font-mono"
                      required
                    />
                  </div>
                </>
              ) : targetProvider === 's3' ? (
                <>
                  <div className="grid grid-cols-2 gap-4">
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Bucket Name</label>
                      <input
                        type="text"
                        placeholder="mein-bucket"
                        value={targetS3Bucket}
                        onChange={(e) => setTargetS3Bucket(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Region</label>
                      <input
                        type="text"
                        placeholder="us-east-1"
                        value={targetS3Region}
                        onChange={(e) => setTargetS3Region(e.target.value)}
                        className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Custom Endpoint URL (Optional)</label>
                    <input
                      type="url"
                      placeholder="https://s3.wasabisys.com oder http://localhost:9000"
                      value={targetS3Endpoint}
                      onChange={(e) => setTargetS3Endpoint(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Access Key</label>
                    <input
                      type="text"
                      placeholder="AKIAIOSFODNN7EXAMPLE"
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Secret Key</label>
                    <input
                      type="password"
                      placeholder="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans font-mono"
                      required
                    />
                  </div>

                  <div className="flex items-center gap-2 pt-1">
                    <input
                      type="checkbox"
                      id="targetS3Insecure"
                      checked={targetS3Insecure}
                      onChange={(e) => setTargetS3Insecure(e.target.checked)}
                      className="rounded border-slate-350 text-portal-orange focus:ring-portal-orange"
                    />
                    <label htmlFor="targetS3Insecure" className="text-slate-650 cursor-pointer font-sans select-none">
                      HTTP erlauben (nur für lokale/MinIO Entwicklungsendpunkte)
                    </label>
                  </div>
                </>
              ) : targetProvider === 'nextcloud' || targetProvider === 'webdav' ? (
                <>
                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">
                      {targetProvider === 'nextcloud' ? 'Nextcloud WebDAV-URL' : 'WebDAV-URL'}
                    </label>
                    <input
                      type="url"
                      placeholder={targetProvider === 'nextcloud' ? 'https://nextcloud.target-domain.de' : 'https://webdav.domain.de/dav'}
                      value={targetUrl}
                      onChange={(e) => setTargetUrl(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">Benutzername</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <div className="flex justify-between items-center mb-1.5">
                      <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">App-Passwort</label>
                      <button
                        type="button"
                        onClick={() => setShowHelp(!showHelp)}
                        className="text-[10px] text-portal-orange hover:text-portal-orange-hover hover:underline font-bold uppercase tracking-wider flex items-center gap-1 cursor-pointer font-mono"
                      >
                        <HelpCircle className="w-3.5 h-3.5" /> Hilfe-Anleitung
                      </button>
                    </div>
                    <input
                      type="password"
                      placeholder="•••• •••• •••• ••••"
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className="w-full bg-slate-50/50 border border-slate-200 rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                      required
                    />
                  </div>
                </>
              ) : (
                <div className="py-2 space-y-1">
                  <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono mb-2">
                    {targetProvider === 'google' ? 'Google Verbindung' : 'Dropbox Verbindung'}
                  </label>
                  {targetPass ? (
                    <div className="bg-emerald-50/80 border border-emerald-200 text-emerald-800 rounded-2xl p-4 flex items-center justify-between shadow-xs">
                      <div className="truncate pr-2">
                        <p className="font-bold text-[9px] uppercase tracking-wider text-emerald-650 font-mono">Verbunden als</p>
                        <p className="text-xs font-bold text-slate-700 truncate">{targetOAuthUser || (targetProvider === 'google' ? 'Google Account' : 'Dropbox Account')}</p>
                      </div>
                      <button
                        type="button"
                        onClick={() => {
                          setTargetPass('');
                          setTargetOAuthUser('');
                        }}
                        className="px-3 py-1.5 bg-white border border-emerald-250 text-emerald-750 text-[10px] font-mono font-bold rounded-xl shadow-xs hover:bg-emerald-100 active:scale-97 transition-all cursor-pointer"
                      >
                        Trennen
                      </button>
                    </div>
                  ) : (
                    <button
                      type="button"
                      onClick={() => openOAuthPopup(targetProvider, 'target')}
                      className="w-full py-3 px-4 bg-portal-navy hover:bg-portal-navy-light text-white font-mono font-bold text-[11px] uppercase tracking-wider rounded-xl shadow-xs hover:shadow-sm hover:scale-[1.01] active:scale-[0.99] transition-all cursor-pointer flex items-center justify-center gap-2"
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
          <div className="bg-slate-100/50 border border-slate-200 p-6 rounded-2xl max-w-2xl mx-auto shadow-xs text-xs leading-relaxed text-slate-650 text-left animate-slide-up">
            <h4 className="font-display font-extrabold text-sm text-portal-navy mb-3 flex items-center gap-1.5">
              <HelpCircle className="w-4 h-4 text-portal-orange shrink-0" />
              <span>Anleitung zur App-Passwort-Erstellung (Nextcloud / WebDAV)</span>
            </h4>
            <ol className="list-decimal list-inside space-y-2 text-slate-600 pl-1">
              <li>Melde dich in deiner Nextcloud über den Webbrowser an.</li>
              <li>Klicke oben rechts auf dein Profilbild und wähle <strong className="text-slate-800">Einstellungen</strong>.</li>
              <li>Klicke im linken Menü auf <strong className="text-slate-800">Sicherheit</strong>.</li>
              <li>Scrolle ganz nach unten zu <strong className="text-slate-800">Geräte & Clients</strong>.</li>
              <li>Gib links einen App-Namen ein (z. B. <code className="bg-slate-250 border border-slate-300 px-1.5 py-0.5 rounded font-mono text-[10px] font-bold">Clumove</code>) und klicke auf <strong className="text-slate-800">Neues App-Passwort erstellen</strong>.</li>
              <li>Kopiere das generierte Passwort und füge es oben ein (dein Hauptpasswort funktioniert oft nicht!).</li>
            </ol>
          </div>
        )}

        {error && (
          <div className="p-4 bg-rose-50/85 border border-rose-250 rounded-2xl flex items-start gap-3 max-w-xl mx-auto text-left animate-fade-in">
            <AlertCircle className="w-5 h-5 text-rose-600 shrink-0 mt-0.5" />
            <div className="text-xs font-semibold text-rose-800 leading-normal">{error}</div>
          </div>
        )}

        {/* Action Button */}
        <div className="flex justify-center pt-4">
          <button
            type="submit"
            disabled={loading}
            className="flex items-center gap-2.5 px-8 py-3.5 bg-gradient-to-r from-portal-orange to-orange-500 hover:from-orange-500 hover:to-portal-orange text-white font-mono text-xs font-bold uppercase tracking-wider rounded-xl shadow-xs hover:shadow-md hover:scale-[1.01] hover:-translate-y-0.5 active:translate-y-0 active:scale-99 transition-all cursor-pointer duration-300 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? (
              <>
                <RefreshCw className="w-4 h-4 animate-spin" />
                <span>Verbindung wird geprüft...</span>
              </>
            ) : (
              <>
                <span>Instanzen verbinden</span>
                <ArrowRight className="w-4 h-4 stroke-[2.5]" />
              </>
            )}
          </button>
        </div>
      </form>
    </div>
  );
};
