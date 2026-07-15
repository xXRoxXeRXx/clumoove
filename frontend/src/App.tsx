import { useState, useEffect, useRef } from 'react';
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
import { CloudSync, LogOut, User as UserIcon, Settings as SettingsIcon, Shield } from 'lucide-react';
import { ThemeProvider } from './contexts/ThemeContext';
import { useTranslation } from 'react-i18next';
import type { User, MigrationConfig, CloudFile } from './types';
import { listenForOAuthMessage } from './utils/oauth';

type Step = 'login' | 'history' | 'connect' | 'select' | 'dashboard' | 'settings' | 'admin' | 'reset-password' | 'confirm-email';

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

let refreshPromise: Promise<string> | null = null;

const setMigrationInUrl = (id: string) => {
  const url = new URL(window.location.href);
  if (id) {
    url.searchParams.set('migration', id);
  } else {
    url.searchParams.delete('migration');
  }
  window.history.replaceState({}, '', url.toString());
};

function App() {
  const { t } = useTranslation();
  const resetTokenFromUrl = typeof window !== 'undefined'
    ? new URLSearchParams(window.location.search).get('reset-token')
    : null;

  const emailChangeTokenFromUrl = typeof window !== 'undefined'
    ? new URLSearchParams(window.location.search).get('email-change-token')
    : null;

  const [step, setStep] = useState<Step>(() =>
    emailChangeTokenFromUrl ? 'confirm-email' : resetTokenFromUrl ? 'reset-password' : 'login'
  );
  const [token, setToken] = useState<string>('');
  const [user, setUser] = useState<User | null>(null);
  const [credentials, setCredentials] = useState<MigrationConfig | null>(null);
  const [initialFiles, setInitialFiles] = useState<CloudFile[]>([]);
  const [migrationId, setMigrationId] = useState<string>('');
  const [isValidating, setIsValidating] = useState<boolean>(
    () => !resetTokenFromUrl && !emailChangeTokenFromUrl && localStorage.getItem('has_session') === 'true'
  );
  const [showUserMenu, setShowUserMenu] = useState<boolean>(false);
  const [resetToken, setResetToken] = useState<string>(resetTokenFromUrl || '');
  const [emailChangeToken, setEmailChangeToken] = useState<string>(emailChangeTokenFromUrl || '');
  const userMenuRef = useRef<HTMLDivElement>(null);

  const handleLogout = async () => {
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
    setMigrationInUrl('');
    setStep('login');
  };

  // Click outside to close user menu
  useEffect(() => {
    const handleOutsideClick = (e: MouseEvent) => {
      if (userMenuRef.current && !userMenuRef.current.contains(e.target as Node)) {
        setShowUserMenu(false);
      }
    };
    if (showUserMenu) {
      document.addEventListener('mousedown', handleOutsideClick);
    }
    return () => {
      document.removeEventListener('mousedown', handleOutsideClick);
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
          const meRes = await fetch(`${API_URL}/api/auth/me`, {
            headers: { 'Authorization': `Bearer ${data.access_token}` },
          });

          if (meRes.ok) {
            const userData = await meRes.json();
            setUser(userData);

            // Check if there is an active migration ID in url
            const params = new URLSearchParams(window.location.search);
            const urlMigId = params.get('migration');
            if (urlMigId) {
              // Verify active migration status
              const migRes = await fetch(`${API_URL}/api/migration/${urlMigId}`, {
                headers: { 'Authorization': `Bearer ${data.access_token}` },
              });
              if (migRes.ok) {
                setMigrationId(urlMigId);
                setStep('dashboard');
              } else {
                setMigrationInUrl('');
                setStep('history');
              }
            } else {
              setStep('history');
            }
          } else {
            localStorage.removeItem('has_session');
            setStep('login');
          }
        } else {
          localStorage.removeItem('has_session');
          setStep('login');
        }
      })
      .catch((err) => {
        console.error('Silent login error:', err);
        localStorage.removeItem('has_session');
        setStep('login');
      })
      .finally(() => {
        setIsValidating(false);
      });
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
          console.log('Access Token refreshed');
        } else {
          handleLogout();
        }
      } catch (e) {
        console.error('Failed silent refresh:', e);
      }
    }, 14 * 60 * 1000); // 14 minutes

    return () => clearInterval(interval);
  }, [token]);

  const handleAuthSuccess = (accessToken: string, loggedUser: User) => {
    localStorage.setItem('has_session', 'true');
    setToken(accessToken);
    setUser(loggedUser);
    setStep('history');
  };

  // OAuth callback page posts tokens to window.opener via postMessage. The
  // receiver validates event.origin against the API origin (M-3) before
  // trusting the token. Tokens are held in memory only, like the password flow.
  useEffect(() => {
    const expectedOrigin = new URL(API_URL).origin;
    return listenForOAuthMessage(expectedOrigin, {
      expectedPurpose: 'login',
      onSuccess: async (msg) => {
        setToken(msg.token);
        try {
          const meRes = await fetch(`${API_URL}/api/auth/me`, {
            headers: { 'Authorization': `Bearer ${msg.token}` },
            credentials: 'include',
          });
          if (meRes.ok) {
            const me = await meRes.json();
            localStorage.setItem('has_session', 'true');
            setUser(me);
            setStep('history');
            return;
          }
        } catch (e) {
          console.error('OAuth login: failed to fetch user:', e);
        }
        handleLogout();
      },
      onError: (msg) => {
        console.error('OAuth login failed:', msg.error);
        setStep('login');
      },
    });
  }, []);

  // Patch global fetch to handle 401 token refresh automatically (I4 frontend fix)
  useEffect(() => {
    const originalFetch = window.fetch;
    window.fetch = async (input, init) => {
      const response = await originalFetch(input, init);

      const url = typeof input === 'string' ? input : (input as Request).url;
      const isAuthRequest = url.includes('/api/auth/login') || url.includes('/api/auth/register') || url.includes('/api/auth/refresh') || url.includes('/api/auth/totp');

      if (response.status === 401 && !isAuthRequest) {
        console.log('401 Unauthorized detected on URL:', url, 'Attempting silent token refresh...');
        try {
          if (!refreshPromise) {
            refreshPromise = (async () => {
              try {
                const refreshRes = await originalFetch(`${API_URL}/api/auth/refresh`, {
                  method: 'POST',
                  credentials: 'include',
                });
                if (refreshRes.ok) {
                  const data = await refreshRes.json();
                  return data.access_token;
                }
                throw new Error('Silent refresh failed');
              } finally {
                setTimeout(() => {
                  refreshPromise = null;
                }, 1000);
              }
            })();
          }

          const newAccessToken = await refreshPromise;
          setToken(newAccessToken);

          // Replay the original request with the refreshed token. Preserve the
          // original init (method + body) — building a fresh init would drop
          // them for non-GET requests. Only inject/override the auth header.
          const replayInit: RequestInit = init ? { ...init } : {};
          const headers = replayInit.headers ? new Headers(replayInit.headers) : new Headers();
          headers.set('Authorization', `Bearer ${newAccessToken}`);
          replayInit.headers = headers;
          return originalFetch(input, replayInit);
        } catch (refreshErr) {
          console.error('Error during automatic token refresh:', refreshErr);
          handleLogout();
        }
      }
      return response;
    };

    return () => {
      window.fetch = originalFetch;
    };
  }, [token]);

  const handleConnectSuccess = (config: MigrationConfig, files: CloudFile[]) => {
    setCredentials(config);
    setInitialFiles(files);
    setStep('select');
  };

  const handleStartSuccess = (id: string) => {
    setMigrationId(id);
    setMigrationInUrl(id);
    // Secrets (source/target passwords, OAuth tokens, SFTP keys) are no longer
    // needed once the migration is created — drop them from memory.
    setCredentials(null);
    setInitialFiles([]);
    setStep('dashboard');
  };

  const handleResetPasswordSuccess = () => {
    // Clean up the URL param and return to login
    const url = new URL(window.location.href);
    url.searchParams.delete('reset-token');
    window.history.replaceState({}, '', url.toString());
    setResetToken('');
    setStep('login');
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
    setMigrationId('');
    setMigrationInUrl('');
    setStep('history');
  };

  if (isValidating) {
    return (
      <div className="min-h-screen bg-[var(--color-bg-primary)] text-[var(--color-text-primary)] flex flex-col items-center justify-center font-sans selection:bg-portal-orange selection:text-white">
        <div className="flex flex-col items-center justify-center gap-6 p-8 glass-panel rounded-2xl shadow-portal border border-[var(--color-glass-border)] max-w-sm w-full mx-4 text-center animate-fade-in">
          <div className="relative">
            <div className="absolute inset-0 bg-portal-orange/20 blur-xl rounded-full animate-pulse-glow" />
            <div className="relative p-4 bg-gradient-to-tr from-portal-navy to-portal-navy-light rounded-2xl text-white shadow-md animate-bounce">
              <CloudSync className="w-8 h-8 stroke-[2.5]" />
            </div>
          </div>
          <div className="space-y-2">
            <h3 className="font-display font-extrabold text-lg text-[var(--color-portal-navy-themed)]">Clumove Portal</h3>
            <div className="flex items-center justify-center gap-2">
              <div className="w-1.5 h-1.5 rounded-full bg-portal-orange animate-bounce [animation-delay:-0.3s]" />
              <div className="w-1.5 h-1.5 rounded-full bg-portal-orange animate-bounce [animation-delay:-0.15s]" />
              <div className="w-1.5 h-1.5 rounded-full bg-portal-orange animate-bounce" />
            </div>
            <p className="text-[10px] font-mono tracking-wider text-[var(--color-text-muted)] uppercase mt-2">{t('common.initializing')}</p>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-[var(--color-bg-primary)] text-[var(--color-text-primary)] flex flex-col font-sans selection:bg-portal-orange selection:text-white relative">
      
      {/* Floating Glassmorphism Header */}
      <header className="sticky top-0 z-50 glass-panel border-b border-[var(--color-border)] backdrop-blur-lg shadow-sm transition-all duration-300">
        <div className="max-w-6xl mx-auto px-6 h-18 flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div 
              onClick={() => step !== 'login' && setStep('history')}
              className="group w-10 h-10 flex items-center justify-center bg-gradient-to-tr from-portal-orange to-orange-500 rounded-xl text-white shadow-sm hover:shadow-md transition-all duration-300 cursor-pointer hover:-translate-y-0.5"
            >
              <CloudSync className="w-5 h-5 stroke-[2.5] group-hover:rotate-12 transition-transform duration-300" />
            </div>
            
            <span className="font-display font-extrabold text-xl tracking-tight leading-none text-[var(--color-portal-navy-themed)] select-none">
              Clumove
            </span>
          </div>

          {/* User Section in Header */}
          {user && (
            <div className="relative" ref={userMenuRef}>
              <div 
                onClick={() => setShowUserMenu(!showUserMenu)}
                className="flex items-center gap-2.5 bg-[var(--color-bg-tertiary)] hover:bg-[var(--color-border)] border border-[var(--color-border)] pl-2.5 pr-4 py-1.5 rounded-full shadow-xs cursor-pointer select-none transition-colors"
              >
                {user.avatar ? (
                  <img 
                    src={user.avatar} 
                    className="w-7 h-7 rounded-full object-cover shadow-xs border border-[var(--color-border)]" 
                    alt={user.display_name}
                  />
                ) : (
                  <div className="w-7 h-7 bg-portal-navy text-white rounded-full flex items-center justify-center shadow-xs">
                    <UserIcon className="w-4 h-4" />
                  </div>
                )}
                <div className="flex flex-col text-left">
                  <span className="font-bold text-[var(--color-text-primary)] leading-tight">{user.display_name}</span>
                  <span className="text-[9px] text-[var(--color-text-muted)] font-mono leading-none mt-0.5">{user.email}</span>
                </div>
              </div>

              {showUserMenu && (
                <div className="absolute right-0 top-full mt-2 w-48 bg-[var(--color-bg-elevated)] backdrop-blur-md border border-[var(--color-border)] rounded-2xl shadow-xl py-1.5 z-50 animate-fade-in">
                  {user?.role === 'ADMIN' && (
                    <button
                      onClick={() => {
                        setStep('admin');
                        setShowUserMenu(false);
                      }}
                      className="w-full flex items-center gap-2 px-3.5 py-2 text-xs font-semibold text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-portal-navy-themed)] transition-colors cursor-pointer text-left font-sans"
                    >
                      <Shield className="w-4 h-4 text-[var(--color-text-muted)]" />
                      {t('nav.admin')}
                    </button>
                  )}
                  <button
                    onClick={() => {
                      setStep('settings');
                      setShowUserMenu(false);
                    }}
                    className="w-full flex items-center gap-2 px-3.5 py-2 text-xs font-semibold text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-portal-navy-themed)] transition-colors cursor-pointer text-left font-sans"
                  >
                    <SettingsIcon className="w-4 h-4 text-[var(--color-text-muted)]" />
                    {t('nav.settings')}
                  </button>
                  <button
                    onClick={() => {
                      handleLogout();
                      setShowUserMenu(false);
                    }}
                    className="w-full flex items-center gap-2 px-3.5 py-2 text-xs font-semibold text-rose-600 hover:bg-rose-50/50 transition-colors cursor-pointer text-left font-sans"
                  >
                    <LogOut className="w-4 h-4 text-rose-450" />
                    {t('nav.logout')}
                  </button>
                </div>
              )}
            </div>
          )}
        </div>
      </header>

      {/* Main Structural Body */}
      <main className="flex-grow flex flex-col justify-center px-6 py-8 max-w-5xl w-full mx-auto relative z-10 animate-slide-up">
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
              onStartNewMigration={() => setStep('connect')}
              onSelectActiveMigration={(id) => {
                setMigrationId(id);
                setMigrationInUrl(id);
                setStep('dashboard');
              }}
            />
          )}

          {step === 'connect' && (
            <ConnectForm 
              onConnectSuccess={handleConnectSuccess} 
              apiUrl={API_URL} 
              token={token}
            />
          )}
          
          {step === 'select' && credentials && (
            <FileBrowser
              initialFiles={initialFiles}
              credentials={credentials}
              apiUrl={API_URL}
              onBack={() => setStep('connect')}
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
              onBack={() => setStep('history')}
              onUpdateUser={(updated) => setUser(updated)}
            />
          )}

          {step === 'admin' && user?.role === 'ADMIN' && (
            <AdminPanel
              apiUrl={API_URL}
              token={token}
              user={user}
              onBack={() => setStep('history')}
            />
          )}
        </div>
      </main>

      {/* Footer */}
      <footer className="border-t border-[var(--color-border)] py-8 mt-12 bg-[var(--color-glass-bg)] backdrop-blur-md">
        <div className="max-w-5xl mx-auto px-6 grid md:grid-cols-2 gap-6 text-[11px] leading-relaxed text-[var(--color-text-muted)]">
          <div>
            <p className="font-bold text-[var(--color-portal-navy-themed)] font-display uppercase tracking-wider mb-1.5">{t('footer.title')}</p>
            <p className="text-[var(--color-text-muted)]">{t('footer.copyright')}</p>
          </div>
          <div className="flex flex-col gap-3">
            <div>
              <p className="font-bold text-[var(--color-portal-navy-themed)] font-display uppercase tracking-wider mb-1.5">{t('footer.bufferTitle')}</p>
              <p className="text-[var(--color-text-muted)]">{t('footer.bufferText')}</p>
            </div>
            <div className="md:flex md:justify-end">
              <LanguageSwitcher />
            </div>
          </div>
        </div>
      </footer>
    </div>
  );
}

// Wrap App with ThemeProvider
function AppWithTheme() {
  return (
    <ThemeProvider>
      <App />
    </ThemeProvider>
  );
}

export default AppWithTheme;
