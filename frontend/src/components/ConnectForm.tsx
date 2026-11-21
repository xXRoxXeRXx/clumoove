import React, { useState, useEffect, useMemo } from 'react';
import {
  ArrowLeftIcon as ArrowLeft,
  ArrowPathIcon as RefreshCw,
  ArrowRightIcon as ArrowRight,
  CheckCircleIcon as CheckCircle2,
  ExclamationCircleIcon as AlertCircle,
  QuestionMarkCircleIcon as HelpCircle,
} from './icons';
import { useTranslation } from 'react-i18next';
import { isOAuthProvider, type CloudFile, type MigrationConfig, type ProviderId } from '../types';

import { useApiError } from '../utils/apiError';
import { useOAuthPopup } from '../hooks/useOAuthPopup';
import { ProfileSelect } from './connect/ProfileSelect';
import { SaveProfileRow } from './connect/SaveProfileRow';
import { ProviderSelector } from './connect/ProviderSelector';
import { FixedCredentialsFields } from './connect/FixedCredentialsFields';
import { OAuthConnectFields } from './connect/OAuthConnectFields';
import type { FtpTlsMode } from '../utils/providerUrls';
import {
  createConnectionWorkflow,
  type ConnectionDraft,
  type SavedProfileSummary,
  type WorkflowFailure,
} from './connect/connectionWorkflow';

interface ConnectFormProps {
  onConnectSuccess: (config: MigrationConfig, initialFiles: CloudFile[]) => void;
  apiUrl: string;
  token: string;
  localStorageEnabled?: boolean;
  oauthProviders?: Record<string, boolean>;
  onBack?: () => void;
}

const formInputClass = 'ui-input w-full px-4 py-2.5 text-sm font-sans';
const formMonoInputClass = `${formInputClass} font-mono`;
const formTextareaClass = `${formMonoInputClass} resize-none`;

function MegaCredentialFields({ idPrefix, username, password, onUsernameChange, onPasswordChange }: { idPrefix: string; username: string; password: string; onUsernameChange: (value: string) => void; onPasswordChange: (value: string) => void }) {
  const { t } = useTranslation();
  return <>
    <div className="ui-alert ui-alert-info p-4 flex items-start gap-2"><AlertCircle className="w-4 h-4 mt-0.5 shrink-0" /><p className="text-xs font-sans leading-relaxed">{t('connect.megaInfo')}</p></div>
    <div className="space-y-1"><label htmlFor={`${idPrefix}-mega-email`} className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.megaEmail')}</label><input id={`${idPrefix}-mega-email`} type="email" required value={username} onChange={(e) => onUsernameChange(e.target.value)} className={formInputClass} placeholder="name@example.com" /></div>
    <div className="space-y-1"><label htmlFor={`${idPrefix}-mega-password`} className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label><input id={`${idPrefix}-mega-password`} type="password" required value={password} onChange={(e) => onPasswordChange(e.target.value)} className={formInputClass} /></div>
  </>;
}

