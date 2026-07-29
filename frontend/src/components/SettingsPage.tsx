import React, { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowLeftIcon as ArrowLeft, ComputerDesktopIcon as Monitor, Cog6ToothIcon as Settings, EnvelopeIcon as Mail, EyeIcon as Eye, EyeSlashIcon as EyeOff, InformationCircleIcon as Info, LockClosedIcon as Lock, MoonIcon as Moon, LinkIcon as Plug, ShieldCheckIcon as ShieldCheck, SunIcon as Sun, SwatchIcon as Palette, TrashIcon as Trash2, ArrowUpTrayIcon as Upload, UserIcon as User } from '@heroicons/react/24/outline';
import { AvatarCropper } from './AvatarCropper';
import { ConnectionManager } from './ConnectionManager';
import { useThemeContext } from '../contexts/useThemeContext';
import { useConfirm } from '../contexts/useConfirm';
import { useApiError } from '../utils/apiError';
import { apiFetch } from '../utils/apiClient';
import { MessageBanner, type MessageState } from './MessageBanner';
import { Toggle } from './Toggle';

export type ApiErrBody = { error_code?: string };

const cardCls = 'ui-card p-6 space-y-5';
const inputCls = 'ui-input w-full px-4 py-2.5 text-sm font-sans';
const primaryBtnCls = 'ui-button-primary py-2.5 text-xs font-bold font-mono disabled:opacity-50 disabled:cursor-not-allowed';
const secondaryBtnCls = 'ui-button-secondary px-4 py-2.5 text-xs font-bold font-mono';
const sectionTitleCls = 'font-display font-semibold text-sm text-[var(--color-text-primary)]';

interface SettingsUser {
  id?: string;
  email?: string;
  display_name?: string;
  role?: string;
  avatar?: string;
}

interface SettingsPageProps {
  apiUrl: string;
  token: string;
  user: SettingsUser | null;
  onBack: () => void;
  onUpdateUser: (updatedUser: Partial<SettingsUser>) => void;
  localStorageEnabled?: boolean;
  oauthProviders?: Record<string, boolean>;
}

