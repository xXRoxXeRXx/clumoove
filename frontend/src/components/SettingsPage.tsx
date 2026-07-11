import React, { useState, useEffect } from 'react';
import { ArrowLeft, User, Image as ImageIcon, Lock, Settings, Trash2, Upload, CloudLightning, Eye, EyeOff } from 'lucide-react';
import { AvatarCropper } from './AvatarCropper';

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

export function SettingsPage({ apiUrl, token, user, onBack, onUpdateUser }: SettingsPageProps) {
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

  // Admin settings state
  const [registrationsEnabled, setRegistrationsEnabled] = useState<boolean>(true);
  const [adminLoading, setAdminLoading] = useState<boolean>(false);
  const [adminMessage, setAdminMessage] = useState<{ text: string; type: 'success' | 'error' } | null>(null);

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
        const text = await res.text();
        throw new Error(text || 'Fehler beim Aktualisieren des Profils.');
      }

      const data = await res.json();
      onUpdateUser({ ...user, display_name: data.display_name });
      setProfileMessage({ text: 'Profil erfolgreich aktualisiert!', type: 'success' });
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
        const text = await res.text();
        throw new Error(text || 'Fehler beim Hochladen des Profilbilds.');
      }

      const data = await res.json();
      onUpdateUser({ ...user, avatar: data.avatar });
      setAvatarMessage({ text: 'Profilbild erfolgreich hochgeladen!', type: 'success' });
    } catch (err) {
      setAvatarMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setAvatarLoading(false);
    }
  };

  const handleDeleteAvatar = async () => {
    if (!window.confirm('Möchtest du dein Profilbild wirklich löschen?')) return;
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
        const text = await res.text();
        throw new Error(text || 'Fehler beim Löschen des Profilbilds.');
      }

      // Remove avatar from state
      const updatedUser = { ...user };
      delete updatedUser.avatar;
      onUpdateUser(updatedUser);
      setAvatarMessage({ text: 'Profilbild erfolgreich gelöscht!', type: 'success' });
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
      setPasswordMessage({ text: 'Die neuen Passwörter stimmen nicht überein.', type: 'error' });
      return;
    }

    if (newPassword.length < 8) {
      setPasswordMessage({ text: 'Das neue Passwort muss mindestens 8 Zeichen lang sein.', type: 'error' });
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
        const text = await res.text();
        throw new Error(text || 'Fehler beim Ändern des Passworts.');
      }

      setPasswordMessage({ text: 'Passwort erfolgreich geändert!', type: 'success' });
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
        const text = await res.text();
        throw new Error(text || 'Fehler beim Speichern der Einstellung.');
      }

      setRegistrationsEnabled(checked);
      setAdminMessage({
        text: checked ? 'Registrierungen wurden aktiviert.' : 'Registrierungen wurden gesperrt.',
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
      <div className="flex items-center justify-between pb-4 border-b border-slate-200/50">
        <button
          onClick={onBack}
          className="flex items-center gap-2 px-4 py-2 bg-white border border-slate-200 rounded-full hover:border-slate-350 hover:bg-slate-50 transition-all font-mono font-bold text-xs cursor-pointer text-slate-650 hover:text-portal-navy shadow-xs"
        >
          <ArrowLeft className="w-4 h-4" />
          Zurück
        </button>
        <div className="flex items-center gap-2">
          <Settings className="w-5 h-5 text-portal-navy" />
          <h2 className="font-display font-extrabold text-xl text-portal-navy leading-none">Einstellungen</h2>
        </div>
      </div>

      {/* Main Grid Layout */}
      <div className="grid md:grid-cols-2 gap-6">
        
        {/* Left Side: Profile Information & Avatar */}
        <div className="space-y-6">
          
          {/* Section 1: Profil */}
          <div className="glass-panel rounded-2xl p-6 border border-white/50 shadow-portal space-y-5">
            <div className="flex items-center gap-2 pb-3 border-b border-slate-100">
              <User className="w-4 h-4 text-portal-orange" />
              <h3 className="font-display font-bold text-sm text-portal-navy">Profil-Details</h3>
            </div>

            {profileMessage && (
              <div className={`p-3 rounded-xl border text-[11px] font-mono text-center leading-relaxed ${
                profileMessage.type === 'success' ? 'bg-emerald-50 border-emerald-200 text-emerald-800' : 'bg-rose-50 border-rose-250 text-rose-800'
              }`}>
                {profileMessage.text}
              </div>
            )}

            <form onSubmit={handleUpdateProfile} className="space-y-4">
              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">
                  E-Mail Adresse (Nicht änderbar)
                </label>
                <input
                  type="text"
                  disabled
                  value={user?.email || ''}
                  className="w-full px-4 py-2.5 bg-slate-100 border border-slate-200/85 rounded-xl text-sm text-slate-500 cursor-not-allowed font-sans"
                />
              </div>

              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">
                  Anzeigename
                </label>
                <input
                  type="text"
                  required
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                  placeholder="Max Mustermann"
                  className="w-full px-4 py-2.5 bg-white/55 border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans"
                />
              </div>

              <button
                type="submit"
                disabled={profileLoading || displayName.trim() === '' || displayName.trim() === user?.display_name}
                className="w-full bg-gradient-to-r from-portal-orange to-orange-500 text-white hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider cursor-pointer"
              >
                {profileLoading ? 'Wird gespeichert...' : 'Änderungen speichern'}
              </button>
            </form>
          </div>

          {/* Section 2: Profilbild */}
          <div className="glass-panel rounded-2xl p-6 border border-white/50 shadow-portal space-y-5">
            <div className="flex items-center gap-2 pb-3 border-b border-slate-100">
              <ImageIcon className="w-4 h-4 text-portal-orange" />
              <h3 className="font-display font-bold text-sm text-portal-navy">Profilbild</h3>
            </div>

            {avatarMessage && (
              <div className={`p-3 rounded-xl border text-[11px] font-mono text-center leading-relaxed ${
                avatarMessage.type === 'success' ? 'bg-emerald-50 border-emerald-200 text-emerald-800' : 'bg-rose-50 border-rose-250 text-rose-800'
              }`}>
                {avatarMessage.text}
              </div>
            )}

            <div className="flex flex-col items-center sm:flex-row gap-5 p-2 bg-slate-50/50 rounded-2xl border border-slate-200/50">
              <div className="relative">
                {user?.avatar ? (
                  <img
                    src={user.avatar}
                    alt="User Avatar"
                    className="w-20 h-20 rounded-full object-cover border border-slate-200 shadow-xs"
                  />
                ) : (
                  <div className="w-20 h-20 bg-portal-navy text-white rounded-full flex items-center justify-center border border-slate-200 shadow-xs">
                    <User className="w-10 h-10" />
                  </div>
                )}
                {avatarLoading && (
                  <div className="absolute inset-0 bg-slate-900/40 rounded-full flex items-center justify-center">
                    <span className="animate-spin rounded-full h-5 w-5 border-2 border-white border-t-transparent"></span>
                  </div>
                )}
              </div>

              <div className="flex-grow space-y-2.5">
                <p className="text-[10px] text-slate-500 font-sans leading-relaxed">
                  Lade ein neues Profilbild hoch. Unterstützt werden PNG, JPEG, WebP und GIF bis max. 2 MB.
                </p>
                <div className="flex flex-wrap gap-2.5">
                  <label className="flex items-center gap-1.5 px-3 py-1.5 bg-white border border-slate-200 rounded-xl hover:border-slate-350 hover:bg-slate-50 transition-all font-mono font-bold text-[10px] cursor-pointer text-slate-650 hover:text-portal-navy shadow-xs">
                    <Upload className="w-3.5 h-3.5" />
                    <span>Bild wählen</span>
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
                      className="flex items-center gap-1.5 px-3 py-1.5 bg-white border border-rose-200 text-rose-600 rounded-xl hover:bg-rose-50/70 hover:border-rose-350 transition-all font-mono font-bold text-[10px] cursor-pointer shadow-xs"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                      Löschen
                    </button>
                  )}
                </div>
              </div>
            </div>
          </div>
        </div>

        {/* Right Side: Password & Admin Settings */}
        <div className="space-y-6">
          
          {/* Section 3: Passwort */}
          <div className="glass-panel rounded-2xl p-6 border border-white/50 shadow-portal space-y-5">
            <div className="flex items-center gap-2 pb-3 border-b border-slate-100">
              <Lock className="w-4 h-4 text-portal-orange" />
              <h3 className="font-display font-bold text-sm text-portal-navy">Passwort ändern</h3>
            </div>

            {passwordMessage && (
              <div className={`p-3 rounded-xl border text-[11px] font-mono text-center leading-relaxed ${
                passwordMessage.type === 'success' ? 'bg-emerald-50 border-emerald-200 text-emerald-800' : 'bg-rose-50 border-rose-250 text-rose-800'
              }`}>
                {passwordMessage.text}
              </div>
            )}

            <form onSubmit={handleChangePassword} className="space-y-4">
              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">
                  Aktuelles Passwort
                </label>
                <div className="relative group">
                  <input
                    type={showCurrentPassword ? 'text' : 'password'}
                    required
                    value={currentPassword}
                    onChange={(e) => setCurrentPassword(e.target.value)}
                    placeholder="••••••••"
                    className="w-full px-4 pr-10 py-2.5 bg-white/55 border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans font-mono"
                  />
                  <button
                    type="button"
                    onClick={() => setShowCurrentPassword(!showCurrentPassword)}
                    className="absolute inset-y-0 right-0 pr-3 flex items-center text-slate-400 hover:text-slate-650"
                  >
                    {showCurrentPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                  </button>
                </div>
              </div>

              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">
                  Neues Passwort
                </label>
                <div className="relative group">
                  <input
                    type={showNewPassword ? 'text' : 'password'}
                    required
                    value={newPassword}
                    onChange={(e) => setNewPassword(e.target.value)}
                    placeholder="••••••••"
                    className="w-full px-4 pr-10 py-2.5 bg-white/55 border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans font-mono"
                  />
                  <button
                    type="button"
                    onClick={() => setShowNewPassword(!showNewPassword)}
                    className="absolute inset-y-0 right-0 pr-3 flex items-center text-slate-400 hover:text-slate-650"
                  >
                    {showNewPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                  </button>
                </div>
              </div>

              <div className="space-y-1.5">
                <label className="block text-[10px] font-bold text-slate-500 uppercase tracking-widest font-mono">
                  Neues Passwort bestätigen
                </label>
                <div className="relative group">
                  <input
                    type={showConfirmPassword ? 'text' : 'password'}
                    required
                    value={confirmPassword}
                    onChange={(e) => setConfirmPassword(e.target.value)}
                    placeholder="••••••••"
                    className="w-full px-4 pr-10 py-2.5 bg-white/55 border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-white transition-all font-sans font-mono"
                  />
                  <button
                    type="button"
                    onClick={() => setShowConfirmPassword(!showConfirmPassword)}
                    className="absolute inset-y-0 right-0 pr-3 flex items-center text-slate-400 hover:text-slate-650"
                  >
                    {showConfirmPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                  </button>
                </div>
              </div>

              <button
                type="submit"
                disabled={passwordLoading || !currentPassword || !newPassword || !confirmPassword}
                className="w-full bg-gradient-to-r from-portal-orange to-orange-500 text-white hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all disabled:opacity-50 disabled:cursor-not-allowed uppercase tracking-wider cursor-pointer"
              >
                {passwordLoading ? 'Wird geändert...' : 'Passwort ändern'}
              </button>
            </form>
          </div>

          {/* Section 4: Registrierungen sperren (Only visible if user role === 'ADMIN') */}
          {user?.role === 'ADMIN' && (
            <div className="glass-panel rounded-2xl p-6 border border-white/50 shadow-portal space-y-5">
              <div className="flex items-center gap-2 pb-3 border-b border-slate-100">
                <CloudLightning className="w-4 h-4 text-portal-orange" />
                <h3 className="font-display font-bold text-sm text-portal-navy">Systemsteuerung</h3>
              </div>

              {adminMessage && (
                <div className={`p-3 rounded-xl border text-[11px] font-mono text-center leading-relaxed ${
                  adminMessage.type === 'success' ? 'bg-emerald-50 border-emerald-200 text-emerald-800' : 'bg-rose-50 border-rose-250 text-rose-800'
                }`}>
                  {adminMessage.text}
                </div>
              )}

              <div className="flex items-center justify-between p-3.5 bg-slate-50/50 border border-slate-200/50 rounded-2xl">
                <div className="text-left space-y-1 pr-4">
                  <h4 className="text-xs font-bold text-slate-800 font-display">Registrierungen erlauben</h4>
                  <p className="text-[10px] text-slate-500 leading-normal">
                    Schalte aus, um neue Benutzerregistrierungen systemweit zu sperren. Bestehende Logins bleiben aktiv.
                  </p>
                </div>
                
                <label className="relative inline-flex items-center cursor-pointer select-none">
                  <input
                    type="checkbox"
                    checked={registrationsEnabled}
                    disabled={adminLoading}
                    onChange={(e) => handleToggleRegistrations(e.target.checked)}
                    className="sr-only peer"
                  />
                  <div className="w-10 h-6 bg-slate-250 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-slate-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-portal-orange"></div>
                </label>
              </div>
            </div>
          )}

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
