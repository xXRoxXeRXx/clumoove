import { useState } from 'react';
import { ConnectForm } from './components/ConnectForm';
import { FileBrowser } from './components/FileBrowser';
import { Dashboard } from './components/Dashboard';
import { Share2 } from 'lucide-react';

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
    <div className="min-h-screen bg-slate-950 text-slate-100 flex flex-col selection:bg-blue-500/30 selection:text-blue-200">
      {/* Top Navbar */}
      <header className="border-b border-slate-900 bg-slate-950/80 backdrop-blur-md sticky top-0 z-50">
        <div className="max-w-6xl mx-auto px-4 h-16 flex items-center justify-between">
          <div className="flex items-center gap-2.5">
            <div className="p-2 bg-gradient-to-tr from-blue-500 to-indigo-600 rounded-xl text-slate-50 shadow-md">
              <Share2 className="w-5 h-5" />
            </div>
            <span className="font-extrabold text-lg tracking-tight bg-gradient-to-r from-slate-100 to-slate-300 bg-clip-text text-transparent">
              CloudMove
            </span>
            <span className="px-2 py-0.5 rounded-md bg-slate-900 border border-slate-800 text-[10px] font-mono text-slate-400">
              v1.0.0 (MVP)
            </span>
          </div>

          <div className="flex items-center gap-4 text-xs font-semibold text-slate-400">
            <a
              href="https://nextcloud.com"
              target="_blank"
              rel="noreferrer"
              className="hover:text-slate-200 transition-colors"
            >
              Nextcloud WebDAV
            </a>
            <span className="w-1.5 h-1.5 rounded-full bg-emerald-500"></span>
            <span>API Online</span>
          </div>
        </div>
      </header>

      {/* Main Body */}
      <main className="flex-grow flex flex-col justify-center">
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
      </main>

      {/* Footer */}
      <footer className="border-t border-slate-900/60 py-6 text-center text-xs text-slate-600 font-medium">
        <p>© 2026 Multi-Cloud Migrations-Plattform. Alle Rechte vorbehalten.</p>
        <p className="mt-1 text-slate-700">Zero Data Retention Security Policy. Transfers flow fleetingly through RAM stream buffers.</p>
      </footer>
    </div>
  );
}

export default App;
