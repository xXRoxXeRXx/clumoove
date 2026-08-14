import React, { useState, useEffect, useCallback, useId, useRef, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { useApiError } from '../utils/apiError';
import { useConfirm } from '../contexts/useConfirm';
import { apiFetch } from '../utils/apiClient';
import { listConnectionProfiles, type ConnectionProfilePublic } from '../api/profiles';
import { MessageBanner, type MessageState } from './MessageBanner';
import { LoadingIndicator } from './LoadingIndicator';
import { useFocusTrap } from '../hooks/useFocusTrap';
import { useOAuthPopup } from '../hooks/useOAuthPopup';
import type { ApiErrBody } from './SettingsPage';
import { LinkIcon as Plug } from '@heroicons/react/24/outline';
import {
  parseSmbUrl,
  buildSmbUrl,
  parseS3Url,
  buildS3Url,
  parseSftpUrl,
  buildSftpUrl,
  sftpHostKeyFingerprintPattern,
  parseFtpUrl,
  buildFtpUrl,
  type FtpTlsMode,
} from '../utils/providerUrls';
import { ProviderFields } from './connect/ProviderFields';
import { ProviderIcon } from './connect/ProviderIcon';
import { ProviderSelector } from './connect/ProviderSelector';
import { isOAuthProvider, type ProviderId } from '../types';

export interface ConnectionManagerProps {
  apiUrl: string;
  token: string;
  localStorageEnabled?: boolean;
  oauthProviders?: Record<string, boolean>;
}

const primaryBtnCls = 'ui-button-primary py-2 text-sm font-medium hover:opacity-90 disabled:opacity-50';
const secondaryBtnCls = 'ui-button-secondary px-3 py-2 text-sm hover:bg-[var(--color-bg-tertiary)]';

const isAbortError = (error: unknown) => error instanceof DOMException && error.name === 'AbortError';

function useRequestAbortController() {
  const controllersRef = useRef(new Set<AbortController>());

  useEffect(() => () => {
    controllersRef.current.forEach((controller) => controller.abort());
    controllersRef.current.clear();
  }, []);

  const create = useCallback(() => {
    const controller = new AbortController();
    controllersRef.current.add(controller);
    return controller;
  }, []);
  const release = useCallback((controller: AbortController) => {
    controllersRef.current.delete(controller);
  }, []);

  return { create, release };
}

function formatExpiry(expiresAt?: string | null): number | null {
  if (!expiresAt) return null;
  const t = new Date(expiresAt).getTime();
  if (isNaN(t)) return null;
  const days = Math.round((t - Date.now()) / (1000 * 60 * 60 * 24));
  return days < 0 ? -1 : days;
}

export function ConnectionManager({ apiUrl, token, localStorageEnabled = false, oauthProviders = {} }: ConnectionManagerProps) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const confirm = useConfirm();
  const { create: createRequestController, release: releaseRequestController } = useRequestAbortController();

  const [profiles, setProfiles] = useState<ConnectionProfilePublic[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [message, setMessage] = useState<MessageState>(null);
  const [editing, setEditing] = useState<ConnectionProfilePublic | null>(null);
  const [creating, setCreating] = useState<boolean>(false);

  const loadProfiles = useCallback(() => {
    const controller = createRequestController();
    void listConnectionProfiles(apiUrl, token, controller.signal)
      .then((result) => {
        if (result.ok === false) throw new Error('profiles request failed');
        if (!controller.signal.aborted) {
          setProfiles(result.data.profiles ?? []);
          setLoading(false);
        }
      })
      .catch((error) => {
        if (!controller.signal.aborted && !isAbortError(error)) {
          setProfiles([]);
          setLoading(false);
        }
      })
      .finally(() => releaseRequestController(controller));
  }, [apiUrl, createRequestController, releaseRequestController, token]);

  useEffect(() => {
    loadProfiles();
  }, [loadProfiles]);

  const handleDelete = async (p: ConnectionProfilePublic) => {
    const ok = await confirm({ message: t('settings.connections.deleteConfirm') });
    if (!ok) return;
    setMessage(null);
    const controller = createRequestController();
    try {
      const res = await apiFetch(`${apiUrl}/api/profiles/${p.id}`, {
        method: 'DELETE',
        headers: { 'Authorization': `Bearer ${token}` },
        signal: controller.signal,
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError(data.error_code));
      }
      if (!controller.signal.aborted) {
        setMessage({ text: t('settings.connections.deleted'), type: 'success' });
        void loadProfiles();
      }
    } catch (err) {
      if (!controller.signal.aborted && !isAbortError(err)) {
        setMessage({ text: (err as Error).message, type: 'error' });
      }
    } finally {
      releaseRequestController(controller);
    }
  };

  const handleTest = async (p: ConnectionProfilePublic) => {
    setMessage(null);
    const controller = createRequestController();
    try {
      const res = await apiFetch(`${apiUrl}/api/profiles/${p.id}/test`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${token}` },
        signal: controller.signal,
      });
      const data = await res.json();
      if (!res.ok || !data.success) {
        throw new Error(translateApiError(data.error_code));
      }
      if (!controller.signal.aborted) {
        setMessage({ text: t('settings.connections.testSuccess'), type: 'success' });
      }
    } catch (err) {
      if (!controller.signal.aborted && !isAbortError(err)) {
        setMessage({ text: (err as Error).message, type: 'error' });
      }
    } finally {
      releaseRequestController(controller);
    }
  };

  const providerOptions = useMemo(
    (): { id: ProviderId; name: string }[] => [
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
      ...(localStorageEnabled ? [{ id: 'local' as const, name: 'Local' }] : []),
    ],
	[localStorageEnabled, oauthProviders.dropbox, oauthProviders.google, oauthProviders.onedrive, oauthProviders.hidrive]
  );

  return (
    <div className="space-y-6">
      <div className="ui-card p-6 space-y-5">
        <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
          <Plug className="w-4 h-4 text-[var(--color-text-muted)]" />
          <h3 className="font-display font-semibold text-sm text-[var(--color-text-primary)]">{t('settings.connections.title')}</h3>
        </div>
        <p className="text-[11px] text-[var(--color-text-secondary)] font-sans leading-relaxed">
          {t('settings.connections.subtitle')}
        </p>

        {message && <MessageBanner message={message} />}

        <button
          type="button"
          onClick={() => { setEditing(null); setCreating(true); }}
          className={`w-full inline-flex items-center justify-center gap-2 ${primaryBtnCls}`}
        >
          {t('settings.connections.newProfile')}
        </button>
      </div>

      {/* Profile list */}
      {loading ? (
        <div className="flex justify-center py-8"><LoadingIndicator label={t('common.loading')} /></div>
      ) : profiles.length === 0 ? (
        <div className="ui-card p-6 text-center text-sm text-[var(--color-text-muted)] font-sans">
          {t('settings.connections.noProfiles')}
        </div>
      ) : (
        <div className="grid gap-4">
          {profiles.map((p) => {
            const exp = formatExpiry(p.token_expires_at);
            const provName = providerOptions.find((o) => o.id === p.provider)?.name || p.provider;
            return (
              <div key={p.id} className="ui-card p-5 space-y-3">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <ProviderIcon provider={p.provider} className="w-5 h-5 shrink-0" />
                      <span className="font-display font-semibold text-sm text-[var(--color-text-primary)] truncate">{p.name}</span>
                      <span className="text-[9px] font-mono font-bold uppercase tracking-wider px-2 py-0.5 rounded-md bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] text-[var(--color-text-secondary)]">
                        {provName}
                      </span>
                    </div>
                    {p.oauth_user && (
                      <p className="text-[10px] font-mono text-[var(--color-text-secondary)] mt-1 truncate">
                        {t('settings.connections.oauthConnectedAs', { user: p.oauth_user })}
                        {exp !== null && (
                          <span className="text-[var(--color-text-muted)]">
                            {' · '}
                            {exp < 0 ? t('settings.connections.tokenExpired') : t('settings.connections.tokenExpiresIn', { days: exp })}
                          </span>
                        )}
                      </p>
                    )}
                    {!p.oauth_user && p.username && (
                      <p className="text-[10px] font-mono text-[var(--color-text-secondary)] mt-1 truncate">{p.username}</p>
                    )}
                  </div>
                </div>

                <div className="flex flex-wrap gap-2">
                  <button
                    type="button"
                    onClick={() => { setCreating(false); setEditing(p); }}
                    className={secondaryBtnCls}
                  >
                    {t('settings.connections.edit')}
                  </button>
                  <button
                    type="button"
                    onClick={() => handleTest(p)}
                    className={secondaryBtnCls}
                  >
                    {t('settings.connections.test')}
                  </button>
                  {isOAuthProvider(p.provider) && (
                    <ReauthorizeButton
                      apiUrl={apiUrl}
                      token={token}
                      profile={p}
                      onReauthorized={() => { setMessage({ text: t('settings.connections.updated'), type: 'success' }); loadProfiles(); }}
                      onError={(msg) => setMessage({ text: msg, type: 'error' })}
                    />
                  )}
                  <button
                    type="button"
                    onClick={() => handleDelete(p)}
                    className="ui-button-secondary px-3 py-2 text-sm border-[var(--color-error-border)] text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)]"
                  >
                    {t('settings.connections.delete')}
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      )}

      {(creating || editing) && (
        <ProfileEditor
          apiUrl={apiUrl}
          token={token}
          providerOptions={providerOptions}
          editing={editing}
          onClose={() => { setCreating(false); setEditing(null); }}
          onSaved={() => { setCreating(false); setEditing(null); setMessage({ text: editing ? t('settings.connections.updated') : t('settings.connections.saved'), type: 'success' }); loadProfiles(); }}
          onError={(msg) => setMessage({ text: msg, type: 'error' })}
        />
      )}
    </div>
  );
}

// ReauthorizeButton opens the provider OAuth popup and, on success, writes the
// new refresh token to the existing profile via PUT.
function ReauthorizeButton({ apiUrl, token, profile, onReauthorized, onError }: {
  apiUrl: string; token: string; profile: ConnectionProfilePublic;
  onReauthorized: () => void; onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const [busy, setBusy] = useState(false);
  const { openOAuthPopup } = useOAuthPopup(apiUrl);
  const { create: createRequestController, release: releaseRequestController } = useRequestAbortController();

  const openReauth = () => {
    const provider = profile.provider;
    setBusy(true);
    openOAuthPopup(provider, 'connect', {
      onSuccess: (msg) => {
        const refreshToken = msg.refreshToken || '';
        if (!refreshToken) {
          setBusy(false);
          onError(t('settings.connections.testFailed'));
          return;
        }
        const controller = createRequestController();
        apiFetch(`${apiUrl}/api/profiles/${profile.id}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
          signal: controller.signal,
          body: JSON.stringify({
            refresh_token: refreshToken,
            oauth_user: msg.username || provider,
          }),
        })
          .then(async (res) => {
            if (controller.signal.aborted) return;
            if (!res.ok) {
              const data = await res.json().catch(() => ({}) as ApiErrBody);
              throw new Error(translateApiError(data.error_code));
            }
            onReauthorized();
          })
          .catch((err) => {
            if (!isAbortError(err)) onError(err instanceof Error ? err.message : t('settings.connections.testFailed'));
          })
          .finally(() => {
            releaseRequestController(controller);
            if (!controller.signal.aborted) setBusy(false);
          });
      },
      onError: (code) => {
        setBusy(false);
        onError(translateApiError(code));
      },
    });
  };

  return (
    <button type="button" onClick={openReauth} disabled={busy} className={secondaryBtnCls}>
      {t('settings.connections.reauthorize')}
    </button>
  );
}

