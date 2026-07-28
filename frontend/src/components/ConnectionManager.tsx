import React, { useState, useEffect, useCallback, useId, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useApiError } from '../utils/apiError';
import { useConfirm } from '../contexts/useConfirm';
import { apiFetch } from '../utils/apiClient';
import { MessageBanner, type MessageState } from './MessageBanner';
import { useFocusTrap } from '../hooks/useFocusTrap';
import type { ApiErrBody } from './SettingsPage';
import { LinkIcon as Plug } from '@heroicons/react/24/outline';

interface ConnectionManagerProps {
  apiUrl: string;
  token: string;
  localStorageEnabled?: boolean;
  oauthProviders?: Record<string, boolean>;
}

type ProviderId = 'nextcloud' | 'dropbox' | 'webdav' | 'magentacloud' | 'google' | 'hidrive' | 'smb' | 's3' | 'sftp' | 'local';

interface ProfilePublic {
  id: string;
  name: string;
  provider: string;
  url?: string;
  username?: string;
  has_password: boolean;
  token_expires_at?: string | null;
  oauth_user?: string;
  created_at: string;
  updated_at: string;
}

const inputCls = 'ui-input w-full px-3 py-2 text-sm font-sans';
const labelCls = 'block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2';
const primaryBtnCls = 'ui-button-primary py-2 text-sm font-medium hover:opacity-90 disabled:opacity-50';
const secondaryBtnCls = 'ui-button-secondary px-3 py-2 text-sm hover:bg-[var(--color-bg-tertiary)]';

function formatExpiry(expiresAt?: string | null): string | null {
  if (!expiresAt) return null;
  const t = new Date(expiresAt).getTime();
  if (isNaN(t)) return null;
  const days = Math.round((t - Date.now()) / (1000 * 60 * 60 * 24));
  if (days < 0) return 'expired';
  return String(days);
}

