import { useState, useEffect } from 'react';
import { ConnectForm } from './components/ConnectForm';
import { FileBrowser } from './components/FileBrowser';
import { Dashboard } from './components/Dashboard';
import { CloudLightning } from 'lucide-react';

type Step = 'connect' | 'select' | 'dashboard';

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
  const [step, setStep] = useState<Step>('connect');
  const [credentials, setCredentials] = useState<any>(null);
  const [initialFiles, setInitialFiles] = useState<any[]>([]);
  const [migrationId, setMigrationId] = useState<string>('');
  const [isValidating, setIsValidating] = useState<boolean>(false);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const urlMigId = params.get('migration');
    if (urlMigId) {
      setIsValidating(true);
      fetch(`${API_URL}/api/migration/${urlMigId}`)
        .then((res) => {
          if (res.ok) {
            setMigrationId(urlMigId);
            setStep('dashboard');
          } else {
            setMigrationInUrl('');
          }
        })
        .catch((err) => {
          console.error('Fehler bei der Migration-Validierung:', err);
          // Default to showing the dashboard as a fallback (resilience against temporary network failures)
          setMigrationId(urlMigId);
          setStep('dashboard');
        })
        .finally(() => {
          setIsValidating(false);
        });
    }
  }, []);

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
    setStep('connect');
  };

  if (isValidating) {
    return (
      <div className="min-h-screen bg-portal-bg text-slate-800 flex flex-col items-center justify-center font-sans selection:bg-portal-orange selection:text-white">
        <div className="flex flex-col items-center justify-center gap-4">
          <div className="animate-spin rounded-full h-10 w-10 border-b-2 border-portal-orange"></div>
          <p className="text-xs italic text-slate-500 font-mono tracking-wider">// PRÜFE AKTIVE MIGRATION...</p>
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
            <div className="p-2.5 bg-portal-orange rounded-lg text-white shadow-sm hover:bg-portal-orange-hover transition-colors cursor-pointer">
              <CloudLightning className="w-5 h-5 stroke-[2.5]" />
            </div>
            
            <div className="flex flex-col">
              <span className="font-display font-extrabold text-xl tracking-tight leading-none">
                CloudMove
              </span>
              <span className="text-[9px] font-mono tracking-wider text-slate-350 uppercase mt-1">
                Migrations-Portal
              </span>
            </div>
          </div>
        </div>
      </header>

      {/* Main Structural Body */}
      <main className="flex-grow flex flex-col justify-center px-6 py-10 max-w-5xl w-full mx-auto relative z-10">
        <div className="w-full">
          {step === 'connect' && (
            <ConnectForm onConnectSuccess={handleConnectSuccess} apiUrl={API_URL} />
          )}
          
          {step === 'select' && (
            <FileBrowser
              initialFiles={initialFiles}
              credentials={credentials}
              apiUrl={API_URL}
              onBack={() => setStep('connect')}
              onStartSuccess={handleStartSuccess}
            />
          )}
          
          {step === 'dashboard' && (
            <Dashboard
              migrationId={migrationId}
              apiUrl={API_URL}
              onReset={handleReset}
            />
          )}
        </div>
      </main>

      {/* Footer */}
      <footer className="border-t border-portal-border py-8 mt-12 bg-white">
        <div className="max-w-5xl mx-auto px-6 grid md:grid-cols-2 gap-6 text-[11px] leading-relaxed text-slate-500">
          <div>
            <p className="font-bold text-portal-navy uppercase mb-1.5">// CloudMove Migrations-Plattform</p>
            <p>© 2026 Alle Rechte vorbehalten. Schnelle und sichere Transfers für Cloud-Infrastrukturen.</p>
          </div>
          <div>
            <p className="font-bold text-portal-navy uppercase mb-1.5">// Zero-Data-Retention</p>
            <p>Die Datenübertragung erfolgt verschlüsselt und flüchtig direkt über den Arbeitsspeicher des Gateways. Es findet keine dauerhafte Speicherung statt.</p>
          </div>
        </div>
      </footer>
    </div>
  );
}

export default App;