// ---------------------------------------------------------------------------
// Profile editor (create + edit)
// ---------------------------------------------------------------------------

interface ProfileEditorProps {
  apiUrl: string;
  token: string;
  providerOptions: { id: ProviderId; name: string }[];
  editing: ConnectionProfilePublic | null;
  onClose: () => void;
  onSaved: () => void;
  onError: (msg: string) => void;
}

interface FormState {
  name: string;
  provider: ProviderId;
  url: string;
  username: string;
  password: string;
  oauthUser: string;
  oauthRefreshToken: string;
  smbHost: string;
  smbPort: string;
  smbShare: string;
  smbDomain: string;
  s3Bucket: string;
  s3Region: string;
  s3Endpoint: string;
  sftpHost: string;
  sftpPort: string;
  sftpHostKey: string;
  sftpAuthMode: 'password' | 'key';
  sftpPrivateKey: string;
  ftpHost: string;
  ftpPort: string;
  ftpTlsMode: FtpTlsMode;
}

function initFormState(editing: ConnectionProfilePublic | null): FormState {
  const provider = (editing?.provider as ProviderId) || 'nextcloud';
  const smb = parseSmbUrl(provider === 'smb' ? editing?.url || '' : '');
  const s3 = parseS3Url(provider === 's3' ? editing?.url || '' : '');
  const sftp = parseSftpUrl(provider === 'sftp' ? editing?.url || '' : '');
  const ftp = parseFtpUrl(provider === 'ftp' ? editing?.url || '' : '');

  return {
    name: editing?.name || '',
    provider,
    url: editing?.url || '',
    username: editing?.username || '',
    password: '',
    oauthUser: editing?.oauth_user || '',
    oauthRefreshToken: '',
    smbHost: smb.host,
    smbPort: smb.port || '445',
    smbShare: smb.share,
    smbDomain: smb.domain,
    s3Bucket: s3.bucket,
    s3Region: s3.region || 'us-east-1',
    s3Endpoint: s3.endpoint,
    sftpHost: sftp.host,
    sftpPort: sftp.port || '22',
    sftpHostKey: sftp.hostKey,
    sftpAuthMode: 'password',
    sftpPrivateKey: '',
    ftpHost: ftp.host,
    ftpPort: ftp.port,
    ftpTlsMode: ftp.tlsMode,
  };
}