export const ConnectForm: React.FC<ConnectFormProps> = ({ onConnectSuccess, apiUrl, token, localStorageEnabled = false, oauthProviders = {}, onBack }) => {
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

  const [targetS3Endpoint, setTargetS3Endpoint] = useState('');
  const [targetS3Region, setTargetS3Region] = useState('us-east-1');
  const [targetS3Bucket, setTargetS3Bucket] = useState('');

  const [sourceSftpHost, setSourceSftpHost] = useState('');
  const [sourceSftpPort, setSourceSftpPort] = useState('22');
	const [sourceSftpHostKey, setSourceSftpHostKey] = useState('');
  const [sourceSftpAuthMode, setSourceSftpAuthMode] = useState<'password' | 'key'>('password');
  const [sourceSftpPrivateKey, setSourceSftpPrivateKey] = useState('');

  const [targetSftpHost, setTargetSftpHost] = useState('');
  const [targetSftpPort, setTargetSftpPort] = useState('22');
	const [targetSftpHostKey, setTargetSftpHostKey] = useState('');
  const [targetSftpAuthMode, setTargetSftpAuthMode] = useState<'password' | 'key'>('password');
  const [targetSftpPrivateKey, setTargetSftpPrivateKey] = useState('');

  const [sourceFtpHost, setSourceFtpHost] = useState('');
  const [sourceFtpPort, setSourceFtpPort] = useState('21');
  const [sourceFtpTlsMode, setSourceFtpTlsMode] = useState<FtpTlsMode>('explicit');
  const [targetFtpHost, setTargetFtpHost] = useState('');
  const [targetFtpPort, setTargetFtpPort] = useState('21');
  const [targetFtpTlsMode, setTargetFtpTlsMode] = useState<FtpTlsMode>('explicit');

  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showHelp, setShowHelp] = useState(false);

  // Two-step wizard: step 1 captures the source, step 2 captures the target.
  const [subStep, setSubStep] = useState<1 | 2>(1);
  const [sourceVerified, setSourceVerified] = useState(false);

  // Reusable connection profiles (role-agnostic; usable as source or target)
  const [profiles, setProfiles] = useState<SavedProfileSummary[]>([]);
  const [sourceProfileId, setSourceProfileId] = useState('');
  const [targetProfileId, setTargetProfileId] = useState('');
  const [sourceSaveProfile, setSourceSaveProfile] = useState(false);
  const [sourceProfileName, setSourceProfileName] = useState('');
  const [targetSaveProfile, setTargetSaveProfile] = useState(false);
  const [targetProfileName, setTargetProfileName] = useState('');

  const { t } = useTranslation();
  const translateApiError = useApiError();
  const { openOAuthPopup } = useOAuthPopup(apiUrl);

  const getProfile = (id: string) => profiles.find((x) => x.id === id);
  const workflow = useMemo(() => createConnectionWorkflow({ apiUrl, token }), [apiUrl, token]);

  // Load reusable connection profiles for the dropdowns (non-fatal on failure).
  const reloadProfiles = () => workflow.listProfiles().then(setProfiles);
  useEffect(() => {
    let cancelled = false;
    workflow.listProfiles().then((list) => { if (!cancelled) setProfiles(list); });
    return () => { cancelled = true; };
  }, [workflow]);

  // Apply a stored profile's credentials into the form. Only overwrites the
  // fields the provider type supports; explicit ad-hoc entry still wins because
  // the user can edit afterwards or clear the dropdown.
  const applyProfile = (role: 'source' | 'target', id: string) => {
    const p = profiles.find((x) => x.id === id);
    if (!p) return;
    if (role === 'source') {
      setSourceProvider(p.provider as ProviderId);
      setSourceProfileId(id);
      setSourceSaveProfile(false);
      setSourceProfileName('');
    } else {
      setTargetProvider(p.provider as ProviderId);
      setTargetProfileId(id);
      setTargetSaveProfile(false);
      setTargetProfileName('');
    }
    // The actual credential values are resolved server-side via the
    // source_profile_id / target_profile_id fields; we only pre-fill the
    // provider and name so the UI reflects the selection.
  };

  // Snapshot of each side's field state, handed to the form workflow.
  const sourceDraft: ConnectionDraft = {
    provider: sourceProvider, profileId: sourceProfileId, url: sourceUrl, username: sourceUser, password: sourcePass,
    refreshToken: sourceRefreshToken, tokenExpiresIn: sourceTokenExpiresIn, oauthUser: sourceOAuthUser,
    smb: { host: sourceSmbHost, port: sourceSmbPort, share: sourceSmbShare, domain: sourceSmbDomain },
    s3: { endpoint: sourceS3Endpoint, region: sourceS3Region, bucket: sourceS3Bucket },
    sftp: { host: sourceSftpHost, port: sourceSftpPort, hostKey: sourceSftpHostKey, authMode: sourceSftpAuthMode, privateKey: sourceSftpPrivateKey },
    ftp: { host: sourceFtpHost, port: sourceFtpPort, tlsMode: sourceFtpTlsMode },
  };
  const targetDraft: ConnectionDraft = {
    provider: targetProvider, profileId: targetProfileId, url: targetUrl, username: targetUser, password: targetPass,
    refreshToken: targetRefreshToken, tokenExpiresIn: targetTokenExpiresIn, oauthUser: targetOAuthUser,
    smb: { host: targetSmbHost, port: targetSmbPort, share: targetSmbShare, domain: targetSmbDomain },
    s3: { endpoint: targetS3Endpoint, region: targetS3Region, bucket: targetS3Bucket },
    sftp: { host: targetSftpHost, port: targetSftpPort, hostKey: targetSftpHostKey, authMode: targetSftpAuthMode, privateKey: targetSftpPrivateKey },
    ftp: { host: targetFtpHost, port: targetFtpPort, tlsMode: targetFtpTlsMode },
  };

  const describeFailure = (failure: WorkflowFailure): string => {
    if (failure.kind === 'validation') return t(failure.messageKey);
    if (failure.kind === 'api') return translateApiError(failure.errorCode);
    return t('connect.errors.networkError');
  };

  const startOAuth = (provider: string, type: 'source' | 'target') => {
    openOAuthPopup(provider, 'connect', {
      onSuccess: (msg) => {
        if (type === 'source') {
          setSourceOAuthUser(msg.username || provider);
          setSourceUrl(provider === 'onedrive' ? 'oauth://onedrive' : `https://api.${provider}.com`);
          setSourceUser(msg.username || provider);
          setSourcePass(msg.token);
          setSourceRefreshToken(msg.refreshToken || '');
          setSourceTokenExpiresIn(msg.expiresIn || 3600);
        } else {
          setTargetOAuthUser(msg.username || provider);
          setTargetUrl(provider === 'onedrive' ? 'oauth://onedrive' : `https://api.${provider}.com`);
          setTargetUser(msg.username || provider);
          setTargetPass(msg.token);
          setTargetRefreshToken(msg.refreshToken || '');
          setTargetTokenExpiresIn(msg.expiresIn || 3600);
        }
      },
      onError: (err) => {
        setError(translateApiError(err));
      },
    });
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLoading(true);
    setError(null);
    try {
      const result = await workflow.connect(sourceDraft, targetDraft);
      if ('failure' in result) {
        setError(describeFailure(result.failure));
        return;
      }
      onConnectSuccess(result.config, result.files);

      // Best-effort: persist the connection as a reusable profile
      // when the user opted in. Fire-and-forget; failures are silent.
      const saves: [boolean, ConnectionDraft, string][] = [
        [sourceSaveProfile, sourceDraft, sourceProfileName],
        [targetSaveProfile, targetDraft, targetProfileName],
      ];
      for (const [enabled, draft, name] of saves) {
        if (enabled) workflow.saveProfile(draft, name.trim() || workflow.defaultProfileName(draft)).then((ok) => { if (ok) reloadProfiles(); });
      }
    } finally {
      setLoading(false);
    }
  };

  const verifyAndAdvance = async () => {
    setSourceVerified(false);
    setLoading(true);
    setError(null);
    try {
      const result = await workflow.testSource(sourceDraft);
      if ('failure' in result) {
        setError(describeFailure(result.failure));
        return;
      }
      setSourceVerified(true);
      setSubStep(2);
    } finally {
      setLoading(false);
    }
  };

  const handleSourceProviderSelect = (val: ProviderId) => {
    setSourceProvider(val);
    if (isOAuthProvider(val)) {
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
    } else if (val === 'sftp') {
      setSourceUrl('');
      setSourceUser('');
      setSourcePass('');
      setSourceSftpHost('');
      setSourceSftpPort('22');
		setSourceSftpHostKey('');
      setSourceSftpAuthMode('password');
      setSourceSftpPrivateKey('');
    } else if (val === 'ftp') {
      setSourceUrl('');
      setSourceUser('');
      setSourcePass('');
      setSourceFtpHost('');
      setSourceFtpPort('21');
      setSourceFtpTlsMode('explicit');
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
    if (isOAuthProvider(val)) {
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
    } else if (val === 'sftp') {
      setTargetUrl('');
      setTargetUser('');
      setTargetPass('');
      setTargetSftpHost('');
      setTargetSftpPort('22');
		setTargetSftpHostKey('');
      setTargetSftpAuthMode('password');
      setTargetSftpPrivateKey('');
    } else if (val === 'ftp') {
      setTargetUrl('');
      setTargetUser('');
      setTargetPass('');
      setTargetFtpHost('');
      setTargetFtpPort('21');
      setTargetFtpTlsMode('explicit');
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

  const providerOptions = useMemo<{ id: ProviderId; name: string }[]>(() => [
    { id: 'nextcloud', name: 'Nextcloud' },
    { id: 'opencloud', name: 'OpenCloud' },
    { id: 'seafile', name: 'Seafile' },
    { id: 'webdav', name: 'WebDAV' },
    { id: 'magentacloud', name: 'MagentaCLOUD' },
    { id: 'koofr', name: 'Koofr' },
    { id: 'smb', name: 'SMB/CIFS' },
    { id: 's3', name: 'S3' },
    { id: 'sftp', name: 'SFTP' },
    { id: 'ftp', name: 'FTPS' },
		{ id: 'mega', name: 'MEGA' },
    ...(localStorageEnabled ? [{ id: 'immich' as const, name: 'Immich' }] : []),
    ...(oauthProviders.dropbox ? [{ id: 'dropbox' as const, name: 'Dropbox' }] : []),
	...(oauthProviders.google ? [{ id: 'google' as const, name: 'Google' }] : []),
	...(oauthProviders.onedrive ? [{ id: 'onedrive' as const, name: 'OneDrive' }] : []),
    ...(oauthProviders.hidrive ? [{ id: 'hidrive' as const, name: 'HiDrive' }] : []),
    ...(oauthProviders.box ? [{ id: 'box' as const, name: 'Box' }] : []),
    ...(localStorageEnabled ? [{ id: 'local' as const, name: 'Local' }] : [])
  ], [localStorageEnabled, oauthProviders]);

  return (
    <div className="w-full max-w-5xl mx-auto py-2 space-y-6">

      {/* Wizard header */}
      <div className="flex items-center justify-between border-b border-[var(--color-border)]/50 pb-4">
        {onBack ? (
          <button
            type="button"
            onClick={subStep === 1 ? onBack : () => { setSourceVerified(false); setSubStep(1); }}
            className="ui-button-secondary flex items-center gap-2 px-3 py-2 text-sm font-medium hover:bg-[var(--color-bg-tertiary)]"
          >
            <ArrowLeft className="w-4 h-4" />
            <span>{t('common.back')}</span>
          </button>
        ) : <span />}
        <h1 className="font-display text-xl font-semibold leading-none text-[var(--color-text-primary)]">
          {subStep === 1 ? t('connect.wizardStepSource') : t('connect.wizardStepTarget')}
        </h1>
      </div>

      <form onSubmit={handleSubmit} className="space-y-6">

          {/* Source Host Card */}
          {subStep === 1 && (
          <fieldset key={sourceProvider} className="ui-card ui-view-enter m-0 min-h-[300px] w-full p-6">
            <legend className="sr-only">{t('connect.sourceTitle')}</legend>

            <div className="grid grid-cols-1 md:grid-cols-12 gap-6">
              <div className="md:col-span-6 border-r-0 md:border-r border-[var(--color-border-light)] md:pr-6 space-y-5">
                <ProfileSelect
                  idPrefix="source"
                  profiles={profiles}
                  selectedId={sourceProfileId}
                  onSelect={(id) => applyProfile('source', id)}
                  onClear={() => { setSourceProfileId(''); setSourceSaveProfile(false); setSourceProfileName(''); }}
                />

                <ProviderSelector
                  providers={providerOptions}
                  selectedProvider={sourceProvider}
                  onSelectProvider={(val) => {
                    handleSourceProviderSelect(val);
                    setSourceProfileId('');
                    setSourceSaveProfile(false);
                    setSourceProfileName('');
                  }}
                  label={t('connect.sourceProvider')}
                />
              </div>

              <div className="md:col-span-6 space-y-5 text-xs text-left">
                <div className="flex justify-end">
                  <button
                    type="button"
                    onClick={() => verifyAndAdvance()}
                    disabled={loading}
                    className="ui-button-primary flex items-center justify-center gap-2 px-5 py-2.5 font-mono text-xs font-bold uppercase tracking-wider hover:opacity-90 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {loading ? (
                      <>
                        <RefreshCw className="w-3.5 h-3.5 animate-spin" />
                        <span>{t('connect.testing')}</span>
                      </>
                    ) : (
                      <>
                        {sourceVerified && <CheckCircle2 className="w-3.5 h-3.5 stroke-[2.5]" />}
                        <span>{t('connect.checkAndContinue')}</span>
                        <ArrowRight className="w-3.5 h-3.5 stroke-[2.5]" />
                      </>
                    )}
                  </button>
                </div>

                {error && (
                  <div role="alert" className="ui-alert ui-alert-error p-3 flex items-start gap-2.5 text-left">
                    <AlertCircle className="w-4 h-4 text-[var(--color-error-text)] shrink-0 mt-0.5" />
                    <div className="text-xs font-semibold text-[var(--color-error-text)] leading-normal">{error}</div>
                  </div>
                )}

                {sourceProfileId ? (
                  <div className="space-y-4 pt-2">
                    <div className="ui-alert ui-alert-success p-4.5 space-y-3">
                      <div className="flex items-center">
                        <span className="text-[10px] font-mono font-bold uppercase tracking-wider text-[var(--color-success-text)] bg-[var(--color-success-bg)] px-2.5 py-1 rounded-full border border-[var(--color-success-border)] flex items-center gap-1.5">
                          {getProfile(sourceProfileId)?.provider.toUpperCase()}
                        </span>
                      </div>
                      <div>
                        <p className="font-display font-bold text-sm text-[var(--color-text-primary)]">
                          {getProfile(sourceProfileId)?.name}
                        </p>
                        <p className="text-xs text-[var(--color-text-muted)] font-sans mt-0.5">
                          {t('settings.connections.usingProfile', { name: getProfile(sourceProfileId)?.name })}
                        </p>
                      </div>
                    </div>
                  </div>
                ) : (
                <>

              {sourceProvider === 'smb' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                    <div className="space-y-1 sm:col-span-2">
                      <label htmlFor="source-smb-host" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        id="source-smb-host"
                        type="text"
                        placeholder="192.168.1.10"
                        value={sourceSmbHost}
                        onChange={(e) => setSourceSmbHost(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label htmlFor="source-smb-port" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.port')}</label>
                      <input
                        id="source-smb-port"
                        type="text"
                        placeholder="445"
                        value={sourceSmbPort}
                        onChange={(e) => setSourceSmbPort(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    <div className="space-y-1">
                      <label htmlFor="source-smb-share" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.share')}</label>
                      <input
                        id="source-smb-share"
                        type="text"
                        placeholder={t('connect.sharePlaceholder')}
                        value={sourceSmbShare}
                        onChange={(e) => setSourceSmbShare(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label htmlFor="source-smb-domain" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.domain')}</label>
                      <input
                        id="source-smb-domain"
                        type="text"
                        placeholder="WORKGROUP"
                        value={sourceSmbDomain}
                        onChange={(e) => setSourceSmbDomain(e.target.value)}
                        className={formInputClass}
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="source-smb-username" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      id="source-smb-username"
                      type="text"
                      autoComplete="section-source username"
                      name="source_username"
                      placeholder={t('connect.usernamePlaceholder')}
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="source-smb-password" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                    <input
                      id="source-smb-password"
                      type="password"
                      autoComplete="section-source current-password"
                      name="source_password"
                      placeholder={t('connect.password')}
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className={formMonoInputClass}
                      required
                    />
                  </div>
                </>
              ) : sourceProvider === 'ftp' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                    <div className="space-y-1 sm:col-span-2">
                      <label htmlFor="source-ftp-host" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input id="source-ftp-host" type="text" placeholder="ftp.example.com" value={sourceFtpHost} onChange={(e) => setSourceFtpHost(e.target.value)} className={formInputClass} required />
                    </div>
                    <div className="space-y-1">
                      <label htmlFor="source-ftp-port" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.port')}</label>
                      <input id="source-ftp-port" type="text" placeholder={sourceFtpTlsMode === 'explicit' ? '21' : '990'} value={sourceFtpPort} onChange={(e) => setSourceFtpPort(e.target.value)} className={formInputClass} required />
                    </div>
                  </div>
                  <div className="space-y-1">
                    <label htmlFor="source-ftp-tls-mode" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.ftpsMode')}</label>
                    <select id="source-ftp-tls-mode" value={sourceFtpTlsMode} onChange={(e) => { const tlsMode = e.target.value as FtpTlsMode; setSourceFtpTlsMode(tlsMode); setSourceFtpPort(tlsMode === 'explicit' ? '21' : '990'); }} className={formInputClass}>
                      <option value="explicit">{t('connect.ftpsExplicit')}</option>
                      <option value="implicit">{t('connect.ftpsImplicit')}</option>
                    </select>
                    <p className="text-xs text-[var(--color-text-muted)]">{t('connect.ftpsHint')}</p>
                  </div>
                  <div className="space-y-1">
                    <label htmlFor="source-ftp-username" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input id="source-ftp-username" type="text" autoComplete="section-source username" name="source_username" placeholder={t('connect.usernamePlaceholder')} value={sourceUser} onChange={(e) => setSourceUser(e.target.value)} className={formInputClass} required />
                  </div>
                  <div className="space-y-1">
                    <label htmlFor="source-ftp-password" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                    <input id="source-ftp-password" type="password" autoComplete="section-source current-password" name="source_password" placeholder={t('connect.password')} value={sourcePass} onChange={(e) => setSourcePass(e.target.value)} className={formMonoInputClass} required />
                  </div>
                </>
              ) : sourceProvider === 'sftp' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                    <div className="space-y-1 sm:col-span-2">
                      <label htmlFor="source-sftp-host" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        id="source-sftp-host"
                        type="text"
                        placeholder="192.168.1.10"
                        value={sourceSftpHost}
                        onChange={(e) => setSourceSftpHost(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label htmlFor="source-sftp-port" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.port')}</label>
                      <input
                        id="source-sftp-port"
                        type="text"
                        placeholder="22"
                        value={sourceSftpPort}
                        onChange={(e) => setSourceSftpPort(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="source-sftp-host-key" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.sftpHostKey')}</label>
                    <input id="source-sftp-host-key" type="text" placeholder="SHA256:..." value={sourceSftpHostKey} onChange={(e) => setSourceSftpHostKey(e.target.value)} className={formMonoInputClass} required />
                    <p className="text-xs text-[var(--color-text-muted)]">{t('connect.sftpHostKeyHint')}</p>
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="source-sftp-username" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      id="source-sftp-username"
                      type="text"
                      autoComplete="section-source username"
                      name="source_username"
                      placeholder="root"
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <p className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.auth')}</p>
                    <div className="flex gap-2">
                      <button
                        type="button"
                        onClick={() => setSourceSftpAuthMode('password')}
                        aria-pressed={sourceSftpAuthMode === 'password'}
                        className={`flex-1 py-2 px-3 text-[11px] font-bold font-mono cursor-pointer ${
                          sourceSftpAuthMode === 'password'
                            ? 'ui-button-primary text-[var(--color-text-inverse)]'
                            : 'ui-button-secondary text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]'
                        }`}
                      >
                        {t('connect.authPassword')}
                      </button>
                      <button
                        type="button"
                        onClick={() => setSourceSftpAuthMode('key')}
                        aria-pressed={sourceSftpAuthMode === 'key'}
                        className={`flex-1 py-2 px-3 text-[11px] font-bold font-mono cursor-pointer ${
                          sourceSftpAuthMode === 'key'
                            ? 'ui-button-primary text-[var(--color-text-inverse)]'
                            : 'ui-button-secondary text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]'
                        }`}
                      >
                        {t('connect.sshKey')}
                      </button>
                    </div>
                  </div>

                  {sourceSftpAuthMode === 'password' ? (
                    <div className="space-y-1">
                      <label htmlFor="source-sftp-password" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                      <input
                        id="source-sftp-password"
                        type="password"
                      autoComplete="section-source current-password"
                      name="source_password"
                      placeholder={t('connect.password')}
                      value={sourcePass}
                        onChange={(e) => setSourcePass(e.target.value)}
                        className={formMonoInputClass}
                        required
                      />
                    </div>
                  ) : (
                    <div className="space-y-1">
                      <label htmlFor="source-sftp-private-key" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.sshKeyPem')}</label>
                      <textarea
                        id="source-sftp-private-key"
                        placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;...&#10;-----END OPENSSH PRIVATE KEY-----"
                        value={sourceSftpPrivateKey}
                        onChange={(e) => setSourceSftpPrivateKey(e.target.value)}
                        rows={4}
                        className={formTextareaClass}
                        required
                      />
                    </div>
                  )}
                </>
              ) : sourceProvider === 's3' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    <div className="space-y-1">
                      <label htmlFor="source-s3-bucket" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Bucket')}</label>
                      <input
                        id="source-s3-bucket"
                        type="text"
                        placeholder={t('connect.bucketPlaceholder')}
                        value={sourceS3Bucket}
                        onChange={(e) => setSourceS3Bucket(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label htmlFor="source-s3-region" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Region')}</label>
                      <input
                        id="source-s3-region"
                        type="text"
                        placeholder="us-east-1"
                        value={sourceS3Region}
                        onChange={(e) => setSourceS3Region(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="source-s3-endpoint" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Endpoint')}</label>
                    <input
                      id="source-s3-endpoint"
                      type="url"
                      placeholder={t('connect.s3EndpointPlaceholder')}
                      value={sourceS3Endpoint}
                      onChange={(e) => setSourceS3Endpoint(e.target.value)}
                      className={formInputClass}
                    />
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="source-s3-access-key" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.accessKey')}</label>
                    <input
                      id="source-s3-access-key"
                      type="text"
                      autoComplete="section-source username"
                      name="source_username"
                      placeholder="AKIAIOSFODNN7EXAMPLE"
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="source-s3-secret-key" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.secretKey')}</label>
                    <input
                      id="source-s3-secret-key"
                      type="password"
                      autoComplete="section-source current-password"
                      name="source_password"
                      placeholder="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className={formMonoInputClass}
                      required
                    />
                  </div>

                </>
              ) : sourceProvider === 'immich' ? (
                <>
                  <div className="ui-alert ui-alert-info p-4 flex items-start gap-2">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.immichPermissionHint')}</p>
                  </div>
                  <div className="space-y-1">
                    <label htmlFor="source-immich-url" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.immichUrl')}</label>
                    <input
                      id="source-immich-url"
                      type="url"
                      placeholder={t('connect.immichUrlPlaceholder')}
                      value={sourceUrl}
                      onChange={(e) => setSourceUrl(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>
                  <div className="space-y-1">
                    <label htmlFor="source-immich-api-key" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.immichApiKey')}</label>
                    <input
                      id="source-immich-api-key"
                      type="password"
                      autoComplete="current-password"
                      name="source_immich_api_key"
                      placeholder={t('connect.immichApiKeyPlaceholder')}
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className={formMonoInputClass}
                      required
                    />
                  </div>
                </>
              ) : sourceProvider === 'mega' ? (
                <MegaCredentialFields idPrefix="source" username={sourceUser} password={sourcePass} onUsernameChange={setSourceUser} onPasswordChange={setSourcePass} />
              ) : sourceProvider === 'nextcloud' || sourceProvider === 'opencloud' || sourceProvider === 'seafile' || sourceProvider === 'webdav' ? (
                <>
                  <div className="space-y-1">
                    <label htmlFor="source-provider-url" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                      {sourceProvider === 'seafile' ? t('connect.seafileUrl') : sourceProvider === 'opencloud' ? t('connect.opencloudUrl') : sourceProvider === 'nextcloud' ? t('connect.nextcloudUrl') : t('connect.webdavUrl')}
                    </label>
                    <input
                      id="source-provider-url"
                      type="url"
                      placeholder={sourceProvider === 'seafile' ? t('connect.seafileUrlPlaceholder') : sourceProvider === 'opencloud' ? t('connect.opencloudUrlPlaceholder') : sourceProvider === 'nextcloud' ? t('connect.nextcloudUrlPlaceholder') : t('connect.webdavUrlPlaceholder')}
                      value={sourceUrl}
                      onChange={(e) => setSourceUrl(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="source-provider-username" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      id="source-provider-username"
                      type="text"
                      autoComplete="section-source username"
                      name="source_username"
                      placeholder={t('connect.usernamePlaceholder')}
                      value={sourceUser}
                      onChange={(e) => setSourceUser(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <div className="flex justify-between items-center mb-1.5">
                      <label htmlFor="source-provider-password" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.appPasswordLabel')}</label>
                      <button
                        type="button"
                        onClick={() => setShowHelp(!showHelp)}
                        className="text-[10px] text-[var(--color-text-link)] hover:underline font-bold uppercase tracking-wider flex items-center gap-1 cursor-pointer font-mono"
                      >
                         <HelpCircle className="w-3.5 h-3.5" /> {t('connect.helpGuide')}
                      </button>
                    </div>
                    <input
                      id="source-provider-password"
                      type="password"
                      autoComplete="section-source current-password"
                      name="source_password"
                      placeholder="•••• •••• •••• ••••"
                      value={sourcePass}
                      onChange={(e) => setSourcePass(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>
                </>
              ) : sourceProvider === 'local' ? (
                <>
                  <div className="ui-alert ui-alert-info p-4 flex items-start gap-2">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.localInfo')}</p>
                  </div>
                </>
              ) : sourceProvider === 'magentacloud' || sourceProvider === 'koofr' ? (
                <FixedCredentialsFields provider={sourceProvider} editing={false} username={sourceUser} password={sourcePass} onUsernameChange={setSourceUser} onPasswordChange={setSourcePass} usernameId={`source-${sourceProvider}-username`} passwordId={`source-${sourceProvider}-password`} usernameName="source_username" passwordName="source_password" inputClassName={formInputClass} labelClassName="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2" fieldClassName="space-y-1" />
              ) : (
                <OAuthConnectFields
                  provider={sourceProvider}
                  oauthUser={sourceOAuthUser}
                  connected={Boolean(sourcePass)}
                  onConnect={() => startOAuth(sourceProvider, 'source')}
                  onDisconnect={() => {
                    setSourcePass('');
                    setSourceOAuthUser('');
                  }}
                />
              )}
              </>
              )}
              {!sourceProfileId && sourceProvider !== 'local' && (
                <SaveProfileRow
                  idPrefix="source"
                  checked={sourceSaveProfile}
                  saveName={sourceProfileName}
                  onSaveChange={setSourceSaveProfile}
                  onNameChange={setSourceProfileName}
                />
              )}
            </div>
          </div>
        </fieldset>
        )}

           {/* Target Host Card */}
           {subStep === 2 && (
          <fieldset key={targetProvider} className="ui-card ui-view-enter m-0 min-h-[300px] w-full p-6">
            <legend className="sr-only">{t('connect.targetTitle')}</legend>

            <div className="grid grid-cols-1 md:grid-cols-12 gap-6">
              <div className="md:col-span-6 border-r-0 md:border-r border-[var(--color-border-light)] md:pr-6 space-y-5">
                <ProfileSelect
                  idPrefix="target"
                  profiles={profiles}
                  selectedId={targetProfileId}
                  onSelect={(id) => applyProfile('target', id)}
                  onClear={() => { setTargetProfileId(''); setTargetSaveProfile(false); setTargetProfileName(''); }}
                />

                <ProviderSelector
                  providers={providerOptions}
                  selectedProvider={targetProvider}
                  onSelectProvider={(val) => {
                    handleTargetProviderSelect(val);
                    setTargetProfileId('');
                    setTargetSaveProfile(false);
                    setTargetProfileName('');
                  }}
                  label={t('connect.targetProvider')}
                />
              </div>

              <div className="md:col-span-6 space-y-5 text-xs text-left">
                <div className="flex justify-end">
                  <button
                    type="submit"
                    disabled={loading}
                    className="ui-button-primary flex items-center justify-center gap-2 px-5 py-2.5 font-mono text-xs font-bold uppercase tracking-wider hover:opacity-90 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {loading ? (
                      <>
                        <RefreshCw className="w-3.5 h-3.5 animate-spin" />
                        <span>{t('connect.testing')}</span>
                      </>
                    ) : (
                      <>
                        <span>{t('connect.connectInstances')}</span>
                        <ArrowRight className="w-3.5 h-3.5 stroke-[2.5]" />
                      </>
                    )}
                  </button>
                </div>

                {targetProfileId ? (
                  <div className="space-y-4 pt-2">
                    <div className="ui-alert ui-alert-success p-4.5 space-y-3">
                      <div className="flex items-center">
                        <span className="text-[10px] font-mono font-bold uppercase tracking-wider text-[var(--color-success-text)] bg-[var(--color-success-bg)] px-2.5 py-1 rounded-full border border-[var(--color-success-border)] flex items-center gap-1.5">
                          {getProfile(targetProfileId)?.provider.toUpperCase()}
                        </span>
                      </div>
                      <div>
                        <p className="font-display font-bold text-sm text-[var(--color-text-primary)]">
                          {getProfile(targetProfileId)?.name}
                        </p>
                        <p className="text-xs text-[var(--color-text-muted)] font-sans mt-0.5">
                          {t('settings.connections.usingProfile', { name: getProfile(targetProfileId)?.name })}
                        </p>
                      </div>
                    </div>
                  </div>
                ) : (
                <>

              {targetProvider === 'smb' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                    <div className="space-y-1 sm:col-span-2">
                      <label htmlFor="target-smb-host" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        id="target-smb-host"
                        type="text"
                        placeholder="192.168.1.10"
                        value={targetSmbHost}
                        onChange={(e) => setTargetSmbHost(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label htmlFor="target-smb-port" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.port')}</label>
                      <input
                        id="target-smb-port"
                        type="text"
                        placeholder="445"
                        value={targetSmbPort}
                        onChange={(e) => setTargetSmbPort(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    <div className="space-y-1">
                      <label htmlFor="target-smb-share" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.share')}</label>
                      <input
                        id="target-smb-share"
                        type="text"
                        placeholder={t('connect.sharePlaceholder')}
                        value={targetSmbShare}
                        onChange={(e) => setTargetSmbShare(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label htmlFor="target-smb-domain" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.domain')}</label>
                      <input
                        id="target-smb-domain"
                        type="text"
                        placeholder="WORKGROUP"
                        value={targetSmbDomain}
                        onChange={(e) => setTargetSmbDomain(e.target.value)}
                        className={formInputClass}
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="target-smb-username" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      id="target-smb-username"
                      type="text"
                      autoComplete="section-target username"
                      name="target_username"
                      placeholder={t('connect.usernamePlaceholder')}
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="target-smb-password" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                    <input
                      id="target-smb-password"
                      type="password"
                      autoComplete="section-target current-password"
                      name="target_password"
                      placeholder={t('connect.password')}
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className={formMonoInputClass}
                      required
                    />
                  </div>
                </>
              ) : targetProvider === 'ftp' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                    <div className="space-y-1 sm:col-span-2">
                      <label htmlFor="target-ftp-host" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input id="target-ftp-host" type="text" placeholder="ftp.example.com" value={targetFtpHost} onChange={(e) => setTargetFtpHost(e.target.value)} className={formInputClass} required />
                    </div>
                    <div className="space-y-1">
                      <label htmlFor="target-ftp-port" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.port')}</label>
                      <input id="target-ftp-port" type="text" placeholder={targetFtpTlsMode === 'explicit' ? '21' : '990'} value={targetFtpPort} onChange={(e) => setTargetFtpPort(e.target.value)} className={formInputClass} required />
                    </div>
                  </div>
                  <div className="space-y-1">
                    <label htmlFor="target-ftp-tls-mode" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.ftpsMode')}</label>
                    <select id="target-ftp-tls-mode" value={targetFtpTlsMode} onChange={(e) => { const tlsMode = e.target.value as FtpTlsMode; setTargetFtpTlsMode(tlsMode); setTargetFtpPort(tlsMode === 'explicit' ? '21' : '990'); }} className={formInputClass}>
                      <option value="explicit">{t('connect.ftpsExplicit')}</option>
                      <option value="implicit">{t('connect.ftpsImplicit')}</option>
                    </select>
                    <p className="text-xs text-[var(--color-text-muted)]">{t('connect.ftpsHint')}</p>
                  </div>
                  <div className="space-y-1">
                    <label htmlFor="target-ftp-username" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input id="target-ftp-username" type="text" autoComplete="section-target username" name="target_username" placeholder={t('connect.usernamePlaceholder')} value={targetUser} onChange={(e) => setTargetUser(e.target.value)} className={formInputClass} required />
                  </div>
                  <div className="space-y-1">
                    <label htmlFor="target-ftp-password" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                    <input id="target-ftp-password" type="password" autoComplete="section-target current-password" name="target_password" placeholder={t('connect.password')} value={targetPass} onChange={(e) => setTargetPass(e.target.value)} className={formMonoInputClass} required />
                  </div>
                </>
              ) : targetProvider === 'sftp' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                    <div className="space-y-1 sm:col-span-2">
                      <label htmlFor="target-sftp-host" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        id="target-sftp-host"
                        type="text"
                        placeholder="192.168.1.10"
                        value={targetSftpHost}
                        onChange={(e) => setTargetSftpHost(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label htmlFor="target-sftp-port" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.port')}</label>
                      <input
                        id="target-sftp-port"
                        type="text"
                        placeholder="22"
                        value={targetSftpPort}
                        onChange={(e) => setTargetSftpPort(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="target-sftp-host-key" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.sftpHostKey')}</label>
                    <input id="target-sftp-host-key" type="text" placeholder="SHA256:..." value={targetSftpHostKey} onChange={(e) => setTargetSftpHostKey(e.target.value)} className={formMonoInputClass} required />
                    <p className="text-xs text-[var(--color-text-muted)]">{t('connect.sftpHostKeyHint')}</p>
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="target-sftp-username" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      id="target-sftp-username"
                      type="text"
                      autoComplete="section-target username"
                      name="target_username"
                      placeholder="root"
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <p className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.auth')}</p>
                    <div className="flex gap-2">
                      <button
                        type="button"
                        onClick={() => setTargetSftpAuthMode('password')}
                        aria-pressed={targetSftpAuthMode === 'password'}
                        className={`flex-1 py-2 px-3 text-[11px] font-bold font-mono cursor-pointer ${
                          targetSftpAuthMode === 'password'
                            ? 'ui-button-primary text-[var(--color-text-inverse)]'
                            : 'ui-button-secondary text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]'
                        }`}
                      >
                        {t('connect.authPassword')}
                      </button>
                      <button
                        type="button"
                        onClick={() => setTargetSftpAuthMode('key')}
                        aria-pressed={targetSftpAuthMode === 'key'}
                        className={`flex-1 py-2 px-3 text-[11px] font-bold font-mono cursor-pointer ${
                          targetSftpAuthMode === 'key'
                            ? 'ui-button-primary text-[var(--color-text-inverse)]'
                            : 'ui-button-secondary text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]'
                        }`}
                      >
                        {t('connect.sshKey')}
                      </button>
                    </div>
                  </div>

                  {targetSftpAuthMode === 'password' ? (
                    <div className="space-y-1">
                      <label htmlFor="target-sftp-password" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                      <input
                        id="target-sftp-password"
                        type="password"
                      autoComplete="section-target current-password"
                      name="target_password"
                      placeholder={t('connect.password')}
                      value={targetPass}
                        onChange={(e) => setTargetPass(e.target.value)}
                        className={formMonoInputClass}
                        required
                      />
                    </div>
                  ) : (
                    <div className="space-y-1">
                      <label htmlFor="target-sftp-private-key" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.sshKeyPem')}</label>
                      <textarea
                        id="target-sftp-private-key"
                        placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;...&#10;-----END OPENSSH PRIVATE KEY-----"
                        value={targetSftpPrivateKey}
                        onChange={(e) => setTargetSftpPrivateKey(e.target.value)}
                        rows={4}
                        className={formTextareaClass}
                        required
                      />
                    </div>
                  )}
                </>
              ) : targetProvider === 's3' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    <div className="space-y-1">
                      <label htmlFor="target-s3-bucket" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Bucket')}</label>
                      <input
                        id="target-s3-bucket"
                        type="text"
                        placeholder={t('connect.bucketPlaceholder')}
                        value={targetS3Bucket}
                        onChange={(e) => setTargetS3Bucket(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                    <div className="space-y-1">
                      <label htmlFor="target-s3-region" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Region')}</label>
                      <input
                        id="target-s3-region"
                        type="text"
                        placeholder="us-east-1"
                        value={targetS3Region}
                        onChange={(e) => setTargetS3Region(e.target.value)}
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="target-s3-endpoint" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Endpoint')}</label>
                    <input
                      id="target-s3-endpoint"
                      type="url"
                      placeholder={t('connect.s3EndpointPlaceholder')}
                      value={targetS3Endpoint}
                      onChange={(e) => setTargetS3Endpoint(e.target.value)}
                      className={formInputClass}
                    />
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="target-s3-access-key" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.accessKey')}</label>
                    <input
                      id="target-s3-access-key"
                      type="text"
                      autoComplete="section-target username"
                      name="target_username"
                      placeholder="AKIAIOSFODNN7EXAMPLE"
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="target-s3-secret-key" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.secretKey')}</label>
                    <input
                      id="target-s3-secret-key"
                      type="password"
                      autoComplete="section-target current-password"
                      name="target_password"
                      placeholder="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className={formMonoInputClass}
                      required
                    />
                  </div>

                </>
              ) : targetProvider === 'immich' ? (
                <>
                  <div className="ui-alert ui-alert-info p-4 flex items-start gap-2">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.immichPermissionHint')}</p>
                  </div>
                  <div className="space-y-1">
                    <label htmlFor="target-immich-url" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.immichUrl')}</label>
                    <input
                      id="target-immich-url"
                      type="url"
                      placeholder={t('connect.immichUrlPlaceholder')}
                      value={targetUrl}
                      onChange={(e) => setTargetUrl(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>
                  <div className="space-y-1">
                    <label htmlFor="target-immich-api-key" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.immichApiKey')}</label>
                    <input
                      id="target-immich-api-key"
                      type="password"
                      autoComplete="current-password"
                      name="target_immich_api_key"
                      placeholder={t('connect.immichApiKeyPlaceholder')}
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className={formMonoInputClass}
                      required
                    />
                  </div>
                </>
              ) : targetProvider === 'mega' ? (
                <MegaCredentialFields idPrefix="target" username={targetUser} password={targetPass} onUsernameChange={setTargetUser} onPasswordChange={setTargetPass} />
              ) : targetProvider === 'nextcloud' || targetProvider === 'opencloud' || targetProvider === 'seafile' || targetProvider === 'webdav' ? (
                <>
                  <div className="space-y-1">
                    <label htmlFor="target-provider-url" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                      {targetProvider === 'seafile' ? t('connect.seafileUrl') : targetProvider === 'opencloud' ? t('connect.opencloudUrl') : targetProvider === 'nextcloud' ? t('connect.nextcloudUrl') : t('connect.webdavUrl')}
                    </label>
                    <input
                      id="target-provider-url"
                      type="url"
                      placeholder={targetProvider === 'seafile' ? t('connect.seafileUrlPlaceholder') : targetProvider === 'opencloud' ? t('connect.opencloudUrlPlaceholder') : targetProvider === 'nextcloud' ? t('connect.nextcloudUrlPlaceholder') : t('connect.webdavUrlPlaceholder')}
                      value={targetUrl}
                      onChange={(e) => setTargetUrl(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label htmlFor="target-provider-username" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
                      id="target-provider-username"
                      type="text"
                      autoComplete="section-target username"
                      name="target_username"
                      placeholder={t('connect.usernamePlaceholder')}
                      value={targetUser}
                      onChange={(e) => setTargetUser(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <div className="flex justify-between items-center mb-1.5">
                      <label htmlFor="target-provider-password" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.appPasswordLabel')}</label>
                      <button
                        type="button"
                        onClick={() => setShowHelp(!showHelp)}
                        className="text-[10px] text-[var(--color-text-link)] hover:underline font-bold uppercase tracking-wider flex items-center gap-1 cursor-pointer font-mono"
                      >
                         <HelpCircle className="w-3.5 h-3.5" /> {t('connect.helpGuide')}
                      </button>
                    </div>
                    <input
                      id="target-provider-password"
                      type="password"
                      autoComplete="section-target current-password"
                      name="target_password"
                      placeholder="•••• •••• •••• ••••"
                      value={targetPass}
                      onChange={(e) => setTargetPass(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>
                </>
              ) : targetProvider === 'local' ? (
                <>
                  <div className="ui-alert ui-alert-info p-4 flex items-start gap-2">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.localInfo')}</p>
                  </div>
                </>
              ) : targetProvider === 'magentacloud' || targetProvider === 'koofr' ? (
                <FixedCredentialsFields provider={targetProvider} editing={false} username={targetUser} password={targetPass} onUsernameChange={setTargetUser} onPasswordChange={setTargetPass} usernameId={`target-${targetProvider}-username`} passwordId={`target-${targetProvider}-password`} usernameName="target_username" passwordName="target_password" inputClassName={formInputClass} labelClassName="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2" fieldClassName="space-y-1" />
              ) : (
                <OAuthConnectFields
                  provider={targetProvider}
                  oauthUser={targetOAuthUser}
                  connected={Boolean(targetPass)}
                  onConnect={() => startOAuth(targetProvider, 'target')}
                  onDisconnect={() => {
                    setTargetPass('');
                    setTargetOAuthUser('');
                  }}
                />
              )}
              </>
              )}
              {!targetProfileId && targetProvider !== 'local' && (
                <SaveProfileRow
                  idPrefix="target"
                  checked={targetSaveProfile}
                  saveName={targetProfileName}
                  onSaveChange={setTargetSaveProfile}
                  onNameChange={setTargetProfileName}
                />
              )}
            </div>
          </div>
        </fieldset>
        )}

        {/* Helpful Info Guide Box */}
        {showHelp && (
          <div className="ui-card bg-[var(--color-bg-tertiary)] p-6 max-w-2xl mx-auto text-xs leading-relaxed text-[var(--color-text-secondary)] text-left">
            <h4 className="font-display font-extrabold text-sm text-[var(--color-text-primary)] mb-3 flex items-center gap-1.5">
              <HelpCircle className="w-4 h-4 text-[var(--color-text-link)] shrink-0" />
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
      </form>
    </div>
  );
};


