import { type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ExclamationCircleIcon as AlertCircle, ArrowPathIcon as RefreshCw } from '../icons';
import { isOAuthProvider, type ProviderId } from '../../types';
import type { FtpTlsMode } from '../../utils/providerUrls';
import { FixedCredentialsFields as SharedFixedCredentialsFields, type FixedCredentialsProvider } from './FixedCredentialsFields';

const inputCls = 'ui-input w-full px-3 py-2 text-sm font-sans';
const labelCls = 'block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2';
const passwordAutoComplete = (editing: boolean) => (editing ? 'current-password' : 'new-password');

export interface ProviderFieldsProps {
  provider: ProviderId;
  editing: boolean;
  oauthUser: string;
  oauthRefreshToken: string;
  onOpenOAuthPopup: () => void;
  onDisconnectOAuth: () => void;
  // Generic URL, Username, Password
  url: string;
  onUrlChange: (v: string) => void;
  username: string;
  onUsernameChange: (v: string) => void;
  password: string;
  onPasswordChange: (v: string) => void;
  // SMB
  smbHost: string; onSmbHostChange: (v: string) => void;
  smbPort: string; onSmbPortChange: (v: string) => void;
  smbShare: string; onSmbShareChange: (v: string) => void;
  smbDomain: string; onSmbDomainChange: (v: string) => void;
  // S3
  s3Bucket: string; onS3BucketChange: (v: string) => void;
  s3Region: string; onS3RegionChange: (v: string) => void;
  s3Endpoint: string; onS3EndpointChange: (v: string) => void;
  // SFTP
  sftpHost: string; onSftpHostChange: (v: string) => void;
  sftpPort: string; onSftpPortChange: (v: string) => void;
  sftpHostKey: string; onSftpHostKeyChange: (v: string) => void;
  sftpAuthMode: 'password' | 'key'; onSftpAuthModeChange: (v: 'password' | 'key') => void;
  sftpPrivateKey: string; onSftpPrivateKeyChange: (v: string) => void;
  // FTPS
  ftpHost: string; onFtpHostChange: (v: string) => void;
  ftpPort: string; onFtpPortChange: (v: string) => void;
  ftpTlsMode: FtpTlsMode; onFtpTlsModeChange: (v: FtpTlsMode) => void;
  // Field IDs for accessibility
  ids: {
    urlId: string;
    usernameId: string;
    passwordId: string;
    smbHostId: string; smbPortId: string; smbShareId: string; smbDomainId: string;
    s3BucketId: string; s3RegionId: string; s3EndpointId: string;
    sftpHostId: string; sftpPortId: string; sftpHostKeyId: string; sftpPrivateKeyId: string;
    ftpHostId: string; ftpPortId: string; ftpTlsModeId: string;
  };
}

export function ProviderFields(props: ProviderFieldsProps) {
  const { provider } = props;
  const isOAuth = isOAuthProvider(provider);

  if (provider === 'local') {
    return <LocalFields />;
  }
  if (isOAuth) {
    return <OAuthFields {...props} provider={provider} />;
  }
  if (provider === 'smb') {
    return <SmbFields {...props} />;
  }
  if (provider === 's3') {
    return <S3Fields {...props} />;
  }
  if (provider === 'sftp') {
    return <SftpFields {...props} />;
  }
  if (provider === 'ftp') {
    return <FtpFields {...props} />;
  }
  if (provider === 'immich') {
    return <ImmichFields {...props} />;
  }
  if (provider === 'magentacloud' || provider === 'koofr') {
    return <FixedCredentialsProviderFields {...props} provider={provider} />;
  }
  if (provider === 'mega') {
    return <MegaFields {...props} />;
  }
  return <NextcloudWebdavFields {...props} />;
}

export function LocalFields() {
  const { t } = useTranslation();
  return (
    <div className="border border-[var(--color-info-border)] bg-[var(--color-info-bg)] p-4 text-[var(--color-info-text)] flex items-start gap-2">
      <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
      <p className="text-xs font-sans leading-relaxed">{t('connect.localInfo')}</p>
    </div>
  );
}