export function SettingsPage({ apiUrl, token, user, onBack, onUpdateUser, localStorageEnabled = false, oauthProviders = {} }: SettingsPageProps) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const confirm = useConfirm();

  const [tab, setTab] = useState<'account' | 'connections' | 'appearance' | 'notifications' | 'about'>('account');
  const tabs = ['account', 'connections', 'appearance', 'notifications', 'about'] as const;
  const tabItems = [
    ['account', User, 'settings.tabs.account'],
    ['connections', Plug, 'settings.tabs.connections'],
    ['appearance', Palette, 'settings.tabs.appearance'],
    ['notifications', Mail, 'settings.tabs.notifications'],
    ['about', Info, 'settings.tabs.about'],
  ] as const;

  // Theme context
  const { preference, setPreference, systemTheme } = useThemeContext();

  // Display name state
  const [displayName, setDisplayName] = useState<string>(user?.display_name || '');
  const [profileLoading, setProfileLoading] = useState<boolean>(false);
  const [profileMessage, setProfileMessage] = useState<MessageState>(null);

  // Avatar crop/upload state
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [showCropper, setShowCropper] = useState<boolean>(false);
  const [avatarLoading, setAvatarLoading] = useState<boolean>(false);
  const [avatarMessage, setAvatarMessage] = useState<MessageState>(null);

  // Password state
  const [currentPassword, setCurrentPassword] = useState<string>('');
  const [newPassword, setNewPassword] = useState<string>('');
  const [confirmPassword, setConfirmPassword] = useState<string>('');
  const [showCurrentPassword, setShowCurrentPassword] = useState<boolean>(false);
  const [showNewPassword, setShowNewPassword] = useState<boolean>(false);
  const [showConfirmPassword, setShowConfirmPassword] = useState<boolean>(false);
  const [passwordLoading, setPasswordLoading] = useState<boolean>(false);
  const [passwordMessage, setPasswordMessage] = useState<MessageState>(null);

  // Email change state
  const [emailChangeAvailable, setEmailChangeAvailable] = useState<boolean | null>(null);
  const [newEmail, setNewEmail] = useState<string>('');
  const [emailChangeLoading, setEmailChangeLoading] = useState<boolean>(false);
  const [emailChangeMessage, setEmailChangeMessage] = useState<MessageState>(null);

  // 2FA state
  const [totpEnabled, setTotpEnabled] = useState<boolean>(false);
  const [totpStatusLoading, setTotpStatusLoading] = useState<boolean>(true);
  const [setupData, setSetupData] = useState<{ otpauth_uri: string; qr_png: string; secret: string } | null>(null);
  const [setupLoading, setSetupLoading] = useState<boolean>(false);
  const [enableCode, setEnableCode] = useState<string>('');
  const [enableLoading, setEnableLoading] = useState<boolean>(false);
  const [backupCodes, setBackupCodes] = useState<string[]>([]);
  const [disableCode, setDisableCode] = useState<string>('');
  const [disableLoading, setDisableLoading] = useState<boolean>(false);
  const [totpMessage, setTotpMessage] = useState<MessageState>(null);

  // Fetch 2FA status on mount
  useEffect(() => {
    let cancelled = false;
    apiFetch(`${apiUrl}/api/auth/2fa/status`, {
      headers: { 'Authorization': `Bearer ${token}` },
    })
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then((data) => {
        if (!cancelled) setTotpEnabled(Boolean(data.totp_enabled));
      })
      .catch(() => {
        if (!cancelled) setTotpEnabled(false);
      })
      .finally(() => {
        if (!cancelled) setTotpStatusLoading(false);
      });
    return () => { cancelled = true; };
  }, [apiUrl, token]);

  const handle2FASetup = async () => {
    setTotpMessage(null);
    setSetupLoading(true);
    try {
      const res = await apiFetch(`${apiUrl}/api/auth/2fa/setup`, {
        method: 'GET',
        headers: { 'Authorization': `Bearer ${token}` },
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError((data as ApiErrBody).error_code));
      }
      const data = await res.json();
      setSetupData(data);
      setBackupCodes([]);
    } catch (err) {
      setTotpMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setSetupLoading(false);
    }
  };

  const handle2FAEnable = async (e: React.FormEvent) => {
    e.preventDefault();
    setTotpMessage(null);
    const code = enableCode.trim();
    if (!code) {
      setTotpMessage({ text: t('settings.messages.totpNeedCode'), type: 'error' });
      return;
    }
    setEnableLoading(true);
    try {
      const res = await apiFetch(`${apiUrl}/api/auth/2fa/enable`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify({ code }),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError((data as ApiErrBody).error_code));
      }
      const data = await res.json();
      setTotpEnabled(true);
      setSetupData(null);
      setEnableCode('');
      setBackupCodes(data.backup_codes || []);
    } catch (err) {
      setTotpMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setEnableLoading(false);
    }
  };

  const handle2FADisable = async (e: React.FormEvent) => {
    e.preventDefault();
    setTotpMessage(null);
    if (!disableCode.trim()) {
      setTotpMessage({ text: t('settings.messages.totpNeedCode'), type: 'error' });
      return;
    }
    setDisableLoading(true);
    try {
      const res = await apiFetch(`${apiUrl}/api/auth/2fa/disable`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify({ code: disableCode.trim() }),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError((data as ApiErrBody).error_code));
      }
      setTotpEnabled(false);
      setDisableCode('');
      setSetupData(null);
      setBackupCodes([]);
      setTotpMessage({ text: t('settings.messages.totpDisabled'), type: 'success' });
    } catch (err) {
      setTotpMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setDisableLoading(false);
    }
  };

  // Fetch whether the system mail service allows email changes
  useEffect(() => {
    let cancelled = false;
    apiFetch(`${apiUrl}/api/auth/email-change-available`)
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then((data) => {
        if (!cancelled) setEmailChangeAvailable(Boolean(data.available));
      })
      .catch(() => {
        if (!cancelled) setEmailChangeAvailable(false);
      });
    return () => { cancelled = true; };
  }, [apiUrl]);

  // SMTP settings state
  const [smtpHost, setSmtpHost] = useState<string>('');
  const [smtpPort, setSmtpPort] = useState<string>('587');
  const [smtpUsername, setSmtpUsername] = useState<string>('');
  const [smtpPassword, setSmtpPassword] = useState<string>('');
  const [smtpFromEmail, setSmtpFromEmail] = useState<string>('');
  const [smtpFromName, setSmtpFromName] = useState<string>('');
  const [smtpEncryption, setSmtpEncryption] = useState<string>('tls');
  const [smtpNotify, setSmtpNotify] = useState<boolean>(true);
  const [smtpHasConfig, setSmtpHasConfig] = useState<boolean>(false);
  const [smtpLoading, setSmtpLoading] = useState<boolean>(false);
  const [smtpMessage, setSmtpMessage] = useState<MessageState>(null);
  type PushChannel = 'gotify' | 'ntfy' | 'telegram' | 'discord';
  const [pushConfigs, setPushConfigs] = useState<Record<PushChannel, Record<string, string>>>({
    gotify: { url: '', token: '' }, ntfy: { url: '', topic: '', token: '', priority: '' },
    telegram: { bot_token: '', chat_id: '' }, discord: { webhook_url: '' },
  });
  const [pushEnabled, setPushEnabled] = useState<Record<PushChannel, boolean>>({ gotify: false, ntfy: false, telegram: false, discord: false });
  const [pushLoading, setPushLoading] = useState<PushChannel | null>(null);
  const [pushMessage, setPushMessage] = useState<MessageState>(null);

  // The email card is one of the notification channels. Other channels can be
  // added without changing the legacy SMTP-shaped form fields below.
  useEffect(() => {
    let cancelled = false;
    apiFetch(`${apiUrl}/api/settings/notifications`, {
      headers: { 'Authorization': `Bearer ${token}` },
    })
      .then((res) => {
        if (res.ok) return res.json();
        throw new Error('no-smtp');
      })
      .then((data) => {
        if (cancelled) return;
        const email = (data.channels || []).find((channel: { type: string }) => channel.type === 'email');
        if (!email) { setSmtpHasConfig(false); } else {
          const config = email.config || {};
          setSmtpHasConfig(true);
          setSmtpHost(config.smtp_host || '');
          setSmtpPort(String(config.smtp_port || '587'));
          setSmtpUsername(config.smtp_username || '');
          setSmtpFromEmail(config.smtp_from_email || '');
          setSmtpFromName(config.smtp_from_name || '');
          setSmtpEncryption(config.smtp_encryption || 'tls');
          setSmtpNotify(email.enabled !== false);
        }
        (['gotify', 'ntfy', 'telegram', 'discord'] as PushChannel[]).forEach((type) => {
          const channel = (data.channels || []).find((item: { type: string }) => item.type === type);
          if (channel) {
            setPushEnabled((current) => ({ ...current, [type]: channel.enabled }));
            const editable = Object.fromEntries(Object.entries(channel.config || {}).filter(([key]) => !key.endsWith('_set')));
            setPushConfigs((current) => ({ ...current, [type]: { ...current[type], ...editable } }));
          }
        });
      })
      .catch(() => {
        if (!cancelled) setSmtpHasConfig(false);
      });
    return () => { cancelled = true; };
  }, [apiUrl, token]);

  const handleSaveSMTP = async (e: React.FormEvent) => {
    e.preventDefault();
    setSmtpMessage(null);
    setSmtpLoading(true);

    const portNum = parseInt(smtpPort, 10);
    if (isNaN(portNum) || portNum < 1 || portNum > 65535) {
      setSmtpMessage({ text: t('settings.messages.smtpPortRange'), type: 'error' });
      setSmtpLoading(false);
      return;
    }

    const config: Record<string, string | number> = {
      smtp_host: smtpHost,
      smtp_port: portNum,
      smtp_username: smtpUsername,
      smtp_from_email: smtpFromEmail,
      smtp_from_name: smtpFromName,
      smtp_encryption: smtpEncryption,
    };
    // Only send the password when the user entered a new one (existing password is kept otherwise)
    if (smtpPassword) {
      config.smtp_password = smtpPassword;
    }

    try {
      const res = await apiFetch(`${apiUrl}/api/settings/notifications`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify({ type: 'email', enabled: smtpNotify, config }),
      });

      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError((data as ApiErrBody).error_code));
      }

      setSmtpHasConfig(true);
      setSmtpPassword('');
      setSmtpMessage({ text: t('settings.messages.smtpSaved'), type: 'success' });
    } catch (err) {
      setSmtpMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setSmtpLoading(false);
    }
  };

  const handleTestSMTP = async () => {
    setSmtpMessage(null);
    setSmtpLoading(true);
    try {
      const res = await apiFetch(`${apiUrl}/api/settings/notifications/test`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
        body: JSON.stringify({ type: 'email', config: {
          smtp_host: smtpHost, smtp_port: parseInt(smtpPort, 10), smtp_username: smtpUsername,
          smtp_password: smtpPassword, smtp_from_email: smtpFromEmail, smtp_from_name: smtpFromName, smtp_encryption: smtpEncryption,
        } }),
      });
      const data = await res.json();
      if (!res.ok || !data.success) {
        throw new Error(translateApiError(data.error_code));
      }
      setSmtpMessage({ text: t('settings.messages.smtpTestSent'), type: 'success' });
    } catch (err) {
      setSmtpMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setSmtpLoading(false);
    }
  };

  const savePushChannel = async (type: PushChannel, test = false) => {
    setPushMessage(null);
    setPushLoading(type);
    try {
      const endpoint = test ? '/api/settings/notifications/test' : '/api/settings/notifications';
      const res = await apiFetch(`${apiUrl}${endpoint}`, {
        method: test ? 'POST' : 'PUT',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
        body: JSON.stringify({ type, enabled: pushEnabled[type], config: pushConfigs[type] }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok || data.success === false) throw new Error(translateApiError(data.error_code));
      setPushMessage({ text: test ? t('settings.notificationTestSent') : t('settings.notificationSaved'), type: 'success' });
      if (!test) {
        setPushConfigs((current) => ({ ...current, [type]: { ...current[type], token: '', bot_token: '', webhook_url: '' } }));
      }
    } catch (err) {
      setPushMessage({ text: (err as Error).message, type: 'error' });
    } finally { setPushLoading(null); }
  };

  const renderPushChannel = (type: PushChannel, fields: Array<{ key: string; label: string; secret?: boolean; placeholder?: string }>) => (
    <div className="ui-card p-6 space-y-4" key={type}>
      <div className="flex items-center justify-between gap-3 pb-3 border-b border-[var(--color-border-light)]">
        <div className="flex items-center gap-2"><Plug className="w-4 h-4 text-[var(--color-text-muted)]" /><h3 className={sectionTitleCls}>{type === 'ntfy' ? 'ntfy' : type[0].toUpperCase() + type.slice(1)}</h3></div>
        <label className="flex items-center gap-2 text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono"><input type="checkbox" checked={pushEnabled[type]} onChange={(e) => setPushEnabled((current) => ({ ...current, [type]: e.target.checked }))} /> {t('settings.notificationEnabled')}</label>
      </div>
      {fields.map((field) => <div className="space-y-1.5" key={field.key}><label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{field.label}</label><input type={field.secret ? 'password' : 'text'} value={pushConfigs[type][field.key] || ''} placeholder={field.placeholder || (field.secret ? t('settings.notificationSecretHint') : '')} onChange={(e) => setPushConfigs((current) => ({ ...current, [type]: { ...current[type], [field.key]: e.target.value } }))} className={inputCls} /></div>)}
      <div className="flex gap-2.5"><button type="button" onClick={() => savePushChannel(type)} disabled={pushLoading !== null} className={`flex-1 ${primaryBtnCls}`}>{pushLoading === type ? t('settings.saving') : t('settings.notificationSave')}</button><button type="button" onClick={() => savePushChannel(type, true)} disabled={pushLoading !== null} className={secondaryBtnCls}>{t('settings.notificationTest')}</button></div>
    </div>
  );

  const handleUpdateProfile = async (e: React.FormEvent) => {
    e.preventDefault();
    setProfileMessage(null);
    setProfileLoading(true);

    try {
      const res = await apiFetch(`${apiUrl}/api/auth/me`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify({ display_name: displayName.trim() }),
      });

      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError((data as ApiErrBody).error_code));
      }

      const data = await res.json();
      onUpdateUser({ ...user, display_name: data.display_name });
      setProfileMessage({ text: t('settings.messages.profileUpdated'), type: 'success' });
    } catch (err) {
      setProfileMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setProfileLoading(false);
    }
  };

  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files && e.target.files.length > 0) {
      setSelectedFile(e.target.files[0]);
      setShowCropper(true);
    }
  };

  const handleCropComplete = async (croppedDataUrl: string) => {
    setShowCropper(false);
    setSelectedFile(null);
    setAvatarMessage(null);
    setAvatarLoading(true);

    // Client-side defense: cap the decoded avatar size before upload to avoid
    // large-payload abuse. The backend remains authoritative for final limits.
    const MAX_AVATAR_BYTES = 2 * 1024 * 1024; // 2 MiB
    const commaIdx = croppedDataUrl.indexOf(',');
    const b64 = commaIdx >= 0 ? croppedDataUrl.slice(commaIdx + 1) : croppedDataUrl;
    const approxBytes = Math.ceil((b64.length * 3) / 4);
    if (approxBytes > MAX_AVATAR_BYTES) {
      setAvatarLoading(false);
      setAvatarMessage({ text: t('settings.messages.avatarTooLarge'), type: 'error' });
      return;
    }

    try {
      const res = await apiFetch(`${apiUrl}/api/user/avatar`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify({ avatar: croppedDataUrl }),
      });

      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError((data as ApiErrBody).error_code));
      }

      const data = await res.json();
      onUpdateUser({ ...user, avatar: data.avatar });
      setAvatarMessage({ text: t('settings.messages.avatarUploaded'), type: 'success' });
    } catch (err) {
      setAvatarMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setAvatarLoading(false);
    }
  };

  const handleDeleteAvatar = async () => {
    const ok = await confirm({ message: t('settings.deleteAvatarConfirm') });
    if (!ok) return;
    setAvatarMessage(null);
    setAvatarLoading(true);

    try {
      const res = await apiFetch(`${apiUrl}/api/user/avatar`, {
        method: 'DELETE',
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });

      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError((data as ApiErrBody).error_code));
      }

      // Remove avatar from state
      const updatedUser = { ...user };
      delete updatedUser.avatar;
      onUpdateUser(updatedUser);
      setAvatarMessage({ text: t('settings.messages.avatarDeleted'), type: 'success' });
    } catch (err) {
      setAvatarMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setAvatarLoading(false);
    }
  };

  const handleChangePassword = async (e: React.FormEvent) => {
    e.preventDefault();
    setPasswordMessage(null);

    if (newPassword !== confirmPassword) {
      setPasswordMessage({ text: t('settings.messages.passwordMismatch'), type: 'error' });
      return;
    }

    if (newPassword.length < 12) {
      setPasswordMessage({ text: t('settings.messages.passwordTooShort'), type: 'error' });
      return;
    }

    setPasswordLoading(true);

    try {
      const res = await apiFetch(`${apiUrl}/api/auth/change-password`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify({
          current_password: currentPassword,
          new_password: newPassword,
          confirm_password: confirmPassword,
        }),
      });

      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError((data as ApiErrBody).error_code));
      }

      setPasswordMessage({ text: t('settings.messages.passwordChanged'), type: 'success' });
      setCurrentPassword('');
      setNewPassword('');
      setConfirmPassword('');
    } catch (err) {
      setPasswordMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setPasswordLoading(false);
    }
  };

  return (
    <div className="max-w-5xl w-full mx-auto my-4 space-y-6">
      {/* Back Header */}
      <div className="flex items-center justify-between pb-4 border-b border-[var(--color-border)]/50">
        <button
          onClick={onBack}
          className="ui-button-secondary flex items-center gap-2 px-3 py-2 text-sm font-medium hover:bg-[var(--color-bg-tertiary)]"
        >
          <ArrowLeft className="w-4 h-4" />
          {t('settings.back')}
        </button>
        <div className="flex items-center gap-2">
          <Settings className="w-5 h-5 text-[var(--color-text-primary)]" />
          <h1 className="font-display font-semibold text-xl text-[var(--color-text-primary)] leading-none">{t('settings.title')}</h1>
        </div>
      </div>

      {/* Settings Tabs */}
      <div
        className="flex flex-wrap gap-2"
        role="tablist"
        aria-label={t('settings.title')}
        onKeyDown={(event) => {
          if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
          event.preventDefault();
          const current = tabs.indexOf(tab);
          const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (current + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
          setTab(tabs[next]);
          document.getElementById(`settings-tab-${tabs[next]}`)?.focus();
        }}
      >
        {tabItems.map(([key, Icon, label]) => (
          <button
            key={key}
            onClick={() => setTab(key)}
            id={`settings-tab-${key}`} role="tab" aria-selected={tab === key} aria-controls={`settings-panel-${key}`} tabIndex={tab === key ? 0 : -1}
            className={`flex items-center gap-1.5 px-4 py-2 border font-medium text-sm ${
              tab === key
                ? 'ui-button-primary border-[var(--color-bg-inverse)]'
                : 'ui-button-secondary hover:bg-[var(--color-bg-tertiary)]'
            }`}
          >
            <Icon className="w-4 h-4" />
            {t(label)}
          </button>
        ))}
      </div>

      {/* Tab Content (stable height to avoid view jumping) */}
      <div key={tab} id={`settings-panel-${tab}`} role="tabpanel" aria-labelledby={`settings-tab-${tab}`} className="ui-view-enter min-h-[60vh]">

      {/* Main Grid Layout */}
      {tab === 'account' && (
       <div className="grid md:grid-cols-2 gap-6">
        
         {/* Left Side: Profile */}
         <div className="space-y-6">

          <div className={cardCls}>
            <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <User className="w-4 h-4 text-[var(--color-text-muted)]" />
              <h3 className={sectionTitleCls}>{t('settings.profile')}</h3>
            </div>

            <MessageBanner message={avatarMessage} />

            <div className="flex flex-col items-center sm:flex-row gap-5 p-2 bg-[var(--color-bg-tertiary)]/50 rounded-lg border border-[var(--color-border)]/50">
              <div className="relative shrink-0">
                {user?.avatar ? (
                  <img
                    src={user.avatar}
                    alt={t('settings.avatarAlt')}
                    className="w-20 h-20 shrink-0 rounded-full object-cover border border-[var(--color-border)] shadow-xs"
                  />
                ) : (
                  <div className="w-20 h-20 shrink-0 bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)] rounded-full flex items-center justify-center border border-[var(--color-border)]">
                    <User className="w-10 h-10" />
                  </div>
                )}
                {avatarLoading && (
                  <div className="absolute inset-0 bg-[var(--color-bg-inverse)]/40 rounded-full flex items-center justify-center">
                    <span className="animate-spin rounded-full h-5 w-5 border-2 border-[var(--color-border)] border-t-transparent"></span>
                  </div>
                )}
              </div>

              <div className="flex-grow space-y-2.5">
                <p className="text-xs text-[var(--color-text-muted)] font-sans leading-relaxed">
                  {t('settings.avatarHint')}
                </p>
                <div className="flex flex-wrap gap-2.5">
                  <label className="ui-button-secondary flex items-center gap-1.5 px-3 py-2 text-sm font-medium cursor-pointer hover:bg-[var(--color-bg-tertiary)]">
                    <Upload className="w-3.5 h-3.5" />
                    <span>{t('settings.selectImage')}</span>
                    <input
                      type="file"
                      accept="image/*"
                      onChange={handleFileChange}
                      className="hidden"
                    />
                  </label>

                  {user?.avatar && (
                    <button
                      onClick={handleDeleteAvatar}
                      disabled={avatarLoading}
                      className="ui-button-secondary flex items-center gap-1.5 px-3 py-2 text-sm font-medium border-[var(--color-error-border)] text-[var(--color-error-text)] cursor-pointer hover:bg-[var(--color-error-bg)] disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                      {t('settings.delete')}
                    </button>
                  )}
                </div>
              </div>
            </div>
            <div className="pt-2 border-t border-[var(--color-border-light)]">
              <h4 className={sectionTitleCls}>{t('settings.profileDetails')}</h4>
            </div>

            <MessageBanner message={profileMessage} />

            {emailChangeAvailable ? (
              <form
                onSubmit={async (e) => {
                  e.preventDefault();
                  setEmailChangeMessage(null);
                  const trimmed = newEmail.trim().toLowerCase();
                  if (!trimmed || !trimmed.includes('@') || !trimmed.includes('.')) {
                    setEmailChangeMessage({ text: t('settings.messages.emailValid'), type: 'error' });
                    return;
                  }
                  if (trimmed === (user?.email || '').toLowerCase()) {
                    setEmailChangeMessage({ text: t('settings.messages.emailSame'), type: 'error' });
                    return;
                  }
                  setEmailChangeLoading(true);
                  try {
                    const res = await apiFetch(`${apiUrl}/api/auth/change-email`, {
                      method: 'POST',
                      headers: {
                        'Content-Type': 'application/json',
                        'Authorization': `Bearer ${token}`,
                      },
                      body: JSON.stringify({ new_email: trimmed }),
                    });
                    if (res.ok) {
                      setNewEmail('');
                      setEmailChangeMessage({ text: t('settings.messages.emailSent'), type: 'success' });
                    } else if (res.status === 409) {
                      setEmailChangeMessage({ text: t('settings.messages.emailInUse'), type: 'error' });
                    } else if (res.status === 400) {
                      setEmailChangeMessage({ text: t('settings.messages.emailInvalid'), type: 'error' });
                    } else {
                      setEmailChangeMessage({ text: t('settings.messages.emailFailed'), type: 'error' });
                    }
                  } catch {
                    setEmailChangeMessage({ text: t('settings.messages.emailConnectionError'), type: 'error' });
                  } finally {
                    setEmailChangeLoading(false);
                  }
                }}
                className="space-y-4"
              >
                <div className="space-y-1.5">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                    {t('settings.currentEmail')}
                  </label>
                  <input
                    type="text"
                    disabled
                    value={user?.email || ''}
                    className="w-full px-4 py-2.5 bg-[var(--color-bg-tertiary)] border border-[var(--color-border)]/85 rounded-xl text-sm text-[var(--color-text-muted)] cursor-not-allowed font-sans font-mono"
                  />
                </div>

                <div className="space-y-1.5">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                    {t('settings.newEmail')}
                  </label>
                  <div className="relative group">
                    <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-[var(--color-text-muted)] group-focus-within:text-[var(--color-text-primary)] transition-colors">
                      <Mail className="w-4 h-4" />
                    </span>
                    <input
                      type="email"
                      value={newEmail}
                      onChange={(e) => setNewEmail(e.target.value)}
                      placeholder={t('settings.newEmailPlaceholder')}
                      className={`${inputCls} pl-10 pr-4`}
                    />
                  </div>
                </div>

                <MessageBanner message={emailChangeMessage} />

                <button
                  type="submit"
                  disabled={emailChangeLoading || newEmail.trim() === ''}
                  className={`w-full ${primaryBtnCls}`}
                >
                  {emailChangeLoading ? t('settings.saving') : t('settings.requestLink')}
                </button>
              </form>
            ) : (
              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                  {t('settings.emailNotChangeable')}
                </label>
                <input
                  type="text"
                  disabled
                  value={user?.email || ''}
                  className="w-full px-4 py-2.5 bg-[var(--color-bg-tertiary)] border border-[var(--color-border)]/85 rounded-xl text-sm text-[var(--color-text-muted)] cursor-not-allowed font-sans"
                />
                {emailChangeAvailable === false && (
                  <p className="text-[10px] text-[var(--color-text-muted)] font-mono mt-1">
                    {t('settings.emailChangeAvailableHint')}
                  </p>
                )}
              </div>
            )}
            <div className="pt-2 border-t border-[var(--color-border-light)]">
              <h4 className={sectionTitleCls}>{t('settings.displayName')}</h4>
            </div>

            <form onSubmit={handleUpdateProfile} className="space-y-4">
              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                  {t('settings.displayName')}
                </label>
                <input
                  type="text"
                  required
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                  placeholder={t('auth.namePlaceholder')}
                  className={inputCls}
                />
              </div>

              <button
                type="submit"
                disabled={profileLoading || displayName.trim() === '' || displayName.trim() === user?.display_name}
                className={`w-full ${primaryBtnCls}`}
              >
                {profileLoading ? t('settings.saving') : t('settings.saveChanges')}
              </button>
            </form>
          </div>
        </div>

        {/* Right Side: Password & 2FA */}
        <div className="space-y-6">
          <div className={cardCls}>
            <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <Lock className="w-4 h-4 text-[var(--color-text-muted)]" />
              <h3 className={sectionTitleCls}>{t('settings.changePassword')}</h3>
            </div>

            <MessageBanner message={passwordMessage} />

            <form onSubmit={handleChangePassword} className="space-y-4">
              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                  {t('settings.currentPassword')}
                </label>
                <div className="relative group">
                  <input
                    type={showCurrentPassword ? 'text' : 'password'}
                    autoComplete="current-password"
                    name="current_password"
                    required
                    value={currentPassword}
                    onChange={(e) => setCurrentPassword(e.target.value)}
                    placeholder="••••••••"
                    className={`${inputCls} pr-10 font-mono`}
                  />
                  <button
                    type="button"
                    onClick={() => setShowCurrentPassword(!showCurrentPassword)}
                    aria-label={showCurrentPassword ? t('auth.hidePassword') : t('auth.showPassword')}
                    className="absolute inset-y-0 right-0 pr-3 flex items-center text-[var(--color-text-muted)] hover:text-[var(--color-text-secondary)]"
                  >
                    {showCurrentPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                  </button>
                </div>
              </div>

              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                  {t('settings.newPassword')}
                </label>
                <div className="relative group">
                  <input
                    type={showNewPassword ? 'text' : 'password'}
                    autoComplete="new-password"
                    name="new_password"
                    required
                    value={newPassword}
                    onChange={(e) => setNewPassword(e.target.value)}
                    placeholder="••••••••"
                    className={`${inputCls} pr-10 font-mono`}
                  />
                  <button
                    type="button"
                    onClick={() => setShowNewPassword(!showNewPassword)}
                    aria-label={showNewPassword ? t('auth.hidePassword') : t('auth.showPassword')}
                    className="absolute inset-y-0 right-0 pr-3 flex items-center text-[var(--color-text-muted)] hover:text-[var(--color-text-secondary)]"
                  >
                    {showNewPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                  </button>
                </div>
              </div>

              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                  {t('settings.confirmPassword')}
                </label>
                <div className="relative group">
                  <input
                    type={showConfirmPassword ? 'text' : 'password'}
                    autoComplete="new-password"
                    name="confirm_password"
                    required
                    value={confirmPassword}
                    onChange={(e) => setConfirmPassword(e.target.value)}
                    placeholder="••••••••"
                    className={`${inputCls} pr-10 font-mono`}
                  />
                  <button
                    type="button"
                    onClick={() => setShowConfirmPassword(!showConfirmPassword)}
                    aria-label={showConfirmPassword ? t('auth.hidePassword') : t('auth.showPassword')}
                    className="absolute inset-y-0 right-0 pr-3 flex items-center text-[var(--color-text-muted)] hover:text-[var(--color-text-secondary)]"
                  >
                    {showConfirmPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                  </button>
                </div>
              </div>

              <button
                type="submit"
                disabled={passwordLoading || !currentPassword || !newPassword || !confirmPassword}
                className={`w-full ${primaryBtnCls}`}
              >
                {passwordLoading ? t('settings.changing') : t('settings.changePassword')}
              </button>
            </form>
          </div>

          {/* 2FA Section */}
          <div className={cardCls}>
            <div className="flex items-center justify-between gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <div className="flex items-center gap-2">
                <ShieldCheck className="w-4 h-4 text-[var(--color-text-muted)]" />
                <h3 className={sectionTitleCls}>{t('settings.twoFactor')}</h3>
              </div>
              {totpStatusLoading ? (
                <span className="text-[10px] font-mono text-[var(--color-text-muted)]">…</span>
              ) : totpEnabled ? (
                <span className="ui-badge ui-badge-success">{t('settings.active')}</span>
              ) : (
                <span className="text-[10px] font-mono font-bold text-[var(--color-text-muted)] bg-[var(--color-bg-secondary)] border border-[var(--color-border)] px-2 py-0.5 rounded-full">{t('settings.inactive')}</span>
              )}
            </div>

            <MessageBanner message={totpMessage} />

            {backupCodes.length > 0 ? (
              <div className="space-y-3">
                <p className="text-[11px] text-[var(--color-text-secondary)] font-sans leading-relaxed">
                  {t('settings.backupCodesHint')}
                </p>
                <div className="grid grid-cols-2 gap-2">
                  {backupCodes.map((code) => (
                    <div key={code} className="px-3 py-2 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-lg text-center font-mono text-sm tracking-widest text-[var(--color-text-primary)]">
                      {code}
                    </div>
                  ))}
                </div>
                <button
                  type="button"
                  onClick={() => { navigator.clipboard?.writeText(backupCodes.join('\n')).catch(() => {}); setTotpMessage({ text: t('settings.copied'), type: 'success' }); }}
                  className={`w-full ${primaryBtnCls}`}
                >
                  {t('settings.copyCodes')}
                </button>
              </div>
            ) : setupData ? (
              <form onSubmit={handle2FAEnable} className="space-y-4">
                <div className="flex flex-col items-center gap-3">
                  {setupData.qr_png.startsWith('data:image/') ? (
                    <img src={setupData.qr_png} alt={t('settings.qrCodeAlt')} className="w-44 h-44 border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-2 [image-rendering:pixelated]" />
                  ) : (
                    <div className="w-44 h-44 border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-2 flex items-center justify-center text-[10px] text-[var(--color-text-muted)] text-center font-mono">
                      {t('settings.messages.qrInvalid')}
                    </div>
                  )}
                  <p className="text-[10px] font-mono text-[var(--color-text-muted)] break-all text-center px-2">
                    {setupData.secret}
                  </p>
                </div>
                <div className="space-y-1.5">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                    {t('settings.confirmCode')}
                  </label>
                  <input
                    type="text"
                    inputMode="numeric"
                    required
                    value={enableCode}
                    onChange={(e) => setEnableCode(e.target.value)}
                    placeholder="123456"
                    className={`${inputCls} tracking-[0.4em] text-center font-mono`}
                  />
                </div>
                <div className="flex gap-2">
                  <button
                    type="submit"
                    disabled={enableLoading || !enableCode}
                    className={`flex-1 ${primaryBtnCls}`}
                  >
                    {enableLoading ? t('settings.activating') : t('settings.activate')}
                  </button>
                  <button
                    type="button"
                    onClick={() => { setSetupData(null); setEnableCode(''); }}
                    className="px-4 py-2.5 rounded-xl text-xs font-mono border border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] transition-all cursor-pointer"
                  >
                    {t('common.cancel')}
                  </button>
                </div>
              </form>
            ) : totpEnabled ? (
              <form onSubmit={handle2FADisable} className="space-y-4">
                <p className="text-[11px] text-[var(--color-text-secondary)] font-sans leading-relaxed">
                  {t('settings.disableHint')}
                </p>
                <div className="space-y-1.5">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                    {t('settings.confirmCode')}
                  </label>
                  <input
                    type="text"
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    name="disable_code"
                    required
                    value={disableCode}
                    onChange={(e) => setDisableCode(e.target.value)}
                    placeholder="123456"
                    className={`${inputCls} font-mono`}
                  />
                </div>
                <button
                  type="submit"
                  disabled={disableLoading || !disableCode.trim()}
                  className="w-full bg-[var(--color-error-bg)] text-[var(--color-error-text)] border border-[var(--color-error-border)] hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider cursor-pointer"
                >
                  {disableLoading ? t('settings.deactivating') : t('settings.deactivate')}
                </button>
              </form>
            ) : (
              <div className="space-y-4">
                <p className="text-[11px] text-[var(--color-text-secondary)] font-sans leading-relaxed">
                  {t('settings.setupHint')}
                </p>
                <button
                  type="button"
                  onClick={handle2FASetup}
                  disabled={setupLoading}
                  className={`w-full ${primaryBtnCls}`}
                >
                  {setupLoading ? t('settings.preparing') : t('settings.setup')}
              </button>
            </div>
          )}
          </div>
        </div>
       </div>
      )}

      {/* Appearance Tab */}
      {tab === 'appearance' && (
        <div className="grid md:grid-cols-2 gap-6">
          <div className={cardCls}>
            <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <Palette className="w-4 h-4 text-[var(--color-text-muted)]" />
              <h3 className={sectionTitleCls}>{t('settings.appearance')}</h3>
            </div>

            <p className="text-[10px] text-[var(--color-text-muted)] font-sans leading-relaxed">
              {t('settings.appearanceHint')}
            </p>

            <div className="grid grid-cols-3 gap-3" role="group" aria-label={t('settings.appearance')}>
              {/* Light Option */}
              <button
                type="button"
                onClick={() => setPreference('light')}
                aria-pressed={preference === 'light'}
                className={`flex flex-col items-center gap-2 p-4 rounded-xl border-2 transition-all cursor-pointer ${
                  preference === 'light'
                    ? 'border-[var(--color-text-primary)] bg-[var(--color-bg-tertiary)]'
                    : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)]/50 hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]'
                }`}
              >
                <Sun className={`w-6 h-6 ${preference === 'light' ? 'text-[var(--color-text-primary)]' : 'text-[var(--color-text-muted)]'}`} />
                <span className={`text-xs font-bold font-mono ${preference === 'light' ? 'text-[var(--color-text-primary)]' : 'text-[var(--color-text-secondary)]'}`}>
                  {t('settings.light')}
                </span>
              </button>

              {/* Dark Option */}
              <button
                type="button"
                onClick={() => setPreference('dark')}
                aria-pressed={preference === 'dark'}
                className={`flex flex-col items-center gap-2 p-4 rounded-xl border-2 transition-all cursor-pointer ${
                  preference === 'dark'
                    ? 'border-[var(--color-text-primary)] bg-[var(--color-bg-tertiary)]'
                    : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)]/50 hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]'
                }`}
              >
                <Moon className={`w-6 h-6 ${preference === 'dark' ? 'text-[var(--color-text-primary)]' : 'text-[var(--color-text-muted)]'}`} />
                <span className={`text-xs font-bold font-mono ${preference === 'dark' ? 'text-[var(--color-text-primary)]' : 'text-[var(--color-text-secondary)]'}`}>
                  {t('settings.dark')}
                </span>
              </button>

              {/* Auto Option */}
              <button
                type="button"
                onClick={() => setPreference('auto')}
                aria-pressed={preference === 'auto'}
                className={`flex flex-col items-center gap-2 p-4 rounded-xl border-2 transition-all cursor-pointer ${
                  preference === 'auto'
                    ? 'border-[var(--color-text-primary)] bg-[var(--color-bg-tertiary)]'
                    : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)]/50 hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]'
                }`}
              >
                <Monitor className={`w-6 h-6 ${preference === 'auto' ? 'text-[var(--color-text-primary)]' : 'text-[var(--color-text-muted)]'}`} />
                <span className={`text-xs font-bold font-mono ${preference === 'auto' ? 'text-[var(--color-text-primary)]' : 'text-[var(--color-text-secondary)]'}`}>
                  {t('settings.auto')}
                </span>
              </button>
            </div>

            {preference === 'auto' && (
              <p className="text-[10px] text-[var(--color-text-muted)] font-mono text-center mt-2">
                {t('settings.currentTheme', { theme: systemTheme === 'dark' ? t('settings.systemDark') : t('settings.systemLight') })}
              </p>
            )}
          </div>
        </div>
      )}

      {/* Notifications Tab (SMTP) */}
      {tab === 'notifications' && (
        <div className="grid md:grid-cols-2 gap-6">
          <div className={cardCls}>
            <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <Mail className="w-4 h-4 text-[var(--color-text-muted)]" />
              <h3 className={sectionTitleCls}>{t('settings.emailNotifications')}</h3>
            </div>

            <MessageBanner message={smtpMessage} />

            <form onSubmit={handleSaveSMTP} className="space-y-4">
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                <div className="space-y-1.5 sm:col-span-2">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                    {t('settings.smtpHost')}
                  </label>
                  <input
                    type="text"
                    required
                    value={smtpHost}
                    onChange={(e) => setSmtpHost(e.target.value)}
                    placeholder="smtp.example.com"
                    className={inputCls}
                  />
                </div>
                <div className="space-y-1.5">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                    {t('settings.smtpPort')}
                  </label>
                  <input
                    type="number"
                    required
                    min={1}
                    max={65535}
                    value={smtpPort}
                    onChange={(e) => setSmtpPort(e.target.value)}
                    className={inputCls}
                  />
                </div>
              </div>

              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                  {t('settings.smtpUsername')}
                </label>
                <input
                  type="text"
                  required
                  value={smtpUsername}
                  onChange={(e) => setSmtpUsername(e.target.value)}
                  placeholder="user@example.com"
                  className={inputCls}
                />
              </div>

              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                  {t('settings.smtpPassword')}
                </label>
                <div className="relative group">
                  <input
                    type="password"
                    autoComplete="current-password"
                    name="smtp_password"
                    required={!smtpHasConfig}
                    value={smtpPassword}
                    onChange={(e) => setSmtpPassword(e.target.value)}
                    placeholder={smtpHasConfig ? `•••••••• ${t('settings.smtpPasswordUnchanged')}` : '••••••••'}
                    className={`${inputCls} font-mono`}
                  />
                </div>
                {smtpHasConfig && (
                  <p className="text-xs text-[var(--color-text-muted)] font-mono leading-relaxed">
                    {t('settings.smtpPasswordHint')}
                  </p>
                )}
              </div>

              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <div className="space-y-1.5">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                    {t('settings.smtpFromEmail')}
                  </label>
                  <input
                    type="email"
                    required
                    value={smtpFromEmail}
                    onChange={(e) => setSmtpFromEmail(e.target.value)}
                    placeholder="noreply@example.com"
                    className={inputCls}
                  />
                </div>
                <div className="space-y-1.5">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                    {t('settings.smtpFromName')}
                  </label>
                  <input
                    type="text"
                    value={smtpFromName}
                    onChange={(e) => setSmtpFromName(e.target.value)}
                    placeholder="Clumoove"
                    className={inputCls}
                  />
                </div>
              </div>

              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                  {t('settings.smtpEncryption')}
                </label>
                <select
                  value={smtpEncryption}
                  onChange={(e) => setSmtpEncryption(e.target.value)}
                  className={inputCls}
                >
                  <option value="tls">TLS (Implicit)</option>
                  <option value="starttls">STARTTLS</option>
                  <option value="none">{t('settings.smtpEncryptionNone')}</option>
                </select>
              </div>

              <div className="flex items-center justify-between p-3.5 bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)]/50 rounded-lg">
                <div className="text-left space-y-1 pr-4">
                  <h4 className="text-xs font-bold text-[var(--color-text-primary)] font-display">{t('settings.smtpNotify')}</h4>
                  <p className="text-[10px] text-[var(--color-text-muted)] leading-normal">
                    {t('settings.smtpNotifyHint')}
                  </p>
                </div>
                <Toggle checked={smtpNotify} onChange={setSmtpNotify} label={t('settings.smtpNotify')} />
              </div>

              <div className="flex flex-wrap gap-2.5">
                <button
                  type="submit"
                  disabled={smtpLoading || !smtpHost || !smtpUsername || !smtpFromEmail}
                  className={`flex-1 ${primaryBtnCls}`}
                >
                  {smtpLoading ? t('settings.saving') : t('settings.saveSmtp')}
                </button>
                <button
                  type="button"
                  onClick={handleTestSMTP}
                  disabled={smtpLoading}
                  className={secondaryBtnCls}
                >
                  {t('settings.testSmtp')}
                </button>
              </div>
            </form>
          </div>
          <div className="space-y-6">
            <MessageBanner message={pushMessage} />
            {renderPushChannel('gotify', [
              { key: 'url', label: t('settings.notificationUrl'), placeholder: 'https://gotify.example.com' },
              { key: 'token', label: t('settings.notificationToken'), secret: true },
            ])}
            {renderPushChannel('ntfy', [
              { key: 'url', label: t('settings.notificationUrl'), placeholder: 'https://ntfy.sh' },
              { key: 'topic', label: t('settings.notificationTopic'), placeholder: 'clumoove' },
              { key: 'token', label: t('settings.notificationToken'), secret: true },
              { key: 'priority', label: t('settings.notificationPriority'), placeholder: 'default' },
            ])}
            {renderPushChannel('telegram', [
              { key: 'bot_token', label: t('settings.telegramBotToken'), secret: true },
              { key: 'chat_id', label: t('settings.telegramChatId') },
            ])}
            {renderPushChannel('discord', [
              { key: 'webhook_url', label: t('settings.discordWebhookUrl'), secret: true },
            ])}
          </div>
        </div>
      )}

      {/* About Tab */}
      {tab === 'about' && (
        <div className="grid md:grid-cols-2 gap-6">
          <div className={cardCls}>
            <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <Info className="w-4 h-4 text-[var(--color-text-muted)]" />
              <h3 className={sectionTitleCls}>{t('settings.about')}</h3>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                {t('settings.name')}
              </span>
              <span className="text-sm font-mono text-[var(--color-text-primary)]">{t('settings.appName')}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                {t('settings.version')}
              </span>
              <span className="text-sm font-mono text-[var(--color-text-primary)]">v{__APP_VERSION__}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                {t('settings.license')}
              </span>
              <span className="text-sm font-mono text-[var(--color-text-primary)]">{t('settings.licenseName')}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                {t('settings.source')}
              </span>
              <a
                href="https://github.com/xXRoxXeRXx/clumoove"
                target="_blank"
                rel="noopener noreferrer"
                className="text-sm font-mono text-[var(--color-text-primary)] hover:underline"
              >
                github.com/xXRoxXeRXx/clumoove
              </a>
            </div>
          </div>
        </div>
      )}

      {/* Connections Tab */}
      {tab === 'connections' && (
        <div className="grid md:grid-cols-2 gap-6">
          <ConnectionManager apiUrl={apiUrl} token={token} localStorageEnabled={localStorageEnabled} oauthProviders={oauthProviders} />
        </div>
      )}

      </div>

      {/* Avatar Cropper Modal Overlay */}
      {showCropper && selectedFile && (
        <AvatarCropper
          file={selectedFile}
          onCrop={handleCropComplete}
          onCancel={() => {
            setShowCropper(false);
            setSelectedFile(null);
          }}
        />
      )}
    </div>
  );
}
