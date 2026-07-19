import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Plug, Plus, Pencil, Trash2, RefreshCw, CheckCircle2, AlertCircle, X } from 'lucide-react';
import { useApiError } from '../utils/apiError';
import type { ApiErrBody } from './SettingsPage';

interface ConnectionManagerProps {
  apiUrl: string;
  token: string;
  localStorageEnabled?: boolean;
  oauthProviders?: Record<string, boolean>;
}

type ProviderId = 'nextcloud' | 'dropbox' | 'webdav' | 'magentacloud' | 'google' | 'googlephotos' | 'smb' | 's3' | 'sftp' | 'local';

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

type MessageState = { text: string; type: 'success' | 'error' } | null;

const inputCls = 'w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans';
const labelCls = 'block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2';
const primaryBtnCls = 'bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all uppercase tracking-wider cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed';
const secondaryBtnCls = 'px-4 py-2.5 rounded-xl text-xs font-mono border border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-portal-navy-themed)] transition-all cursor-pointer';

function MessageBanner({ message }: { message: MessageState }) {
  if (!message) return null;
  return (
    <div className={`p-3 rounded-xl border text-[11px] font-mono text-center leading-relaxed ${
      message.type === 'success' ? 'bg-emerald-50 border-emerald-200 text-emerald-800' : 'bg-[var(--color-error-bg)] border-[var(--color-error-border)] text-[var(--color-error-text)]'
    }`}>
      {message.text}
    </div>
  );
}

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

  const [profiles, setProfiles] = useState<ProfilePublic[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [message, setMessage] = useState<MessageState>(null);
  const [editing, setEditing] = useState<ProfilePublic | null>(null);
  const [creating, setCreating] = useState<boolean>(false);

  const loadProfiles = useCallback(() => {
    fetch(`${apiUrl}/api/profiles`, {
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
    if (!window.confirm(t('settings.connections.deleteConfirm'))) return;
    setMessage(null);
    try {
      const res = await fetch(`${apiUrl}/api/profiles/${p.id}`, {
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
      const res = await fetch(`${apiUrl}/api/profiles/${p.id}/test`, {
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
    ...(oauthProviders.googlephotos ? [{ id: 'googlephotos' as const, name: 'Google Photos' }] : []),
    ...(localStorageEnabled ? [{ id: 'local' as const, name: 'Local' }] : []),
  ];

  const isOAuth = (prov: string) => prov === 'dropbox' || prov === 'google' || prov === 'googlephotos';

  return (
    <div className="space-y-6">
      <div className="glass-panel rounded-2xl p-6 border border-[var(--color-glass-border)]/50 shadow-portal space-y-5">
        <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
          <Plug className="w-4 h-4 text-[var(--color-portal-orange-themed)]" />
          <h3 className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)]">{t('settings.connections.title')}</h3>
        </div>
        <p className="text-[11px] text-[var(--color-text-secondary)] font-sans leading-relaxed">
          {t('settings.connections.subtitle')}
        </p>

        {message && <MessageBanner message={message} />}

        <button
          onClick={() => { setEditing(null); setCreating(true); }}
          className={`w-full inline-flex items-center justify-center gap-2 ${primaryBtnCls}`}
        >
          <Plus className="w-4 h-4" />
          {t('settings.connections.newProfile')}
        </button>
      </div>

      {/* Profile list */}
      {loading ? (
        <div className="text-center text-[11px] font-mono text-[var(--color-text-muted)] py-8">{t('common.loading')}</div>
      ) : profiles.length === 0 ? (
        <div className="glass-panel rounded-2xl p-6 border border-[var(--color-glass-border)]/50 shadow-portal text-center text-[11px] text-[var(--color-text-muted)] font-sans">
          {t('settings.connections.noProfiles')}
        </div>
      ) : (
        <div className="grid gap-4">
          {profiles.map((p) => {
            const exp = formatExpiry(p.token_expires_at);
            const provName = providerOptions.find((o) => o.id === p.provider)?.name || p.provider;
            return (
              <div key={p.id} className="glass-panel rounded-2xl p-5 border border-[var(--color-glass-border)]/50 shadow-portal space-y-3">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)] truncate">{p.name}</span>
                      <span className="text-[9px] font-mono font-bold uppercase tracking-wider px-2 py-0.5 rounded-full bg-portal-orange/10 border border-portal-orange text-[var(--color-portal-orange-themed)]">
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
                    <span className="flex items-center gap-1.5"><Pencil className="w-3.5 h-3.5" />{t('settings.connections.edit')}</span>
                  </button>
                  <button
                    onClick={() => handleTest(p)}
                    className={secondaryBtnCls}
                  >
                    <span className="flex items-center gap-1.5"><CheckCircle2 className="w-3.5 h-3.5" />{t('settings.connections.test')}</span>
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
                    className="px-4 py-2.5 rounded-xl text-xs font-mono border border-[var(--color-error-border)] text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)]/70 transition-all cursor-pointer"
                  >
                    <span className="flex items-center gap-1.5"><Trash2 className="w-3.5 h-3.5" />{t('settings.connections.delete')}</span>
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
        fetch(`${apiUrl}/api/profiles/${profile.id}`, {
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
      <span className="flex items-center gap-1.5"><RefreshCw className="w-3.5 h-3.5" />{t('settings.connections.reauthorize')}</span>
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

  const [name, setName] = useState<string>(editing?.name || '');
  const [provider, setProvider] = useState<ProviderId>((editing?.provider as ProviderId) || 'nextcloud');
  const [url, setUrl] = useState<string>(editing?.url || '');
  const [username, setUsername] = useState<string>(editing?.username || '');
  const [password, setPassword] = useState<string>('');
  const [oauthUser, setOauthUser] = useState<string>(editing?.oauth_user || '');
  const [oauthRefreshToken, setOauthRefreshToken] = useState<string>('');
  const [saving, setSaving] = useState<boolean>(false);

  const isOAuth = provider === 'dropbox' || provider === 'google' || provider === 'googlephotos';
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
      const res = await fetch(urlStr, {
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
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-blue-900/40 backdrop-blur-sm p-4">
      <div className="w-full max-w-lg bg-[var(--color-bg-primary)] rounded-2xl p-6 border border-[var(--color-border)] shadow-portal space-y-5">
        <div className="flex items-center justify-between pb-3 border-b border-[var(--color-border-light)]">
          <h3 className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)]">
            {editing ? t('settings.connections.edit') : t('settings.connections.newProfile')}
          </h3>
          <button onClick={onClose} className="text-[var(--color-text-muted)] hover:text-[var(--color-text-secondary)] cursor-pointer">
            <X className="w-4 h-4" />
          </button>
        </div>

        <form onSubmit={handleSave} className="space-y-4">
          <div className="space-y-1.5">
            <label className={labelCls}>{t('settings.connections.nameLabel')}</label>
            <input type="text" required value={name} onChange={(e) => setName(e.target.value)} className={inputCls} placeholder="Mein Cloud" />
          </div>

          <div className="space-y-1.5">
            <label className={labelCls}>{t('settings.connections.providerLabel')}</label>
            <select
              value={provider}
              disabled={!!editing}
              onChange={(e) => setProvider(e.target.value as ProviderId)}
              className={inputCls}
            >
              {providerOptions.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}
            </select>
          </div>

          {provider === 'local' ? (
            <div className="bg-blue-50/80 border border-blue-200 text-blue-800 rounded-2xl p-4 flex items-start gap-2 shadow-xs">
              <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
              <p className="text-xs font-sans leading-relaxed">{t('connect.localInfo')}</p>
            </div>
          ) : isOAuth ? (
            <div className="space-y-3">
              {oauthRefreshToken ? (
                <div className="bg-emerald-50/80 border border-emerald-200 text-emerald-800 rounded-2xl p-4 flex items-center justify-between shadow-xs">
                  <div className="truncate pr-2">
                    <p className="font-bold text-[9px] uppercase tracking-wider text-emerald-650 font-mono">{t('settings.connections.oauthConnectedAs', { user: oauthUser || provider })}</p>
                  </div>
                  <button type="button" onClick={() => { setOauthRefreshToken(''); setOauthUser(''); }} className="px-3 py-1.5 bg-[var(--color-bg-secondary)] border border-emerald-250 text-emerald-750 text-[10px] font-mono font-bold rounded-xl shadow-xs hover:bg-emerald-100 cursor-pointer">
                    {t('connect.disconnect')}
                  </button>
                </div>
              ) : (
                <button type="button" onClick={openOAuthPopup}
                  className="w-full py-3 px-4 bg-portal-navy hover:bg-portal-navy-light text-white font-mono font-bold text-[11px] uppercase tracking-wider rounded-xl shadow-xs hover:shadow-sm transition-all cursor-pointer flex items-center justify-center gap-2">
                  <RefreshCw className="w-4 h-4" /> {t('connect.oauthConnect', { provider: provider === 'google' ? 'Google' : provider === 'googlephotos' ? 'Google Photos' : 'Dropbox' })}
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
                  <label className={labelCls}>{t('connect.nextcloudUrl')}</label>
                  <input type="text" value={url} onChange={(e) => setUrl(e.target.value)} className={inputCls} placeholder="https://cloud.example.com" />
                </div>
              )}
              <div className="space-y-1.5">
                <label className={labelCls}>{t('connect.username')}</label>
                <input type="text" value={username} onChange={(e) => setUsername(e.target.value)} className={inputCls} placeholder="benutzername" />
              </div>
              <div className="space-y-1.5">
                <label className={labelCls}>{t('settings.connections.passwordLabel')}</label>
                <input
                  type="password"
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
