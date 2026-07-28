import { useState, useEffect, useRef, useCallback } from 'react';
import { SyncDashboard } from './components/SyncDashboard';
import { ConnectForm } from './components/ConnectForm';
import { FileBrowser } from './components/FileBrowser';
import { Dashboard } from './components/Dashboard';
import { AuthForm } from './components/AuthForm';
import { MigrationsDashboard } from './components/MigrationsDashboard';
import { ResetPasswordForm } from './components/ResetPasswordForm';
import { ConfirmEmailChangeForm } from './components/ConfirmEmailChangeForm';
import { SettingsPage } from './components/SettingsPage';
import { LanguageSwitcher } from './components/LanguageSwitcher';
import { AdminPanel } from './components/AdminPanel';
import { ThemeProvider } from './contexts/ThemeContext';
import { ConfirmationProvider } from './contexts/ConfirmationContext';
import { ToastProvider } from './contexts/ToastContext';
import { useDismissConfirm } from './contexts/useConfirm';
import { useTranslation } from 'react-i18next';
import type { User, MigrationConfig, CloudFile } from './types';
import { configureApiClient, apiFetch } from './utils/apiClient';

type Step = 'login' | 'history' | 'connect' | 'select' | 'dashboard' | 'settings' | 'admin' | 'reset-password' | 'confirm-email' | 'syncdetail';

const getApiUrl = () => {
  const envUrl = import.meta.env.VITE_API_URL;
  // If the env variable is set and NOT pointing to localhost/127.0.0.1, use it.
  // Otherwise, dynamically determine it based on the browser address.
  if (envUrl && !envUrl.includes('localhost') && !envUrl.includes('127.0.0.1')) {
    return envUrl;
  }
  // Fallback: Dynamically determine the backend API URL.
  // If we are running on standard ports (no port, 80, or 443) on a custom domain,
  // use the same host without a port to route through the reverse proxy.
  const protocol = window.location.protocol;
  const hostname = window.location.hostname;
  const port = window.location.port;
  if (hostname !== 'localhost' && hostname !== '127.0.0.1' && (!port || port === '80' || port === '443')) {
    return `${protocol}//${hostname}`;
  }
  return `${protocol}//${hostname}:8001`;
};

const API_URL = getApiUrl();

// Security: warn when the API is reached over plaintext HTTP on a non-loopback
// host, since access tokens and connection credentials would then transit in clear (A04).
if (API_URL.startsWith('http://') && !/(localhost|127\.0\.0\.1)/.test(new URL(API_URL).hostname)) {
  console.warn('[security] API communication is over plaintext HTTP. Use HTTPS to protect tokens and credentials.');
}