export function ConnectionManager({ apiUrl, token, localStorageEnabled = false, oauthProviders = {} }: ConnectionManagerProps) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const confirm = useConfirm();

  const [profiles, setProfiles] = useState<ProfilePublic[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [message, setMessage] = useState<MessageState>(null);
  const [editing, setEditing] = useState<ProfilePublic | null>(null);
  const [creating, setCreating] = useState<boolean>(false);

  const loadProfiles = useCallback(() => {
    apiFetch(`${apiUrl}/api/profiles`, {
      headers: { 'Authorization': `Bearer ${token}` },
    })
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then((data: { profiles?: ProfilePublic[] }) => {
        setProfiles(data.profiles ?? []);
        setLoading(false);
      })
      .catch(() => {
        setProfiles([]);
        setLoading(false);
      });
  }, [apiUrl, token]);

  useEffect(() => {
    loadProfiles();
  }, [loadProfiles]);

  const handleDelete = async (p: ProfilePublic) => {
    const ok = await confirm({ message: t('settings.connections.deleteConfirm') });
    if (!ok) return;
    setMessage(null);
    try {
      const res = await apiFetch(`${apiUrl}/api/profiles/${p.id}`, {
        method: 'DELETE',
        headers: { 'Authorization': `Bearer ${token}` },
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError(data.error_code));
      }
      setMessage({ text: t('settings.connections.deleted'), type: 'success' });
      loadProfiles();
    } catch (err) {
      setMessage({ text: (err as Error).message, type: 'error' });
    }
  };

  const handleTest = async (p: ProfilePublic) => {
    setMessage(null);
    try {
      const res = await apiFetch(`${apiUrl}/api/profiles/${p.id}/test`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${token}` },
      });
      const data = await res.json();
      if (!res.ok || !data.success) {
        throw new Error(translateApiError(data.error_code));
      }
      setMessage({ text: t('settings.connections.testSuccess'), type: 'success' });
    } catch (err) {
      setMessage({ text: (err as Error).message, type: 'error' });
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
    ...(oauthProviders.hidrive ? [{ id: 'hidrive' as const, name: 'HiDrive' }] : []),
    ...(localStorageEnabled ? [{ id: 'local' as const, name: 'Local' }] : []),
  ];

  const isOAuth = (prov: string) => prov === 'dropbox' || prov === 'google' || prov === 'hidrive';

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
          onClick={() => { setEditing(null); setCreating(true); }}
          className={`w-full inline-flex items-center justify-center gap-2 ${primaryBtnCls}`}
        >
          {t('settings.connections.newProfile')}
        </button>
      </div>

      {/* Profile list */}
      {loading ? (
        <div className="text-center text-[11px] font-mono text-[var(--color-text-muted)] py-8">{t('common.loading')}</div>
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
                      <span className="font-display font-semibold text-sm text-[var(--color-text-primary)] truncate">{p.name}</span>
                      <span className="text-[9px] font-mono font-bold uppercase tracking-wider px-2 py-0.5 rounded-md bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] text-[var(--color-text-secondary)]">
                        {provName}
                      </span>
                    </div>
                    {p.oauth_user && (
                      <p className="text-[10px] font-mono text-[var(--color-text-secondary)] mt-1 truncate">
                        {t('settings.connections.oauthConnectedAs', { user: p.oauth_user })}
                        {exp && (
                          <span className="text-[var(--color-text-muted)]"> · {t('settings.connections.tokenExpiresIn', { days: exp })}</span>
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
                    onClick={() => { setCreating(false); setEditing(p); }}
                    className={secondaryBtnCls}
                  >
                    {t('settings.connections.edit')}
                  </button>
                  <button
                    onClick={() => handleTest(p)}
                    className={secondaryBtnCls}
                  >
                    {t('settings.connections.test')}
                  </button>
                  {isOAuth(p.provider) && (
                    <ReauthorizeButton
                      apiUrl={apiUrl}
                      token={token}
                      profile={p}
                      onReauthorized={() => { setMessage({ text: t('settings.connections.updated'), type: 'success' }); loadProfiles(); }}
                      onError={(msg) => setMessage({ text: msg, type: 'error' })}
                    />
                  )}
                  <button
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
  apiUrl: string; token: string; profile: ProfilePublic;
  onReauthorized: () => void; onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const [busy, setBusy] = useState(false);

  const openReauth = () => {
    const provider = profile.provider;
    const width = 600, height = 700;
    const left = window.screen.width / 2 - width / 2;
    const top = window.screen.height / 2 - height / 2;
    const targetOrigin = new URL(apiUrl, window.location.origin).origin;

    const popup = window.open(
      `${apiUrl}/api/oauth/auth?provider=${provider}&purpose=connect&origin=${encodeURIComponent(window.location.origin)}`,
      'OAuth',
      `width=${width},height=${height},left=${left},top=${top}`
    );
    setBusy(true);

    const cleanup = () => {
      window.removeEventListener('message', handleMessage);
      clearInterval(checkClosed);
      setBusy(false);
    };
    const handleMessage = (event: MessageEvent) => {
      if (event.origin !== targetOrigin || event.source !== popup) return;
      if (event.data?.type === 'oauth-success' && event.data.provider === provider && event.data.purpose === 'connect') {
        const refreshToken: string = event.data.refreshToken || '';
        cleanup();
        if (!refreshToken) {
          onError(t('settings.connections.testFailed'));
          return;
        }
        // Persist the new refresh token onto the existing profile.
        apiFetch(`${apiUrl}/api/profiles/${profile.id}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
          body: JSON.stringify({
            refresh_token: refreshToken,
            oauth_user: event.data.username || provider,
          }),
        })
          .then(async (res) => {
            if (!res.ok) {
              const data = await res.json().catch(() => ({}) as ApiErrBody);
              throw new Error(translateApiError(data.error_code));
            }
            onReauthorized();
          })
          .catch((err) => onError(err instanceof Error ? err.message : t('settings.connections.testFailed')));
      } else if (event.data?.type === 'oauth-error') {
        cleanup();
        onError(t('settings.connections.testFailed'));
      }
    };
    const checkClosed = setInterval(() => {
      let closed = true;
      try { closed = !popup || popup.closed; } catch { /* ignore */ }
      if (closed) cleanup();
    }, 1000);
    window.addEventListener('message', handleMessage);
  };

  return (
    <button onClick={openReauth} disabled={busy} className={secondaryBtnCls}>
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
  editing: ProfilePublic | null;
  onClose: () => void;
  onSaved: () => void;
  onError: (msg: string) => void;
}

