import { useState, useEffect, useRef, useCallback, lazy, Suspense } from 'react';
import { SyncDashboard } from './components/SyncDashboard';
import { ConnectForm } from './components/ConnectForm';
import { FileBrowser } from './components/FileBrowser';
import { Dashboard } from './components/Dashboard';
import { AuthForm } from './components/AuthForm';
import { MigrationsDashboard } from './components/MigrationsDashboard';
import { ResetPasswordForm } from './components/ResetPasswordForm';
import { ConfirmEmailChangeForm } from './components/ConfirmEmailChangeForm';
import { SettingsPage, type SettingsTab } from './components/SettingsPage';
import { LanguageSwitcher } from './components/LanguageSwitcher';
import { AdminPanel } from './components/AdminPanel';
import { LoadingIndicator } from './components/LoadingIndicator';
import { ErrorBoundary } from './components/ErrorBoundary';
import { ThemeProvider } from './contexts/ThemeContext';
import { ConfirmationProvider } from './contexts/ConfirmationContext';
import { ToastProvider } from './contexts/ToastContext';
import { useDismissConfirm } from './contexts/useConfirm';
import { useTranslation } from 'react-i18next';
import type { User, MigrationConfig, CloudFile } from './types';
import { configureApiClient, apiFetch } from './utils/apiClient';
import { logger } from './utils/logger';
import { configuredApiOrigin } from './utils/runtimeConfig';
import { useAppHistory } from './hooks/useAppHistory';
import { safeAvatarUrl } from './utils/avatar';

function parseSettingsTab(tab: string): SettingsTab {
  if (tab === 'connections' || tab === 'appearance' || tab === 'notifications' || tab === 'about') {
    return tab;
  }
  return 'account';
}
import { resolveFilePath, type FileBreadcrumb } from './api/files';

const FileManager = lazy(() => import('./components/FileManager/FileManager').then((m) => ({ default: m.FileManager })));

function getApiUrl(): string {
  // Production nginx injects a validated runtime origin before this bundle
  // loads. VITE_API_URL remains a development/build-time fallback.
  const configuredOrigin = configuredApiOrigin();
  if (configuredOrigin) return configuredOrigin;
  // Fallback: Dynamically determine the backend API URL.
  // If running behind Nginx or on a non-dev port, use same-origin so requests route through the reverse proxy.
  const protocol = window.location.protocol;
  const hostname = window.location.hostname;
  const port = window.location.port;

  // In standalone local development (e.g. running Vite dev on port 3000 directly without reverse proxy),
  // connect directly to backend API on port 8001.
  if ((hostname === 'localhost' || hostname === '127.0.0.1') && port === '3000') {
    return `${protocol}//${hostname}:8001`;
  }
  // Otherwise, use same origin (e.g. production Nginx, Umbrel app_proxy, custom domain).
  return window.location.origin;
}

const API_URL = getApiUrl();

let nextFormControlId = 0;

// Provider-specific fields mount dynamically; retain native label semantics
// without duplicating IDs across every provider branch.
function associateUnlinkedFormLabels(root: ParentNode) {
  const labels = root instanceof HTMLLabelElement
    ? [root]
    : Array.from(root.querySelectorAll<HTMLLabelElement>('label:not([for])'));
  labels.forEach((label) => {
    if (label.control || label.closest('[role="group"], [role="radiogroup"]')) return;
    const control = label.parentElement?.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>('input:not([type="hidden"]), select, textarea');
    if (!control) return;
    if (!control.id) {
      nextFormControlId += 1;
      control.id = `ui-field-${nextFormControlId}`;
    }
    label.htmlFor = control.id;
  });
}

// Security: warn when the API is reached over plaintext HTTP on a non-loopback
// host, since access tokens and connection credentials would then transit in clear (A04).
if (API_URL.startsWith('http://') && !['localhost', '127.0.0.1'].includes(new URL(API_URL).hostname)) {
  logger.warn('[security] API communication is over plaintext HTTP. Use HTTPS to protect tokens and credentials.');
}