export function OAuthFields({
  provider,
  editing,
  oauthUser,
  oauthRefreshToken,
  onOpenOAuthPopup,
  onDisconnectOAuth,
}: ProviderFieldsProps & { provider: ProviderId }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-3">
      {oauthRefreshToken || oauthUser ? (
        <div className="border border-[var(--color-success-border)] bg-[var(--color-success-bg)] p-4 text-[var(--color-success-text)] flex items-center justify-between">
          <div className="truncate pr-2">
            <p className="font-bold text-[9px] uppercase tracking-wider text-[var(--color-success-text)] font-mono">
              {t('settings.connections.oauthConnectedAs', { user: oauthUser || provider })}
            </p>
          </div>
          <button type="button" onClick={onDisconnectOAuth} className="ui-button-secondary px-3 py-1.5 text-[10px] font-mono font-bold">
            {t('connect.disconnect')}
          </button>
        </div>
      ) : (
        <button
          type="button"
          onClick={onOpenOAuthPopup}
          className="ui-button-primary w-full py-3 px-4 font-mono font-bold text-[11px] uppercase tracking-wider flex items-center justify-center gap-2"
        >
          <RefreshCw className="w-4 h-4" />
          {t('connect.oauthConnect', { provider: t(`connect.providerName.${provider}`) })}
        </button>
      )}
      {editing && !oauthRefreshToken && (
        <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.reauthorizeHint')}</p>
      )}
    </div>
  );
}

export function SmbFields({
  editing,
  username, onUsernameChange,
  password, onPasswordChange,
  smbHost, onSmbHostChange,
  smbPort, onSmbPortChange,
  smbShare, onSmbShareChange,
  smbDomain, onSmbDomainChange,
  ids,
}: ProviderFieldsProps) {
  const { t } = useTranslation();
  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <div className="space-y-1.5 sm:col-span-2">
          <label htmlFor={ids.smbHostId} className={labelCls}>{t('connect.serverHost')}</label>
          <input id={ids.smbHostId} type="text" required value={smbHost} onChange={(e) => onSmbHostChange(e.target.value)} className={inputCls} placeholder="192.168.1.10" />
        </div>
        <div className="space-y-1.5">
          <label htmlFor={ids.smbPortId} className={labelCls}>{t('connect.port')}</label>
          <input id={ids.smbPortId} type="number" min="1" max="65535" step="1" inputMode="numeric" required value={smbPort} onChange={(e) => onSmbPortChange(e.target.value)} className={inputCls} placeholder="445" />
        </div>
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="space-y-1.5">
          <label htmlFor={ids.smbShareId} className={labelCls}>{t('connect.share')}</label>
          <input id={ids.smbShareId} type="text" required value={smbShare} onChange={(e) => onSmbShareChange(e.target.value)} className={inputCls} placeholder={t('connect.sharePlaceholder')} />
        </div>
        <div className="space-y-1.5">
          <label htmlFor={ids.smbDomainId} className={labelCls}>{t('connect.domain')}</label>
          <input id={ids.smbDomainId} type="text" value={smbDomain} onChange={(e) => onSmbDomainChange(e.target.value)} className={inputCls} placeholder="WORKGROUP" />
        </div>
      </div>

      <div className="space-y-1.5">
        <label htmlFor={ids.usernameId} className={labelCls}>{t('connect.username')}</label>
        <input id={ids.usernameId} type="text" autoComplete="username" required value={username} onChange={(e) => onUsernameChange(e.target.value)} className={inputCls} placeholder={t('connect.usernamePlaceholder')} />
      </div>

      <div className="space-y-1.5">
        <label htmlFor={ids.passwordId} className={labelCls}>{t('settings.connections.passwordLabel')}</label>
        <input
          id={ids.passwordId}
          type="password"
          value={password}
          onChange={(e) => onPasswordChange(e.target.value)}
          className={inputCls}
          autoComplete={passwordAutoComplete(editing)}
          placeholder={editing ? `•••• (${t('settings.smtpPasswordUnchanged')})` : t('connect.password')}
          required={!editing}
        />
        {editing && <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.saveProfileHint')}</p>}
      </div>
    </>
  );
}

