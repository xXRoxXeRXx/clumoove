import React, { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowLeft, User, Image as ImageIcon, Lock, Settings, Trash2, Upload, CloudSync, Eye, EyeOff, Palette, Sun, Moon, Monitor, Mail } from 'lucide-react';
import { AvatarCropper } from './AvatarCropper';
import { Toggle } from './Toggle';
import { useThemeContext } from '../contexts/useThemeContext';
import { useApiError } from '../utils/apiError';

type ApiErrBody = { error_code?: string };

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
}

type MessageState = { text: string; type: 'success' | 'error' } | null;

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

export function SettingsPage({ apiUrl, token, user, onBack, onUpdateUser }: SettingsPageProps) {
  const { t } = useTranslation();
  const translateApiError = useApiError();

  // Theme context
  const { preference, setPreference, systemTheme } = useThemeContext();

  // Display name state
  const [displayName, setDisplayName] = useState<string>(user?.display_name || '');
  const [profileLoading, setProfileLoading] = useState<boolean>(false);
  const [profileMessage, setProfileMessage] = useState<{ text: string; type: 'success' | 'error' } | null>(null);

  // Avatar crop/upload state
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [showCropper, setShowCropper] = useState<boolean>(false);
  const [avatarLoading, setAvatarLoading] = useState<boolean>(false);
  const [avatarMessage, setAvatarMessage] = useState<{ text: string; type: 'success' | 'error' } | null>(null);

  // Password state
  const [currentPassword, setCurrentPassword] = useState<string>('');
  const [newPassword, setNewPassword] = useState<string>('');
  const [confirmPassword, setConfirmPassword] = useState<string>('');
  const [showCurrentPassword, setShowCurrentPassword] = useState<boolean>(false);
  const [showNewPassword, setShowNewPassword] = useState<boolean>(false);
  const [showConfirmPassword, setShowConfirmPassword] = useState<boolean>(false);
  const [passwordLoading, setPasswordLoading] = useState<boolean>(false);
  const [passwordMessage, setPasswordMessage] = useState<{ text: string; type: 'success' | 'error' } | null>(null);

  // Email change state
  const [emailChangeAvailable, setEmailChangeAvailable] = useState<boolean | null>(null);
  const [newEmail, setNewEmail] = useState<string>('');
  const [emailChangeLoading, setEmailChangeLoading] = useState<boolean>(false);
  const [emailChangeMessage, setEmailChangeMessage] = useState<{ text: string; type: 'success' | 'error' } | null>(null);

  // 2FA state
  const [totpEnabled, setTotpEnabled] = useState<boolean>(false);
  const [totpStatusLoading, setTotpStatusLoading] = useState<boolean>(true);
  const [setupData, setSetupData] = useState<{ otpauth_uri: string; qr_png: string; secret: string } | null>(null);
  const [setupLoading, setSetupLoading] = useState<boolean>(false);
  const [enableCode, setEnableCode] = useState<string>('');
  const [enableLoading, setEnableLoading] = useState<boolean>(false);
  const [backupCodes, setBackupCodes] = useState<string[]>([]);
  const [disablePassword, setDisablePassword] = useState<string>('');
  const [disableLoading, setDisableLoading] = useState<boolean>(false);
  const [totpMessage, setTotpMessage] = useState<{ text: string; type: 'success' | 'error' } | null>(null);

  // Fetch 2FA status on mount
  useEffect(() => {
    fetch(`${apiUrl}/api/auth/2fa/status`, {
      headers: { 'Authorization': `Bearer ${token}` },
    })
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then((data) => {
        setTotpEnabled(Boolean(data.totp_enabled));
      })
      .catch(() => {
        setTotpEnabled(false);
      })
      .finally(() => {
        setTotpStatusLoading(false);
      });
  }, [apiUrl, token]);

  const handle2FASetup = async () => {
    setTotpMessage(null);
    setSetupLoading(true);
    try {
      const res = await fetch(`${apiUrl}/api/auth/2fa/setup`, {
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
      const res = await fetch(`${apiUrl}/api/auth/2fa/enable`, {
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
    if (!disablePassword) {
      setTotpMessage({ text: t('settings.messages.totpNeedPassword'), type: 'error' });
      return;
    }
    setDisableLoading(true);
    try {
      const res = await fetch(`${apiUrl}/api/auth/2fa/disable`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify({ password: disablePassword }),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError((data as ApiErrBody).error_code));
      }
      setTotpEnabled(false);
      setDisablePassword('');
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
    fetch(`${apiUrl}/api/auth/email-change-available`)
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then((data) => {
        setEmailChangeAvailable(Boolean(data.available));
      })
      .catch(() => {
        setEmailChangeAvailable(false);
      });
  }, [apiUrl]);

  // Admin settings state
  const [registrationsEnabled, setRegistrationsEnabled] = useState<boolean>(true);
  const [adminLoading, setAdminLoading] = useState<boolean>(false);
  const [adminMessage, setAdminMessage] = useState<{ text: string; type: 'success' | 'error' } | null>(null);

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
  const [smtpMessage, setSmtpMessage] = useState<{ text: string; type: 'success' | 'error' } | null>(null);

  // Fetch SMTP settings
  useEffect(() => {
    fetch(`${apiUrl}/api/settings/smtp`, {
      headers: { 'Authorization': `Bearer ${token}` },
    })
      .then((res) => {
        if (res.ok) return res.json();
        throw new Error('no-smtp');
      })
      .then((data) => {
        setSmtpHasConfig(true);
        setSmtpHost(data.smtp_host || '');
        setSmtpPort(String(data.smtp_port || '587'));
        setSmtpUsername(data.smtp_username || '');
        setSmtpFromEmail(data.smtp_from_email || '');
        setSmtpFromName(data.smtp_from_name || '');
        setSmtpEncryption(data.smtp_encryption || 'tls');
        setSmtpNotify(data.notify_on_completion !== false);
      })
      .catch(() => {
        setSmtpHasConfig(false);
      });
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

    const payload: Record<string, string | number | boolean> = {
      smtp_host: smtpHost,
      smtp_port: portNum,
      smtp_username: smtpUsername,
      smtp_from_email: smtpFromEmail,
      smtp_from_name: smtpFromName,
      smtp_encryption: smtpEncryption,
      notify_on_completion: smtpNotify,
    };
    // Only send the password when the user entered a new one (existing password is kept otherwise)
    if (smtpPassword) {
      payload.smtp_password = smtpPassword;
    }

    try {
      const res = await fetch(`${apiUrl}/api/settings/smtp`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify(payload),
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
      const res = await fetch(`${apiUrl}/api/settings/smtp/test`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${token}` },
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

  // Fetch admin settings if admin
  useEffect(() => {
    if (user?.role !== 'ADMIN') return;

    fetch(`${apiUrl}/api/settings`)
      .then((res) => res.json())
      .then((data) => {
        setRegistrationsEnabled(data.registrations_enabled !== 'false');
      })
      .catch((err) => {
        console.error('Failed to fetch settings:', err);
      });
  }, [apiUrl, user]);

  const handleUpdateProfile = async (e: React.FormEvent) => {
    e.preventDefault();
    setProfileMessage(null);
    setProfileLoading(true);

    try {
      const res = await fetch(`${apiUrl}/api/auth/me`, {
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
      const res = await fetch(`${apiUrl}/api/user/avatar`, {
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
    if (!window.confirm(t('settings.deleteAvatarConfirm'))) return;
    setAvatarMessage(null);
    setAvatarLoading(true);

    try {
      const res = await fetch(`${apiUrl}/api/user/avatar`, {
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
      const res = await fetch(`${apiUrl}/api/auth/change-password`, {
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

  const handleToggleRegistrations = async (checked: boolean) => {
    setAdminMessage(null);
    setAdminLoading(true);

    try {
      const res = await fetch(`${apiUrl}/api/settings`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify({
          key: 'registrations_enabled',
          value: checked ? 'true' : 'false',
        }),
      });

      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as ApiErrBody);
        throw new Error(translateApiError((data as ApiErrBody).error_code));
      }

      setRegistrationsEnabled(checked);
      setAdminMessage({
        text: checked ? t('settings.messages.adminSavedOn') : t('settings.messages.adminSavedOff'),
        type: 'success',
      });
    } catch (err) {
      setAdminMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setAdminLoading(false);
    }
  };

  return (
    <div className="max-w-4xl w-full mx-auto my-4 space-y-6">
      {/* Back Header */}
      <div className="flex items-center justify-between pb-4 border-b border-[var(--color-border)]/50">
        <button
          onClick={onBack}
          className="flex items-center gap-2 px-4 py-2 bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded-full hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)] transition-all font-mono font-bold text-xs cursor-pointer text-[var(--color-text-secondary)] hover:text-[var(--color-portal-navy-themed)] shadow-xs"
        >
          <ArrowLeft className="w-4 h-4" />
          {t('settings.back')}
        </button>
        <div className="flex items-center gap-2">
          <Settings className="w-5 h-5 text-[var(--color-portal-navy-themed)]" />
          <h2 className="font-display font-extrabold text-xl text-[var(--color-portal-navy-themed)] leading-none">{t('settings.title')}</h2>
        </div>
      </div>

      {/* Main Grid Layout */}
      <div className="grid md:grid-cols-2 gap-6">
        
        {/* Left Side: Profile picture, profile details & password */}
        <div className="space-y-6">
          
          {/* Section 1: Profile picture */}
          <div className="glass-panel rounded-2xl p-6 border border-[var(--color-glass-border)]/50 shadow-portal space-y-5">
            <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <ImageIcon className="w-4 h-4 text-[var(--color-portal-orange-themed)]" />
              <h3 className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)]">{t('settings.profilePicture')}</h3>
            </div>

            <MessageBanner message={avatarMessage} />

            <div className="flex flex-col items-center sm:flex-row gap-5 p-2 bg-[var(--color-bg-tertiary)]/50 rounded-2xl border border-[var(--color-border)]/50">
              <div className="relative shrink-0">
                {user?.avatar ? (
                  <img
                    src={user.avatar}
                    alt="User Avatar"
                    className="w-20 h-20 shrink-0 rounded-full object-cover border border-[var(--color-border)] shadow-xs"
                  />
                ) : (
                  <div className="w-20 h-20 shrink-0 bg-portal-navy text-[var(--color-text-inverse)] rounded-full flex items-center justify-center border border-[var(--color-border)] shadow-xs">
                    <User className="w-10 h-10" />
                  </div>
                )}
                {avatarLoading && (
                  <div className="absolute inset-0 bg-[var(--color-bg-inverse)]/40 rounded-full flex items-center justify-center">
                    <span className="animate-spin rounded-full h-5 w-5 border-2 border-[var(--color-glass-border)] border-t-transparent"></span>
                  </div>
                )}
              </div>

              <div className="flex-grow space-y-2.5">
                <p className="text-[10px] text-[var(--color-text-muted)] font-sans leading-relaxed">
                  {t('settings.avatarHint')}
                </p>
                <div className="flex flex-wrap gap-2.5">
                  <label className="flex items-center gap-1.5 px-3 py-1.5 bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded-xl hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)] transition-all font-mono font-bold text-[10px] cursor-pointer text-[var(--color-text-secondary)] hover:text-[var(--color-portal-navy-themed)] shadow-xs">
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
                      className="flex items-center gap-1.5 px-3 py-1.5 bg-[var(--color-bg-secondary)] border border-[var(--color-error-border)] text-[var(--color-error-text)] rounded-xl hover:bg-[var(--color-error-bg)]/70 hover:border-rose-350 transition-all font-mono font-bold text-[10px] cursor-pointer shadow-xs"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                      {t('settings.delete')}
                    </button>
                  )}
                </div>
              </div>
            </div>
          </div>

{/* Section 2: Profile details */}
          <div className="glass-panel rounded-2xl p-6 border border-[var(--color-glass-border)]/50 shadow-portal space-y-5">
            <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <User className="w-4 h-4 text-[var(--color-portal-orange-themed)]" />
              <h3 className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)]">{t('settings.profileDetails')}</h3>
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
                    const res = await fetch(`${apiUrl}/api/auth/change-email`, {
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
                    <span className="absolute inset-y-0 left-0 pl-3.5 flex items-center text-[var(--color-text-muted)] group-focus-within:text-portal-orange transition-colors">
                      <Mail className="w-4 h-4" />
                    </span>
                    <input
                      type="email"
                      value={newEmail}
                      onChange={(e) => setNewEmail(e.target.value)}
                      placeholder="neue.adresse@beispiel.de"
                      className="w-full pl-10 pr-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                    />
                  </div>
                </div>

                <MessageBanner message={emailChangeMessage} />

                <button
                  type="submit"
                  disabled={emailChangeLoading || newEmail.trim() === ''}
                  className="w-full bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider cursor-pointer"
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
          </div>

          <div className="glass-panel rounded-2xl p-6 border border-[var(--color-glass-border)]/50 shadow-portal space-y-5">
            <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <User className="w-4 h-4 text-[var(--color-portal-orange-themed)]" />
              <h3 className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)]">{t('settings.displayName')}</h3>
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
                  placeholder="Max Mustermann"
                  className="w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                />
              </div>

              <button
                type="submit"
                disabled={profileLoading || displayName.trim() === '' || displayName.trim() === user?.display_name}
                className="w-full bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider cursor-pointer"
              >
                {profileLoading ? t('settings.saving') : t('settings.saveChanges')}
              </button>
            </form>
          </div>

          <div className="glass-panel rounded-2xl p-6 border border-[var(--color-glass-border)]/50 shadow-portal space-y-5">
            <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <Lock className="w-4 h-4 text-[var(--color-portal-orange-themed)]" />
              <h3 className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)]">{t('settings.changePassword')}</h3>
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
                    required
                    value={currentPassword}
                    onChange={(e) => setCurrentPassword(e.target.value)}
                    placeholder="••••••••"
                    className="w-full px-4 pr-10 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
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
                    required
                    value={newPassword}
                    onChange={(e) => setNewPassword(e.target.value)}
                    placeholder="••••••••"
                    className="w-full px-4 pr-10 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
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
                    required
                    value={confirmPassword}
                    onChange={(e) => setConfirmPassword(e.target.value)}
                    placeholder="••••••••"
                    className="w-full px-4 pr-10 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
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
                className="w-full bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider cursor-pointer"
              >
                {passwordLoading ? t('settings.changing') : t('settings.changePassword')}
              </button>
            </form>
          </div>

          {/* 2FA Section */}
          <div className="glass-panel rounded-2xl p-6 border border-[var(--color-glass-border)]/50 shadow-portal space-y-5">
            <div className="flex items-center justify-between gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <div className="flex items-center gap-2">
                <Lock className="w-4 h-4 text-[var(--color-portal-orange-themed)]" />
                <h3 className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)]">{t('settings.twoFactor')}</h3>
              </div>
              {totpStatusLoading ? (
                <span className="text-[10px] font-mono text-[var(--color-text-muted)]">…</span>
              ) : totpEnabled ? (
                <span className="text-[10px] font-mono font-bold text-emerald-700 bg-emerald-50 border border-emerald-200 px-2 py-0.5 rounded-full">{t('settings.active')}</span>
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
                    <div key={code} className="px-3 py-2 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-lg text-center font-mono text-sm tracking-widest text-[var(--color-portal-navy-themed)]">
                      {code}
                    </div>
                  ))}
                </div>
                <button
                  type="button"
                  onClick={() => { navigator.clipboard?.writeText(backupCodes.join('\n')); setTotpMessage({ text: t('settings.copied'), type: 'success' }); }}
                  className="w-full bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all uppercase tracking-wider cursor-pointer"
                >
                  {t('settings.copyCodes')}
                </button>
              </div>
            ) : setupData ? (
              <form onSubmit={handle2FAEnable} className="space-y-4">
                <div className="flex flex-col items-center gap-3">
                  {setupData.qr_png.startsWith('data:image/') ? (
                    <img src={setupData.qr_png} alt="2FA QR-Code" className="w-44 h-44 rounded-xl border border-[var(--color-border)] bg-white p-2" />
                  ) : (
                    <div className="w-44 h-44 rounded-xl border border-[var(--color-border)] bg-white p-2 flex items-center justify-center text-[10px] text-[var(--color-text-muted)] text-center font-mono">
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
                    className="w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm tracking-[0.4em] text-center focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-mono"
                  />
                </div>
                <div className="flex gap-2">
                  <button
                    type="submit"
                    disabled={enableLoading || !enableCode}
                    className="flex-1 bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider cursor-pointer"
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
                    {t('settings.currentPassword')}
                  </label>
                  <input
                    type="password"
                    required
                    value={disablePassword}
                    onChange={(e) => setDisablePassword(e.target.value)}
                    placeholder="••••••••"
                    className="w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-mono"
                  />
                </div>
                <button
                  type="submit"
                  disabled={disableLoading || !disablePassword}
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
                  className="w-full bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider cursor-pointer"
                >
                  {setupLoading ? t('settings.preparing') : t('settings.setup')}
                </button>
              </div>
            )}
          </div>
        </div>

        {/* Right Side: Appearance, system control & email notifications */}
        <div className="space-y-6">
          
          {/* Section 4: Appearance */}
          <div className="glass-panel rounded-2xl p-6 border border-[var(--color-glass-border)]/50 shadow-portal space-y-5">
            <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <Palette className="w-4 h-4 text-[var(--color-portal-orange-themed)]" />
              <h3 className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)]">{t('settings.appearance')}</h3>
            </div>

            <p className="text-[10px] text-[var(--color-text-muted)] font-sans leading-relaxed">
              {t('settings.appearanceHint')}
            </p>

            <div className="grid grid-cols-3 gap-3">
              {/* Light Option */}
              <button
                onClick={() => setPreference('light')}
                className={`flex flex-col items-center gap-2 p-4 rounded-xl border-2 transition-all cursor-pointer ${
                  preference === 'light'
                    ? 'border-portal-orange bg-portal-orange/10 shadow-sm'
                    : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)]/50 hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]'
                }`}
              >
                <Sun className={`w-6 h-6 ${preference === 'light' ? 'text-[var(--color-portal-orange-themed)]' : 'text-[var(--color-text-muted)]'}`} />
                <span className={`text-xs font-bold font-mono ${preference === 'light' ? 'text-[var(--color-portal-orange-themed)]' : 'text-[var(--color-text-secondary)]'}`}>
                  {t('settings.light')}
                </span>
              </button>

              {/* Dark Option */}
              <button
                onClick={() => setPreference('dark')}
                className={`flex flex-col items-center gap-2 p-4 rounded-xl border-2 transition-all cursor-pointer ${
                  preference === 'dark'
                    ? 'border-portal-orange bg-portal-orange/10 shadow-sm'
                    : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)]/50 hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]'
                }`}
              >
                <Moon className={`w-6 h-6 ${preference === 'dark' ? 'text-[var(--color-portal-orange-themed)]' : 'text-[var(--color-text-muted)]'}`} />
                <span className={`text-xs font-bold font-mono ${preference === 'dark' ? 'text-[var(--color-portal-orange-themed)]' : 'text-[var(--color-text-secondary)]'}`}>
                  {t('settings.dark')}
                </span>
              </button>

              {/* Auto Option */}
              <button
                onClick={() => setPreference('auto')}
                className={`flex flex-col items-center gap-2 p-4 rounded-xl border-2 transition-all cursor-pointer ${
                  preference === 'auto'
                    ? 'border-portal-orange bg-portal-orange/10 shadow-sm'
                    : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)]/50 hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]'
                }`}
              >
                <Monitor className={`w-6 h-6 ${preference === 'auto' ? 'text-[var(--color-portal-orange-themed)]' : 'text-[var(--color-text-muted)]'}`} />
                <span className={`text-xs font-bold font-mono ${preference === 'auto' ? 'text-[var(--color-portal-orange-themed)]' : 'text-[var(--color-text-secondary)]'}`}>
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

{/* Section 5: System control (only visible for ADMIN role) */}
          {user?.role === 'ADMIN' && (
            <div className="glass-panel rounded-2xl p-6 border border-[var(--color-glass-border)]/50 shadow-portal space-y-5">
              <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
                <CloudSync className="w-4 h-4 text-[var(--color-portal-orange-themed)]" />
                <h3 className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)]">{t('settings.systemControl')}</h3>
              </div>

              <MessageBanner message={adminMessage} />

              <div className="flex items-center justify-between p-3.5 bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)]/50 rounded-2xl">
                <div className="text-left space-y-1 pr-4">
                  <h4 className="text-xs font-bold text-[var(--color-text-primary)] font-display">{t('settings.allowRegistrations')}</h4>
                  <p className="text-[10px] text-[var(--color-text-muted)] leading-normal">
                    {t('settings.allowRegistrationsHint')}
                  </p>
                </div>
                
                <Toggle
                  checked={registrationsEnabled}
                  disabled={adminLoading}
                  onChange={handleToggleRegistrations}
                />
              </div>
            </div>
          )}

{/* Section 6: Email notifications (SMTP) */}
          <div className="glass-panel rounded-2xl p-6 border border-[var(--color-glass-border)]/50 shadow-portal space-y-5">
            <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
              <Mail className="w-4 h-4 text-[var(--color-portal-orange-themed)]" />
              <h3 className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)]">{t('settings.emailNotifications')}</h3>
            </div>

            <MessageBanner message={smtpMessage} />

            <form onSubmit={handleSaveSMTP} className="space-y-4">
              <div className="grid grid-cols-3 gap-4">
                <div className="col-span-2 space-y-1.5">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                    {t('settings.smtpHost')}
                  </label>
                  <input
                    type="text"
                    required
                    value={smtpHost}
                    onChange={(e) => setSmtpHost(e.target.value)}
                    placeholder="smtp.example.com"
                    className="w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
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
                    className="w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
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
                  className="w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                />
              </div>

              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
                  {t('settings.smtpPassword')}
                </label>
                <div className="relative group">
                  <input
                    type="password"
                    required={!smtpHasConfig}
                    value={smtpPassword}
                    onChange={(e) => setSmtpPassword(e.target.value)}
                    placeholder={smtpHasConfig ? `•••••••• ${t('settings.smtpPasswordUnchanged')}` : '••••••••'}
                    className="w-full px-4 pr-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans font-mono"
                  />
                </div>
                {smtpHasConfig && (
                  <p className="text-[9px] text-[var(--color-text-muted)] font-mono leading-relaxed">
                    {t('settings.smtpPasswordHint')}
                  </p>
                )}
              </div>

              <div className="grid grid-cols-2 gap-4">
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
                    className="w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
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
                    className="w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
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
                  className="w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans"
                >
                  <option value="tls">TLS (Implicit)</option>
                  <option value="starttls">STARTTLS</option>
                  <option value="none">Keine</option>
                </select>
              </div>

              <div className="flex items-center justify-between p-3.5 bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)]/50 rounded-2xl">
                <div className="text-left space-y-1 pr-4">
                  <h4 className="text-xs font-bold text-[var(--color-text-primary)] font-display">{t('settings.smtpNotify')}</h4>
                  <p className="text-[10px] text-[var(--color-text-muted)] leading-normal">
                    {t('settings.smtpNotifyHint')}
                  </p>
                </div>
                <label className="relative inline-flex items-center cursor-pointer select-none">
                  <input
                    type="checkbox"
                    checked={smtpNotify}
                    onChange={(e) => setSmtpNotify(e.target.checked)}
                    className="sr-only peer"
                  />
                  <div className="w-10 h-6 bg-[var(--color-border)] peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-[var(--color-glass-border)] after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-[var(--color-bg-secondary)] after:border-[var(--color-border)] after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-portal-orange"></div>
                </label>
              </div>

              <div className="flex flex-wrap gap-2.5">
                <button
                  type="submit"
                  disabled={smtpLoading || !smtpHost || !smtpUsername || !smtpFromEmail}
                  className="flex-1 bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider cursor-pointer"
                >
                  {smtpLoading ? t('settings.saving') : t('settings.saveSmtp')}
                </button>
                <button
                  type="button"
                  onClick={handleTestSMTP}
                  disabled={smtpLoading}
                  className="px-4 py-2.5 bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded-xl hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)] transition-all font-mono font-bold text-[10px] cursor-pointer text-[var(--color-text-secondary)] hover:text-[var(--color-portal-navy-themed)] shadow-xs"
                >
                  {t('settings.testSmtp')}
                </button>
              </div>
            </form>
          </div>

        </div>

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