function App() {
  const { t, i18n } = useTranslation();
  const dismissConfirm = useDismissConfirm();
  const mainRef = useRef<HTMLElement>(null);
  useEffect(() => {
    const main = mainRef.current;
    if (!main) return;

    associateUnlinkedFormLabels(main);
    const pendingRoots = new Set<ParentNode>();
    let frameId: number | undefined;
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        Array.from(record.addedNodes).forEach((node) => {
          if (node instanceof Element) pendingRoots.add(node.parentElement ?? node);
        });
      }
      if (frameId !== undefined || pendingRoots.size === 0) return;
      frameId = window.requestAnimationFrame(() => {
        pendingRoots.forEach(associateUnlinkedFormLabels);
        pendingRoots.clear();
        frameId = undefined;
      });
    });
    observer.observe(main, { childList: true, subtree: true });
    return () => {
      observer.disconnect();
      if (frameId !== undefined) window.cancelAnimationFrame(frameId);
    };
  }, []);
  const resetTokenFromUrl = typeof window !== 'undefined'
    ? new URLSearchParams(window.location.search).get('reset-token')
    : null;

  const emailChangeTokenFromUrl = typeof window !== 'undefined'
    ? new URLSearchParams(window.location.search).get('email-change-token')
    : null;

  const hasStoredSession = localStorage.getItem('has_session') === 'true';
  const [token, setToken] = useState<string>('');
  const tokenRef = useRef<string>('');
  const [user, setUser] = useState<User | null>(null);
  const [credentials, setCredentials] = useState<MigrationConfig | null>(null);
  const [initialFiles, setInitialFiles] = useState<CloudFile[]>([]);
  const [fileStart, setFileStart] = useState<{ profileId: string; breadcrumbs: FileBreadcrumb[]; fallback: boolean } | null>(null);
  const resolveAbortControllerRef = useRef<AbortController | null>(null);
  const resolveRequestIdRef = useRef<number>(0);
  const clearCreationState = useCallback(() => {
    setCredentials(null);
    setInitialFiles([]);
  }, []);
  const {
    step,
    migrationId,
    syncId,
    profileId,
    settingsTab,
    initialMigrationId,
    initialSyncId,
    initialProfileId,
    replaceNav,
    navigate,
    goToOverview,
    goBack,
  } = useAppHistory({
    resetToken: resetTokenFromUrl,
    emailChangeToken: emailChangeTokenFromUrl,
    hasStoredSession,
    onLeaveCreationFlow: clearCreationState,
  });
  const [isValidating, setIsValidating] = useState<boolean>(
    () => !resetTokenFromUrl && !emailChangeTokenFromUrl && hasStoredSession
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
      .catch((error: unknown) => {
        logger.debug('Settings fetch failed', error);
      });
  }, []);
  const userMenuRef = useRef<HTMLDivElement>(null);

  const handleLogout = useCallback(async () => {
    try {
      await fetch(`${API_URL}/api/auth/logout`, { method: 'POST', credentials: 'include' });
    } catch (e) {
      logger.error('Logout request failed', e);
    }
    localStorage.removeItem('has_session');
    setToken('');
    setUser(null);
    clearCreationState();
    replaceNav('login', '');
  }, [clearCreationState, replaceNav]);

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
    userMenuRef.current?.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus();
    const handleOutsideClick = (e: MouseEvent) => {
      if (userMenuRef.current && !userMenuRef.current.contains(e.target as Node)) {
        setShowUserMenu(false);
        userMenuButtonRef.current?.focus();
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

            // Confirm the authenticated deep link captured before history seeding.
            if (initialMigrationId) {
              // Verify active migration status
              const migRes = await apiFetch(`${API_URL}/api/migration/${initialMigrationId}`, {
                headers: { 'Authorization': `Bearer ${data.access_token}` },
              });
              if (migRes.ok) {
                replaceNav('dashboard', initialMigrationId);
              } else {
                replaceNav('history', '');
              }
            } else if (initialSyncId) {
              // Verify active sync status
              const syncRes = await apiFetch(`${API_URL}/api/sync/${initialSyncId}`, {
                headers: { 'Authorization': `Bearer ${data.access_token}` },
              });
              if (syncRes.ok) {
                replaceNav('syncdetail', initialSyncId);
              } else {
                replaceNav('history', '');
              }
            } else if (initialProfileId) {
              const profileRes = await apiFetch(`${API_URL}/api/profiles/${initialProfileId}`, {
                headers: { 'Authorization': `Bearer ${data.access_token}` },
              });
              replaceNav(profileRes.ok ? 'files' : 'history', profileRes.ok ? initialProfileId : '');
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
        logger.error('Silent login error', err);
        localStorage.removeItem('has_session');
        replaceNav('login', '');
      })
      .finally(() => {
        setIsValidating(false);
      });
  }, [emailChangeTokenFromUrl, i18n, initialMigrationId, initialProfileId, initialSyncId, replaceNav, resetTokenFromUrl]);

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
          void handleLogout();
        }
      } catch {
        void handleLogout();
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
    clearCreationState();
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
    void handleLogout();
  };

  useEffect(() => {
    return () => {
      resolveAbortControllerRef.current?.abort();
    };
  }, []);

  const handleReset = () => {
    resolveAbortControllerRef.current?.abort();
    resolveRequestIdRef.current += 1;
    clearCreationState();
    goToOverview();
  };

  const openFileManagerAtPath = useCallback(async (nextProfileId: string, path: string) => {
    resolveAbortControllerRef.current?.abort();
    const controller = new AbortController();
    resolveAbortControllerRef.current = controller;
    const reqId = ++resolveRequestIdRef.current;

    const result = await resolveFilePath(API_URL, token, nextProfileId, path, controller.signal);
    if (resolveRequestIdRef.current !== reqId || controller.signal.aborted) {
      return;
    }
    if (result.ok) {
      setFileStart({ profileId: nextProfileId, breadcrumbs: result.data.breadcrumbs, fallback: result.data.fallback });
    } else {
      // A deleted/moved quick-link target should not prevent opening the
      // manager. The resolve handler normally returns the nearest ancestor;
      // this path is only for a transient request failure.
      setFileStart(null);
    }
    navigate('files', nextProfileId);
  }, [navigate, token]);

  const openFileManagerRoot = useCallback((nextProfileId = '') => {
    resolveAbortControllerRef.current?.abort();
    resolveRequestIdRef.current += 1;
    setFileStart(null);
    navigate('files', nextProfileId);
  }, [navigate]);

  const handleBack = () => {
    clearCreationState();
    goBack();
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

  const userAvatar = safeAvatarUrl(user?.avatar);

  return (
    <div className="min-h-screen bg-[var(--color-bg-primary)] text-[var(--color-text-primary)] flex flex-col font-sans relative">
      
      <header className="sticky top-0 z-[var(--layer-sticky)] border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)]">
          <div className="mx-auto flex h-16 max-w-6xl items-center gap-3 px-4 sm:px-6">
          <div className="min-w-0">
            {step !== 'login' ? (
            <button
              type="button"
              onClick={goToOverview}
              aria-label={t('nav.overview')}
              className="flex items-center gap-2 text-lg font-semibold tracking-tight text-[var(--color-text-primary)]"
            >
              <span aria-hidden="true" className="ui-brand-logo h-8 w-8 bg-[var(--color-text-primary)]" />
              Clumoove
            </button>
            ) : (
              <div className="flex items-center gap-2 text-lg font-semibold tracking-tight text-[var(--color-text-primary)]">
                <span aria-hidden="true" className="ui-brand-logo h-8 w-8 bg-[var(--color-text-primary)]" />
                Clumoove
              </div>
            )}
          </div>

          {/* User Section in Header */}
          {user && (
            <div className="relative ml-auto" ref={userMenuRef}>
              <button
                ref={userMenuButtonRef}
                type="button"
                onClick={() => setShowUserMenu((open) => !open)}
                aria-haspopup="menu"
                aria-expanded={showUserMenu}
                aria-controls="user-menu"
                className="flex items-center gap-2 cursor-pointer p-0 text-sm"
              >
                <span className="min-w-0 max-w-36 truncate font-medium text-[var(--color-text-primary)] sm:max-w-56">{user.display_name}</span>
                {userAvatar ? (
                  <img
                    src={userAvatar}
                    className="h-7 w-7 rounded-full object-cover"
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
                  className="absolute right-0 top-full z-[var(--layer-menu)] mt-2 w-48 border border-[var(--color-border)] bg-[var(--color-bg-elevated)] py-1"
                >
                  {user?.role === 'ADMIN' && (
                    <button
                      type="button"
                      role="menuitem"
                      tabIndex={-1}
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
                    tabIndex={-1}
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
                    tabIndex={-1}
                    onClick={() => {
                      void handleLogout();
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

      <main ref={mainRef} className={`mx-auto flex w-full max-w-6xl flex-grow flex-col px-4 py-6 sm:px-6 sm:py-8 ${step === 'login' || step === 'reset-password' || step === 'confirm-email' ? 'justify-center' : 'justify-start'}`}>
        <div key={`${step}:${migrationId}:${syncId}:${profileId}`} className="ui-view-enter w-full">
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
              onOpenFileManager={(id, path) => void openFileManagerAtPath(id, path)}
              onOpenFilemanagerRoot={() => openFileManagerRoot(profileId)}
            />
          )}

          {step === 'files' && (
            <Suspense
              fallback={(
                <div className="flex min-h-[50vh] items-center justify-center p-8">
                  <LoadingIndicator label={t('common.loading')} />
                </div>
              )}
            >
              <FileManager
                apiUrl={API_URL}
                token={token}
                profileId={profileId}
                initialBreadcrumbs={fileStart?.profileId === profileId ? fileStart.breadcrumbs : undefined}
                initialPathFallback={fileStart?.profileId === profileId && fileStart.fallback}
                onProfileChange={openFileManagerRoot}
                onOpenManager={() => navigate('settings', 'connections')}
                onBack={handleBack}
              />
            </Suspense>
          )}

          {step === 'syncdetail' && (
            <ErrorBoundary
              scope="transfer"
              fallback={() => (
                <TransferErrorFallback onBack={handleBack} />
              )}
            >
              <SyncDashboard
                syncId={syncId}
                apiUrl={API_URL}
                onBack={handleBack}
                token={token}
              />
            </ErrorBoundary>
          )}

          {step === 'connect' && (
            <ConnectForm 
              onConnectSuccess={handleConnectSuccess} 
              apiUrl={API_URL} 
              token={token}
              localStorageEnabled={localStorageEnabled}
              oauthProviders={oauthProviders}
              onBack={handleBack}
            />
          )}
          
          {step === 'select' && credentials && (
            <FileBrowser
              initialFiles={initialFiles}
              credentials={credentials}
              apiUrl={API_URL}
              onBack={handleBack}
              onStartSuccess={handleStartSuccess}
              token={token}
            />
          )}
          
          {step === 'dashboard' && (
            <ErrorBoundary
              scope="transfer"
              fallback={() => (
                <TransferErrorFallback onReset={handleReset} />
              )}
            >
              <Dashboard
                migrationId={migrationId}
                apiUrl={API_URL}
                onReset={handleReset}
                token={token}
              />
            </ErrorBoundary>
          )}

          {step === 'settings' && (
            <SettingsPage
              key={`${user?.id}:${settingsTab || 'account'}`}
              apiUrl={API_URL}
              token={token}
              user={user}
              onBack={handleBack}
              onUpdateUser={(updated) => setUser(updated)}
              oauthProviders={oauthProviders}
              localStorageEnabled={localStorageEnabled}
              initialTab={parseSettingsTab(settingsTab)}
            />
          )}

          {step === 'admin' && user?.role === 'ADMIN' && (
            <AdminPanel
              apiUrl={API_URL}
              token={token}
              user={user}
              onBack={handleBack}
            />
          )}
        </div>
      </main>

      {/* Footer */}
      <footer className="mt-auto border-t border-[var(--color-border)] bg-[var(--color-bg-secondary)] py-4">
        <div className="mx-auto flex max-w-6xl items-center justify-end px-4 sm:px-6">
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

type TransferErrorFallbackProps = {
  onReset?: () => void;
  onBack?: () => void;
};

function TransferErrorFallback({ onReset, onBack }: TransferErrorFallbackProps) {
  const { t } = useTranslation();
  return (
    <main className="flex min-h-screen items-center justify-center bg-[var(--color-bg-primary)] p-6 text-[var(--color-text-primary)]">
      <section className="ui-card max-w-md space-y-4 p-6 text-center" role="alert" aria-live="assertive">
        <h1 className="text-xl font-semibold">{t('errorBoundary.transferTitle')}</h1>
        <p className="text-[var(--color-text-secondary)]">{t('errorBoundary.transferDescription')}</p>
        <div className="flex justify-center gap-3">
          <button className="ui-button-primary px-4 py-2" type="button" onClick={() => window.location.reload()}>
            {t('errorBoundary.reload')}
          </button>
          {(onReset || onBack) && (
            <button
              className="ui-button-secondary px-4 py-2"
              type="button"
              onClick={() => (onReset ?? onBack)?.()}
            >
              {t('errorBoundary.backToOverview')}
            </button>
          )}
        </div>
      </section>
    </main>
  );
}
