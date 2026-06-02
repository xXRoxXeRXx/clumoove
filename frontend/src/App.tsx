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
    <div className="min-h-screen bg-bauhaus-paper text-bauhaus-ink flex flex-col font-sans selection:bg-bauhaus-rust selection:text-white relative z-10 pb-8">
      
      {/* Bauhaus Print Masthead Header */}
      <header className="border-b-2 border-bauhaus-ink bg-bauhaus-paper sticky top-0 z-50">
        <div className="max-w-6xl mx-auto px-6 h-20 flex items-center justify-between">
          <div className="flex items-center gap-4">
            {/* Flat block logo with zero border-radius */}
            <div className="p-3 bg-bauhaus-rust text-bauhaus-paper border-2 border-bauhaus-ink shadow-flat hover:translate-x-[2px] hover:translate-y-[2px] hover:shadow-flat-active transition-all duration-150 cursor-pointer">
              <CloudLightning className="w-6 h-6 stroke-[2.5]" />
            </div>
            
            <div className="flex flex-col">
              <span className="font-serif font-black text-2xl tracking-tight leading-none uppercase">
                CloudMove
              </span>
              <span className="text-[10px] font-mono font-bold tracking-widest uppercase text-slate-500 mt-1">
                // SYSTEM.MIGRATION.PROTOCOL
              </span>
            </div>
            
            <span className="hidden sm:inline-block ml-3 px-3 py-0.5 border border-bauhaus-ink font-mono text-[9px] font-bold bg-bauhaus-sand">
              RELEASE v1.0.0
            </span>
          </div>

          <div className="flex items-center gap-6">
            <div className="hidden md:flex items-center gap-2 font-mono text-xs font-bold text-slate-550">
              <Info className="w-4 h-4 text-bauhaus-rust" />
              <span>NEXTCLOUD WEBDAV GATEWAY</span>
            </div>
            
            {/* Postage stamp style status */}
            <div className="flex items-center gap-2 border-l border-bauhaus-ink pl-6 h-10">
              <span className="w-2.5 h-2.5 bg-bauhaus-moss border border-bauhaus-ink"></span>
              <span className="font-mono text-xs font-bold tracking-wider uppercase text-bauhaus-moss">API.ONLINE</span>
            </div>
          </div>
        </div>
      </header>

      {/* Main Structural Body */}
      <main className="flex-grow flex flex-col justify-center px-6 py-12 max-w-5xl w-full mx-auto relative z-10">
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
      <footer className="border-t-2 border-bauhaus-ink py-10 mt-12 bg-bauhaus-sand">
        <div className="max-w-5xl mx-auto px-6 grid md:grid-cols-2 gap-6 font-mono text-[10px] leading-relaxed text-slate-600">
          <div>
            <p className="font-bold text-bauhaus-ink uppercase mb-2">// HERAUSGEGEBEN DURCH CLOUDMOVE LABS</p>
            <p>© 2026 Multi-Cloud Migrations-Plattform. Entworfen nach funktionalen Bauhaus-Prinzipien.</p>
          </div>
          <div>
            <p className="font-bold text-bauhaus-ink uppercase mb-2">// SICHERHEITS-DIREKTIVE</p>
            <p>Datensätze werden ausschließlich im flüchtigen RAM verarbeitet. Keine permanente Speicherung von Zugangsdaten oder Inhalten (Zero Data Retention Policy).</p>
          </div>
        </div>
      </footer>
    </div>
  );
}

export default App;
