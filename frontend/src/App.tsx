import { useState, useEffect } from 'react';
import { ConnectForm } from './components/ConnectForm';
import { FileBrowser } from './components/FileBrowser';
import { Dashboard } from './components/Dashboard';
import { AuthForm } from './components/AuthForm';
import { MigrationsDashboard } from './components/MigrationsDashboard';
import { CloudLightning, LogOut, User as UserIcon } from 'lucide-react';

type Step = 'login' | 'history' | 'connect' | 'select' | 'dashboard';

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
  const [step, setStep] = useState<Step>('login');
  const [token, setToken] = useState<string>('');
  const [user, setUser] = useState<any>(null);
  const [credentials, setCredentials] = useState<any>(null);
  const [initialFiles, setInitialFiles] = useState<any[]>([]);
  const [migrationId, setMigrationId] = useState<string>('');
  const [isValidating, setIsValidating] = useState<boolean>(true);

  // 1. Silent login / Refresh Token check on load
  useEffect(() => {
    if (localStorage.getItem('has_session') !== 'true') {
      setStep('login');
      setIsValidating(false);
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
  }, []);

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

  const handleAuthSuccess = (accessToken: string, loggedUser: any) => {
    localStorage.setItem('has_session', 'true');
    setToken(accessToken);
    setUser(loggedUser);
    setStep('history');
  };

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

  const handleConnectSuccess = (config: any, files: any[]) => {
    setCredentials(config);
    setInitialFiles(files);
    setStep('select');
  };

  const handleStartSuccess = (id: string) => {
    setMigrationId(id);
    setMigrationInUrl(id);
    setStep('dashboard');
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
      <div className="min-h-screen bg-portal-bg text-slate-800 flex flex-col items-center justify-center font-sans selection:bg-portal-orange selection:text-white">
        <div className="flex flex-col items-center justify-center gap-4">
          <div className="animate-spin rounded-full h-10 w-10 border-b-2 border-portal-orange"></div>
          <p className="text-xs italic text-slate-500 font-mono tracking-wider">// INITIALISIERE PORTAL...</p>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-portal-bg text-slate-800 flex flex-col font-sans selection:bg-portal-orange selection:text-white relative pb-8">
      
      {/* Deep-Navy Portal Header */}
      <header className="bg-portal-navy text-white sticky top-0 z-50 shadow-md">
        <div className="max-w-6xl mx-auto px-6 h-18 flex items-center justify-between">
          <div className="flex items-center gap-3">
            {/* Clean logo badge with 8px rounded corners */}
            <div 
              onClick={() => step !== 'login' && setStep('history')}
              className="p-2.5 bg-portal-orange rounded-lg text-white shadow-sm hover:bg-portal-orange-hover transition-colors cursor-pointer"
            >
              <CloudLightning className="w-5 h-5 stroke-[2.5]" />
            </div>
            
            <div className="flex flex-col">
              <span className="font-display font-extrabold text-xl tracking-tight leading-none">
                Clumove
              </span>
              <span className="text-[9px] font-mono tracking-wider text-slate-350 uppercase mt-1">
                Migrations-Portal
              </span>
            </div>
          </div>

          {/* User Section in Header */}
          {user && (
            <div className="flex items-center gap-4 text-xs font-mono">
              <div className="flex items-center gap-2 border-r border-slate-700 pr-4 py-1">
                <div className="w-6.5 h-6.5 bg-portal-orange/20 border border-portal-orange text-portal-orange rounded-full flex items-center justify-center font-bold">
                  <UserIcon className="w-3.5 h-3.5" />
                </div>
                <div className="flex flex-col text-left font-sans">
                  <span className="font-bold text-white leading-tight">{user.display_name}</span>
                  <span className="text-[9px] text-slate-400 leading-none mt-0.5">{user.email}</span>
                </div>
              </div>
              
              <button
                onClick={handleLogout}
                className="flex items-center gap-1.5 px-3 py-1.5 border border-slate-700 hover:border-slate-550 rounded-lg hover:bg-slate-800 transition-all font-semibold cursor-pointer text-slate-300 hover:text-white"
              >
                <LogOut className="w-3.5 h-3.5" />
                <span>Abmelden</span>
              </button>
            </div>
          )}
        </div>
      </header>

      {/* Main Structural Body */}
      <main className="flex-grow flex flex-col justify-center px-6 py-10 max-w-5xl w-full mx-auto relative z-10">
        <div className="w-full">
          {step === 'login' && (
            <AuthForm apiUrl={API_URL} onAuthSuccess={handleAuthSuccess} />
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
          
          {step === 'select' && (
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
        </div>
      </main>

      {/* Footer */}
      <footer className="border-t border-portal-border py-8 mt-12 bg-white">
        <div className="max-w-5xl mx-auto px-6 grid md:grid-cols-2 gap-6 text-[11px] leading-relaxed text-slate-500">
          <div>
            <p className="font-bold text-portal-navy uppercase mb-1.5">// Clumove Migrations-Plattform</p>
            <p>© 2026 Alle Rechte vorbehalten. Schnelle und sichere Transfers für Cloud-Infrastrukturen.</p>
          </div>
          <div>
            <p className="font-bold text-portal-navy uppercase mb-1.5">// Zero-Data-Retention-Puffer</p>
            <p>Die Datenübertragung erfolgt verschlüsselt und flüchtig direkt über den Arbeitsspeicher des Gateways. Es findet keine dauerhafte Speicherung der Transferdaten statt.</p>
          </div>
        </div>
      </footer>
    </div>
  );
}

export default App;
