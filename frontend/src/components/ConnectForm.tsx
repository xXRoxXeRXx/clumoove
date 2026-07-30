import React, { useState, useEffect } from 'react';
import { ArrowLeftIcon as ArrowLeft, ArrowPathIcon as RefreshCw, ArrowRightIcon as ArrowRight, CheckCircleIcon as CheckCircle2, ExclamationCircleIcon as AlertCircle, QuestionMarkCircleIcon as HelpCircle } from '@heroicons/react/24/outline';
import { useTranslation } from 'react-i18next';
import type { CloudFile, MigrationConfig } from '../types';

import { useApiError } from '../utils/apiError';
import { useOAuthPopup } from '../hooks/useOAuthPopup';
import { apiFetch } from '../utils/apiClient';
import { ProfileSelect } from './connect/ProfileSelect';
import { SaveProfileRow } from './connect/SaveProfileRow';

type ConnectResponse = { success: boolean; files?: CloudFile[]; error_code?: string };

interface ConnectFormProps {
  onConnectSuccess: (config: MigrationConfig, initialFiles: CloudFile[]) => void;
  apiUrl: string;
  token: string;
  localStorageEnabled?: boolean;
  oauthProviders?: Record<string, boolean>;
  onBack?: () => void;
}

type ProviderId = 'nextcloud' | 'dropbox' | 'webdav' | 'magentacloud' | 'google' | 'hidrive' | 'smb' | 's3' | 'sftp' | 'local' | 'immich';

const sftpHostKeyFingerprintPattern = /^SHA256:[A-Za-z0-9+/]{43}$/;

