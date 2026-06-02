import { useState } from 'react';
import { ConnectForm } from './components/ConnectForm';
import { FileBrowser } from './components/FileBrowser';
import { Dashboard } from './components/Dashboard';
import { CloudLightning, Info } from 'lucide-react';

type Step = 'connect' | 'select' | 'dashboard';

const getApiUrl = () => {
  const envUrl = import.meta.env.VITE_API_URL;
  // If the env variable is set and NOT pointing to localhost/127.0.0.1, use it.
  // Otherwise, dynamically determine it based on the browser address.
  if (envUrl && !envUrl.includes('localhost') && !envUrl.includes('127.0.0.1')) {
    return envUrl;
  }
  // Fallback: Dynamically determine the backend API URL on port 8001 using browser hostname
  const protocol = window.location.protocol;
  const hostname = window.location.hostname;
  return `${protocol}//${hostname}:8001`;
};

const API_URL = getApiUrl();

function App() {
  const [step, setStep] = useState<Step>('connect');
  const [credentials, setCredentials] = useState<any>(null);
  const [initialFiles, setInitialFiles] = useState<any[]>([]);
  const [migrationId, setMigrationId] = useState<string>('');

  const handleConnectSuccess = (config: any, files: any[]) => {
    setCredentials(config);
    setInitialFiles(files);
    setStep('select');
  };

  const handleStartSuccess = (id: string) => {
    setMigrationId(id);
    setStep('dashboard');
  };

  const handleReset = () => {
    setCredentials(null);
    setInitialFiles([]);
    setMigrationId('');
    setStep('connect');
  };

  return (
    <div className="min-h-screen bg-cozy-darker text-slate-100 flex flex-col font-sans selection:bg-cozy-peach/30 selection:text-cozy-peach overflow-hidden relative">
      {/* Background Ambient Glows */}
      <div className="ambient-glow-coral"></div>
      <div className="ambient-glow-indigo"></div>
      <div className="ambient-glow-mint"></div>

      {/* Floating Navbar */}
      <header className="mt-4 mx-4 max-w-5xl md:mx-auto w-[calc(100%-2rem)] md:w-full cozy-glass rounded-2xl sticky top-4 z-50 transition-all duration-300">
        <div className="px-5 py-3.5 flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="p-2.5 bg-gradient-to-tr from-cozy-indigo to-cozy-coral rounded-xl text-white shadow-cozy-coral/45 shadow-lg hover:scale-105 hover:rotate-2 transition-all duration-300">
              <CloudLightning className="w-5 h-5" />
            </div>
            <div className="flex flex-col">
              <span className="font-display font-extrabold text-xl tracking-tight bg-gradient-to-r from-white via-slate-200 to-cozy-peach bg-clip-text text-transparent leading-none">
                CloudMove
              </span>
              <span className="text-[10px] text-slate-450 font-medium tracking-wide mt-0.5">
                Einfach & Sicher
              </span>
            </div>
            <span className="ml-2 px-2.5 py-0.5 rounded-full bg-cozy-indigo/15 border border-cozy-indigo/25 text-[10px] font-semibold text-cozy-peach tracking-wide">
              v1.0.0
            </span>
          </div>

          <div className="flex items-center gap-4">
            <div className="hidden sm:flex items-center gap-1.5 text-xs text-slate-450 bg-slate-900/60 py-1 px-3 rounded-full border border-slate-850">
              <Info className="w-3.5 h-3.5 text-cozy-indigo" />
              <span>Nextcloud WebDAV-Brücke</span>
            </div>
            <div className="flex items-center gap-2 bg-cozy-mint/10 border border-cozy-mint/25 py-1 px-3 rounded-full">
              <span className="w-2 h-2 rounded-full bg-cozy-mint animate-pulse"></span>
              <span className="text-[10.5px] font-semibold text-cozy-mint-light">API Online</span>
            </div>
          </div>
        </div>
      </header>

      {/* Main Body with Transition Wrapper */}
      <main className="flex-grow flex flex-col justify-center px-4 py-8 z-10 relative max-w-5xl w-full mx-auto">
        <div className="animate-pulse-slow absolute top-1/4 left-1/2 -translate-x-1/2 w-[500px] h-[500px] bg-cozy-indigo/5 rounded-full filter blur-[100px] pointer-events-none"></div>
        
        <div className="w-full transition-all duration-550 ease-in-out">
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
      <footer className="border-t border-slate-900/80 py-8 text-center text-xs text-slate-500 font-medium z-10 bg-cozy-darker/60 backdrop-blur-md">
        <div className="max-w-5xl mx-auto px-4 space-y-2">
          <p>© 2026 Multi-Cloud Migrations-Plattform. Mit Liebe zum Detail entwickelt.</p>
          <p className="text-[11px] text-slate-500 flex items-center justify-center gap-1.5 max-w-md mx-auto leading-relaxed">
            <span>🛡️</span>
            <span>Zero-Data-Retention: Ihre Passwörter und Daten fließen verschlüsselt direkt durch den Arbeitsspeicher des Servers und werden niemals gespeichert.</span>
          </p>
        </div>
      </footer>
    </div>
  );
}

export default App;