function App() {
  const { t, i18n } = useTranslation();
  const dismissConfirm = useDismissConfirm();
  const resetTokenFromUrl = typeof window !== 'undefined'
    ? new URLSearchParams(window.location.search).get('reset-token')
    : null;

  const emailChangeTokenFromUrl = typeof window !== 'undefined'
    ? new URLSearchParams(window.location.search).get('email-change-token')
    : null;

  const initialStep: Step = emailChangeTokenFromUrl ? 'confirm-email' : resetTokenFromUrl ? 'reset-password' : 'login';
  const [step, setStep] = useState<Step>(initialStep);
  const [token, setToken] = useState<string>('');
  const tokenRef = useRef<string>('');
  const [user, setUser] = useState<User | null>(null);
  const [credentials, setCredentials] = useState<MigrationConfig | null>(null);
  const [initialFiles, setInitialFiles] = useState<CloudFile[]>([]);
  const [migrationId, setMigrationId] = useState<string>('');
  const [syncId, setSyncId] = useState<string>('');
  const [isValidating, setIsValidating] = useState<boolean>(
    () => !resetTokenFromUrl && !emailChangeTokenFromUrl && localStorage.getItem('has_session') === 'true'
  );
  const [showUserMenu, setShowUserMenu] = useState<boolean>(false);
  const [resetToken, setResetToken] = useState<string>(resetTokenFromUrl || '');
  const [emailChangeToken, setEmailChangeToken] = useState<string>(emailChangeTokenFromUrl || '');
  const [localStorageEnabled, setLocalStorageEnabled] = useState<boolean>(false);
  const [oauthProviders, setOauthProviders] = useState<Record<string, boolean>>({});
  const userMenuButtonRef = useRef<HTMLButtonElement>(null);

  // Cancel any open confirm when the user leaves the view that opened it.
  useEffect(() => {
    dismissConfirm();
  }, [step, dismissConfirm]);

  useEffect(() => {
    fetch(`${API_URL}/api/settings`)
      .then((res) => res.json())
      .then((data) => {
        if (data && data.local_storage_enabled === true) {
          setLocalStorageEnabled(true);
        }
        if (data && data.oauth_providers && typeof data.oauth_providers === 'object') {
          setOauthProviders(data.oauth_providers);
        }
      })
      .catch(() => {});
  }, []);
  const userMenuRef = useRef<HTMLDivElement>(null);
  // Tracks how many app-pushed history entries sit above the seeded top-level
  // entry, so "back to overview" can pop deterministically instead of using a
  // one-way latch that never resets.
  const historyDepth = useRef(0);
  // Whether the entry we are currently sitting on was pushed by the app
  // (vs. a seeded/replaced baseline or an external entry). Used by popstate to
  // decide whether leaving it should decrement historyDepth.
  const currentAppEntry = useRef(false);
  // Tracks history length so popstate can tell back (length shrinks) from
  // forward (length grows) and keep historyDepth in sync for both directions.
  const prevHistoryLen = useRef(window.history.length);

  // Capture the migration ID from the initial URL once on mount. Using a ref
  // prevents re-renders (caused by in-app navigation changing window.location.search)
  // from re-triggering the seed effect and resetting the step to 'login'.
  const initialUrlMigIdRef = useRef(new URLSearchParams(window.location.search).get('migration') ?? '');
  const urlMigId = initialUrlMigIdRef.current;

  // Build the URL (keeping the ?migration= param) and push/replace a history entry
  // carrying the in-app navigation state, then sync React state.
  const applyHistory = (nextStep: Step, idVal: string, replace: boolean) => {
    const url = new URL(window.location.href);
    const state: Record<string, unknown> = { step: nextStep, appEntry: !replace };
    if (nextStep === 'syncdetail') {
      url.searchParams.set('sync', idVal);
      url.searchParams.delete('migration');
      state.sync = idVal;
    } else if (idVal && nextStep !== 'history') {
      url.searchParams.set('migration', idVal);
      url.searchParams.delete('sync');
      state.migration = idVal;
    } else {
      url.searchParams.delete('migration');
      url.searchParams.delete('sync');
    }

    if (replace) {
      // A replace establishes a fresh baseline: forget any pushed entries.
      window.history.replaceState(state, '', url.toString());
      historyDepth.current = 0;
      currentAppEntry.current = false;
    } else {
      historyDepth.current += 1;
      currentAppEntry.current = true;
      window.history.pushState(state, '', url.toString());
      prevHistoryLen.current = window.history.length;
    }
    setStep(nextStep);
    if (nextStep === 'syncdetail') {
      setSyncId(idVal);
    } else {
      setMigrationId(idVal);
    }
  };

  // Replace the current history entry (no new navigable entry). Used for
  // post-auth / deep-link restores where browser-back should leave intentionally.
  const replaceNav = useCallback((nextStep: Step, migId: string = '') => applyHistory(nextStep, migId, true), []);

  // Forward in-app navigation: push a new history entry.
  const navigate = (nextStep: Step, migId?: string) => {
    applyHistory(nextStep, migId ?? migrationId, false);
  };

  // Clicking the logo always returns to the top-level migration overview,
  // replacing the current entry so further browser-back leaves the app.
  const goToOverview = () => {
    replaceNav('history');
  };

  // In-app back (FileBrowser / Settings / Admin).
  const goBack = () => {
    window.history.back();
  };

  const handleLogout = useCallback(async () => {
    try {
      await fetch(`${API_URL}/api/auth/logout`, { method: 'POST', credentials: 'include' });
    } catch (e) {
      console.error('Logout request failed:', e);
    }
    localStorage.removeItem('has_session');
    setToken('');
    setUser(null);
    setCredentials(null);
    setInitialFiles([]);
    setMigrationId('');
    replaceNav('login', '');
  }, [replaceNav]);

  useEffect(() => {
    tokenRef.current = token;
  }, [token]);

  // Scoped API client: single-flight 401 refresh without patching window.fetch.
  useEffect(() => {
    configureApiClient({
      apiUrl: API_URL,
      getAccessToken: () => tokenRef.current,
      setAccessToken: (tkn) => {
        tokenRef.current = tkn;
        setToken(tkn);
      },
      onAuthFailure: () => {
        void handleLogout();
      },
    });
  }, [handleLogout]);

  // Click outside / Escape to close user menu
  useEffect(() => {
    if (!showUserMenu) return;
    const handleOutsideClick = (e: MouseEvent) => {
      if (userMenuRef.current && !userMenuRef.current.contains(e.target as Node)) {
        setShowUserMenu(false);
      }
    };
    const handleKey = (e: KeyboardEvent) => {
        if (e.key === 'Escape') {
          setShowUserMenu(false);
          userMenuButtonRef.current?.focus();
        }
    };
    document.addEventListener('mousedown', handleOutsideClick);
    document.addEventListener('keydown', handleKey);
    return () => {
      document.removeEventListener('mousedown', handleOutsideClick);
      document.removeEventListener('keydown', handleKey);
    };
  }, [showUserMenu]);

  // Seed the initial history entry with the current step/migration so the very
  // first entry carries navigable state (replace, not push). Depends only on
  // initialStep (which is also stable) so this runs exactly once on mount.
  useEffect(() => {
    applyHistory(initialStep, urlMigId, true);
  // urlMigId is stable (backed by a ref), so this is effectively [initialStep].
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialStep]);

  // Handle browser back/forward between in-app screens.
  useEffect(() => {
    const onPop = (e: PopStateEvent) => {
      const s = e.state as { step?: Step; migration?: string; sync?: string; appEntry?: boolean } | null;
      // Keep historyDepth in sync for both back (length shrinks) and forward
      // (length grows) so the seeded top-level overview remains the back target.
      const newLen = window.history.length;
      if (newLen < prevHistoryLen.current && currentAppEntry.current) {
        historyDepth.current = Math.max(0, historyDepth.current - 1);
      } else if (newLen > prevHistoryLen.current && s?.appEntry) {
        historyDepth.current += 1;
      }
      currentAppEntry.current = s?.appEntry ?? false;
      prevHistoryLen.current = newLen;
      if (s?.step) {
        setStep(s.step);
        if (s.step === 'syncdetail') {
          setSyncId(s.sync ?? new URLSearchParams(window.location.search).get('sync') ?? '');
        } else {
          setMigrationId(s.migration ?? new URLSearchParams(window.location.search).get('migration') ?? '');
        }
        // Credentials/initialFiles are only needed by `select`; clear them when
        // navigating to an unrelated screen to avoid stale secrets in memory.
        if (s.step !== 'dashboard' && s.step !== 'select') {
          setCredentials(null);
          setInitialFiles([]);
        }
      } else {
        // Pre-app / external entry: re-derive step from session like initial load.
        const params = new URLSearchParams(window.location.search);
        const mig = params.get('migration');
        const syncJ = params.get('sync');
        if (localStorage.getItem('has_session') === 'true' && mig) {
          setMigrationId(mig);
          setStep('dashboard');
        } else if (localStorage.getItem('has_session') === 'true' && syncJ) {
          setSyncId(syncJ);
          setStep('syncdetail');
        } else if (localStorage.getItem('has_session') === 'true') {
          setStep('history');
        } else {
          setStep('login');
        }
      }
    };
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, []);

  // 1. Silent login / Refresh Token check on load
  useEffect(() => {
    // If we arrived via a password reset link or email change link, skip auth validation entirely.
    if (resetTokenFromUrl || emailChangeTokenFromUrl) {
      return;
    }

    // No session stored -> stay on login (initial state already covers this).
    if (localStorage.getItem('has_session') !== 'true') {
      return;
    }

    fetch(`${API_URL}/api/auth/refresh`, { method: 'POST', credentials: 'include' })
      .then(async (res) => {
        if (res.ok) {
          const data = await res.json();
          setToken(data.access_token);
          
          // Fetch user profile
          const meRes = await apiFetch(`${API_URL}/api/auth/me`, {
            headers: { 'Authorization': `Bearer ${data.access_token}` },
          });

          if (meRes.ok) {
            const userData = await meRes.json();
            setUser(userData);
			if (userData.language === 'de' || userData.language === 'en') {
			  localStorage.setItem('i18nextLng', userData.language);
			  void i18n.changeLanguage(userData.language);
			}

            // Check if there is an active migration ID in url
            const params = new URLSearchParams(window.location.search);
            const urlMigId = params.get('migration');
            const urlSyncId = params.get('sync');
            if (urlMigId) {
              // Verify active migration status
              const migRes = await apiFetch(`${API_URL}/api/migration/${urlMigId}`, {
                headers: { 'Authorization': `Bearer ${data.access_token}` },
              });
              if (migRes.ok) {
                replaceNav('dashboard', urlMigId);
              } else {
                replaceNav('history', '');
              }
            } else if (urlSyncId) {
              // Verify active sync status
              const syncRes = await apiFetch(`${API_URL}/api/sync/${urlSyncId}`, {
                headers: { 'Authorization': `Bearer ${data.access_token}` },
              });
              if (syncRes.ok) {
                replaceNav('syncdetail', urlSyncId);
              } else {
                replaceNav('history', '');
              }
            } else {
              replaceNav('history', '');
            }
          } else {
            localStorage.removeItem('has_session');
            replaceNav('login', '');
          }
        } else {
          localStorage.removeItem('has_session');
          replaceNav('login', '');
        }
      })
      .catch((err) => {
        console.error('Silent login error:', err);
        localStorage.removeItem('has_session');
        replaceNav('login', '');
      })
      .finally(() => {
        setIsValidating(false);
      });
    // replaceNav / applyHistory are stable in intent; intentionally not deps.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [resetTokenFromUrl, emailChangeTokenFromUrl]);

  // 2. Silent JWT refresh (every 14 minutes)
  useEffect(() => {
    if (!token) return;
    const interval = setInterval(async () => {
      try {
        const res = await fetch(`${API_URL}/api/auth/refresh`, { method: 'POST', credentials: 'include' });
        if (res.ok) {
          const data = await res.json();
          setToken(data.access_token);
        } else {
          handleLogout();
        }
      } catch (e) {
        console.error('Failed silent refresh:', e);
      }
    }, 14 * 60 * 1000); // 14 minutes

    return () => clearInterval(interval);
  }, [token, handleLogout]);

  const handleAuthSuccess = (accessToken: string, loggedUser: User) => {
    localStorage.setItem('has_session', 'true');
    setToken(accessToken);
    setUser(loggedUser);
	if (loggedUser.language === 'de' || loggedUser.language === 'en') {
	  localStorage.setItem('i18nextLng', loggedUser.language);
	  void i18n.changeLanguage(loggedUser.language);
	}
    replaceNav('history', '');
  };

  const handleConnectSuccess = (config: MigrationConfig, files: CloudFile[]) => {
    setCredentials(config);
    setInitialFiles(files);
    navigate('select');
  };

  const handleStartSuccess = (id: string, isSync?: boolean) => {
    // Secrets (source/target passwords, OAuth tokens, SFTP keys) are no longer
    // needed once the migration is created — drop them from memory.
    setCredentials(null);
    setInitialFiles([]);
    if (isSync) {
      navigate('syncdetail', id);
    } else {
      navigate('dashboard', id);
    }
  };

  const handleResetPasswordSuccess = () => {
    // Clean up the URL param and return to login
    const url = new URL(window.location.href);
    url.searchParams.delete('reset-token');
    window.history.replaceState({}, '', url.toString());
    setResetToken('');
    replaceNav('login', '');
  };

  const handleConfirmEmailChangeSuccess = () => {
    // Clean up the URL param and return to login (refresh tokens were invalidated)
    const url = new URL(window.location.href);
    url.searchParams.delete('email-change-token');
    window.history.replaceState({}, '', url.toString());
    setEmailChangeToken('');
    handleLogout();
  };

  const handleReset = () => {
    setCredentials(null);
    setInitialFiles([]);
    goToOverview();
  };

  if (isValidating) {
    return (
      <div className="min-h-screen bg-[var(--color-bg-primary)] text-[var(--color-text-primary)] flex items-center justify-center px-4">
        <div className="w-full max-w-sm border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-6 text-center">
          <h1 className="text-lg font-semibold">Clumoove</h1>
          <p className="mt-2 text-sm text-[var(--color-text-secondary)]">{t('common.initializing')}</p>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-[var(--color-bg-primary)] text-[var(--color-text-primary)] flex flex-col font-sans relative">
      
      <header className="sticky top-0 z-50 border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)]">
          <div className="mx-auto flex h-16 max-w-6xl items-center justify-between px-4 sm:px-6">
          <div>
            <button
              type="button"
              onClick={step !== 'login' ? goToOverview : undefined}
              disabled={step === 'login'}
              aria-label="Clumoove – go to overview"
              className="flex items-center gap-2 text-lg font-semibold tracking-tight text-[var(--color-text-primary)] disabled:cursor-default"
            >
              <img src="/clumoove_logo.png" alt="" className="h-8 w-8 object-contain" />
              Clumoove
            </button>
          </div>

          {/* User Section in Header */}
          {user && (
            <div className="relative" ref={userMenuRef}>
              <button
                ref={userMenuButtonRef}
                type="button"
                onClick={() => setShowUserMenu((open) => !open)}
                aria-haspopup="menu"
                aria-expanded={showUserMenu}
                aria-controls="user-menu"
                className="flex items-center gap-2 cursor-pointer p-0 text-sm"
              >
                <span className="font-medium text-[var(--color-text-primary)]">{user.display_name}</span>
                {user.avatar ? (
                  <img 
                    src={user.avatar} 
                    className="w-7 h-7 rounded-full object-cover" 
                    alt={user.display_name}
                  />
                ) : (
                  <span className="flex h-7 w-7 items-center justify-center rounded-full bg-[var(--color-bg-tertiary)] text-xs font-medium text-[var(--color-text-secondary)]" aria-hidden="true">
                    {user.display_name.slice(0, 1).toUpperCase()}
                  </span>
                )}
              </button>

              {showUserMenu && (
                <div
                  id="user-menu"
                  role="menu"
                  aria-label={user.display_name}
                  onKeyDown={(event) => {
                    const items = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitem"]'));
                    const currentIndex = items.indexOf(document.activeElement as HTMLButtonElement);
                    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
                      event.preventDefault();
                      const direction = event.key === 'ArrowDown' ? 1 : -1;
                      items[(currentIndex + direction + items.length) % items.length]?.focus();
                    }
                    if (event.key === 'Home' || event.key === 'End') {
                      event.preventDefault();
                      items[event.key === 'Home' ? 0 : items.length - 1]?.focus();
                    }
                  }}
                  className="absolute right-0 top-full z-50 mt-2 w-48 border border-[var(--color-border)] bg-[var(--color-bg-elevated)] py-1"
                >
                  {user?.role === 'ADMIN' && (
                    <button
                      type="button"
                      role="menuitem"
                      onClick={() => {
                        navigate('admin');
                        setShowUserMenu(false);
                      }}
                      className="w-full px-3 py-2 text-left text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]"
                    >
                      {t('nav.admin')}
                    </button>
                  )}
                  <button
                    type="button"
                    role="menuitem"
                    onClick={() => {
                      navigate('settings');
                      setShowUserMenu(false);
                    }}
                    className="w-full px-3 py-2 text-left text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]"
                  >
                    {t('nav.settings')}
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    onClick={() => {
                      handleLogout();
                      setShowUserMenu(false);
                    }}
                      className="w-full px-3 py-2 text-left text-sm text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)]"
                  >
                    {t('nav.logout')}
                  </button>
                </div>
              )}
            </div>
          )}
        </div>
      </header>

      <main className={`mx-auto flex w-full max-w-6xl flex-grow flex-col px-4 py-6 sm:px-6 sm:py-8 ${step === 'connect' ? 'justify-start' : 'justify-center'}`}>
        <div className="w-full">
          {step === 'login' && (
            <AuthForm apiUrl={API_URL} onAuthSuccess={handleAuthSuccess} />
          )}

          {step === 'reset-password' && (
            <ResetPasswordForm
              apiUrl={API_URL}
              token={resetToken}
              onSuccess={handleResetPasswordSuccess}
            />
          )}

          {step === 'confirm-email' && (
            <ConfirmEmailChangeForm
              apiUrl={API_URL}
              token={emailChangeToken}
              onSuccess={handleConfirmEmailChangeSuccess}
            />
          )}

          {step === 'history' && (
            <MigrationsDashboard
              apiUrl={API_URL}
              token={token}
              user={user}
              onStartNewMigration={() => navigate('connect')}
              onSelectActiveMigration={(id) => {
                navigate('dashboard', id);
              }}
              onSelectActiveSync={(id) => {
                navigate('syncdetail', id);
              }}
            />
          )}

          {step === 'syncdetail' && (
            <SyncDashboard
              syncId={syncId}
              apiUrl={API_URL}
              onBack={() => goBack()}
              token={token}
            />
          )}

          {step === 'connect' && (
            <ConnectForm 
              onConnectSuccess={handleConnectSuccess} 
              apiUrl={API_URL} 
              token={token}
              localStorageEnabled={localStorageEnabled}
              oauthProviders={oauthProviders}
              onBack={() => goBack()}
            />
          )}
          
          {step === 'select' && credentials && (
            <FileBrowser
              initialFiles={initialFiles}
              credentials={credentials}
              apiUrl={API_URL}
              onBack={() => goBack()}
              onStartSuccess={handleStartSuccess}
              token={token}
            />
          )}
          
          {step === 'dashboard' && (
            <Dashboard
              migrationId={migrationId}
              apiUrl={API_URL}
              onReset={handleReset}
              token={token}
            />
          )}

          {step === 'settings' && (
            <SettingsPage
              key={user?.id}
              apiUrl={API_URL}
              token={token}
              user={user}
              onBack={() => goBack()}
              onUpdateUser={(updated) => setUser(updated)}
              oauthProviders={oauthProviders}
              localStorageEnabled={localStorageEnabled}
            />
          )}

          {step === 'admin' && user?.role === 'ADMIN' && (
            <AdminPanel
              apiUrl={API_URL}
              token={token}
              user={user}
              onBack={() => goBack()}
            />
          )}
        </div>
      </main>

      {/* Footer */}
      <footer className="mt-auto border-t border-[var(--color-border)] bg-[var(--color-bg-secondary)] py-4">
        <div className="mx-auto flex max-w-6xl items-center justify-end px-6">
          <LanguageSwitcher authenticated={Boolean(user && token)} />
        </div>
      </footer>
    </div>
  );
}

// Wrap App with ThemeProvider, ConfirmationProvider, ToastProvider
function AppWithTheme() {
  return (
    <ThemeProvider>
      <ConfirmationProvider>
        <ToastProvider>
          <App />
        </ToastProvider>
      </ConfirmationProvider>
    </ThemeProvider>
  );
}

export default AppWithTheme;