const formInputClass = 'ui-input w-full px-4 py-2.5 text-sm font-sans';
const formMonoInputClass = `${formInputClass} font-mono`;
const formTextareaClass = `${formMonoInputClass} resize-none`;

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
  const [sourceS3Insecure, setSourceS3Insecure] = useState(false);

  const [targetS3Endpoint, setTargetS3Endpoint] = useState('');
  const [targetS3Region, setTargetS3Region] = useState('us-east-1');
  const [targetS3Bucket, setTargetS3Bucket] = useState('');
  const [targetS3Insecure, setTargetS3Insecure] = useState(false);

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

  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showHelp, setShowHelp] = useState(false);

  // Two-step wizard: step 1 captures the source, step 2 captures the target.
  const [subStep, setSubStep] = useState<1 | 2>(1);
  const [sourceVerified, setSourceVerified] = useState(false);

  // Reusable connection profiles (role-agnostic; usable as source or target)
  const [profiles, setProfiles] = useState<{ id: string; name: string; provider: string }[]>([]);
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
  // Load reusable connection profiles for the dropdowns.
  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const res = await apiFetch(`${apiUrl}/api/profiles`, {
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

  // Build the final provider URL for the source side (mirrors handleSubmit's logic).
  const finalSourceUrlValue = (): string => sourceProfileId !== '' ? '' : (sourceProvider === 'smb'
    ? `smb://${sourceSmbHost}:${sourceSmbPort}/${sourceSmbShare.replace(/^\//, '')}${sourceSmbDomain ? '?domain=' + encodeURIComponent(sourceSmbDomain) : ''}`
    : sourceProvider === 's3'
    ? `s3://${sourceS3Bucket}?region=${encodeURIComponent(sourceS3Region)}${sourceS3Endpoint ? '&endpoint=' + encodeURIComponent(sourceS3Endpoint) : ''}${sourceS3Insecure ? '&insecure=true' : ''}`
    : sourceProvider === 'sftp'
    ? `sftp://${sourceSftpHost}:${sourceSftpPort}?host_key=${encodeURIComponent(sourceSftpHostKey.trim())}`
    : sourceProvider === 'magentacloud' || sourceProvider === 'local'
    ? ''
    : ((sourceProvider === 'dropbox' || sourceProvider === 'google' || sourceProvider === 'hidrive') ? `https://api.${sourceProvider}.com` : sourceUrl));

  // Build the final provider URL for the target side.
  const finalTargetUrlValue = (): string => targetProfileId !== '' ? '' : (targetProvider === 'smb'
    ? `smb://${targetSmbHost}:${targetSmbPort}/${targetSmbShare.replace(/^\//, '')}${targetSmbDomain ? '?domain=' + encodeURIComponent(targetSmbDomain) : ''}`
    : targetProvider === 's3'
    ? `s3://${targetS3Bucket}?region=${encodeURIComponent(targetS3Region)}${targetS3Endpoint ? '&endpoint=' + encodeURIComponent(targetS3Endpoint) : ''}${targetS3Insecure ? '&insecure=true' : ''}`
    : targetProvider === 'sftp'
    ? `sftp://${targetSftpHost}:${targetSftpPort}?host_key=${encodeURIComponent(targetSftpHostKey.trim())}`
    : targetProvider === 'magentacloud' || targetProvider === 'local'
    ? ''
    : ((targetProvider === 'dropbox' || targetProvider === 'google' || targetProvider === 'hidrive') ? `https://api.${targetProvider}.com` : targetUrl));
  // Build the final credentials for the source side (reuses shared URL/user/pass logic).
  const finalSourceUserValue = (): string => sourceProfileId !== '' ? '' : (sourceProvider === 'local'
    ? ''
    : (sourceProvider === 'dropbox' || sourceProvider === 'google' || sourceProvider === 'hidrive') ? (sourceOAuthUser || sourceProvider) : sourceUser);
  const finalSourcePassValue = (): string => sourceProfileId !== '' ? '' : (sourceProvider === 'local'
    ? ''
    : sourceProvider === 'sftp' && sourceSftpAuthMode === 'key' ? sourceSftpPrivateKey : sourcePass);

  // Build the final credentials for the target side.
  const finalTargetUserValue = (): string => targetProfileId !== '' ? '' : (targetProvider === 'local'
    ? ''
    : (targetProvider === 'dropbox' || targetProvider === 'google' || targetProvider === 'hidrive') ? (targetOAuthUser || targetProvider) : targetUser);
  const finalTargetPassValue = (): string => targetProfileId !== '' ? '' : (targetProvider === 'local'
    ? ''
    : targetProvider === 'sftp' && targetSftpAuthMode === 'key' ? targetSftpPrivateKey : targetPass);
  // Persist a connection as a reusable profile (called after a successful connect).
  const saveProfile = async (role: 'source' | 'target', name: string) => {
    if (!name.trim()) return false;
    const isOAuth = (role === 'source'
      ? (sourceProvider === 'dropbox' || sourceProvider === 'google' || sourceProvider === 'hidrive')
      : (targetProvider === 'dropbox' || targetProvider === 'google' || targetProvider === 'hidrive'));
    const payload: Record<string, unknown> = {
      name: name.trim(),
      provider: role === 'source' ? sourceProvider : targetProvider,
      url: role === 'source' ? finalSourceUrlValue() : finalTargetUrlValue(),
      username: role === 'source' ? (isOAuth ? (sourceOAuthUser || sourceProvider) : sourceUser) : (isOAuth ? (targetOAuthUser || targetProvider) : targetUser),
    };
    if (!isOAuth && role === 'source' && sourcePass) payload.password = sourcePass;
    if (!isOAuth && role === 'target' && targetPass) payload.password = targetPass;
    if (isOAuth && role === 'source' && sourceRefreshToken) {
      payload.refresh_token = sourceRefreshToken;
      payload.oauth_user = sourceOAuthUser || sourceProvider;
    }
    if (isOAuth && role === 'target' && targetRefreshToken) {
      payload.refresh_token = targetRefreshToken;
      payload.oauth_user = targetOAuthUser || targetProvider;
    }
    try {
      const res = await apiFetch(`${apiUrl}/api/profiles`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
        body: JSON.stringify(payload),
      });
      return res.ok;
    } catch {
      return false;
    }
  };

  // Reload the saved profiles so the dropdowns reflect a freshly created one.
  const refreshProfiles = () => {
    apiFetch(`${apiUrl}/api/profiles`, { headers: { 'Authorization': `Bearer ${token}` } })
      .then((res) => res.json())
      .then((data: { profiles?: { id: string; name: string; provider: string }[] }) => {
        if (data.profiles) setProfiles(data.profiles);
      })
      .catch(() => { /* non-fatal */ });
  };

  const startOAuth = (provider: string, type: 'source' | 'target') => {
    openOAuthPopup(provider, 'connect', {
      onSuccess: (msg) => {
        if (type === 'source') {
          setSourceOAuthUser(msg.username || provider);
          setSourceUrl(`https://api.${provider}.com`);
          setSourceUser(msg.username || provider);
          setSourcePass(msg.token);
          setSourceRefreshToken(msg.refreshToken || '');
          setSourceTokenExpiresIn(msg.expiresIn || 3600);
        } else {
          setTargetOAuthUser(msg.username || provider);
          setTargetUrl(`https://api.${provider}.com`);
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

    // A selected saved profile supplies the credentials server-side, so the
    // client-side field checks are satisfied by its presence.
    const sourceProfileSelected = sourceProfileId !== '';
    const targetProfileSelected = targetProfileId !== '';

    const finalSourceUrl = finalSourceUrlValue();
    const finalSourceUser = finalSourceUserValue();
    const finalSourcePass = finalSourcePassValue();
    const finalTargetUrl = finalTargetUrlValue();
    const finalTargetUser = finalTargetUserValue();
    const finalTargetPass = finalTargetPassValue();

    if (sourceProvider === 'sftp' && !sourceProfileSelected) {
      if (!sourceSftpHost.trim()) {
        setError(t('connect.errors.sourceSftpHost'));
        return;
      }
      if (!sftpHostKeyFingerprintPattern.test(sourceSftpHostKey.trim())) {
        setError(t('connect.errors.sourceSftpHostKey'));
        return;
      }
      if (sourceSftpAuthMode === 'key' && !sourceSftpPrivateKey.trim()) {
        setError(t('connect.errors.sourceSftpKey'));
        return;
      }
    }
    if (targetProvider === 'sftp' && !targetProfileSelected) {
      if (!targetSftpHost.trim()) {
        setError(t('connect.errors.targetSftpHost'));
        return;
      }
      if (!sftpHostKeyFingerprintPattern.test(targetSftpHostKey.trim())) {
        setError(t('connect.errors.targetSftpHostKey'));
        return;
      }
      if (targetSftpAuthMode === 'key' && !targetSftpPrivateKey.trim()) {
        setError(t('connect.errors.targetSftpKey'));
        return;
      }
    }
    if (sourceProvider === 'smb' && !sourceProfileSelected) {
      if (!sourceSmbHost.trim() || !sourceSmbShare.trim()) {
        setError(t('connect.errors.sourceSmb'));
        return;
      }
    }
    if (sourceProvider === 's3' && !sourceProfileSelected) {
      if (!sourceS3Bucket.trim() || !sourceS3Region.trim()) {
        setError(t('connect.errors.sourceS3'));
        return;
      }
    }
    if (targetProvider === 'smb' && !targetProfileSelected) {
      if (!targetSmbHost.trim() || !targetSmbShare.trim()) {
        setError(t('connect.errors.targetSmb'));
        return;
      }
    }
    if (targetProvider === 's3' && !targetProfileSelected) {
      if (!targetS3Bucket.trim() || !targetS3Region.trim()) {
        setError(t('connect.errors.targetS3'));
        return;
      }
    }

    const sourceUrlRequired = sourceProvider !== 'magentacloud' && sourceProvider !== 'local';
    const targetUrlRequired = targetProvider !== 'magentacloud' && targetProvider !== 'local';

    if (
      (sourceUrlRequired && !sourceProfileSelected && !finalSourceUrl) ||
      (sourceProvider !== 'local' && sourceProvider !== 'immich' && !sourceProfileSelected && !finalSourceUser) ||
      (sourceProvider !== 'local' && !sourceProfileSelected && !finalSourcePass) ||
      (targetUrlRequired && !targetProfileSelected && !finalTargetUrl) ||
      (targetProvider !== 'local' && targetProvider !== 'immich' && !targetProfileSelected && !finalTargetUser) ||
      (targetProvider !== 'local' && !targetProfileSelected && !finalTargetPass)
    ) {
      setError(t('connect.errors.missingFields'));
      return;
    }

    setLoading(true);
    setError(null);

    try {
      const response = await apiFetch(`${apiUrl}/api/migration/connect`, {
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
            source_profile_id: sourceProfileId,
            target_profile_id: targetProfileId,
          },
          data.files || []
        );

        // Best-effort: persist the connection as a reusable profile
        // when the user opted in. Fire-and-forget; failures are silent.
        if (sourceSaveProfile) {
          const name = sourceProfileName.trim() || `${finalSourceUrlValue() || sourceProvider}`;
          saveProfile('source', name).then((ok) => { if (ok) refreshProfiles(); });
        }
        if (targetSaveProfile) {
          const name = targetProfileName.trim() || `${finalTargetUrlValue() || targetProvider}`;
          saveProfile('target', name).then((ok) => { if (ok) refreshProfiles(); });
        }
      } else {
        setError(translateApiError(data.error_code));
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : t('connect.errors.networkError'));
    } finally {
      setLoading(false);
    }
  };
  const verifyAndAdvance = async () => {

    const sourceProfileSelected = sourceProfileId !== '';

    const finalSourceUrl = finalSourceUrlValue();
    const finalSourceUser = finalSourceUserValue();
    const finalSourcePass = finalSourcePassValue();

    if (sourceProvider === 'sftp' && !sourceProfileSelected) {
      if (!sourceSftpHost.trim()) {
        setError(t('connect.errors.sourceSftpHost'));
        return;
      }
      if (!sftpHostKeyFingerprintPattern.test(sourceSftpHostKey.trim())) {
        setError(t('connect.errors.sourceSftpHostKey'));
        return;
      }
      if (sourceSftpAuthMode === 'key' && !sourceSftpPrivateKey.trim()) {
        setError(t('connect.errors.sourceSftpKey'));
        return;
      }
    }
    if (sourceProvider === 'smb' && !sourceProfileSelected) {
      if (!sourceSmbHost.trim() || !sourceSmbShare.trim()) {
        setError(t('connect.errors.sourceSmb'));
        return;
      }
    }
    if (sourceProvider === 's3' && !sourceProfileSelected) {
      if (!sourceS3Bucket.trim() || !sourceS3Region.trim()) {
        setError(t('connect.errors.sourceS3'));
        return;
      }
    }

    const sourceUrlRequired = sourceProvider !== 'magentacloud' && sourceProvider !== 'local';
    if (
      (sourceUrlRequired && !sourceProfileSelected && !finalSourceUrl) ||
      (sourceProvider !== 'local' && sourceProvider !== 'immich' && !sourceProfileSelected && !finalSourceUser) ||
      (sourceProvider !== 'local' && !sourceProfileSelected && !finalSourcePass)
    ) {
      setError(t('connect.errors.missingFields'));
      return;
    }

    setLoading(true);
    setError(null);

    try {
      const response = await apiFetch(`${apiUrl}/api/migration/connect/test`, {
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
          source_provider: sourceProvider,
          source_profile_id: sourceProfileId,
          role: 'source',
        }),
      });

      const body = await response.json().catch(() => ({} as { success?: boolean; error_code?: string }));
      if (!response.ok || !body.success) {
        setError(translateApiError(body.error_code));
        return;
      }

      setSourceVerified(true);
      setSubStep(2);
    } catch {
      setError(t('connect.errors.networkError'));
    } finally {
      setLoading(false);
    }
  };

  const handleSourceProviderSelect = (val: ProviderId) => {
    setSourceProvider(val);
    if (val === 'dropbox' || val === 'google' || val === 'hidrive') {
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
		setSourceSftpHostKey('');
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
    if (val === 'dropbox' || val === 'google' || val === 'hidrive') {
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
		setTargetSftpHostKey('');
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
    ...(localStorageEnabled ? [{ id: 'immich' as const, name: 'Immich' }] : []),
    ...(oauthProviders.dropbox ? [{ id: 'dropbox' as const, name: 'Dropbox' }] : []),
    ...(oauthProviders.google ? [{ id: 'google' as const, name: 'Google' }] : []),
    ...(oauthProviders.hidrive ? [{ id: 'hidrive' as const, name: 'HiDrive' }] : []),
    ...(localStorageEnabled ? [{ id: 'local' as const, name: 'Local' }] : [])
  ];

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
          <fieldset key={sourceProvider} className="ui-card ui-view-enter m-0 min-h-[300px] w-full p-6 md:w-1/2 mx-auto">
            <legend className="sr-only">{t('connect.sourceTitle')}</legend>

            
            <div className="space-y-5 text-xs text-left">
              <ProfileSelect
                idPrefix="source"
                profiles={profiles}
                selectedId={sourceProfileId}
                onSelect={(id) => applyProfile('source', id)}
                onClear={() => { setSourceProfileId(''); setSourceSaveProfile(false); setSourceProfileName(''); }}
              />

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
                <div id="source-provider-label" className="block text-xs font-bold text-[var(--color-text-muted)] uppercase tracking-wider font-mono mb-2">{t('connect.sourceProvider')}</div>
                
                {/* Visual Provider Pills */}
                <div className="grid grid-cols-2 gap-2 sm:grid-cols-3" role="group" aria-labelledby="source-provider-label">
                  {providerOptions.map(opt => (
                    <button
                      key={opt.id}
                      type="button"
                      onClick={() => handleSourceProviderSelect(opt.id)}
                      aria-pressed={sourceProvider === opt.id}
                      className={`min-h-10 px-2 py-2 text-xs font-bold font-mono cursor-pointer ${
                        sourceProvider === opt.id
                          ? 'ui-button-primary text-[var(--color-text-inverse)]'
                          : 'ui-button-secondary text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-primary)]'
                      }`}
                    >
                      {opt.name}
                    </button>
                  ))}
                </div>

              {sourceProvider === 'smb' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                    <div className="space-y-1 sm:col-span-2">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={sourceSmbHost}
                        onChange={(e) => setSourceSmbHost(e.target.value)}
                        className={formInputClass}
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
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.share')}</label>
                      <input
                        type="text"
                        placeholder={t('connect.sharePlaceholder')}
                        value={sourceSmbShare}
                        onChange={(e) => setSourceSmbShare(e.target.value)}
                        className={formInputClass}
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
                        className={formInputClass}
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
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
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                    <input
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
              ) : sourceProvider === 'sftp' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                    <div className="space-y-1 sm:col-span-2">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={sourceSftpHost}
                        onChange={(e) => setSourceSftpHost(e.target.value)}
                        className={formInputClass}
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
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.sftpHostKey')}</label>
                    <input type="text" placeholder="SHA256:..." value={sourceSftpHostKey} onChange={(e) => setSourceSftpHostKey(e.target.value)} className={formMonoInputClass} required />
                    <p className="text-xs text-[var(--color-text-muted)]">{t('connect.sftpHostKeyHint')}</p>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
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
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.auth')}</label>
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
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                      <input
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
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.sshKeyPem')}</label>
                      <textarea
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
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Bucket')}</label>
                      <input
                        type="text"
                        placeholder={t('connect.bucketPlaceholder')}
                        value={sourceS3Bucket}
                        onChange={(e) => setSourceS3Bucket(e.target.value)}
                        className={formInputClass}
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
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Endpoint')}</label>
                    <input
                      type="url"
                      placeholder={t('connect.s3EndpointPlaceholder')}
                      value={sourceS3Endpoint}
                      onChange={(e) => setSourceS3Endpoint(e.target.value)}
                      className={formInputClass}
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.accessKey')}</label>
                    <input
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
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.secretKey')}</label>
                    <input
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

                  <div className="flex items-center gap-2 pt-1">
                    <input
                      type="checkbox"
                      id="sourceS3Insecure"
                      checked={sourceS3Insecure}
                      onChange={(e) => setSourceS3Insecure(e.target.checked)}
                      className="ui-checkbox"
                    />
                    <label htmlFor="sourceS3Insecure" className="text-[var(--color-text-secondary)] cursor-pointer font-sans select-none">
                       {t('connect.s3AllowHttp')}
                     </label>
                  </div>
                </>
              ) : sourceProvider === 'immich' ? (
                <>
                  <div className="ui-alert ui-alert-info p-4 flex items-start gap-2">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.immichPermissionHint')}</p>
                  </div>
                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.immichUrl')}</label>
                    <input
                      type="url"
                      placeholder={t('connect.immichUrlPlaceholder')}
                      value={sourceUrl}
                      onChange={(e) => setSourceUrl(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>
                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.immichApiKey')}</label>
                    <input
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
              ) : sourceProvider === 'nextcloud' || sourceProvider === 'webdav' ? (
                <>
                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                      {sourceProvider === 'nextcloud' ? t('connect.nextcloudUrl') : t('connect.webdavUrl')}
                    </label>
                    <input
                      type="url"
                      placeholder={sourceProvider === 'nextcloud' ? t('connect.nextcloudUrlPlaceholder') : t('connect.webdavUrlPlaceholder')}
                      value={sourceUrl}
                      onChange={(e) => setSourceUrl(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
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
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.appPasswordLabel')}</label>
                      <button
                        type="button"
                        onClick={() => setShowHelp(!showHelp)}
                        className="text-[10px] text-[var(--color-text-link)] hover:underline font-bold uppercase tracking-wider flex items-center gap-1 cursor-pointer font-mono"
                      >
                         <HelpCircle className="w-3.5 h-3.5" /> {t('connect.helpGuide')}
                      </button>
                    </div>
                    <input
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
              ) : sourceProvider === 'magentacloud' ? (
                <>
                  <div className="ui-alert ui-alert-info p-4 flex items-start gap-2">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.magentacloudInfo')}</p>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
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
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.appPasswordLabel')}</label>
                    <input
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
              ) : (
                <div className="py-2 space-y-1">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">
                    {sourceProvider === 'google' ? t('connect.googleConnect') : sourceProvider === 'hidrive' ? t('connect.hidriveConnect') : t('connect.dropboxConnect')}
                  </label>
                   {sourcePass ? (
                    <div className="ui-alert ui-alert-success p-4 flex items-center justify-between">
                      <div className="truncate pr-2">
                        <p className="font-bold text-[9px] uppercase tracking-wider text-[var(--color-success-text)] font-mono">{t('connect.connectedAs')}</p>
                        <p className="text-xs font-bold text-[var(--color-text-secondary)] truncate">{sourceOAuthUser || (sourceProvider === 'google' ? t('connect.googleAccount') : sourceProvider === 'hidrive' ? t('connect.hidriveAccount') : t('connect.dropboxAccount'))}</p>
                      </div>
                       <button
                        type="button"
                        onClick={() => {
                          setSourcePass('');
                          setSourceOAuthUser('');
                        }}
                        className="ui-button-secondary px-3 py-1.5 text-[10px] font-mono font-bold cursor-pointer"
                      >
                         {t('connect.disconnect')}
                       </button>
                    </div>
                  ) : (
                    <button
                      type="button"
                      onClick={() => startOAuth(sourceProvider, 'source')}
                      className="ui-button-primary w-full py-3 px-4 font-mono font-bold text-[11px] uppercase tracking-wider hover:opacity-90 flex items-center justify-center gap-2"
                    >
                      <RefreshCw className="w-4 h-4" /> {t('connect.oauthConnect', { provider: sourceProvider === 'google' ? 'Google' : sourceProvider === 'hidrive' ? 'HiDrive' : 'Dropbox' })}
                    </button>
                  )}
                </div>
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
          </fieldset>
          )}

           {/* Target Host Card */}
           {subStep === 2 && (
          <fieldset key={targetProvider} className="ui-card ui-view-enter m-0 min-h-[300px] w-full p-6 md:w-1/2 mx-auto">
            <legend className="sr-only">{t('connect.targetTitle')}</legend>

            
            <div className="space-y-5 text-xs text-left">
              <ProfileSelect
                idPrefix="target"
                profiles={profiles}
                selectedId={targetProfileId}
                onSelect={(id) => applyProfile('target', id)}
                onClear={() => { setTargetProfileId(''); setTargetSaveProfile(false); setTargetProfileName(''); }}
              />

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
                <div id="target-provider-label" className="block text-xs font-bold text-[var(--color-text-muted)] uppercase tracking-wider font-mono mb-2">{t('connect.targetProvider')}</div>
                
                {/* Visual Provider Pills */}
                <div className="grid grid-cols-2 gap-2 sm:grid-cols-3" role="group" aria-labelledby="target-provider-label">
                  {providerOptions.map(opt => (
                    <button
                      key={opt.id}
                      type="button"
                      onClick={() => handleTargetProviderSelect(opt.id)}
                      aria-pressed={targetProvider === opt.id}
                      className={`min-h-10 px-2 py-2 text-xs font-bold font-mono cursor-pointer ${
                        targetProvider === opt.id
                          ? 'ui-button-primary text-[var(--color-text-inverse)]'
                          : 'ui-button-secondary text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-primary)]'
                      }`}
                    >
                      {opt.name}
                    </button>
                  ))}
                </div>

              {targetProvider === 'smb' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                    <div className="space-y-1 sm:col-span-2">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={targetSmbHost}
                        onChange={(e) => setTargetSmbHost(e.target.value)}
                        className={formInputClass}
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
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    <div className="space-y-1">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.share')}</label>
                      <input
                        type="text"
                        placeholder={t('connect.sharePlaceholder')}
                        value={targetSmbShare}
                        onChange={(e) => setTargetSmbShare(e.target.value)}
                        className={formInputClass}
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
                        className={formInputClass}
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
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
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                    <input
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
              ) : targetProvider === 'sftp' ? (
                <>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                    <div className="space-y-1 sm:col-span-2">
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.serverHost')}</label>
                      <input
                        type="text"
                        placeholder="192.168.1.10"
                        value={targetSftpHost}
                        onChange={(e) => setTargetSftpHost(e.target.value)}
                        className={formInputClass}
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
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.sftpHostKey')}</label>
                    <input type="text" placeholder="SHA256:..." value={targetSftpHostKey} onChange={(e) => setTargetSftpHostKey(e.target.value)} className={formMonoInputClass} required />
                    <p className="text-xs text-[var(--color-text-muted)]">{t('connect.sftpHostKeyHint')}</p>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
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
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.auth')}</label>
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
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.password')}</label>
                      <input
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
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.sshKeyPem')}</label>
                      <textarea
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
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Bucket')}</label>
                      <input
                        type="text"
                        placeholder={t('connect.bucketPlaceholder')}
                        value={targetS3Bucket}
                        onChange={(e) => setTargetS3Bucket(e.target.value)}
                        className={formInputClass}
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
                        className={formInputClass}
                        required
                      />
                    </div>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.s3Endpoint')}</label>
                    <input
                      type="url"
                      placeholder={t('connect.s3EndpointPlaceholder')}
                      value={targetS3Endpoint}
                      onChange={(e) => setTargetS3Endpoint(e.target.value)}
                      className={formInputClass}
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.accessKey')}</label>
                    <input
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
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.secretKey')}</label>
                    <input
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

                  <div className="flex items-center gap-2 pt-1">
                    <input
                      type="checkbox"
                      id="targetS3Insecure"
                      checked={targetS3Insecure}
                      onChange={(e) => setTargetS3Insecure(e.target.checked)}
                      className="ui-checkbox"
                    />
                    <label htmlFor="targetS3Insecure" className="text-[var(--color-text-secondary)] cursor-pointer font-sans select-none">
                       {t('connect.s3AllowHttp')}
                     </label>
                  </div>
                </>
              ) : targetProvider === 'immich' ? (
                <>
                  <div className="ui-alert ui-alert-info p-4 flex items-start gap-2">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.immichPermissionHint')}</p>
                  </div>
                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.immichUrl')}</label>
                    <input
                      type="url"
                      placeholder={t('connect.immichUrlPlaceholder')}
                      value={targetUrl}
                      onChange={(e) => setTargetUrl(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>
                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.immichApiKey')}</label>
                    <input
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
              ) : targetProvider === 'nextcloud' || targetProvider === 'webdav' ? (
                <>
                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                      {targetProvider === 'nextcloud' ? t('connect.nextcloudUrl') : t('connect.webdavUrl')}
                    </label>
                    <input
                      type="url"
                      placeholder={targetProvider === 'nextcloud' ? t('connect.nextcloudUrlPlaceholder') : t('connect.webdavUrlPlaceholder')}
                      value={targetUrl}
                      onChange={(e) => setTargetUrl(e.target.value)}
                      className={formInputClass}
                      required
                    />
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
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
                      <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.appPasswordLabel')}</label>
                      <button
                        type="button"
                        onClick={() => setShowHelp(!showHelp)}
                        className="text-[10px] text-[var(--color-text-link)] hover:underline font-bold uppercase tracking-wider flex items-center gap-1 cursor-pointer font-mono"
                      >
                         <HelpCircle className="w-3.5 h-3.5" /> {t('connect.helpGuide')}
                      </button>
                    </div>
                    <input
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
              ) : targetProvider === 'magentacloud' ? (
                <>
                  <div className="ui-alert ui-alert-info p-4 flex items-start gap-2">
                    <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                    <p className="text-xs font-sans leading-relaxed">{t('connect.magentacloudInfo')}</p>
                  </div>

                  <div className="space-y-1">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('connect.username')}</label>
                    <input
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
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">{t('connect.appPasswordLabel')}</label>
                    <input
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
              ) : (
                <div className="py-2 space-y-1">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">
                    {targetProvider === 'google' ? t('connect.googleConnect') : targetProvider === 'hidrive' ? t('connect.hidriveConnect') : t('connect.dropboxConnect')}
                  </label>
                  {targetPass ? (
                    <div className="ui-alert ui-alert-success p-4 flex items-center justify-between">
                      <div className="truncate pr-2">
                        <p className="font-bold text-[9px] uppercase tracking-wider text-[var(--color-success-text)] font-mono">{t('connect.connectedAs')}</p>
                        <p className="text-xs font-bold text-[var(--color-text-secondary)] truncate">{targetOAuthUser || (targetProvider === 'google' ? t('connect.googleAccount') : targetProvider === 'hidrive' ? t('connect.hidriveAccount') : t('connect.dropboxAccount'))}</p>
                      </div>
                      <button
                        type="button"
                        onClick={() => {
                          setTargetPass('');
                          setTargetOAuthUser('');
                        }}
                        className="ui-button-secondary px-3 py-1.5 text-[10px] font-mono font-bold cursor-pointer"
                      >
                         {t('connect.disconnect')}
                       </button>
                    </div>
                  ) : (
                    <button
                      type="button"
                      onClick={() => startOAuth(targetProvider, 'target')}
                      className="ui-button-primary w-full py-3 px-4 font-mono font-bold text-[11px] uppercase tracking-wider hover:opacity-90 flex items-center justify-center gap-2"
                    >
                      <RefreshCw className="w-4 h-4" /> {t('connect.oauthConnect', { provider: targetProvider === 'google' ? 'Google' : targetProvider === 'hidrive' ? 'HiDrive' : 'Dropbox' })}
                    </button>
                  )}
                </div>
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

        {error && (
          <div role="alert" className="ui-alert ui-alert-error p-4 flex items-start gap-3 max-w-xl mx-auto text-left">
            <AlertCircle className="w-5 h-5 text-[var(--color-error-text)] shrink-0 mt-0.5" />
            <div className="text-xs font-semibold text-[var(--color-error-text)] leading-normal">{error}</div>
          </div>
        )}

        {/* Action Button */}
        <div className="flex justify-center pt-4 gap-3">
          {subStep === 1 ? (
            <button
              type="button"
              onClick={() => verifyAndAdvance()}
              disabled={loading}
              className="ui-button-primary flex items-center gap-2.5 px-8 py-3.5 font-mono text-xs font-bold uppercase tracking-wider hover:opacity-90 disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {loading ? (
                <>
                  <RefreshCw className="w-4 h-4" />
                  <span>{t('connect.testing')}</span>
                </>
                ) : (
                  <>
                    {sourceVerified && <CheckCircle2 className="w-4 h-4 stroke-[2.5]" />}
                    <span>{t('connect.checkAndContinue')}</span>
                    <ArrowRight className="w-4 h-4 stroke-[2.5]" />
                  </>
                )}
            </button>
          ) : (
            <button
              type="submit"
              disabled={loading}
                className="ui-button-primary flex items-center gap-2.5 px-8 py-3.5 font-mono text-xs font-bold uppercase tracking-wider hover:opacity-90 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {loading ? (
                  <>
                    <RefreshCw className="w-4 h-4" />
                    <span>{t('connect.testing')}</span>
                  </>
                ) : (
                  <>
                    <span>{t('connect.connectInstances')}</span>
                    <ArrowRight className="w-4 h-4 stroke-[2.5]" />
                  </>
                )}
              </button>
          )}
        </div>
      </form>
    </div>
  );
};


