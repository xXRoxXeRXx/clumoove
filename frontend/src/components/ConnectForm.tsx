import React, { useState, useEffect } from 'react';
import { Server, ArrowRight, RefreshCw, AlertCircle, HelpCircle } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import type { CloudFile, MigrationConfig } from '../types';

type ConnectResponse = { success: boolean; files?: CloudFile[]; error_code?: string };
import { useApiError } from '../utils/apiError';

interface ConnectFormProps {
  onConnectSuccess: (config: MigrationConfig, initialFiles: CloudFile[]) => void;
  apiUrl: string;
  token: string;
  localStorageEnabled?: boolean;
  oauthProviders?: Record<string, boolean>;
}

type ProviderId = 'nextcloud' | 'dropbox' | 'webdav' | 'magentacloud' | 'google' | 'googlephotos' | 'smb' | 's3' | 'sftp' | 'local';

export const ConnectForm: React.FC<ConnectFormProps> = ({ onConnectSuccess, apiUrl, token, localStorageEnabled = false, oauthProviders = {} }) => {
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
  const [sourceProvider, setSourceProvider] = useState<ProviderId>('nextcloud');
  const [targetProvider, setTargetProvider] = useState<ProviderId>('nextcloud');
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

  const [sourceSftpHost, setSourceSftpHost] = useState('');
  const [sourceSftpPort, setSourceSftpPort] = useState('22');
  const [sourceSftpAuthMode, setSourceSftpAuthMode] = useState<'password' | 'key'>('password');
  const [sourceSftpPrivateKey, setSourceSftpPrivateKey] = useState('');

  const [targetSftpHost, setTargetSftpHost] = useState('');
  const [targetSftpPort, setTargetSftpPort] = useState('22');
  const [targetSftpAuthMode, setTargetSftpAuthMode] = useState<'password' | 'key'>('password');
  const [targetSftpPrivateKey, setTargetSftpPrivateKey] = useState('');

  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showHelp, setShowHelp] = useState(false);

  // Reusable connection profiles (role-agnostic; usable as source or target)
  const [profiles, setProfiles] = useState<{ id: string; name: string; provider: string }[]>([]);
  const [sourceProfileId, setSourceProfileId] = useState('');
  const [targetProfileId, setTargetProfileId] = useState('');

  const { t } = useTranslation();
  const translateApiError = useApiError();

  const getProfileName = (id: string) => profiles.find((x) => x.id === id)?.name ?? '';
  // Load reusable connection profiles for the dropdowns.
  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const res = await fetch(`${apiUrl}/api/profiles`, {
          headers: { 'Authorization': `Bearer ${token}` },
        });
        const data = await res.json().catch(() => ({ profiles: [] })) as { profiles?: { id: string; name: string; provider: string }[] };
        if (!cancelled && data.profiles) {
          setProfiles(data.profiles);
        }
      } catch {
        // Non-fatal: dropdowns simply stay empty.
      }
    };
    load();
    return () => { cancelled = true; };
  }, [apiUrl, token]);

  // Apply a stored profile's credentials into the form. Only overwrites the
  // fields the provider type supports; explicit ad-hoc entry still wins because
  // the user can edit afterwards or clear the dropdown.
  const applyProfile = (role: 'source' | 'target', id: string) => {
    const p = profiles.find((x) => x.id === id);
    if (!p) return;
    if (role === 'source') {
      setSourceProvider(p.provider as ProviderId);
      setSourceProfileId(id);
    } else {
      setTargetProvider(p.provider as ProviderId);
      setTargetProfileId(id);
    }
    // The actual credential values are resolved server-side via the
    // source_profile_id / target_profile_id fields; we only pre-fill the
    // provider and name so the UI reflects the selection.
  };

  const openOAuthPopup = (provider: string, type: 'source' | 'target') => {
    const width = 600;
    const height = 700;
    const left = window.screen.width / 2 - width / 2;
    const top = window.screen.height / 2 - height / 2;

    const targetOrigin = new URL(apiUrl, window.location.origin).origin;

    const popup = window.open(
      `${apiUrl}/api/oauth/auth?provider=${provider}&purpose=connect&origin=${encodeURIComponent(window.location.origin)}`,
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
      if (event.data && event.data.type === 'oauth-success' && event.data.provider === provider && event.data.purpose === 'connect') {
        if (type === 'source') {
          setSourceOAuthUser(event.data.username || provider);
          setSourceUrl(`https://api.${provider}.com`);
          setSourceUser(event.data.username || provider);
          setSourcePass(event.data.token);
          setSourceRefreshToken(event.data.refreshToken || '');
          setSourceTokenExpiresIn(event.data.expiresIn || 3600);
          // For Google Photos, create a Picker session so the user can select
          // media in the embedded Picker widget. Other providers skip this.
          if (provider === 'googlephotos') {
            // Google Photos source selection happens on the next screen
            // (file selection), not here — we only store the OAuth tokens.
          }
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
        setError(t('connect.errors.oauthError', { error: event.data.error }));
        cleanup();
      }
    };

    // Periodically check if user closed the popup manually to clean up listener leaks (I7 fix).
    // Accessing popup.closed can throw / log a console warning when the SPA document is served
    // with Cross-Origin-Opener-Policy: same-origin (the cross-origin popup becomes inaccessible),
    // so guard the read. The postMessage path above remains the primary completion signal.
    const checkClosedInterval = setInterval(() => {
      let closed = false;
      try {
        closed = !popup || popup.closed;
      } catch {
        // popup.closed is blocked by COOP; treat as not-yet-closed and rely on postMessage.
      }
      if (closed) {
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
      : sourceProvider === 'sftp'
      ? `sftp://${sourceSftpHost}:${sourceSftpPort}`
      : sourceProvider === 'magentacloud' || sourceProvider === 'local'
      ? ''
      : ((sourceProvider === 'dropbox' || sourceProvider === 'google' || sourceProvider === 'googlephotos') ? `https://api.${sourceProvider}.com` : sourceUrl);
    const finalSourceUser = sourceProvider === 'local'
      ? ''
      : (sourceProvider === 'dropbox' || sourceProvider === 'google' || sourceProvider === 'googlephotos') ? (sourceOAuthUser || sourceProvider) : sourceUser;
    const finalSourcePass = sourceProvider === 'local'
      ? ''
      : sourceProvider === 'sftp' && sourceSftpAuthMode === 'key' ? sourceSftpPrivateKey : sourcePass;
    const finalTargetUrl = targetProvider === 'smb'
      ? `smb://${targetSmbHost}:${targetSmbPort}/${targetSmbShare.replace(/^\//, '')}${targetSmbDomain ? '?domain=' + encodeURIComponent(targetSmbDomain) : ''}`
      : targetProvider === 's3'
      ? `s3://${targetS3Bucket}?region=${encodeURIComponent(targetS3Region)}${targetS3Endpoint ? '&endpoint=' + encodeURIComponent(targetS3Endpoint) : ''}${targetS3Insecure ? '&insecure=true' : ''}`
      : targetProvider === 'sftp'
      ? `sftp://${targetSftpHost}:${targetSftpPort}`
      : targetProvider === 'magentacloud' || targetProvider === 'local'
      ? ''
      : ((targetProvider === 'dropbox' || targetProvider === 'google' || targetProvider === 'googlephotos') ? `https://api.${targetProvider}.com` : targetUrl);
    const finalTargetUser = targetProvider === 'local'
      ? ''
      : (targetProvider === 'dropbox' || targetProvider === 'google' || targetProvider === 'googlephotos') ? (targetOAuthUser || targetProvider) : targetUser;
    const finalTargetPass = targetProvider === 'local'
      ? ''
      : targetProvider === 'sftp' && targetSftpAuthMode === 'key' ? targetSftpPrivateKey : targetPass;

    if (sourceProvider === 'sftp') {
      if (!sourceSftpHost.trim()) {
        setError(t('connect.errors.sourceSftpHost'));
        return;
      }
      if (sourceSftpAuthMode === 'key' && !sourceSftpPrivateKey.trim()) {
        setError(t('connect.errors.sourceSftpKey'));
        return;
      }
    }
    if (targetProvider === 'sftp') {
      if (!targetSftpHost.trim()) {
        setError(t('connect.errors.targetSftpHost'));
        return;
      }
      if (targetSftpAuthMode === 'key' && !targetSftpPrivateKey.trim()) {
        setError(t('connect.errors.targetSftpKey'));
        return;
      }
    }
    if (sourceProvider === 'smb') {
      if (!sourceSmbHost.trim() || !sourceSmbShare.trim()) {
        setError(t('connect.errors.sourceSmb'));
        return;
      }
    }
    if (sourceProvider === 's3') {
      if (!sourceS3Bucket.trim() || !sourceS3Region.trim()) {
        setError(t('connect.errors.sourceS3'));
        return;
      }
    }
    if (targetProvider === 'smb') {
      if (!targetSmbHost.trim() || !targetSmbShare.trim()) {
        setError(t('connect.errors.targetSmb'));
        return;
      }
    }
    if (targetProvider === 's3') {
      if (!targetS3Bucket.trim() || !targetS3Region.trim()) {
        setError(t('connect.errors.targetS3'));
        return;
      }
    }

    const sourceUrlRequired = sourceProvider !== 'magentacloud' && sourceProvider !== 'local';
    const targetUrlRequired = targetProvider !== 'magentacloud' && targetProvider !== 'local';

    // A selected saved profile supplies the credentials server-side, so the
    // client-side field checks are satisfied by its presence.
    const sourceProfileSelected = sourceProfileId !== '';
    const targetProfileSelected = targetProfileId !== '';

    if (
      (sourceUrlRequired && !sourceProfileSelected && !finalSourceUrl) ||
      (sourceProvider !== 'local' && !sourceProfileSelected && !finalSourceUser) ||
      (sourceProvider !== 'local' && !sourceProfileSelected && !finalSourcePass) ||
      (targetUrlRequired && !targetProfileSelected && !finalTargetUrl) ||
      (targetProvider !== 'local' && !targetProfileSelected && !finalTargetUser) ||
      (targetProvider !== 'local' && !targetProfileSelected && !finalTargetPass)
    ) {
      setError(t('connect.errors.missingFields'));
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
          source_password: finalSourcePass,
          source_refresh_token: sourceRefreshToken,
          source_token_expires_in: sourceTokenExpiresIn,
          target_url: finalTargetUrl,
          target_username: finalTargetUser,
          target_password: finalTargetPass,
          target_refresh_token: targetRefreshToken,
          target_token_expires_in: targetTokenExpiresIn,
          source_provider: sourceProvider,
          target_provider: targetProvider,
          source_picker_session_id: '',
          source_profile_id: sourceProfileId,
          target_profile_id: targetProfileId,
        }),
      });

      if (!response.ok) {
        const body = await response.json().catch(() => ({} as { error_code?: string }));
        throw new Error(translateApiError(body.error_code));
      }

      const data = await response.json() as ConnectResponse;
      if (data.success) {
        let pickerSessionId = '';
        let pickerUri = '';
        // For a Google Photos source, create the Picker session now so the
        // file-selection screen can present the Picker immediately (the user
        // selects media there, not on the connect screen).
        if (sourceProvider === 'googlephotos') {
          try {
            const pickerResp = await fetch(`${apiUrl}/api/googlephotos/picker/session`, {
              method: 'POST',
              headers: {
                'Content-Type': 'application/json',
                Authorization: `Bearer ${token}`,
              },
              body: JSON.stringify({
                provider: 'googlephotos',
                access_token: sourcePass,
                refresh_token: sourceRefreshToken,
              }),
            });
            const pickerData = await pickerResp.json().catch(() => ({} as { success?: boolean; session_id?: string; picker_uri?: string }));
            if (pickerData.success && pickerData.session_id) {
              pickerSessionId = pickerData.session_id;
              pickerUri = pickerData.picker_uri || '';
            }
          } catch {
            // Picker session creation is best-effort here; the file-selection
            // screen can retry it. Proceed without blocking the connect.
          }
        }
        onConnectSuccess(
          {
            source_url: finalSourceUrl,
            source_username: finalSourceUser,
          source_password: finalSourcePass,
            source_refresh_token: sourceRefreshToken,
            source_token_expires_in: sourceTokenExpiresIn,
            target_url: finalTargetUrl,
            target_username: finalTargetUser,
          target_password: finalTargetPass,
            target_refresh_token: targetRefreshToken,
            target_token_expires_in: targetTokenExpiresIn,
            source_provider: sourceProvider,
            target_provider: targetProvider,
            source_picker_session_id: pickerSessionId,
            source_picker_uri: pickerUri,
          },
          data.files || []
        );

        // Best-effort: persist the connection as a reusable profile
        // when the user opted in. Fire-and-forget; failures are silent.
      } else {
        setError(translateApiError(data.error_code));
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : t('connect.errors.networkError'));
    } finally {
      setLoading(false);
    }
  };
  const handleSourceProviderSelect = (val: ProviderId) => {
    setSourceProvider(val);
    if (val === 'dropbox' || val === 'google' || val === 'googlephotos') {
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
    } else if (val === 'sftp') {
      setSourceUrl('');
      setSourceUser('');
      setSourcePass('');
      setSourceSftpHost('');
      setSourceSftpPort('22');
      setSourceSftpAuthMode('password');
      setSourceSftpPrivateKey('');
    } else if (val === 'local') {
      setSourceUrl('');
      setSourceUser('');
      setSourcePass('');
    } else {
      setSourceUrl('');
      setSourceUser('');
      setSourcePass('');
    }
  };

  const handleTargetProviderSelect = (val: ProviderId) => {
    setTargetProvider(val);
    if (val === 'dropbox' || val === 'google' || val === 'googlephotos') {
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
    } else if (val === 'sftp') {
      setTargetUrl('');
      setTargetUser('');
      setTargetPass('');
      setTargetSftpHost('');
      setTargetSftpPort('22');
      setTargetSftpAuthMode('password');
      setTargetSftpPrivateKey('');
    } else if (val === 'local') {
      setTargetUrl('');
      setTargetUser('');
      setTargetPass('');
    } else {
      setTargetUrl('');
      setTargetUser('');
      setTargetPass('');
    }
  };

  const providerOptions: { id: ProviderId; name: string }[] = [
    { id: 'nextcloud', name: 'Nextcloud' },
    { id: 'webdav', name: 'WebDAV' },
    { id: 'magentacloud', name: 'MagentaCLOUD' },
    { id: 'smb', name: 'SMB/CIFS' },
    { id: 's3', name: 'S3' },
    { id: 'sftp', name: 'SFTP' },
    ...(oauthProviders.dropbox ? [{ id: 'dropbox' as const, name: 'Dropbox' }] : []),
    ...(oauthProviders.google ? [{ id: 'google' as const, name: 'Google' }] : []),
    ...(oauthProviders.googlephotos ? [{ id: 'googlephotos' as const, name: 'Google Photos' }] : []),
    ...(localStorageEnabled ? [{ id: 'local' as const, name: 'Local' }] : [])
  ];

  return (
    <div className="w-full max-w-4xl mx-auto py-2 animate-fade-in">
      
      <form onSubmit={handleSubmit} className="space-y-6">
        <div className="grid md:grid-cols-2 gap-8">
          
          {/* Source Host Card */}
          <div className="glass-panel border border-[var(--color-glass-border)] rounded-3xl p-6.5 shadow-portal hover:shadow-portal-hover transition-all duration-300 relative overflow-hidden flex flex-col group">
            <div className="absolute top-0 left-0 w-full h-1 bg-gradient-to-r from-portal-orange to-orange-500" />
            
            <div className="flex items-center gap-3.5 mb-6 border-b border-[var(--color-border-light)] pb-4.5">
              <div className="p-2.5 bg-[var(--color-bg-tertiary)] text-[var(--color-portal-navy-themed)] rounded-xl group-hover:bg-portal-orange/10 group-hover:text-portal-orange transition-colors duration-300">
                <Server className="w-5 h-5" />
              </div>
              <div className="text-left">
                <h2 className="font-display font-extrabold text-lg text-[var(--color-portal-navy-themed)] leading-none">{t('connect.sourceTitle')}</h2>
                <p className="text-[10px] font-mono text-[var(--color-text-muted)] mt-1 uppercase tracking-wider">{t('connect.sourceSubtitle')}</p>
              </div>
            </div>

            <div className="space-y-5 text-xs text-left">
              <ProfileSelect
                profiles={profiles}
                selectedId={sourceProfileId}
                onSelect={(id) => applyProfile('source', id)}
                onClear={() => { setSourceProfileId(''); }}
              />

              {sourceProfileId ? (
                <div className="space-y-3 pt-2">
                  <div className="bg-emerald-50/80 border border-emerald-200 text-emerald-800 rounded-2xl p-4 flex items-center gap-2 shadow-xs">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('settings.connections.usingProfile', { name: getProfileName(sourceProfileId) })}</p>
                  </div>
                </div>
              ) : (
              <>
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.provider')}</label>
                
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
                          : 'bg-[var(--color-bg-tertiary)]/50 border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-primary)]'
                      }`}
                    >
                      {opt.name}
                    </button>
                  ))}
                </div>

              {sourceProvider === 'smb' ? (
                <>
                  <div className="grid grid-cols-3 gap-4">
                    <div className="col-span-2 space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={sourceSmbHost}
                        onChange={(e) => setSourceSmbHost(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.port')}</label>
                      <input
                        type="text"
                        placeholder="445"
                        value={sourceSmbPort}
                        onChange={(e) => setSourceSmbPort(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="grid grid-cols-2 gap-4">
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.share')}</label>
                      <input
                        type="text"
                        placeholder="projekte"
                        value={sourceSmbShare}
                        onChange={(e) => setSourceSmbShare(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.domain')}</label>
                      <input
                        type="text"
                        placeholder="WORKGROUP"
                        value={sourceSmbDomain}
                        onChange={(e) => setSourceSmbDomain(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                    <input
                      type="password"
                      placeholder={t('connect.password')}
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
                      required
                    />
                  </div>
                </>
              ) : sourceProvider === 'sftp' ? (
                <>
                  <div className="grid grid-cols-3 gap-4">
                    <div className="col-span-2 space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={sourceSftpHost}
                        onChange={(e) => setSourceSftpHost(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.port')}</label>
                      <input
                        type="text"
                        placeholder="22"
                        value={sourceSftpPort}
                        onChange={(e) => setSourceSftpPort(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      type="text"
                      placeholder="root"
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.auth')}</label>
                    <div className="flex gap-2">
                      <button
                        type="button"
                        onClick={() => setSourceSftpAuthMode('password')}
                        className={`flex-1 py-2 px-3 rounded-xl text-[11px] font-bold font-mono transition-all duration-200 border cursor-pointer ${
                          sourceSftpAuthMode === 'password'
                            ? 'bg-portal-navy border-portal-navy text-white'
                            : 'bg-[var(--color-bg-tertiary)]/50 border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]'
                        }`}
                      >
                        {t('connect.authPassword')}
                      </button>
                      <button
                        type="button"
                        onClick={() => setSourceSftpAuthMode('key')}
                        className={`flex-1 py-2 px-3 rounded-xl text-[11px] font-bold font-mono transition-all duration-200 border cursor-pointer ${
                          sourceSftpAuthMode === 'key'
                            ? 'bg-portal-navy border-portal-navy text-white'
                            : 'bg-[var(--color-bg-tertiary)]/50 border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]'
                        }`}
                      >
                        {t('connect.sshKey')}
                      </button>
                    </div>
                  </div>

                  {sourceSftpAuthMode === 'password' ? (
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                      <input
                        type="password"
                        placeholder={t('connect.password')}
                        value={sourcePass}
                        onChange={(e) => setSourcePass(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
                        required
                      />
                    </div>
                  ) : (
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.sshKeyPem')}</label>
                      <textarea
                        placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;...&#10;-----END OPENSSH PRIVATE KEY-----"
                        value={sourceSftpPrivateKey}
                        onChange={(e) => setSourceSftpPrivateKey(e.target.value)}
                        rows={4}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-mono resize-none"
                        required
                      />
                    </div>
                  )}
                </>
              ) : sourceProvider === 's3' ? (
                <>
                  <div className="grid grid-cols-2 gap-4">
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Bucket')}</label>
                      <input
                        type="text"
                        placeholder="mein-bucket"
                        value={sourceS3Bucket}
                        onChange={(e) => setSourceS3Bucket(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Region')}</label>
                      <input
                        type="text"
                        placeholder="us-east-1"
                        value={sourceS3Region}
                        onChange={(e) => setSourceS3Region(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Endpoint')}</label>
                    <input
                      type="url"
                      placeholder="https://s3.wasabisys.com oder http://localhost:9000"
                      value={sourceS3Endpoint}
                      onChange={(e) => setSourceS3Endpoint(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.accessKey')}</label>
                    <input
                      type="text"
                      placeholder="AKIAIOSFODNN7EXAMPLE"
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.secretKey')}</label>
                    <input
                      type="password"
                      placeholder="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
                      required
                    />
                  </div>

                  <div className="flex items-center gap-2 pt-1">
                    <input
                      type="checkbox"
                      id="sourceS3Insecure"
                      checked={sourceS3Insecure}
                      onChange={(e) => setSourceS3Insecure(e.target.checked)}
                      className="rounded border-[var(--color-border)] text-portal-orange focus:ring-portal-orange"
                    />
                    <label htmlFor="sourceS3Insecure" className="text-[var(--color-text-secondary)] cursor-pointer font-sans select-none">
                       {t('connect.s3AllowHttp')}
                     </label>
                  </div>
                </>
              ) : sourceProvider === 'nextcloud' || sourceProvider === 'webdav' ? (
                <>
                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                      {sourceProvider === 'nextcloud' ? t('connect.nextcloudUrl') : t('connect.webdavUrl')}
                    </label>
                    <input
                      type="url"
                      placeholder={sourceProvider === 'nextcloud' ? 'https://nextcloud.source-domain.de' : 'https://webdav.domain.de/dav'}
                      value={sourceUrl}
                      onChange={(e) => setSourceUrl(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <div className="flex justify-between items-center mb-1.5">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.appPasswordLabel')}</label>
                      <button
                        type="button"
                        onClick={() => setShowHelp(!showHelp)}
                        className="text-[10px] text-portal-orange hover:text-portal-orange-hover hover:underline font-bold uppercase tracking-wider flex items-center gap-1 cursor-pointer font-mono"
                      >
                         <HelpCircle className="w-3.5 h-3.5" /> {t('connect.helpGuide')}
                      </button>
                    </div>
                    <input
                      type="password"
                      placeholder="•••• •••• •••• ••••"
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>
                </>
              ) : sourceProvider === 'local' ? (
                <>
                  <div className="bg-blue-50/80 border border-blue-200 text-blue-800 rounded-2xl p-4 flex items-start gap-2 shadow-xs">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.localInfo')}</p>
                  </div>
                </>
              ) : sourceProvider === 'magentacloud' ? (
                <>
                  <div className="bg-blue-50/80 border border-blue-200 text-blue-800 rounded-2xl p-4 flex items-start gap-2 shadow-xs">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.magentacloudInfo')}</p>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.appPasswordLabel')}</label>
                    <input
                      type="password"
                      placeholder="•••• •••• •••• ••••"
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>
                </>
              ) : (
                <div className="py-2 space-y-1">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">
                    {sourceProvider === 'google' ? t('connect.googleConnect') : sourceProvider === 'googlephotos' ? t('connect.googlePhotosConnect') : t('connect.dropboxConnect')}
                  </label>
                   {sourcePass ? (
                    <div className="bg-emerald-50/80 border border-emerald-200 text-emerald-800 rounded-2xl p-4 flex items-center justify-between shadow-xs">
                      <div className="truncate pr-2">
                        <p className="font-bold text-[9px] uppercase tracking-wider text-emerald-650 font-mono">{t('connect.connectedAs')}</p>
                        <p className="text-xs font-bold text-[var(--color-text-secondary)] truncate">{sourceOAuthUser || (sourceProvider === 'google' ? t('connect.googleAccount') : sourceProvider === 'googlephotos' ? t('connect.googlePhotosAccount') : t('connect.dropboxAccount'))}</p>
                      </div>
                       <button
                        type="button"
                        onClick={() => {
                          setSourcePass('');
                          setSourceOAuthUser('');
                        }}
                        className="px-3 py-1.5 bg-[var(--color-bg-secondary)] border border-emerald-250 text-emerald-750 text-[10px] font-mono font-bold rounded-xl shadow-xs hover:bg-emerald-100 active:scale-97 transition-all cursor-pointer"
                      >
                         {t('connect.disconnect')}
                       </button>
                    </div>
                  ) : (
                    <button
                      type="button"
                      onClick={() => openOAuthPopup(sourceProvider, 'source')}
                      className="w-full py-3 px-4 bg-portal-navy hover:bg-portal-navy-light text-white font-mono font-bold text-[11px] uppercase tracking-wider rounded-xl shadow-xs hover:shadow-sm hover:scale-[1.01] active:scale-[0.99] transition-all cursor-pointer flex items-center justify-center gap-2"
                    >
                      <RefreshCw className="w-4 h-4" /> {t('connect.oauthConnect', { provider: sourceProvider === 'google' ? 'Google' : sourceProvider === 'googlephotos' ? 'Google Photos' : 'Dropbox' })}
                    </button>
                  )}
                </div>
              )}
              </>
              )}
            </div>
          </div>
          
          {/* Target Host Card */}
          <div className="glass-panel border border-[var(--color-glass-border)] rounded-3xl p-6.5 shadow-portal hover:shadow-portal-hover transition-all duration-300 relative overflow-hidden flex flex-col group">
            <div className="absolute top-0 left-0 w-full h-1 bg-gradient-to-r from-portal-navy to-portal-navy-light" />
            
            <div className="flex items-center gap-3.5 mb-6 border-b border-[var(--color-border-light)] pb-4.5">
              <div className="p-2.5 bg-[var(--color-bg-tertiary)] text-[var(--color-portal-navy-themed)] rounded-xl group-hover:bg-portal-navy/10 group-hover:text-portal-navy-light transition-colors duration-300">
                <Server className="w-5 h-5" />
              </div>
              <div className="text-left">
                <h2 className="font-display font-extrabold text-lg text-[var(--color-portal-navy-themed)] leading-none">{t('connect.targetTitle')}</h2>
                <p className="text-[10px] font-mono text-[var(--color-text-muted)] mt-1 uppercase tracking-wider">{t('connect.targetSubtitle')}</p>
              </div>
            </div>

            <div className="space-y-5 text-xs text-left">
              <ProfileSelect
                profiles={profiles}
                selectedId={targetProfileId}
                onSelect={(id) => applyProfile('target', id)}
                onClear={() => { setTargetProfileId(''); }}
              />

              {targetProfileId ? (
                <div className="space-y-3 pt-2">
                  <div className="bg-emerald-50/80 border border-emerald-200 text-emerald-800 rounded-2xl p-4 flex items-center gap-2 shadow-xs">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('settings.connections.usingProfile', { name: getProfileName(targetProfileId) })}</p>
                  </div>
                </div>
              ) : (
              <>
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.provider')}</label>
                
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
                          : 'bg-[var(--color-bg-tertiary)]/50 border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-primary)]'
                      }`}
                    >
                      {opt.name}
                    </button>
                  ))}
                </div>

              {targetProvider === 'smb' ? (
                <>
                  <div className="grid grid-cols-3 gap-4">
                    <div className="col-span-2 space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={targetSmbHost}
                        onChange={(e) => setTargetSmbHost(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.port')}</label>
                      <input
                        type="text"
                        placeholder="445"
                        value={targetSmbPort}
                        onChange={(e) => setTargetSmbPort(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="grid grid-cols-2 gap-4">
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.share')}</label>
                      <input
                        type="text"
                        placeholder="projekte"
                        value={targetSmbShare}
                        onChange={(e) => setTargetSmbShare(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.domain')}</label>
                      <input
                        type="text"
                        placeholder="WORKGROUP"
                        value={targetSmbDomain}
                        onChange={(e) => setTargetSmbDomain(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                    <input
                      type="password"
                      placeholder={t('connect.password')}
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
                      required
                    />
                  </div>
                </>
              ) : targetProvider === 'sftp' ? (
                <>
                  <div className="grid grid-cols-3 gap-4">
                    <div className="col-span-2 space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={targetSftpHost}
                        onChange={(e) => setTargetSftpHost(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.port')}</label>
                      <input
                        type="text"
                        placeholder="22"
                        value={targetSftpPort}
                        onChange={(e) => setTargetSftpPort(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      type="text"
                      placeholder="root"
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.auth')}</label>
                    <div className="flex gap-2">
                      <button
                        type="button"
                        onClick={() => setTargetSftpAuthMode('password')}
                        className={`flex-1 py-2 px-3 rounded-xl text-[11px] font-bold font-mono transition-all duration-200 border cursor-pointer ${
                          targetSftpAuthMode === 'password'
                            ? 'bg-portal-navy border-portal-navy text-white'
                            : 'bg-[var(--color-bg-tertiary)]/50 border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]'
                        }`}
                      >
                        {t('connect.authPassword')}
                      </button>
                      <button
                        type="button"
                        onClick={() => setTargetSftpAuthMode('key')}
                        className={`flex-1 py-2 px-3 rounded-xl text-[11px] font-bold font-mono transition-all duration-200 border cursor-pointer ${
                          targetSftpAuthMode === 'key'
                            ? 'bg-portal-navy border-portal-navy text-white'
                            : 'bg-[var(--color-bg-tertiary)]/50 border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]'
                        }`}
                      >
                        {t('connect.sshKey')}
                      </button>
                    </div>
                  </div>

                  {targetSftpAuthMode === 'password' ? (
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                      <input
                        type="password"
                        placeholder={t('connect.password')}
                        value={targetPass}
                        onChange={(e) => setTargetPass(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
                        required
                      />
                    </div>
                  ) : (
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.sshKeyPem')}</label>
                      <textarea
                        placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;...&#10;-----END OPENSSH PRIVATE KEY-----"
                        value={targetSftpPrivateKey}
                        onChange={(e) => setTargetSftpPrivateKey(e.target.value)}
                        rows={4}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-mono resize-none"
                        required
                      />
                    </div>
                  )}
                </>
              ) : targetProvider === 's3' ? (
                <>
                  <div className="grid grid-cols-2 gap-4">
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Bucket')}</label>
                      <input
                        type="text"
                        placeholder="mein-bucket"
                        value={targetS3Bucket}
                        onChange={(e) => setTargetS3Bucket(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Region')}</label>
                      <input
                        type="text"
                        placeholder="us-east-1"
                        value={targetS3Region}
                        onChange={(e) => setTargetS3Region(e.target.value)}
                        className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Endpoint')}</label>
                    <input
                      type="url"
                      placeholder="https://s3.wasabisys.com oder http://localhost:9000"
                      value={targetS3Endpoint}
                      onChange={(e) => setTargetS3Endpoint(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.accessKey')}</label>
                    <input
                      type="text"
                      placeholder="AKIAIOSFODNN7EXAMPLE"
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.secretKey')}</label>
                    <input
                      type="password"
                      placeholder="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
                      required
                    />
                  </div>

                  <div className="flex items-center gap-2 pt-1">
                    <input
                      type="checkbox"
                      id="targetS3Insecure"
                      checked={targetS3Insecure}
                      onChange={(e) => setTargetS3Insecure(e.target.checked)}
                      className="rounded border-[var(--color-border)] text-portal-orange focus:ring-portal-orange"
                    />
                    <label htmlFor="targetS3Insecure" className="text-[var(--color-text-secondary)] cursor-pointer font-sans select-none">
                       {t('connect.s3AllowHttp')}
                     </label>
                  </div>
                </>
              ) : targetProvider === 'nextcloud' || targetProvider === 'webdav' ? (
                <>
                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                      {targetProvider === 'nextcloud' ? t('connect.nextcloudUrl') : t('connect.webdavUrl')}
                    </label>
                    <input
                      type="url"
                      placeholder={targetProvider === 'nextcloud' ? 'https://nextcloud.target-domain.de' : 'https://webdav.domain.de/dav'}
                      value={targetUrl}
                      onChange={(e) => setTargetUrl(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <div className="flex justify-between items-center mb-1.5">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.appPasswordLabel')}</label>
                      <button
                        type="button"
                        onClick={() => setShowHelp(!showHelp)}
                        className="text-[10px] text-portal-orange hover:text-portal-orange-hover hover:underline font-bold uppercase tracking-wider flex items-center gap-1 cursor-pointer font-mono"
                      >
                         <HelpCircle className="w-3.5 h-3.5" /> {t('connect.helpGuide')}
                      </button>
                    </div>
                    <input
                      type="password"
                      placeholder="•••• •••• •••• ••••"
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>
                </>
              ) : targetProvider === 'local' ? (
                <>
                  <div className="bg-blue-50/80 border border-blue-200 text-blue-800 rounded-2xl p-4 flex items-start gap-2 shadow-xs">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.localInfo')}</p>
                  </div>
                </>
              ) : targetProvider === 'magentacloud' ? (
                <>
                  <div className="bg-blue-50/80 border border-blue-200 text-blue-800 rounded-2xl p-4 flex items-start gap-2 shadow-xs">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.magentacloudInfo')}</p>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      type="text"
                      placeholder="benutzername"
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.appPasswordLabel')}</label>
                    <input
                      type="password"
                      placeholder="•••• •••• •••• ••••"
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className="w-full bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                      required
                    />
                  </div>
                </>
              ) : (
                <div className="py-2 space-y-1">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">
                    {targetProvider === 'google' ? t('connect.googleConnect') : targetProvider === 'googlephotos' ? t('connect.googlePhotosConnect') : t('connect.dropboxConnect')}
                  </label>
                  {targetPass ? (
                    <div className="bg-emerald-50/80 border border-emerald-200 text-emerald-800 rounded-2xl p-4 flex items-center justify-between shadow-xs">
                      <div className="truncate pr-2">
                        <p className="font-bold text-[9px] uppercase tracking-wider text-emerald-650 font-mono">{t('connect.connectedAs')}</p>
                        <p className="text-xs font-bold text-[var(--color-text-secondary)] truncate">{targetOAuthUser || (targetProvider === 'google' ? t('connect.googleAccount') : targetProvider === 'googlephotos' ? t('connect.googlePhotosAccount') : t('connect.dropboxAccount'))}</p>
                      </div>
                      <button
                        type="button"
                        onClick={() => {
                          setTargetPass('');
                          setTargetOAuthUser('');
                        }}
                        className="px-3 py-1.5 bg-[var(--color-bg-secondary)] border border-emerald-250 text-emerald-750 text-[10px] font-mono font-bold rounded-xl shadow-xs hover:bg-emerald-100 active:scale-97 transition-all cursor-pointer"
                      >
                         {t('connect.disconnect')}
                       </button>
                    </div>
                  ) : (
                    <button
                      type="button"
                      onClick={() => openOAuthPopup(targetProvider, 'target')}
                      className="w-full py-3 px-4 bg-portal-navy hover:bg-portal-navy-light text-white font-mono font-bold text-[11px] uppercase tracking-wider rounded-xl shadow-xs hover:shadow-sm hover:scale-[1.01] active:scale-[0.99] transition-all cursor-pointer flex items-center justify-center gap-2"
                    >
                      <RefreshCw className="w-4 h-4" /> {t('connect.oauthConnect', { provider: targetProvider === 'google' ? 'Google' : targetProvider === 'googlephotos' ? 'Google Photos' : 'Dropbox' })}
                    </button>
                  )}
                </div>
              )}
              </>
              )}
            </div>
          </div>
        </div>

        {/* Helpful Info Guide Box */}
        {showHelp && (
          <div className="bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)] p-6 rounded-2xl max-w-2xl mx-auto shadow-xs text-xs leading-relaxed text-[var(--color-text-secondary)] text-left animate-slide-up">
            <h4 className="font-display font-extrabold text-sm text-[var(--color-portal-navy-themed)] mb-3 flex items-center gap-1.5">
              <HelpCircle className="w-4 h-4 text-portal-orange shrink-0" />
              <span>{t('connect.appPassword.title')}</span>
            </h4>
            <ol className="list-decimal list-inside space-y-2 text-[var(--color-text-secondary)] pl-1">
              <li>{t('connect.appPassword.step1')}</li>
              <li>{t('connect.appPassword.step2')}</li>
              <li>{t('connect.appPassword.step3')}</li>
              <li>{t('connect.appPassword.step4')}</li>
              <li>{t('connect.appPassword.step5')}</li>
              <li>{t('connect.appPassword.step6')}</li>
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
                <span>{t('connect.testing')}</span>
              </>
            ) : (
              <>
                <span>{t('connect.connectInstances')}</span>
                <ArrowRight className="w-4 h-4 stroke-[2.5]" />
              </>
            )}
          </button>
        </div>
      </form>
    </div>
  );
};

// ProfileSelect renders the "use saved profile" dropdown at the top of a
// ConnectForm card. It is only shown when at least one saved profile exists.
// Selecting a profile applies its credentials and hides the rest of the form.
function ProfileSelect({ profiles, selectedId, onSelect, onClear }: {
  profiles: { id: string; name: string; provider: string }[];
  selectedId: string;
  onSelect: (id: string) => void;
  onClear: () => void;
}) {
  const { t } = useTranslation();
  if (profiles.length === 0) return null;

  return (
    <div className="space-y-1.5">
      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('settings.connections.useProfile')}</label>
      <div className="flex gap-2">
        <select
          value={selectedId}
          onChange={(e) => onSelect(e.target.value)}
          className="flex-1 px-3 py-2 text-xs bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange transition-all font-sans"
        >
          <option value="">—</option>
          {profiles.map((p) => (
            <option key={p.id} value={p.id}>{p.name}</option>
          ))}
        </select>
        {selectedId && (
          <button type="button" onClick={onClear} className="px-3 py-2 rounded-xl text-[10px] font-mono border border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] transition-all cursor-pointer">
            {t('common.cancel')}
          </button>
        )}
      </div>
    </div>
  );
}

