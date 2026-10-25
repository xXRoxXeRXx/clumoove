import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { User } from '../types';
import { apiFetch, configureApiClient } from '../utils/apiClient';
import { logger } from '../utils/logger';
import type { AppStep } from './useAppHistory';

type SessionNavigation = {
  initialMigrationId?: string;
  initialSyncId?: string;
  initialBackupId?: string;
  initialProfileId?: string;
};

type UseSessionOptions = {
  apiUrl: string;
  resetToken: string | null;
  emailChangeToken: string | null;
  initialNavigation: SessionNavigation;
  replaceNav: (step: AppStep, id?: string) => void;
  onLogout?: () => void;
};

type DeepLinkTarget = { path: string; id: string; step: AppStep };

function deepLinkTarget({ initialMigrationId, initialSyncId, initialBackupId, initialProfileId }: SessionNavigation): DeepLinkTarget | null {
  if (initialMigrationId) return { path: 'migration', id: initialMigrationId, step: 'dashboard' };
  if (initialSyncId) return { path: 'sync', id: initialSyncId, step: 'syncdetail' };
  if (initialBackupId) return { path: 'backup', id: initialBackupId, step: 'backupdetail' };
  if (initialProfileId) return { path: 'profiles', id: initialProfileId, step: 'files' };
  return null;
}

function usePreferredLanguage() {
  const { i18n } = useTranslation();
  return useCallback((user: User) => {
    if (user.language === 'de' || user.language === 'en') {
      localStorage.setItem('i18nextLng', user.language);
      void i18n.changeLanguage(user.language);
    }
  }, [i18n]);
}

export function useSession({
  apiUrl,
  resetToken,
  emailChangeToken,
  initialNavigation,
  replaceNav,
  onLogout,
}: UseSessionOptions) {
  const { initialMigrationId, initialSyncId, initialBackupId, initialProfileId } = initialNavigation;
  const hasStoredSession = localStorage.getItem('has_session') === 'true';
  const [token, setToken] = useState('');
  const tokenRef = useRef('');
  const [user, setUser] = useState<User | null>(null);
  const [isValidating, setIsValidating] = useState(
    () => !resetToken && !emailChangeToken && hasStoredSession,
  );
  const applyPreferredLanguage = usePreferredLanguage();
  const refreshAccessToken = useCallback(async () => {
    const response = await fetch(`${apiUrl}/api/auth/refresh`, { method: 'POST', credentials: 'include' });
    if (!response.ok) throw new Error('Session refresh failed');
    const data = await response.json() as { access_token?: string };
    if (!data.access_token) throw new Error('Session refresh failed');
    return data.access_token;
  }, [apiUrl]);

  const logout = useCallback(async () => {
    try {
      await fetch(`${apiUrl}/api/auth/logout`, { method: 'POST', credentials: 'include' });
    } catch (error) {
      logger.error('Logout request failed', error);
    }
    localStorage.removeItem('has_session');
    tokenRef.current = '';
    setToken('');
    setUser(null);
    onLogout?.();
    replaceNav('login', '');
  }, [apiUrl, onLogout, replaceNav]);

  useEffect(() => {
    configureApiClient({
      apiUrl,
      getAccessToken: () => tokenRef.current,
      setAccessToken: (nextToken) => {
        tokenRef.current = nextToken;
        setToken(nextToken);
      },
      onAuthFailure: () => void logout(),
    });
  }, [apiUrl, logout]);

  useEffect(() => {
    if (resetToken || emailChangeToken || !hasStoredSession) return;
    let cancelled = false;

    const restore = async () => {
      try {
        const accessToken = await refreshAccessToken();
        if (cancelled) return;
        tokenRef.current = accessToken;
        setToken(accessToken);

        const meResponse = await apiFetch(`${apiUrl}/api/auth/me`);
        if (!meResponse.ok) throw new Error('Profile fetch failed');
        const nextUser = await meResponse.json() as User;
        if (cancelled) return;
        setUser(nextUser);
        applyPreferredLanguage(nextUser);

        const target = deepLinkTarget({ initialMigrationId, initialSyncId, initialBackupId, initialProfileId });
        if (target) {
          const response = await apiFetch(`${apiUrl}/api/${target.path}/${target.id}`);
          replaceNav(response.ok ? target.step : 'history', response.ok ? target.id : '');
        } else {
          replaceNav('history', '');
        }
      } catch (error) {
        if (!cancelled) {
          logger.error('Silent login error', error);
          localStorage.removeItem('has_session');
          replaceNav('login', '');
        }
      } finally {
        if (!cancelled) setIsValidating(false);
      }
    };

    void restore();
    return () => { cancelled = true; };
  }, [apiUrl, applyPreferredLanguage, emailChangeToken, hasStoredSession, initialBackupId, initialMigrationId, initialProfileId, initialSyncId, refreshAccessToken, replaceNav, resetToken]);

  useEffect(() => {
    if (!token) return;
    const interval = window.setInterval(async () => {
      try {
        const accessToken = await refreshAccessToken();
        tokenRef.current = accessToken;
        setToken(accessToken);
      } catch {
        void logout();
      }
    }, 14 * 60 * 1000);
    return () => window.clearInterval(interval);
  }, [logout, refreshAccessToken, token]);

  const authenticate = useCallback((accessToken: string, loggedUser: User) => {
    localStorage.setItem('has_session', 'true');
    tokenRef.current = accessToken;
    setToken(accessToken);
    setUser(loggedUser);
    applyPreferredLanguage(loggedUser);
    replaceNav('history', '');
  }, [applyPreferredLanguage, replaceNav]);

  return { token, user, isValidating, authenticate, logout, updateUser: setUser };
}