export function S3Fields({
  editing,
  username, onUsernameChange,
  password, onPasswordChange,
  s3Bucket, onS3BucketChange,
  s3Region, onS3RegionChange,
  s3Endpoint, onS3EndpointChange,
  ids,
}: ProviderFieldsProps) {
  const { t } = useTranslation();
  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="space-y-1.5">
          <label htmlFor={ids.s3BucketId} className={labelCls}>{t('connect.s3Bucket')}</label>
          <input id={ids.s3BucketId} type="text" required value={s3Bucket} onChange={(e) => onS3BucketChange(e.target.value)} className={inputCls} placeholder={t('connect.bucketPlaceholder')} />
        </div>
        <div className="space-y-1.5">
          <label htmlFor={ids.s3RegionId} className={labelCls}>{t('connect.s3Region')}</label>
          <input id={ids.s3RegionId} type="text" required value={s3Region} onChange={(e) => onS3RegionChange(e.target.value)} className={inputCls} placeholder="us-east-1" />
        </div>
      </div>

      <div className="space-y-1.5">
        <label htmlFor={ids.s3EndpointId} className={labelCls}>{t('connect.s3Endpoint')}</label>
        <input id={ids.s3EndpointId} type="url" value={s3Endpoint} onChange={(e) => onS3EndpointChange(e.target.value)} className={inputCls} placeholder={t('connect.s3EndpointPlaceholder')} />
      </div>

      <div className="space-y-1.5">
        <label htmlFor={ids.usernameId} className={labelCls}>{t('connect.accessKey')}</label>
        <input id={ids.usernameId} type="text" autoComplete="off" required value={username} onChange={(e) => onUsernameChange(e.target.value)} className={inputCls} placeholder="AKIAIOSFODNN7EXAMPLE" />
      </div>

      <div className="space-y-1.5">
        <label htmlFor={ids.passwordId} className={labelCls}>{t('connect.secretKey')}</label>
        <input
          id={ids.passwordId}
          type="password"
          value={password}
          onChange={(e) => onPasswordChange(e.target.value)}
          className={`${inputCls} font-mono`}
          autoComplete="off"
          placeholder={editing ? `•••• (${t('settings.smtpPasswordUnchanged')})` : 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY'}
          required={!editing}
        />
        {editing && <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.saveProfileHint')}</p>}
      </div>

    </>
  );
}

export function SftpFields({
  editing,
  username, onUsernameChange,
  password, onPasswordChange,
  sftpHost, onSftpHostChange,
  sftpPort, onSftpPortChange,
  sftpHostKey, onSftpHostKeyChange,
  sftpAuthMode, onSftpAuthModeChange,
  sftpPrivateKey, onSftpPrivateKeyChange,
  ids,
}: ProviderFieldsProps) {
  const { t } = useTranslation();
  const handleAuthModeKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return;

    event.preventDefault();
    const nextMode = event.key === 'ArrowLeft' || event.key === 'ArrowUp' || event.key === 'Home'
      ? 'password'
      : 'key';
    onSftpAuthModeChange(nextMode);
    const buttons = event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>('[role="radio"]');
    buttons?.[nextMode === 'password' ? 0 : 1]?.focus();
  };
  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <div className="space-y-1.5 sm:col-span-2">
          <label htmlFor={ids.sftpHostId} className={labelCls}>{t('connect.serverHost')}</label>
          <input id={ids.sftpHostId} type="text" required value={sftpHost} onChange={(e) => onSftpHostChange(e.target.value)} className={inputCls} placeholder="192.168.1.10" />
        </div>
        <div className="space-y-1.5">
          <label htmlFor={ids.sftpPortId} className={labelCls}>{t('connect.port')}</label>
          <input id={ids.sftpPortId} type="number" min="1" max="65535" step="1" inputMode="numeric" required value={sftpPort} onChange={(e) => onSftpPortChange(e.target.value)} className={inputCls} placeholder="22" />
        </div>
      </div>

      <div className="space-y-1.5">
        <label htmlFor={ids.sftpHostKeyId} className={labelCls}>{t('connect.sftpHostKey')}</label>
        <input id={ids.sftpHostKeyId} type="text" autoComplete="off" required value={sftpHostKey} onChange={(e) => onSftpHostKeyChange(e.target.value)} className={`${inputCls} font-mono`} placeholder="SHA256:..." />
        <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('connect.sftpHostKeyHint')}</p>
      </div>

      <div className="space-y-1.5">
        <label htmlFor={ids.usernameId} className={labelCls}>{t('connect.username')}</label>
        <input id={ids.usernameId} type="text" autoComplete="username" required value={username} onChange={(e) => onUsernameChange(e.target.value)} className={inputCls} placeholder="root" />
      </div>

      <fieldset className="space-y-1.5">
        <legend className={labelCls}>{t('connect.auth')}</legend>
        <div className="flex gap-2">
          <button
            type="button"
            onClick={() => onSftpAuthModeChange('password')}
            onKeyDown={handleAuthModeKeyDown}
            role="radio"
            aria-checked={sftpAuthMode === 'password'}
            tabIndex={sftpAuthMode === 'password' ? 0 : -1}
            className={`flex-1 py-1.5 px-3 text-[11px] font-bold font-mono cursor-pointer ${
              sftpAuthMode === 'password'
                ? 'ui-button-primary text-[var(--color-text-inverse)]'
                : 'ui-button-secondary text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]'
            }`}
          >
            {t('connect.authPassword')}
          </button>
          <button
            type="button"
            onClick={() => onSftpAuthModeChange('key')}
            onKeyDown={handleAuthModeKeyDown}
            role="radio"
            aria-checked={sftpAuthMode === 'key'}
            tabIndex={sftpAuthMode === 'key' ? 0 : -1}
            className={`flex-1 py-1.5 px-3 text-[11px] font-bold font-mono cursor-pointer ${
              sftpAuthMode === 'key'
                ? 'ui-button-primary text-[var(--color-text-inverse)]'
                : 'ui-button-secondary text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]'
            }`}
          >
            {t('connect.sshKey')}
          </button>
        </div>
      </fieldset>

      {sftpAuthMode === 'password' ? (
        <div className="space-y-1.5">
          <label htmlFor={ids.passwordId} className={labelCls}>{t('connect.password')}</label>
          <input
            id={ids.passwordId}
            type="password"
            value={password}
            onChange={(e) => onPasswordChange(e.target.value)}
            className={`${inputCls} font-mono`}
            autoComplete={passwordAutoComplete(editing)}
            placeholder={editing ? `•••• (${t('settings.smtpPasswordUnchanged')})` : t('connect.password')}
            required={!editing}
          />
          {editing && <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.saveProfileHint')}</p>}
        </div>
      ) : (
        <div className="space-y-1.5">
          <label htmlFor={ids.sftpPrivateKeyId} className={labelCls}>{t('connect.sshKeyPem')}</label>
          <textarea
            id={ids.sftpPrivateKeyId}
            value={sftpPrivateKey}
            onChange={(e) => onSftpPrivateKeyChange(e.target.value)}
            rows={4}
            className={`${inputCls} font-mono resize-none`}
            autoComplete="off"
            placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;...&#10;-----END OPENSSH PRIVATE KEY-----"
            required={!editing}
          />
          {editing && <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.saveProfileHint')}</p>}
        </div>
      )}
    </>
  );
}

