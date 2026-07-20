import { useState, useEffect, useRef, useCallback } from 'react';
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
import { PrivacyPage } from './components/PrivacyPage';
import { TermsPage } from './components/TermsPage';
import { CloudSync, LogOut, User as UserIcon, Settings as SettingsIcon, Shield } from 'lucide-react';
import { ThemeProvider } from './contexts/ThemeContext';
import { useTranslation } from 'react-i18next';
import type { User, MigrationConfig, CloudFile } from './types';
import { listenForOAuthMessage } from './utils/oauth';

type Step = 'login' | 'history' | 'connect' | 'select' | 'dashboard' | 'settings' | 'admin' | 'privacy' | 'terms' | 'reset-password' | 'confirm-email';

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

function App() {
  const { t } = useTranslation();
  const resetTokenFromUrl = typeof window !== 'undefined'
    ? new URLSearchParams(window.location.search).get('reset-token')
    : null;

  const emailChangeTokenFromUrl = typeof window !== 'undefined'
    ? new URLSearchParams(window.location.search).get('email-change-token')
    : null;

  const initialStep: Step = emailChangeTokenFromUrl ? 'confirm-email' : resetTokenFromUrl ? 'reset-password' : 'login';
  const [step, setStep] = useState<Step>(initialStep);
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
  const [localStorageEnabled, setLocalStorageEnabled] = useState<boolean>(false);
  const [oauthProviders, setOauthProviders] = useState<Record<string, boolean>>({});

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
  const applyHistory = (nextStep: Step, migId: string, replace: boolean) => {
    const url = new URL(window.location.href);
    if (migId) {
      url.searchParams.set('migration', migId);
    } else {
      url.searchParams.delete('migration');
    }
    const state = { step: nextStep, migration: migId, appEntry: !replace };
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
    setMigrationId(migId);
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

  // Seed the initial history entry with the current step/migration so the very
  // first entry carries navigable state (replace, not push). Depends only on
  // initialStep (which is also stable) so this runs exactly once on mount.
  useEffect(() => {
    // Seeding history state on mount is intentional; ignore set-state-in-effect.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    applyHistory(initialStep, urlMigId, true);
  // urlMigId is stable (backed by a ref), so this is effectively [initialStep].
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialStep]);

  // Handle browser back/forward between in-app screens.
  useEffect(() => {
    const onPop = (e: PopStateEvent) => {
      const s = e.state as { step?: Step; migration?: string; appEntry?: boolean } | null;
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
        setMigrationId(s.migration ?? new URLSearchParams(window.location.search).get('migration') ?? '');
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
        if (localStorage.getItem('has_session') === 'true' && mig) {
          setMigrationId(mig);
          setStep('dashboard');
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
                replaceNav('dashboard', urlMigId);
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
          console.log('Access Token refreshed');
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
    replaceNav('history', '');
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
            replaceNav('history', '');
            return;
          }
        } catch (e) {
          console.error('OAuth login: failed to fetch user:', e);
        }
        handleLogout();
      },
      onError: (msg) => {
        console.error('OAuth login failed:', msg.error);
        replaceNav('login', '');
      },
    });
    // handleLogout / replaceNav are stable in intent; intentionally not deps.
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
  }, [token, handleLogout]);

  const handleConnectSuccess = (config: MigrationConfig, files: CloudFile[]) => {
    setCredentials(config);
    setInitialFiles(files);
    navigate('select');
  };

  const handleStartSuccess = (id: string) => {
    // Secrets (source/target passwords, OAuth tokens, SFTP keys) are no longer
    // needed once the migration is created — drop them from memory.
    setCredentials(null);
    setInitialFiles([]);
    navigate('dashboard', id);
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
      <div className="min-h-screen bg-[var(--color-bg-primary)] text-[var(--color-text-primary)] flex flex-col items-center justify-center font-sans selection:bg-portal-orange selection:text-white">
        <div className="flex flex-col items-center justify-center gap-6 p-8 glass-panel rounded-2xl shadow-portal border border-[var(--color-glass-border)] max-w-sm w-full mx-4 text-center animate-fade-in">
          <div className="relative">
            <div className="absolute inset-0 bg-portal-orange/20 blur-xl rounded-full animate-pulse-glow" />
            <div className="relative p-4 bg-gradient-to-tr from-portal-navy to-portal-navy-light rounded-2xl text-white shadow-md animate-bounce">
              <CloudSync className="w-8 h-8 stroke-[2.5]" />
            </div>
          </div>
          <div className="space-y-2">
            <h3 className="font-display font-extrabold text-lg text-[var(--color-portal-navy-themed)]">Clumoove Portal</h3>
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
    <div className="min-h-screen bg-[var(--color-bg-primary)] text-[var(--color-text-primary)] flex flex-col font-sans selection:bg-portal-orange selection:text-white relative overflow-x-hidden">

      {/* Full-screen Europa background with servers & data flow */}
      <style>{`
        @keyframes orbit { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
        @keyframes pulse-dot { 0%,100% { opacity:0.3; r:3; } 50% { opacity:1; r:6; } }
        @keyframes data-flow { to { stroke-dashoffset: -200; } }
        @keyframes float { 0%,100% { transform: translateY(0); } 50% { transform: translateY(-8px); } }
        @keyframes fly-right { 0% { transform: translateX(-100px) scale(0.5); opacity:0; } 10% { opacity:1; } 90% { opacity:1; } 100% { transform: translateX(1600px) scale(0.8); opacity:0; } }
        @keyframes fly-right2 { 0% { transform: translateX(-100px) scale(0.4); opacity:0; } 10% { opacity:0.8; } 90% { opacity:0.8; } 100% { transform: translateX(1600px) scale(0.6); opacity:0; } }
        @keyframes fly-right3 { 0% { transform: translateX(-100px) scale(0.6); opacity:0; } 10% { opacity:0.9; } 90% { opacity:0.9; } 100% { transform: translateX(1600px) scale(0.7); opacity:0; } }
        @keyframes fly-left { 0% { transform: translateX(1600px) scale(0.5); opacity:0; } 10% { opacity:1; } 90% { opacity:1; } 100% { transform: translateX(-100px) scale(0.8); opacity:0; } }
        @keyframes fly-left2 { 0% { transform: translateX(1600px) scale(0.4); opacity:0; } 10% { opacity:0.8; } 90% { opacity:0.8; } 100% { transform: translateX(-100px) scale(0.6); opacity:0; } }
        @keyframes fly-up { 0% { transform: translateY(900px) scale(0.4); opacity:0; } 10% { opacity:0.8; } 90% { opacity:0.8; } 100% { transform: translateY(-100px) scale(0.6); opacity:0; } }
        @keyframes fly-down { 0% { transform: translateY(-100px) scale(0.6); opacity:0; } 10% { opacity:0.9; } 90% { opacity:0.9; } 100% { transform: translateY(900px) scale(0.7); opacity:0; } }
      `}</style>
      <div className="fixed inset-0 z-0 pointer-events-none overflow-hidden" aria-hidden="true">
        <svg viewBox="0 0 1440 900" xmlns="http://www.w3.org/2000/svg" className="w-full h-full" preserveAspectRatio="xMidYMid slice">
          <defs>
            <radialGradient id="glow-blue" cx="50%" cy="50%" r="50%">
              <stop offset="0%" stopColor="#1e3a8a" stopOpacity="0.12" />
              <stop offset="100%" stopColor="#1e3a8a" stopOpacity="0" />
            </radialGradient>
            <radialGradient id="glow-yellow" cx="50%" cy="50%" r="50%">
              <stop offset="0%" stopColor="#ffd700" stopOpacity="0.15" />
              <stop offset="100%" stopColor="#ffd700" stopOpacity="0" />
            </radialGradient>
            <radialGradient id="server-pulse" cx="50%" cy="50%" r="50%">
              <stop offset="0%" stopColor="#ffd700" stopOpacity="0.6" />
              <stop offset="100%" stopColor="#ffd700" stopOpacity="0" />
            </radialGradient>
          </defs>

          {/* Ambient glows */}
          <circle cx="200" cy="200" r="300" fill="url(#glow-blue)" />
          <circle cx="1200" cy="700" r="350" fill="url(#glow-blue)" />
          <circle cx="720" cy="100" r="280" fill="url(#glow-yellow)" />
          <circle cx="720" cy="800" r="280" fill="url(#glow-yellow)" />

          {/* Europa landmass (simplified, full map) */}
          <g opacity="0.08" fill="#1e3a8a">
            <path d="M520 150 Q540 130 570 125 Q600 120 630 130 Q660 140 680 150 Q700 160 720 170 Q740 180 750 200 Q760 220 755 240 Q750 260 740 280 Q730 300 710 310 Q690 320 670 325 Q650 330 630 335 Q610 340 590 345 Q570 350 550 360 Q530 370 510 380 Q490 390 480 410 Q470 430 465 450 Q460 470 455 490 Q450 510 440 525 Q430 540 420 550 Q410 560 400 565 Q390 570 380 560 Q370 550 365 530 Q360 510 358 490 Q355 470 360 450 Q365 430 375 410 Q385 390 400 370 Q415 350 430 335 Q445 320 460 305 Q475 290 490 270 Q505 250 515 230 Q525 210 525 190 Q525 170 520 150Z" />
            <path d="M570 125 Q580 110 600 100 Q620 90 640 95 Q660 100 670 115 Q680 130 685 145 Q690 160 680 170 Q670 180 655 185 Q640 190 625 185 Q610 180 600 165 Q590 150 580 140 Q570 130 570 125Z" />
            <path d="M750 200 Q770 190 790 195 Q810 200 820 220 Q830 240 825 260 Q820 280 810 295 Q800 310 780 315 Q760 320 750 310 Q740 300 745 280 Q750 260 750 240 Q750 220 750 200Z" />
            <path d="M520 40 Q540 30 560 35 Q580 40 590 60 Q600 80 595 100 Q590 120 580 130 Q570 140 555 135 Q540 130 535 110 Q530 90 528 70 Q526 50 520 40Z" opacity="0.7" />
            <path d="M590 35 Q610 25 630 30 Q650 35 655 55 Q660 75 655 90 Q650 105 640 115 Q630 125 615 120 Q600 115 595 95 Q590 75 592 55 Q594 35 590 35Z" opacity="0.6" />
            <path d="M450 150 Q460 140 475 145 Q490 150 495 170 Q500 190 495 210 Q490 230 480 240 Q470 250 460 245 Q450 240 448 220 Q445 200 448 180 Q450 160 450 150Z" opacity="0.75" />
            <path d="M420 370 Q435 360 455 365 Q475 370 480 390 Q485 410 478 430 Q470 450 455 460 Q440 470 425 465 Q410 460 405 440 Q400 420 405 400 Q410 380 420 370Z" opacity="0.75" />
            <path d="M590 345 Q600 340 610 345 Q620 350 625 370 Q630 390 632 410 Q635 430 632 450 Q630 470 620 480 Q610 490 600 485 Q590 480 588 460 Q585 440 585 420 Q585 400 587 380 Q590 360 590 345Z" opacity="0.75" />
            <path d="M650 325 Q670 320 690 330 Q710 340 720 360 Q730 380 728 400 Q725 420 715 435 Q705 450 690 455 Q675 460 660 455 Q645 450 640 430 Q635 410 638 390 Q640 370 645 350 Q650 330 650 325Z" opacity="0.7" />
            <path d="M680 170 Q710 160 740 165 Q770 170 790 190 Q810 210 815 240 Q820 270 810 300 Q800 330 780 345 Q760 360 735 355 Q710 350 695 335 Q680 320 675 300 Q670 280 670 260 Q670 240 672 220 Q675 200 680 180Z" opacity="0.65" />
          </g>

          {/* Server/Data Centre markers with pulsing animation */}
          <g>
            <circle cx="485" cy="200" r="18" fill="url(#server-pulse)">
              <animate attributeName="r" values="12;22;12" dur="2s" repeatCount="indefinite" />
            </circle>
            <circle cx="485" cy="200" r="4" fill="#ffd700">
              <animate attributeName="r" values="3;6;3" dur="2s" repeatCount="indefinite" />
            </circle>
            <text x="475" y="188" fontSize="10" fill="#ffd700" fontWeight="bold" fontFamily="monospace">LON</text>

            <circle cx="600" cy="130" r="18" fill="url(#server-pulse)">
              <animate attributeName="r" values="12;22;12" dur="2.5s" repeatCount="indefinite" />
            </circle>
            <circle cx="600" cy="130" r="4" fill="#ffd700">
              <animate attributeName="r" values="3;6;3" dur="2.5s" repeatCount="indefinite" />
            </circle>
            <text x="590" y="118" fontSize="10" fill="#ffd700" fontWeight="bold" fontFamily="monospace">AMS</text>

            <circle cx="700" cy="170" r="18" fill="url(#server-pulse)">
              <animate attributeName="r" values="12;22;12" dur="1.8s" repeatCount="indefinite" />
            </circle>
            <circle cx="700" cy="170" r="4" fill="#ffd700">
              <animate attributeName="r" values="3;6;3" dur="1.8s" repeatCount="indefinite" />
            </circle>
            <text x="690" y="158" fontSize="10" fill="#ffd700" fontWeight="bold" fontFamily="monospace">FRA</text>

            <circle cx="750" cy="270" r="18" fill="url(#server-pulse)">
              <animate attributeName="r" values="12;22;12" dur="3s" repeatCount="indefinite" />
            </circle>
            <circle cx="750" cy="270" r="4" fill="#ffd700">
              <animate attributeName="r" values="3;6;3" dur="3s" repeatCount="indefinite" />
            </circle>
            <text x="740" y="258" fontSize="10" fill="#ffd700" fontWeight="bold" fontFamily="monospace">FRA2</text>

            <circle cx="600" cy="330" r="18" fill="url(#server-pulse)">
              <animate attributeName="r" values="12;22;12" dur="2.2s" repeatCount="indefinite" />
            </circle>
            <circle cx="600" cy="330" r="4" fill="#ffd700">
              <animate attributeName="r" values="3;6;3" dur="2.2s" repeatCount="indefinite" />
            </circle>
            <text x="590" y="318" fontSize="10" fill="#ffd700" fontWeight="bold" fontFamily="monospace">MIL</text>

            <circle cx="440" cy="430" r="18" fill="url(#server-pulse)">
              <animate attributeName="r" values="12;22;12" dur="2.7s" repeatCount="indefinite" />
            </circle>
            <circle cx="440" cy="430" r="4" fill="#ffd700">
              <animate attributeName="r" values="3;6;3" dur="2.7s" repeatCount="indefinite" />
            </circle>
            <text x="430" y="418" fontSize="10" fill="#ffd700" fontWeight="bold" fontFamily="monospace">MAD</text>

            <circle cx="695" cy="370" r="18" fill="url(#server-pulse)">
              <animate attributeName="r" values="12;22;12" dur="2s" repeatCount="indefinite" />
            </circle>
            <circle cx="695" cy="370" r="4" fill="#ffd700">
              <animate attributeName="r" values="3;6;3" dur="2s" repeatCount="indefinite" />
            </circle>
            <text x="685" y="358" fontSize="10" fill="#ffd700" fontWeight="bold" fontFamily="monospace">VIE</text>

            <circle cx="790" cy="310" r="18" fill="url(#server-pulse)">
              <animate attributeName="r" values="12;22;12" dur="2.8s" repeatCount="indefinite" />
            </circle>
            <circle cx="790" cy="310" r="4" fill="#ffd700">
              <animate attributeName="r" values="3;6;3" dur="2.8s" repeatCount="indefinite" />
            </circle>
            <text x="780" y="298" fontSize="10" fill="#ffd700" fontWeight="bold" fontFamily="monospace">WAW</text>

            <circle cx="540" cy="90" r="18" fill="url(#server-pulse)">
              <animate attributeName="r" values="12;22;12" dur="2.3s" repeatCount="indefinite" />
            </circle>
            <circle cx="540" cy="90" r="4" fill="#ffd700">
              <animate attributeName="r" values="3;6;3" dur="2.3s" repeatCount="indefinite" />
            </circle>
            <text x="530" y="78" fontSize="10" fill="#ffd700" fontWeight="bold" fontFamily="monospace">CPH</text>

            <circle cx="650" cy="260" r="18" fill="url(#server-pulse)">
              <animate attributeName="r" values="12;22;12" dur="1.9s" repeatCount="indefinite" />
            </circle>
            <circle cx="650" cy="260" r="4" fill="#ffd700">
              <animate attributeName="r" values="3;6;3" dur="1.9s" repeatCount="indefinite" />
            </circle>
            <text x="640" y="248" fontSize="10" fill="#ffd700" fontWeight="bold" fontFamily="monospace">ZRH</text>

            {/* Extra data flow server markers */}
            <circle cx="800" cy="150" r="12" fill="url(#server-pulse)">
              <animate attributeName="r" values="8;16;8" dur="3.2s" repeatCount="indefinite" />
            </circle>
            <circle cx="800" cy="150" r="3" fill="#ffd700">
              <animate attributeName="r" values="2;5;2" dur="3.2s" repeatCount="indefinite" />
            </circle>
            <circle cx="500" cy="300" r="12" fill="url(#server-pulse)">
              <animate attributeName="r" values="8;16;8" dur="2.6s" repeatCount="indefinite" />
            </circle>
            <circle cx="500" cy="300" r="3" fill="#ffd700">
              <animate attributeName="r" values="2;5;2" dur="2.6s" repeatCount="indefinite" />
            </circle>
            <circle cx="680" cy="450" r="12" fill="url(#server-pulse)">
              <animate attributeName="r" values="8;16;8" dur="3.5s" repeatCount="indefinite" />
            </circle>
            <circle cx="680" cy="450" r="3" fill="#ffd700">
              <animate attributeName="r" values="2;5;2" dur="3.5s" repeatCount="indefinite" />
            </circle>
          </g>

          {/* Data flow lines with animated dash offset */}
          <g opacity="0.15">
            <line x1="485" y1="200" x2="600" y2="130" stroke="url(#glow-yellow)" strokeWidth="2" strokeDasharray="8,6">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="3s" repeatCount="indefinite" />
            </line>
            <line x1="600" y1="130" x2="700" y2="170" stroke="url(#glow-yellow)" strokeWidth="2" strokeDasharray="8,6">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="2.5s" repeatCount="indefinite" />
            </line>
            <line x1="700" y1="170" x2="750" y2="270" stroke="url(#glow-yellow)" strokeWidth="2" strokeDasharray="8,6">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="4s" repeatCount="indefinite" />
            </line>
            <line x1="600" y1="330" x2="695" y2="370" stroke="url(#glow-yellow)" strokeWidth="2" strokeDasharray="8,6">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="3.5s" repeatCount="indefinite" />
            </line>
            <line x1="650" y1="260" x2="600" y2="330" stroke="url(#glow-yellow)" strokeWidth="2" strokeDasharray="8,6">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="2.8s" repeatCount="indefinite" />
            </line>
            <line x1="750" y1="270" x2="695" y2="370" stroke="url(#glow-yellow)" strokeWidth="2" strokeDasharray="8,6">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="3.2s" repeatCount="indefinite" />
            </line>
            <line x1="750" y1="270" x2="790" y2="310" stroke="url(#glow-yellow)" strokeWidth="2" strokeDasharray="8,6">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="2.2s" repeatCount="indefinite" />
            </line>
            <line x1="440" y1="430" x2="600" y2="330" stroke="url(#glow-yellow)" strokeWidth="2" strokeDasharray="8,6">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="4.5s" repeatCount="indefinite" />
            </line>
            <line x1="700" y1="170" x2="790" y2="310" stroke="url(#glow-yellow)" strokeWidth="2" strokeDasharray="8,6">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="3.8s" repeatCount="indefinite" />
            </line>
            <line x1="600" y1="130" x2="540" y2="90" stroke="url(#glow-yellow)" strokeWidth="2" strokeDasharray="8,6">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="2s" repeatCount="indefinite" />
            </line>
            <line x1="650" y1="260" x2="700" y2="170" stroke="url(#glow-yellow)" strokeWidth="2" strokeDasharray="8,6">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="3s" repeatCount="indefinite" />
            </line>
            <line x1="800" y1="150" x2="700" y2="170" stroke="url(#glow-yellow)" strokeWidth="1.5" strokeDasharray="6,8">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="3.3s" repeatCount="indefinite" />
            </line>
            <line x1="500" y1="300" x2="600" y2="330" stroke="url(#glow-yellow)" strokeWidth="1.5" strokeDasharray="6,8">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="2.7s" repeatCount="indefinite" />
            </line>
            <line x1="680" y1="450" x2="600" y2="330" stroke="url(#glow-yellow)" strokeWidth="1.5" strokeDasharray="6,8">
              <animate attributeName="stroke-dashoffset" from="0" to="-200" dur="4.2s" repeatCount="indefinite" />
            </line>
          </g>

          {/* Professional tech infrastructure icons */}
          <g opacity="0.12" fill="none" stroke="#ffd700" strokeWidth="1.5">
            {/* Database cluster */}
            <g transform="translate(160,260)">
              <ellipse cx="0" cy="-6" rx="8" ry="3" />
              <path d="M-8,-6 L-8,6 Q-8,9 0,9 Q8,9 8,6 L8,-6" />
              <path d="M-8,-2 Q-8,1 0,1 Q8,1 8,-2" strokeWidth="1" />
              <circle cx="-3" cy="4" r="1" fill="#ffd700" stroke="none" />
              <circle cx="0" cy="4" r="1" fill="#ffd700" stroke="none" />
              <circle cx="3" cy="4" r="1" fill="#ffd700" stroke="none" />
            </g>
            <g transform="translate(1080,200)">
              <ellipse cx="0" cy="-6" rx="8" ry="3" />
              <path d="M-8,-6 L-8,6 Q-8,9 0,9 Q8,9 8,6 L8,-6" />
              <path d="M-8,-2 Q-8,1 0,1 Q8,1 8,-2" strokeWidth="1" />
              <circle cx="-3" cy="4" r="1" fill="#ffd700" stroke="none" />
              <circle cx="0" cy="4" r="1" fill="#ffd700" stroke="none" />
              <circle cx="3" cy="4" r="1" fill="#ffd700" stroke="none" />
            </g>

            {/* Network switch / router */}
            <g transform="translate(300,430)">
              <rect x="-10" y="-6" width="20" height="12" rx="2" />
              <circle cx="-5" cy="0" r="1.5" fill="#ffd700" stroke="none" />
              <circle cx="0" cy="0" r="1.5" fill="#ffd700" stroke="none" />
              <circle cx="5" cy="0" r="1.5" fill="#ffd700" stroke="none" />
              <line x1="-3" y1="-3" x2="3" y2="-3" strokeWidth="1" />
              <line x1="-3" y1="3" x2="3" y2="3" strokeWidth="1" />
            </g>
            <g transform="translate(1180,370)">
              <rect x="-10" y="-6" width="20" height="12" rx="2" />
              <circle cx="-5" cy="0" r="1.5" fill="#ffd700" stroke="none" />
              <circle cx="0" cy="0" r="1.5" fill="#ffd700" stroke="none" />
              <circle cx="5" cy="0" r="1.5" fill="#ffd700" stroke="none" />
              <line x1="-3" y1="-3" x2="3" y2="-3" strokeWidth="1" />
              <line x1="-3" y1="3" x2="3" y2="3" strokeWidth="1" />
            </g>

            {/* Cloud with arrows up/down (sync) */}
            <g transform="translate(140,560)">
              <path d="M-9,2 Q-9,-3 -5,-4 Q-4,-8 1,-8 Q6,-8 7,-4 Q10,-3 9,2 Q10,5 7,6 L-6,6 Q-10,5 -9,2Z" />
              <path d="M-2,-14 L-2,-2" strokeWidth="1.5" />
              <polygon points="-4,-12 -2,-15 0,-12" />
              <path d="M5,-14 L5,-2" strokeWidth="1.5" />
              <polygon points="3,-12 5,-15 7,-12" />
            </g>
            <g transform="translate(920,720)">
              <path d="M-9,2 Q-9,-3 -5,-4 Q-4,-8 1,-8 Q6,-8 7,-4 Q10,-3 9,2 Q10,5 7,6 L-6,6 Q-10,5 -9,2Z" />
              <path d="M-2,-14 L-2,-2" strokeWidth="1.5" />
              <polygon points="-4,-12 -2,-15 0,-12" />
              <path d="M5,-14 L5,-2" strokeWidth="1.5" />
              <polygon points="3,-12 5,-15 7,-12" />
            </g>
            <g transform="translate(1240,120)">
              <path d="M-9,2 Q-9,-3 -5,-4 Q-4,-8 1,-8 Q6,-8 7,-4 Q10,-3 9,2 Q10,5 7,6 L-6,6 Q-10,5 -9,2Z" />
              <path d="M-2,-14 L-2,-2" strokeWidth="1.5" />
              <polygon points="-4,-12 -2,-15 0,-12" />
              <path d="M5,-14 L5,-2" strokeWidth="1.5" />
              <polygon points="3,-12 5,-15 7,-12" />
            </g>

            {/* Server rack */}
            <g transform="translate(100,160)">
              <rect x="-7" y="-10" width="14" height="20" rx="1.5" />
              <rect x="-5" y="-8" width="10" height="3" rx="0.5" fill="#ffd700" opacity="0.15" stroke="none" />
              <rect x="-5" y="-3" width="10" height="3" rx="0.5" fill="#ffd700" opacity="0.15" stroke="none" />
              <rect x="-5" y="2" width="10" height="3" rx="0.5" fill="#ffd700" opacity="0.15" stroke="none" />
              <rect x="-5" y="7" width="10" height="3" rx="0.5" fill="#ffd700" opacity="0.15" stroke="none" />
              <circle cx="4" cy="8.5" r="0.8" fill="#ffd700" stroke="none" />
            </g>
            <g transform="translate(1360,520)">
              <rect x="-7" y="-10" width="14" height="20" rx="1.5" />
              <rect x="-5" y="-8" width="10" height="3" rx="0.5" fill="#ffd700" opacity="0.15" stroke="none" />
              <rect x="-5" y="-3" width="10" height="3" rx="0.5" fill="#ffd700" opacity="0.15" stroke="none" />
              <rect x="-5" y="2" width="10" height="3" rx="0.5" fill="#ffd700" opacity="0.15" stroke="none" />
              <rect x="-5" y="7" width="10" height="3" rx="0.5" fill="#ffd700" opacity="0.15" stroke="none" />
              <circle cx="4" cy="8.5" r="0.8" fill="#ffd700" stroke="none" />
            </g>

            {/* Globe with network nodes */}
            <g transform="translate(420,130)">
              <circle cx="0" cy="0" r="9" />
              <ellipse cx="0" cy="0" rx="4.5" ry="9" strokeWidth="1" />
              <line x1="-9" y1="0" x2="9" y2="0" strokeWidth="1" />
              <circle cx="-12" cy="-4" r="1.5" fill="#ffd700" stroke="none" />
              <circle cx="12" cy="4" r="1.5" fill="#ffd700" stroke="none" />
              <line x1="-12" y1="-4" x2="-3" y2="-1" strokeWidth="0.8" />
              <line x1="12" y1="4" x2="3" y2="1" strokeWidth="0.8" />
            </g>
            <g transform="translate(1120,520)">
              <circle cx="0" cy="0" r="9" />
              <ellipse cx="0" cy="0" rx="4.5" ry="9" strokeWidth="1" />
              <line x1="-9" y1="0" x2="9" y2="0" strokeWidth="1" />
              <circle cx="-12" cy="-4" r="1.5" fill="#ffd700" stroke="none" />
              <circle cx="12" cy="4" r="1.5" fill="#ffd700" stroke="none" />
              <line x1="-12" y1="-4" x2="-3" y2="-1" strokeWidth="0.8" />
              <line x1="12" y1="4" x2="3" y2="1" strokeWidth="0.8" />
            </g>

            {/* Shield / security */}
            <g transform="translate(720,720)">
              <path d="M-8,0 L-8,-8 L0,-11 L8,-8 L8,0 Q8,6 0,9 Q-8,6 -8,0Z" />
              <polyline points="-4,-3 0,1 4,-4" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
            </g>
            <g transform="translate(50,410)">
              <path d="M-8,0 L-8,-8 L0,-11 L8,-8 L8,0 Q8,6 0,9 Q-8,6 -8,0Z" />
              <polyline points="-4,-3 0,1 4,-4" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
            </g>

            {/* Data flow node */}
            <g transform="translate(350,770)">
              <circle cx="0" cy="0" r="4" />
              <path d="M-12,-6 Q-6,-10 0,-8 Q6,-6 12,-8" strokeWidth="1" />
              <path d="M-12,6 Q-6,10 0,8 Q6,6 12,8" strokeWidth="1" />
            </g>
            <g transform="translate(1280,660)">
              <circle cx="0" cy="0" r="4" />
              <path d="M-12,-6 Q-6,-10 0,-8 Q6,-6 12,-8" strokeWidth="1" />
              <path d="M-12,6 Q-6,10 0,8 Q6,6 12,8" strokeWidth="1" />
            </g>
          </g>

          {/* Orbiting data packets */}
          <g opacity="0.12">
            <circle cx="540" cy="90" r="2" fill="#ffd700">
              <animateMotion dur="8s" repeatCount="indefinite" path="M540,90 Q600,130 700,170 Q800,150 790,310 Q750,270 700,170 Q600,130 540,90" />
            </circle>
            <circle cx="750" cy="270" r="2" fill="#ffd700">
              <animateMotion dur="10s" repeatCount="indefinite" path="M750,270 Q790,310 695,370 Q600,330 650,260 Q700,170 750,270" />
            </circle>
            <circle cx="600" cy="330" r="2" fill="#ffd700">
              <animateMotion dur="12s" repeatCount="indefinite" path="M600,330 Q440,430 500,300 Q600,130 650,260 Q695,370 600,330" />
            </circle>
            <circle cx="700" cy="170" r="2" fill="#ffd700">
              <animateMotion dur="9s" repeatCount="indefinite" path="M700,170 Q650,260 600,330 Q695,370 750,270 Q790,310 800,150 Q700,170" />
            </circle>
          </g>

          {/* Flying file icons between servers — multi-directional migration traffic */}
          <g>
            {/* Document → right */}
            <g style={{animation: 'fly-right 10s ease-in-out infinite'}} opacity="0.3">
              <rect x="0" y="0" width="18" height="22" rx="2" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="5" y1="6" x2="13" y2="6" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="5" y1="10" x2="13" y2="10" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="5" y1="14" x2="11" y2="14" stroke="#ffd700" strokeWidth="1.5" />
            </g>
            {/* Video → right */}
            <g style={{animation: 'fly-right2 12s ease-in-out infinite 2s'}} opacity="0.3">
              <rect x="0" y="0" width="20" height="16" rx="2" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <polygon points="8,4 16,8 8,12" fill="#ffd700" opacity="0.5" />
            </g>
            {/* Table → right */}
            <g style={{animation: 'fly-right3 14s ease-in-out infinite 4s'}} opacity="0.3">
              <rect x="0" y="0" width="18" height="18" rx="2" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="0" y1="6" x2="18" y2="6" stroke="#ffd700" strokeWidth="1" />
              <line x1="0" y1="12" x2="18" y2="12" stroke="#ffd700" strokeWidth="1" />
              <line x1="6" y1="0" x2="6" y2="18" stroke="#ffd700" strokeWidth="1" />
              <line x1="12" y1="0" x2="12" y2="18" stroke="#ffd700" strokeWidth="1" />
            </g>
            {/* Folder → right */}
            <g style={{animation: 'fly-right 15s ease-in-out infinite 6s'}} opacity="0.3">
              <path d="M0,18 L0,4 Q0,2 2,2 L7,2 L9,4 L18,4 Q20,4 20,6 L20,18 Q20,20 18,20 L2,20 Q0,20 0,18Z" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="4" y1="10" x2="16" y2="10" stroke="#ffd700" strokeWidth="1" />
              <line x1="4" y1="14" x2="14" y2="14" stroke="#ffd700" strokeWidth="1" />
            </g>
            {/* Image → left */}
            <g style={{animation: 'fly-left 11s ease-in-out infinite 1s'}} opacity="0.25">
              <rect x="0" y="0" width="18" height="18" rx="2" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <circle cx="6" cy="6" r="3" fill="#ffd700" opacity="0.4" />
              <polygon points="4,14 10,8 14,14" fill="none" stroke="#ffd700" strokeWidth="1" />
            </g>
            {/* Spreadsheet → left */}
            <g style={{animation: 'fly-left2 13s ease-in-out infinite 3s'}} opacity="0.25">
              <rect x="0" y="0" width="18" height="22" rx="2" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="3" y1="6" x2="15" y2="6" stroke="#ffd700" strokeWidth="1" />
              <line x1="3" y1="10" x2="15" y2="10" stroke="#ffd700" strokeWidth="1" />
              <line x1="3" y1="14" x2="15" y2="14" stroke="#ffd700" strokeWidth="1" />
              <line x1="3" y1="18" x2="11" y2="18" stroke="#ffd700" strokeWidth="1" />
              <rect x="9" y="0" width="1" height="22" fill="#ffd700" opacity="0.3" />
            </g>
            {/* Archive → up */}
            <g style={{animation: 'fly-up 16s ease-in-out infinite 5s'}} opacity="0.2">
              <rect x="0" y="0" width="20" height="16" rx="2" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <path d="M2,16 L2,6 Q2,4 4,4 L16,4 Q18,4 18,6 L18,16" fill="none" stroke="#ffd700" strokeWidth="1" />
              <line x1="5" y1="10" x2="15" y2="10" stroke="#ffd700" strokeWidth="1" />
            </g>
            {/* Code file → down */}
            <g style={{animation: 'fly-down 14s ease-in-out infinite 7s'}} opacity="0.2">
              <rect x="0" y="0" width="18" height="22" rx="2" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <polyline points="5,8 8,11 5,14" fill="none" stroke="#ffd700" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
              <polyline points="13,8 10,11 13,14" fill="none" stroke="#ffd700" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
            </g>
            {/* Database → up */}
            <g style={{animation: 'fly-up 18s ease-in-out infinite 2s'}} opacity="0.2">
              <ellipse cx="9" cy="4" rx="8" ry="3" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <path d="M1,4 L1,14 Q1,17 9,17 Q17,17 17,14 L17,4" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <path d="M1,9 Q1,12 9,12 Q17,12 17,9" fill="none" stroke="#ffd700" strokeWidth="1" />
            </g>
            {/* Document → down */}
            <g style={{animation: 'fly-down 12s ease-in-out infinite 4s'}} opacity="0.25">
              <rect x="0" y="0" width="16" height="20" rx="2" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="4" y1="6" x2="12" y2="6" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="4" y1="10" x2="12" y2="10" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="4" y1="14" x2="10" y2="14" stroke="#ffd700" strokeWidth="1.5" />
            </g>
          </g>
          {/* Additional right-bound migration traffic */}
          <g>
            <g style={{animation: 'fly-right2 11s ease-in-out infinite 8s'}} opacity="0.3">
              <rect x="0" y="0" width="16" height="20" rx="2" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="4" y1="5" x2="12" y2="5" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="4" y1="9" x2="12" y2="9" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="4" y1="13" x2="10" y2="13" stroke="#ffd700" strokeWidth="1.5" />
            </g>
            {/* Video icon flying MAD→VIE */}
            <g style={{animation: 'fly-right3 13s ease-in-out infinite 10s'}} opacity="0.3">
              <rect x="0" y="0" width="18" height="14" rx="2" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <polygon points="7,3 14,7 7,11" fill="#ffd700" opacity="0.5" />
            </g>
            {/* Archive icon flying VIE→WAW */}
            <g style={{animation: 'fly-right 16s ease-in-out infinite 12s'}} opacity="0.3">
              <path d="M0,16 L0,3 Q0,1 2,1 L6,1 L8,3 L16,3 Q18,3 18,5 L18,16 Q18,18 16,18 L2,18 Q0,18 0,16Z" fill="none" stroke="#ffd700" strokeWidth="1.5" />
              <line x1="3" y1="9" x2="15" y2="9" stroke="#ffd700" strokeWidth="1" />
              <line x1="3" y1="13" x2="12" y2="13" stroke="#ffd700" strokeWidth="1" />
            </g>
          </g>
        </svg>
      </div>

      {/* Floating Glassmorphism Header */}
      <header className="sticky top-0 z-50 glass-panel border-b border-[var(--color-border)] backdrop-blur-lg shadow-sm transition-all duration-300">
        <div className="max-w-6xl mx-auto px-6 h-18 flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div 
              onClick={() => step !== 'login' && goToOverview()}
              className="group w-10 h-10 flex items-center justify-center bg-gradient-to-tr from-portal-orange to-yellow-500 rounded-xl text-portal-navy shadow-sm hover:shadow-md transition-all duration-300 cursor-pointer hover:-translate-y-0.5"
            >
              <CloudSync className="w-5 h-5 stroke-[2.5] group-hover:rotate-12 transition-transform duration-300" />
            </div>
            
            <span className="font-display font-extrabold text-xl tracking-tight leading-none text-[var(--color-portal-navy-themed)] select-none">
              Clumoove
            </span>
          </div>

          {/* User Section in Header */}
          {user && (
            <div className="relative" ref={userMenuRef}>
              <div 
                onClick={() => setShowUserMenu(!showUserMenu)}
                className="flex items-center gap-2.5 pl-4 pr-2.5 py-1.5 rounded-full shadow-xs cursor-pointer select-none transition-colors"
              >
                <span className="font-bold text-[var(--color-text-primary)] leading-tight">{user.display_name}</span>
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
              </div>

              {showUserMenu && (
                <div className="absolute right-0 top-full mt-2 w-48 bg-[var(--color-bg-elevated)] backdrop-blur-md border border-[var(--color-border)] rounded-2xl shadow-xl py-1.5 z-50 animate-fade-in">
                  {user?.role === 'ADMIN' && (
                    <button
                      onClick={() => {
                        navigate('admin');
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
                        navigate('settings');
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
              onStartNewMigration={() => navigate('connect')}
              onSelectActiveMigration={(id) => {
                navigate('dashboard', id);
              }}
            />
          )}

          {step === 'connect' && (
            <ConnectForm 
              onConnectSuccess={handleConnectSuccess} 
              apiUrl={API_URL} 
              token={token}
              localStorageEnabled={localStorageEnabled}
              oauthProviders={oauthProviders}
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

          {step === 'privacy' && (
            <PrivacyPage onBack={() => goBack()} />
          )}

          {step === 'terms' && (
            <TermsPage onBack={() => goBack()} />
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
            <div className="flex flex-wrap items-center gap-4 md:justify-end">
              <button
                onClick={() => navigate('privacy')}
                className="text-[11px] font-semibold uppercase tracking-wider text-[var(--color-text-muted)] hover:text-[var(--color-portal-navy-themed)] transition-colors cursor-pointer"
              >
                Privacy Policy
              </button>
              <button
                onClick={() => navigate('terms')}
                className="text-[11px] font-semibold uppercase tracking-wider text-[var(--color-text-muted)] hover:text-[var(--color-portal-navy-themed)] transition-colors cursor-pointer"
              >
                Terms of Service
              </button>
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
