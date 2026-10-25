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
        const refreshResponse = await fetch(`${apiUrl}/api/auth/refresh`, { method: 'POST', credentials: 'include' });
        if (!refreshResponse.ok) throw new Error('Session refresh failed');
        const refreshData = await refreshResponse.json() as { access_token?: string };
        if (!refreshData.access_token) throw new Error('Session refresh failed');
        if (cancelled) return;
        tokenRef.current = refreshData.access_token;
        setToken(refreshData.access_token);

        const meResponse = await apiFetch(`${apiUrl}/api/auth/me`, {
          headers: { Authorization: `Bearer ${refreshData.access_token}` },
        });
        if (!meResponse.ok) throw new Error('Profile fetch failed');
        const nextUser = await meResponse.json() as User;
        if (cancelled) return;
        setUser(nextUser);
        applyPreferredLanguage(nextUser);

        if (initialMigrationId) {
          const response = await apiFetch(`${apiUrl}/api/migration/${initialMigrationId}`, { headers: { Authorization: `Bearer ${refreshData.access_token}` } });
          replaceNav(response.ok ? 'dashboard' : 'history', response.ok ? initialMigrationId : '');
        } else if (initialSyncId) {
          const response = await apiFetch(`${apiUrl}/api/sync/${initialSyncId}`, { headers: { Authorization: `Bearer ${refreshData.access_token}` } });
          replaceNav(response.ok ? 'syncdetail' : 'history', response.ok ? initialSyncId : '');
        } else if (initialBackupId) {
          const response = await apiFetch(`${apiUrl}/api/backup/${initialBackupId}`, { headers: { Authorization: `Bearer ${refreshData.access_token}` } });
          replaceNav(response.ok ? 'backupdetail' : 'history', response.ok ? initialBackupId : '');
        } else if (initialProfileId) {
          const response = await apiFetch(`${apiUrl}/api/profiles/${initialProfileId}`, { headers: { Authorization: `Bearer ${refreshData.access_token}` } });
          replaceNav(response.ok ? 'files' : 'history', response.ok ? initialProfileId : '');
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
  }, [apiUrl, applyPreferredLanguage, emailChangeToken, hasStoredSession, initialBackupId, initialMigrationId, initialProfileId, initialSyncId, replaceNav, resetToken]);

  useEffect(() => {
    if (!token) return;
    const interval = window.setInterval(async () => {
      try {
        const response = await fetch(`${apiUrl}/api/auth/refresh`, { method: 'POST', credentials: 'include' });
        if (!response.ok) throw new Error('Session refresh failed');
        const data = await response.json() as { access_token?: string };
        if (!data.access_token) throw new Error('Session refresh failed');
        tokenRef.current = data.access_token;
        setToken(data.access_token);
      } catch {
        void logout();
      }
    }, 14 * 60 * 1000);
    return () => window.clearInterval(interval);
  }, [apiUrl, logout, token]);

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