export function FtpFields({
  editing,
  username, onUsernameChange,
  password, onPasswordChange,
  ftpHost, onFtpHostChange,
  ftpPort, onFtpPortChange,
  ftpTlsMode, onFtpTlsModeChange,
  ids,
}: ProviderFieldsProps) {
  const { t } = useTranslation();
  const selectTlsMode = (tlsMode: FtpTlsMode) => {
    onFtpTlsModeChange(tlsMode);
    onFtpPortChange(tlsMode === 'explicit' ? '21' : '990');
  };

  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <div className="space-y-1.5 sm:col-span-2">
          <label htmlFor={ids.ftpHostId} className={labelCls}>{t('connect.serverHost')}</label>
          <input id={ids.ftpHostId} type="text" required value={ftpHost} onChange={(e) => onFtpHostChange(e.target.value)} className={inputCls} placeholder="ftp.example.com" />
        </div>
        <div className="space-y-1.5">
          <label htmlFor={ids.ftpPortId} className={labelCls}>{t('connect.port')}</label>
          <input id={ids.ftpPortId} type="number" min="1" max="65535" step="1" inputMode="numeric" required value={ftpPort} onChange={(e) => onFtpPortChange(e.target.value)} className={inputCls} placeholder={ftpTlsMode === 'explicit' ? '21' : '990'} />
        </div>
      </div>
      <div className="space-y-1.5">
        <label htmlFor={ids.ftpTlsModeId} className={labelCls}>{t('connect.ftpsMode')}</label>
        <select id={ids.ftpTlsModeId} value={ftpTlsMode} onChange={(e) => selectTlsMode(e.target.value as FtpTlsMode)} className={inputCls}>
          <option value="explicit">{t('connect.ftpsExplicit')}</option>
          <option value="implicit">{t('connect.ftpsImplicit')}</option>
        </select>
        <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('connect.ftpsHint')}</p>
      </div>
      <div className="space-y-1.5">
        <label htmlFor={ids.usernameId} className={labelCls}>{t('connect.username')}</label>
        <input id={ids.usernameId} type="text" autoComplete="username" required value={username} onChange={(e) => onUsernameChange(e.target.value)} className={inputCls} placeholder={t('connect.usernamePlaceholder')} />
      </div>
      <div className="space-y-1.5">
        <label htmlFor={ids.passwordId} className={labelCls}>{t('connect.password')}</label>
        <input id={ids.passwordId} type="password" autoComplete={passwordAutoComplete(editing)} value={password} onChange={(e) => onPasswordChange(e.target.value)} className={`${inputCls} font-mono`} placeholder={editing ? `•••• (${t('settings.smtpPasswordUnchanged')})` : t('connect.password')} required={!editing} />
        {editing && <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.saveProfileHint')}</p>}
      </div>
    </>
  );
}