function ProfileEditor({ apiUrl, token, providerOptions, editing, onClose, onSaved, onError }: ProfileEditorProps) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  const nameId = useId();

  const fieldIds = useMemo(() => ({
    urlId: 'profile-editor-url',
    usernameId: 'profile-editor-username',
    passwordId: 'profile-editor-password',
    smbHostId: 'profile-editor-smb-host',
    smbPortId: 'profile-editor-smb-port',
    smbShareId: 'profile-editor-smb-share',
    smbDomainId: 'profile-editor-smb-domain',
    s3BucketId: 'profile-editor-s3-bucket',
    s3RegionId: 'profile-editor-s3-region',
    s3EndpointId: 'profile-editor-s3-endpoint',
    sftpHostId: 'profile-editor-sftp-host',
    sftpPortId: 'profile-editor-sftp-port',
    sftpHostKeyId: 'profile-editor-sftp-hostkey',
    sftpPrivateKeyId: 'profile-editor-sftp-privatekey',
    ftpHostId: 'profile-editor-ftp-host',
    ftpPortId: 'profile-editor-ftp-port',
    ftpTlsModeId: 'profile-editor-ftp-tls-mode',
  }), []);

  const { openOAuthPopup: triggerOAuthPopup } = useOAuthPopup(apiUrl);

  const [form, setForm] = useState<FormState>(() => initFormState(editing));
  const [saving, setSaving] = useState<boolean>(false);
  const { create: createRequestController, release: releaseRequestController } = useRequestAbortController();
  useFocusTrap(dialogRef, closeRef, onClose);

  const isOAuth = isOAuthProvider(form.provider);
  const needsPassword = !isOAuth && form.provider !== 'local';

  const updateField = <K extends keyof FormState>(key: K, value: FormState[K]) => {
    setForm((prev) => ({ ...prev, [key]: value }));
  };

  const handleProviderSelect = (newProvider: ProviderId) => {
    setForm((prev) => {
      const next = { ...prev, provider: newProvider };
      if (editing && editing.provider === newProvider && editing.url) {
        if (newProvider === 'smb') {
          const smb = parseSmbUrl(editing.url);
          next.smbHost = smb.host; next.smbPort = smb.port || '445'; next.smbShare = smb.share; next.smbDomain = smb.domain;
        } else if (newProvider === 's3') {
          const s3 = parseS3Url(editing.url);
          next.s3Bucket = s3.bucket; next.s3Region = s3.region || 'us-east-1'; next.s3Endpoint = s3.endpoint;
        } else if (newProvider === 'sftp') {
          const sftp = parseSftpUrl(editing.url);
          next.sftpHost = sftp.host; next.sftpPort = sftp.port || '22'; next.sftpHostKey = sftp.hostKey;
        } else if (newProvider === 'ftp') {
          const ftp = parseFtpUrl(editing.url);
          next.ftpHost = ftp.host; next.ftpPort = ftp.port; next.ftpTlsMode = ftp.tlsMode;
        } else {
          next.url = editing.url;
        }
      } else {
        if (newProvider === 'smb' && !prev.smbPort) next.smbPort = '445';
        if (newProvider === 's3' && !prev.s3Region) next.s3Region = 'us-east-1';
        if (newProvider === 'sftp' && !prev.sftpPort) next.sftpPort = '22';
        if (newProvider === 'ftp' && !prev.ftpPort) next.ftpPort = '21';
      }
      return next;
    });
  };

  const openOAuthPopup = () => {
    triggerOAuthPopup(form.provider, 'connect', {
      onSuccess: (msg) => {
        updateField('oauthUser', msg.username || form.provider);
        updateField('oauthRefreshToken', msg.refreshToken || '');
      },
      onError: (code) => {
        onError(translateApiError(code));
      },
    });
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name.trim()) {
      onError(t('settings.connections.nameRequired'));
      return;
    }

    let finalUrl: string;
    let finalUsername = form.username;
    let finalPassword = form.password;

    if (form.provider === 'smb') {
      if (!form.smbHost.trim() || !form.smbShare.trim()) {
        onError(t('common.required'));
        return;
      }
      finalUrl = buildSmbUrl(form.smbHost, form.smbPort, form.smbShare, form.smbDomain);
      if (!finalUrl) {
        onError(t('connect.errors.invalidConnectionUrl'));
        return;
      }
    } else if (form.provider === 's3') {
      if (!form.s3Bucket.trim()) {
        onError(t('common.required'));
        return;
      }
      finalUrl = buildS3Url(form.s3Bucket, form.s3Region, form.s3Endpoint);
      if (!finalUrl) {
        onError(t('connect.errors.invalidConnectionUrl'));
        return;
      }
    } else if (form.provider === 'sftp') {
      if (!form.sftpHost.trim() || !form.sftpHostKey.trim()) {
        onError(t('common.required'));
        return;
      }
      if (!sftpHostKeyFingerprintPattern.test(form.sftpHostKey.trim())) {
        onError(t('connect.sftpHostKeyHint'));
        return;
      }
      finalUrl = buildSftpUrl(form.sftpHost, form.sftpPort, form.sftpHostKey);
      if (!finalUrl) {
        onError(t('connect.errors.invalidConnectionUrl'));
        return;
      }
      finalPassword = form.sftpAuthMode === 'key' ? form.sftpPrivateKey : form.password;
    } else if (form.provider === 'ftp') {
      if (!form.ftpHost.trim()) {
        onError(t('connect.errors.ftpHost'));
        return;
      }
      finalUrl = buildFtpUrl(form.ftpHost, form.ftpPort, form.ftpTlsMode);
      if (!finalUrl) {
        onError(t('connect.errors.ftpPort'));
        return;
      }
    } else if (form.provider === 'immich') {
      if (!form.url.trim()) {
        onError(t('common.required'));
        return;
      }
      finalUrl = form.url;
      finalUsername = '';
      finalPassword = form.password;
    } else if (form.provider === 'magentacloud' || form.provider === 'koofr' || form.provider === 'local' || form.provider === 'mega') {
      finalUrl = '';
      finalUsername = form.provider === 'local' ? '' : form.username;
    } else if (isOAuth) {
      // Backend ignores the URL for OAuth, setting placeholder to satisfy NOT NULL constraints
      finalUrl = `oauth://${form.provider}`;
      finalUsername = form.oauthUser || form.provider;
    } else {
      if (!form.url.trim()) {
        onError(t('common.required'));
        return;
      }
      finalUrl = form.url;
    }

    setSaving(true);

    const payload: Record<string, unknown> = {
      name: form.name.trim(),
      provider: form.provider,
      url: finalUrl,
      username: finalUsername,
    };

    if (isOAuth) {
      payload.username = form.oauthUser || form.provider;
      if (form.oauthRefreshToken) {
        payload.refresh_token = form.oauthRefreshToken;
        payload.oauth_user = form.oauthUser || form.provider;
      }
    } else if (needsPassword && finalPassword) {
      payload.password = finalPassword;
    }

    const controller = createRequestController();
    try {
      const method = editing ? 'PUT' : 'POST';
      const urlStr = editing ? `${apiUrl}/api/profiles/${editing.id}?url=1&username=1` : `${apiUrl}/api/profiles`;
      const res = await apiFetch(urlStr, {
        method,
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
        body: JSON.stringify(payload),
        signal: controller.signal,
      });
      if (controller.signal.aborted) return;
      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError(data.error_code));
      }
      onSaved();
    } catch (err) {
      if (!isAbortError(err)) onError((err as Error).message);
    } finally {
      releaseRequestController(controller);
      if (!controller.signal.aborted) setSaving(false);
    }
  };

  const labelCls = 'block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2';
  const inputCls = 'ui-input w-full px-3 py-2 text-sm font-sans';

  return (
    <div
      className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4"
      onClick={(event) => { if (event.target === event.currentTarget && !saving) onClose(); }}
    >
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1} className="w-full max-w-3xl max-h-[90vh] overflow-y-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-primary)] p-6 space-y-5">
        <div className="flex items-center justify-between pb-3 border-b border-[var(--color-border-light)]">
          <h3 id={titleId} className="font-display font-semibold text-sm text-[var(--color-text-primary)]">
            {editing ? t('settings.connections.edit') : t('settings.connections.newProfile')}
          </h3>
          <button ref={closeRef} type="button" onClick={onClose} disabled={saving} className="text-[var(--color-text-muted)] hover:text-[var(--color-text-secondary)] cursor-pointer disabled:cursor-not-allowed disabled:opacity-55">
            {t('common.cancel')}
          </button>
        </div>

        <form onSubmit={handleSave} className="space-y-4">
          <div className="grid grid-cols-1 md:grid-cols-12 gap-6">
            <div className="md:col-span-6 border-r-0 md:border-r border-[var(--color-border-light)] md:pr-4">
              <ProviderSelector
                providers={providerOptions}
                selectedProvider={form.provider}
                onSelectProvider={(val) => {
                  if (!editing) {
                    handleProviderSelect(val as ProviderId);
                  }
                }}
                label={t('settings.connections.providerLabel')}
              />
            </div>

            <div className="md:col-span-6 space-y-4">
              <div className="space-y-1.5">
                <label htmlFor={nameId} className={labelCls}>{t('settings.connections.nameLabel')}</label>
                <input id={nameId} type="text" required value={form.name} onChange={(e) => updateField('name', e.target.value)} className={inputCls} placeholder={t('connect.profileNamePlaceholder')} />
              </div>

              <ProviderFields
                provider={form.provider}
                editing={!!editing}
                oauthUser={form.oauthUser}
                oauthRefreshToken={form.oauthRefreshToken}
                onOpenOAuthPopup={openOAuthPopup}
                onDisconnectOAuth={() => { updateField('oauthRefreshToken', ''); updateField('oauthUser', ''); }}
                url={form.url}
                onUrlChange={(v) => updateField('url', v)}
                username={form.username}
                onUsernameChange={(v) => updateField('username', v)}
                password={form.password}
                onPasswordChange={(v) => updateField('password', v)}
                smbHost={form.smbHost}
                onSmbHostChange={(v) => updateField('smbHost', v)}
                smbPort={form.smbPort}
                onSmbPortChange={(v) => updateField('smbPort', v)}
                smbShare={form.smbShare}
                onSmbShareChange={(v) => updateField('smbShare', v)}
                smbDomain={form.smbDomain}
                onSmbDomainChange={(v) => updateField('smbDomain', v)}
                s3Bucket={form.s3Bucket}
                onS3BucketChange={(v) => updateField('s3Bucket', v)}
                s3Region={form.s3Region}
                onS3RegionChange={(v) => updateField('s3Region', v)}
                s3Endpoint={form.s3Endpoint}
                onS3EndpointChange={(v) => updateField('s3Endpoint', v)}
                sftpHost={form.sftpHost}
                onSftpHostChange={(v) => updateField('sftpHost', v)}
                sftpPort={form.sftpPort}
                onSftpPortChange={(v) => updateField('sftpPort', v)}
                sftpHostKey={form.sftpHostKey}
                onSftpHostKeyChange={(v) => updateField('sftpHostKey', v)}
                sftpAuthMode={form.sftpAuthMode}
                onSftpAuthModeChange={(v) => updateField('sftpAuthMode', v)}
                sftpPrivateKey={form.sftpPrivateKey}
                onSftpPrivateKeyChange={(v) => updateField('sftpPrivateKey', v)}
                ftpHost={form.ftpHost}
                onFtpHostChange={(v) => updateField('ftpHost', v)}
                ftpPort={form.ftpPort}
                onFtpPortChange={(v) => updateField('ftpPort', v)}
                ftpTlsMode={form.ftpTlsMode}
                onFtpTlsModeChange={(v) => updateField('ftpTlsMode', v)}
                ids={fieldIds}
              />

              <div className="flex gap-2 pt-4">
                <button type="submit" disabled={saving} className={`flex-1 ${primaryBtnCls}`}>
                  {saving ? t('settings.saving') : (editing ? t('settings.connections.edit') : t('settings.connections.saveProfile'))}
                </button>
                <button type="button" onClick={onClose} disabled={saving} className={secondaryBtnCls}>{t('common.cancel')}</button>
              </div>
            </div>
          </div>
        </form>
      </div>
    </div>
  );
}