function ProfileEditor({ apiUrl, token, providerOptions, editing, onClose, onSaved, onError }: ProfileEditorProps) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  const nameId = useId();
  const providerId = useId();
  const urlId = useId();
  const usernameId = useId();
  const passwordId = useId();

  const [name, setName] = useState<string>(editing?.name || '');
  const [provider, setProvider] = useState<ProviderId>((editing?.provider as ProviderId) || 'nextcloud');
  const [url, setUrl] = useState<string>(editing?.url || '');
  const [username, setUsername] = useState<string>(editing?.username || '');
  const [password, setPassword] = useState<string>('');
  const [oauthUser, setOauthUser] = useState<string>(editing?.oauth_user || '');
  const [oauthRefreshToken, setOauthRefreshToken] = useState<string>('');
  const [saving, setSaving] = useState<boolean>(false);
  useFocusTrap(dialogRef, closeRef, onClose);

  const isOAuth = provider === 'dropbox' || provider === 'google';
  const needsPassword = !isOAuth && provider !== 'local';

  const openOAuthPopup = () => {
    const width = 600, height = 700;
    const left = window.screen.width / 2 - width / 2;
    const top = window.screen.height / 2 - height / 2;
    const targetOrigin = new URL(apiUrl, window.location.origin).origin;
    const popup = window.open(
      `${apiUrl}/api/oauth/auth?provider=${provider}&purpose=connect&origin=${encodeURIComponent(window.location.origin)}`,
      'OAuth',
      `width=${width},height=${height},left=${left},top=${top}`
    );
    const cleanup = () => { window.removeEventListener('message', handleMessage); clearInterval(checkClosed); };
    const handleMessage = (event: MessageEvent) => {
      if (event.origin !== targetOrigin || event.source !== popup) return;
      if (event.data?.type === 'oauth-success' && event.data.provider === provider && event.data.purpose === 'connect') {
        cleanup();
        setOauthUser(event.data.username || provider);
        setOauthRefreshToken(event.data.refreshToken || '');
      } else if (event.data?.type === 'oauth-error') {
        cleanup();
        onError(t('settings.connections.testFailed'));
      }
    };
    const checkClosed = setInterval(() => {
      let closed = true;
      try { closed = !popup || popup.closed; } catch { /* ignore */ }
      if (closed) cleanup();
    }, 1000);
    window.addEventListener('message', handleMessage);
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) { onError(t('settings.connections.nameLabel') + ' ' + t('common.required').toLowerCase()); return; }
    setSaving(true);

    const payload: Record<string, unknown> = {
      name: name.trim(),
      provider,
    };
    // Only send url/username when present (PUT leaves omitted fields unchanged).
    if (url) payload.url = url;
    if (isOAuth) {
      payload.username = oauthUser || provider;
    } else if (username) {
      payload.username = username;
    }
    // Only send credentials when present (PUT leaves omitted fields unchanged).
    if (needsPassword && password) payload.password = password;
    if (isOAuth && oauthRefreshToken) {
      payload.refresh_token = oauthRefreshToken;
      payload.oauth_user = oauthUser || provider;
    }

    try {
      const method = editing ? 'PUT' : 'POST';
      const urlStr = editing ? `${apiUrl}/api/profiles/${editing.id}` : `${apiUrl}/api/profiles`;
      const res = await apiFetch(urlStr, {
        method,
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
        body: JSON.stringify(payload),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError(data.error_code));
      }
      onSaved();
    } catch (err) {
      onError((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-[var(--color-bg-inverse)]/40 p-4">
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby={titleId} className="w-full max-w-lg rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-primary)] p-6 space-y-5">
        <div className="flex items-center justify-between pb-3 border-b border-[var(--color-border-light)]">
          <h3 id={titleId} className="font-display font-semibold text-sm text-[var(--color-text-primary)]">
            {editing ? t('settings.connections.edit') : t('settings.connections.newProfile')}
          </h3>
          <button ref={closeRef} type="button" onClick={onClose} aria-label={t('common.cancel')} className="text-[var(--color-text-muted)] hover:text-[var(--color-text-secondary)] cursor-pointer">
            {t('common.cancel')}
          </button>
        </div>

        <form onSubmit={handleSave} className="space-y-4">
          <div className="space-y-1.5">
            <label htmlFor={nameId} className={labelCls}>{t('settings.connections.nameLabel')}</label>
            <input id={nameId} type="text" required value={name} onChange={(e) => setName(e.target.value)} className={inputCls} placeholder={t('connect.profileNamePlaceholder')} />
          </div>

          <div className="space-y-1.5">
            <label htmlFor={providerId} className={labelCls}>{t('settings.connections.providerLabel')}</label>
            <select
              id={providerId}
              value={provider}
              disabled={!!editing}
              onChange={(e) => setProvider(e.target.value as ProviderId)}
              className={inputCls}
            >
              {providerOptions.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}
            </select>
          </div>

          {provider === 'local' ? (
            <div className="border border-[var(--color-info-border)] bg-[var(--color-info-bg)] p-4 text-[var(--color-info-text)] flex items-start gap-2">
              <p className="text-xs font-sans leading-relaxed">{t('connect.localInfo')}</p>
            </div>
          ) : isOAuth ? (
            <div className="space-y-3">
              {oauthRefreshToken ? (
                <div className="border border-[var(--color-success-border)] bg-[var(--color-success-bg)] p-4 text-[var(--color-success-text)] flex items-center justify-between">
                  <div className="truncate pr-2">
                    <p className="font-bold text-[9px] uppercase tracking-wider text-[var(--color-success-text)] font-mono">{t('settings.connections.oauthConnectedAs', { user: oauthUser || provider })}</p>
                  </div>
                  <button type="button" onClick={() => { setOauthRefreshToken(''); setOauthUser(''); }} className="ui-button-secondary px-3 py-1.5 text-[10px] font-mono font-bold">
                    {t('connect.disconnect')}
                  </button>
                </div>
              ) : (
                <button type="button" onClick={openOAuthPopup}
                  className="ui-button-primary w-full py-3 px-4 font-mono font-bold text-[11px] uppercase tracking-wider flex items-center justify-center gap-2">
                  {t('connect.oauthConnect', { provider: provider === 'google' ? 'Google' : 'Dropbox' })}
                </button>
              )}
              {editing && !oauthRefreshToken && (
                <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.reauthorizeHint')}</p>
              )}
            </div>
          ) : (
            <>
              {provider !== 'magentacloud' && (
                <div className="space-y-1.5">
                  <label htmlFor={urlId} className={labelCls}>{t('connect.nextcloudUrl')}</label>
                  <input id={urlId} type="text" value={url} onChange={(e) => setUrl(e.target.value)} className={inputCls} placeholder={provider === 'nextcloud' ? t('connect.nextcloudUrlPlaceholder') : t('connect.webdavUrlPlaceholder')} />
                </div>
              )}
              <div className="space-y-1.5">
                <label htmlFor={usernameId} className={labelCls}>{t('connect.username')}</label>
                <input id={usernameId} type="text" autoComplete="username" name="username" value={username} onChange={(e) => setUsername(e.target.value)} className={inputCls} placeholder={t('connect.usernamePlaceholder')} />
              </div>
              <div className="space-y-1.5">
                <label htmlFor={passwordId} className={labelCls}>{t('settings.connections.passwordLabel')}</label>
                <input
                  id={passwordId}
                  type="password"
                  autoComplete="current-password"
                  name="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className={inputCls}
                  placeholder={editing ? `•••• (${t('settings.smtpPasswordUnchanged')})` : t('connect.password')}
                />
                {editing && <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.saveProfileHint')}</p>}
              </div>
            </>
          )}

          <div className="flex gap-2 pt-2">
            <button type="submit" disabled={saving} className={`flex-1 ${primaryBtnCls}`}>
              {saving ? t('settings.saving') : (editing ? t('settings.connections.edit') : t('settings.connections.saveProfile'))}
            </button>
            <button type="button" onClick={onClose} className={secondaryBtnCls}>{t('common.cancel')}</button>
          </div>
        </form>
      </div>
    </div>
  );
}