export function ImmichFields({
  editing,
  url, onUrlChange,
  password, onPasswordChange,
  ids,
}: ProviderFieldsProps) {
  const { t } = useTranslation();
  return (
    <>
      <div className="ui-alert ui-alert-info p-4 flex items-start gap-2">
        <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
        <p className="text-xs font-sans leading-relaxed">{t('connect.immichPermissionHint')}</p>
      </div>
      <div className="space-y-1.5">
        <label htmlFor={ids.urlId} className={labelCls}>{t('connect.immichUrl')}</label>
        <input id={ids.urlId} type="url" required value={url} onChange={(e) => onUrlChange(e.target.value)} className={inputCls} placeholder={t('connect.immichUrlPlaceholder')} />
      </div>
      <div className="space-y-1.5">
        <label htmlFor={ids.passwordId} className={labelCls}>{t('connect.immichApiKey')}</label>
        <input
          id={ids.passwordId}
          type="password"
          value={password}
          onChange={(e) => onPasswordChange(e.target.value)}
          className={`${inputCls} font-mono`}
          autoComplete="off"
          placeholder={editing ? `•••• (${t('settings.smtpPasswordUnchanged')})` : t('connect.immichApiKeyPlaceholder')}
          required={!editing}
        />
        {editing && <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.saveProfileHint')}</p>}
      </div>
    </>
  );
}

function FixedCredentialsProviderFields({
  provider,
  editing,
  username, onUsernameChange,
  password, onPasswordChange,
  ids,
}: ProviderFieldsProps & { provider: FixedCredentialsProvider }) {
  return (
    <SharedFixedCredentialsFields
      provider={provider}
      editing={editing}
      username={username}
      password={password}
      onUsernameChange={onUsernameChange}
      onPasswordChange={onPasswordChange}
      usernameId={ids.usernameId}
      passwordId={ids.passwordId}
      inputClassName={inputCls}
      labelClassName={labelCls}
      fieldClassName="space-y-1.5"
    />
  );
}

export function MegaFields({ editing, username, onUsernameChange, password, onPasswordChange, ids }: ProviderFieldsProps) {
  const { t } = useTranslation();
  return (
    <>
      <div className="ui-alert ui-alert-info p-4 flex items-start gap-2">
        <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
        <p className="text-xs font-sans leading-relaxed">{t('connect.megaInfo')}</p>
      </div>
      <div className="space-y-1.5">
        <label htmlFor={ids.usernameId} className={labelCls}>{t('connect.megaEmail')}</label>
        <input id={ids.usernameId} type="email" autoComplete="username" required value={username} onChange={(e) => onUsernameChange(e.target.value)} className={inputCls} placeholder="name@example.com" />
      </div>
      <div className="space-y-1.5">
        <label htmlFor={ids.passwordId} className={labelCls}>{t('connect.password')}</label>
        <input
          id={ids.passwordId}
          type="password"
          autoComplete={passwordAutoComplete(editing)}
          required={!editing}
          value={password}
          onChange={(e) => onPasswordChange(e.target.value)}
          className={inputCls}
          placeholder={editing ? `•••• (${t('settings.smtpPasswordUnchanged')})` : t('connect.password')}
        />
        {editing && <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.saveProfileHint')}</p>}
      </div>
    </>
  );
}

const urlLabelKeys: Record<string, string> = {
  opencloud: 'connect.opencloudUrl',
  nextcloud: 'connect.nextcloudUrl',
  seafile: 'connect.seafileUrl',
};

const urlPlaceholderKeys: Record<string, string> = {
  opencloud: 'connect.opencloudUrlPlaceholder',
  nextcloud: 'connect.nextcloudUrlPlaceholder',
  seafile: 'connect.seafileUrlPlaceholder',
};

export function NextcloudWebdavFields({
  provider,
  editing,
  url, onUrlChange,
  username, onUsernameChange,
  password, onPasswordChange,
  ids,
}: ProviderFieldsProps) {
  const { t } = useTranslation();
  return (
    <>
      <div className="space-y-1.5">
        <label htmlFor={ids.urlId} className={labelCls}>
          {t(urlLabelKeys[provider] ?? 'connect.webdavUrl')}
        </label>
        <input
          id={ids.urlId}
          type="url"
          required
          value={url}
          onChange={(e) => onUrlChange(e.target.value)}
          className={inputCls}
          placeholder={t(urlPlaceholderKeys[provider] ?? 'connect.webdavUrlPlaceholder')}
        />
      </div>
      <div className="space-y-1.5">
        <label htmlFor={ids.usernameId} className={labelCls}>{t('connect.username')}</label>
        <input id={ids.usernameId} type="text" autoComplete="username" required={provider !== 'seafile'} value={username} onChange={(e) => onUsernameChange(e.target.value)} className={inputCls} placeholder={t('connect.usernamePlaceholder')} />
      </div>
      <div className="space-y-1.5">
        <label htmlFor={ids.passwordId} className={labelCls}>
          {provider === 'nextcloud' ? t('connect.appPasswordLabel') : t('connect.password')}
        </label>
        <input
          id={ids.passwordId}
          type="password"
          value={password}
          onChange={(e) => onPasswordChange(e.target.value)}
          className={inputCls}
          autoComplete={passwordAutoComplete(editing)}
          placeholder={editing ? `•••• (${t('settings.smtpPasswordUnchanged')})` : (provider === 'nextcloud' ? '•••• •••• •••• ••••' : t('connect.password'))}
          required={!editing}
        />
        {editing && <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.saveProfileHint')}</p>}
      </div>
    </>
  );
}
