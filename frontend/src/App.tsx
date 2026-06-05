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
            
            <span className="ml-2 px-2.5 py-0.5 bg-white/10 border border-white/20 text-[9px] font-semibold text-slate-100 rounded-full">
              v1.0.0
            </span>
          </div>

          <div className="flex items-center gap-6">
            <div className="hidden md:flex items-center gap-2 text-xs font-semibold text-slate-300">
              <Info className="w-4 h-4 text-portal-orange" />
              <span>WebDAV Datenübertragungsbrücke</span>
            </div>
            
            {/* Status indicator pill */}
            <div className="flex items-center gap-2 bg-white/10 px-3.5 py-1.5 rounded-full border border-white/10">
              <span className="w-2 h-2 rounded-full bg-emerald-400 animate-pulse"></span>
              <span className="font-mono text-[10px] font-bold tracking-wider uppercase text-emerald-400">ONLINE</span>
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
