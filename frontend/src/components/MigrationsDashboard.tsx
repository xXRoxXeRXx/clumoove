import { useState, useEffect } from 'react';
import { Play, Trash2, ArrowRight, RefreshCw, Layers, Calendar, HardDrive, CheckCircle2, XCircle, Loader2 } from 'lucide-react';

interface MigrationsDashboardProps {
  apiUrl: string;
  token: string;
  user: any;
  onStartNewMigration: () => void;
  onSelectActiveMigration: (id: string) => void;
}

export function MigrationsDashboard({
  apiUrl,
  token,
  user,
  onStartNewMigration,
  onSelectActiveMigration,
}: MigrationsDashboardProps) {
  const [migrations, setMigrations] = useState<any[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string>('');
  const [deleteLoading, setDeleteLoading] = useState<string | null>(null);

  const fetchMigrations = async () => {
    try {
      const response = await fetch(`${apiUrl}/api/migration`, {
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });
      if (!response.ok) {
        throw new Error('Migrationsverlauf konnte nicht geladen werden.');
      }
      const data = await response.json();
      setMigrations(data || []);
    } catch (err: any) {
      setError(err.message || 'Verbindungsfehler');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchMigrations();
    // Poll every 10 seconds for active updates in the table
    const interval = setInterval(fetchMigrations, 10000);
    return () => clearInterval(interval);
  }, [apiUrl, token]);

  const handleDelete = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation(); // Avoid triggering row selection click
    
    if (!window.confirm('Möchtest du diese Migration wirklich unwiderruflich löschen? Alle Übertragungsprotokolle und gespeicherten Zugangsdaten werden gelöscht.')) {
      return;
    }

    setDeleteLoading(id);
    try {
      const response = await fetch(`${apiUrl}/api/migration/${id}`, {
        method: 'DELETE',
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });
      if (!response.ok) {
        throw new Error('Löschen fehlgeschlagen.');
      }
      setMigrations((prev) => prev.filter((m) => m.id !== id));
    } catch (err: any) {
      alert(err.message || 'Fehler beim Löschen');
    } finally {
      setDeleteLoading(null);
    }
  };

  const getStatusBadge = (status: string) => {
    switch (status) {
      case 'COMPLETED':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-emerald-50 text-emerald-700 border border-emerald-200">
            <CheckCircle2 className="w-3.5 h-3.5" />
            ABGESCHLOSSEN
          </span>
        );
      case 'FAILED':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-rose-50 text-rose-700 border border-rose-200">
            <XCircle className="w-3.5 h-3.5" />
            FEHLGESCHLAGEN
          </span>
        );
      case 'CANCELLED':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-rose-50 text-rose-700 border border-rose-200">
            <XCircle className="w-3.5 h-3.5" />
            ABGEBROCHEN
          </span>
        );
      case 'RUNNING':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-blue-50 text-blue-700 border border-blue-200 animate-pulse">
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
            AKTIV
          </span>
        );
      case 'INDEXING':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-amber-50 text-amber-700 border border-amber-200">
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
            INDEXIERUNG
          </span>
        );
      case 'PAUSED_CONNECTION_LOSS':
      case 'PAUSED':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-slate-100 text-slate-700 border border-slate-300">
            PAUSIERT
          </span>
        );
      default:
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-slate-50 text-slate-600 border border-slate-200">
            {status}
          </span>
        );
    }
  };

  const formatBytes = (bytes: number) => {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
  };

  // Calculate Stats
  const totalMigrations = migrations.length;
  const activeMigrations = migrations.filter(m => m.status === 'RUNNING' || m.status === 'INDEXING').length;
  const completedMigrations = migrations.filter(m => m.status === 'COMPLETED').length;
  const failedMigrations = migrations.filter(m => m.status === 'FAILED' || m.status === 'CANCELLED').length;
  
  const successRate = (completedMigrations + failedMigrations) > 0 
    ? Math.round((completedMigrations / (completedMigrations + failedMigrations)) * 100) 
    : 100;

  const totalBytesMigrated = migrations.reduce((acc, m) => acc + (m.processed_bytes || 0), 0);

  return (
    <div className="w-full space-y-6 animate-fade-in">
      
      {/* Welcome Banner */}
      <div className="relative rounded-3xl p-8 bg-gradient-to-r from-portal-navy via-slate-900 to-portal-navy-dark text-white overflow-hidden shadow-md">
        <div className="absolute inset-0 bg-[radial-gradient(circle_at_30%_20%,rgba(255,102,0,0.15),transparent_60%)] pointer-events-none" />
        <div className="absolute -right-20 -bottom-20 w-80 h-80 bg-portal-orange/10 rounded-full blur-3xl pointer-events-none" />
        
        <div className="relative z-10 flex flex-col md:flex-row justify-between items-start md:items-center gap-6">
          <div className="space-y-2">
            <p className="text-[9px] font-mono tracking-widest text-portal-orange font-bold uppercase">// SAAS migrations-system</p>
            <h1 className="font-display font-extrabold text-3xl tracking-tight">
              Hallo, {user.display_name || 'Benutzer'}
            </h1>
            <p className="text-sm text-slate-350 max-w-xl">
              Verwalte und überwache deine Datenübertragungen zwischen Cloud-Systemen in Echtzeit auf einer zentralen Oberfläche.
            </p>
          </div>
          
          <button
            onClick={onStartNewMigration}
            className="group flex items-center gap-2 bg-gradient-to-r from-portal-orange to-orange-500 hover:from-orange-500 hover:to-portal-orange text-white px-5 py-3 rounded-2xl text-xs font-mono font-bold tracking-wider uppercase transition-all duration-300 shadow-sm hover:shadow-md hover:-translate-y-0.5 active:translate-y-0 cursor-pointer shrink-0"
          >
            <Play className="w-4 h-4 fill-white group-hover:scale-110 transition-transform" />
            <span>Neue Migration</span>
          </button>
        </div>
      </div>

      {/* Stats Widgets Grid */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        {/* Total Bytes */}
        <div className="glass-panel border border-white/50 rounded-2xl p-4.5 shadow-portal flex items-center gap-4">
          <div className="p-3 bg-blue-50 text-portal-navy rounded-xl">
            <HardDrive className="w-5 h-5 stroke-[2]" />
          </div>
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-slate-400 uppercase tracking-wider">Daten übertragen</span>
            <span className="font-display font-extrabold text-lg text-slate-800 leading-tight mt-0.5">
              {formatBytes(totalBytesMigrated)}
            </span>
          </div>
        </div>

        {/* Total Migrations */}
        <div className="glass-panel border border-white/50 rounded-2xl p-4.5 shadow-portal flex items-center gap-4">
          <div className="p-3 bg-purple-50 text-brand-violet rounded-xl">
            <Layers className="w-5 h-5 stroke-[2]" />
          </div>
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-slate-400 uppercase tracking-wider">Migrationen</span>
            <span className="font-display font-extrabold text-lg text-slate-800 leading-tight mt-0.5">
              {totalMigrations}
            </span>
          </div>
        </div>

        {/* Active Transits */}
        <div className="glass-panel border border-white/50 rounded-2xl p-4.5 shadow-portal flex items-center gap-4 relative overflow-hidden">
          {activeMigrations > 0 && (
            <div className="absolute top-2 right-2 flex h-2 w-2">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75"></span>
              <span className="relative inline-flex rounded-full h-2 w-2 bg-emerald-500"></span>
            </div>
          )}
          <div className="p-3 bg-emerald-50 text-emerald-600 rounded-xl">
            <RefreshCw className={`w-5 h-5 stroke-[2] ${activeMigrations > 0 ? 'animate-spin' : ''}`} />
          </div>
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-slate-400 uppercase tracking-wider">Aktiv</span>
            <span className="font-display font-extrabold text-lg text-slate-800 leading-tight mt-0.5">
              {activeMigrations}
            </span>
          </div>
        </div>

        {/* Success Rate */}
        <div className="glass-panel border border-white/50 rounded-2xl p-4.5 shadow-portal flex items-center gap-4">
          <div className="p-3 bg-amber-50 text-portal-orange rounded-xl">
            <CheckCircle2 className="w-5 h-5 stroke-[2]" />
          </div>
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-slate-400 uppercase tracking-wider">Erfolgsquote</span>
            <span className="font-display font-extrabold text-lg text-slate-800 leading-tight mt-0.5">
              {successRate}%
            </span>
          </div>
        </div>
      </div>

      {/* Main Section */}
      <div className="glass-panel rounded-3xl border border-white/50 shadow-portal p-6">
        
        {/* Header toolbar */}
        <div className="flex justify-between items-center mb-6">
          <h2 className="font-display font-extrabold text-lg text-portal-navy">
            Deine Migrationen
          </h2>
          
          <button
            onClick={fetchMigrations}
            className="p-2.5 border border-slate-200 rounded-xl text-slate-500 hover:text-portal-navy hover:bg-slate-100/50 hover:border-slate-300 transition-all focus:outline-none cursor-pointer"
            title="Aktualisieren"
          >
            <RefreshCw className={`w-4 h-4 ${loading ? 'animate-spin' : ''}`} />
          </button>
        </div>

        {/* Status / Table */}
        {loading ? (
          <div className="flex flex-col items-center justify-center py-20 gap-4">
            <Loader2 className="w-8 h-8 text-portal-orange animate-spin" />
            <p className="text-[10px] font-mono text-slate-450 tracking-wider">// LADE DATENSÄTZE...</p>
          </div>
        ) : error ? (
          <div className="p-4 bg-rose-50/80 border border-rose-200 text-rose-800 rounded-xl text-xs font-mono text-center">
            {error}
          </div>
        ) : migrations.length === 0 ? (
          <div className="text-center py-16 border-2 border-dashed border-slate-200 rounded-2xl bg-slate-50/30">
            <Layers className="w-10 h-10 text-slate-300 mx-auto mb-4" />
            <p className="font-display font-bold text-slate-700">Keine Migrationen gefunden</p>
            <p className="text-[10px] text-slate-400 font-mono mt-1 mb-5">// DATENBANK IST LEER</p>
            <button
              onClick={onStartNewMigration}
              className="bg-gradient-to-r from-portal-orange to-orange-500 text-white hover:shadow-sm px-5 py-2.5 rounded-xl text-xs font-bold font-mono uppercase tracking-wider transition-all cursor-pointer"
            >
              Erste Migration starten
            </button>
          </div>
        ) : (
          <div className="overflow-x-auto scrollbar-portal">
            <table className="w-full text-left border-collapse min-w-[600px]">
              <thead>
                <tr className="border-b border-slate-200/60 text-[10px] font-bold text-slate-400 uppercase font-mono tracking-wider">
                  <th className="py-4.5 px-4 font-semibold">Erstellt am</th>
                  <th className="py-4.5 px-4 font-semibold">Quelle / Ziel</th>
                  <th className="py-4.5 px-4 font-semibold">Status</th>
                  <th className="py-4.5 px-4 font-semibold">Fortschritt</th>
                  <th className="py-4.5 px-4 font-semibold text-right">Aktionen</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {migrations.map((mig) => {
                  const createdDate = new Date(mig.created_at).toLocaleDateString('de-DE', {
                    day: '2-digit',
                    month: '2-digit',
                    year: 'numeric',
                    hour: '2-digit',
                    minute: '2-digit',
                  });

                  return (
                    <tr
                      key={mig.id}
                      onClick={() => onSelectActiveMigration(mig.id)}
                      className="hover:bg-slate-100/50 transition-all duration-200 cursor-pointer group"
                    >
                      {/* Date */}
                      <td className="py-4 px-4 whitespace-nowrap">
                        <div className="flex items-center gap-2 text-xs font-mono text-slate-650">
                          <Calendar className="w-3.5 h-3.5 text-slate-400 group-hover:text-portal-orange transition-colors" />
                          {createdDate}
                        </div>
                      </td>

                      {/* Providers */}
                      <td className="py-4 px-4">
                        <div className="flex items-center gap-2.5">
                          <div className="flex flex-col text-left">
                            <span className="text-xs font-bold text-slate-800 capitalize leading-snug">
                              {mig.source_provider}
                            </span>
                            <span className="text-[10px] text-slate-400 max-w-[120px] truncate block">
                              {mig.source_url || 'OAuth-Authentifiziert'}
                            </span>
                          </div>
                          
                          <ArrowRight className="w-3 h-3 text-slate-400 shrink-0 group-hover:translate-x-0.5 transition-transform" />
                          
                          <div className="flex flex-col text-left">
                            <span className="text-xs font-bold text-slate-800 capitalize leading-snug">
                              {mig.target_provider}
                            </span>
                            <span className="text-[10px] text-slate-400 max-w-[120px] truncate block">
                              {mig.target_url || 'OAuth-Authentifiziert'}
                            </span>
                          </div>
                        </div>
                      </td>

                      {/* Status */}
                      <td className="py-4 px-4 whitespace-nowrap">
                        {getStatusBadge(mig.status)}
                      </td>

                      {/* Progress */}
                      <td className="py-4 px-4">
                        <div className="flex flex-col gap-1.5 min-w-[120px]">
                          <div className="flex items-center justify-between text-[10px] font-mono text-slate-500">
                            <span>
                              {mig.processed_files} / {mig.total_files} Dateien
                            </span>
                            {mig.total_bytes > 0 && (
                              <span>
                                {formatBytes(mig.processed_bytes)}
                              </span>
                            )}
                          </div>
                          
                          {/* Progress bar */}
                          <div className="w-full bg-slate-100 rounded-full h-1.5 overflow-hidden shadow-inner">
                            <div
                              className={`h-full rounded-full transition-all duration-500 ${
                                mig.status === 'FAILED'
                                  ? 'bg-rose-500'
                                  : mig.status === 'COMPLETED'
                                  ? 'bg-emerald-500'
                                  : 'bg-portal-orange'
                              }`}
                              style={{
                                width: `${
                                  mig.total_files > 0
                                    ? (mig.processed_files / mig.total_files) * 100
                                    : 0
                                  }%`,
                              }}
                            />
                          </div>
                        </div>
                      </td>

                      {/* Actions */}
                      <td className="py-4 px-4 text-right whitespace-nowrap" onClick={(e) => e.stopPropagation()}>
                        <div className="flex justify-end items-center gap-2">
                          <button
                            onClick={() => onSelectActiveMigration(mig.id)}
                            className="p-1.5 bg-slate-100 hover:bg-portal-navy hover:text-white rounded-lg text-slate-500 transition-all cursor-pointer"
                            title="Dashboard öffnen"
                          >
                            <Play className="w-3.5 h-3.5 fill-current" />
                          </button>
                          <button
                            onClick={(e) => handleDelete(mig.id, e)}
                            disabled={deleteLoading === mig.id || mig.status === 'RUNNING' || mig.status === 'INDEXING'}
                            className="p-1.5 bg-slate-100 border border-transparent rounded-lg text-slate-400 hover:text-rose-600 hover:border-rose-100 hover:bg-rose-50/50 transition-all focus:outline-none disabled:opacity-30 disabled:pointer-events-none cursor-pointer"
                            title="Migration löschen"
                          >
                            {deleteLoading === mig.id ? (
                              <Loader2 className="w-3.5 h-3.5 animate-spin" />
                            ) : (
                              <Trash2 className="w-3.5 h-3.5" />
                            )}
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

        {/* Dashboard hints */}
        {!loading && migrations.length > 0 && (
          <div className="mt-6 border-t border-slate-200/40 pt-4 flex items-center gap-2 text-[10px] font-mono text-slate-400">
            <HardDrive className="w-3.5 h-3.5 shrink-0" />
            <span>
              // INFORMATION: Um sensible Verbindungsdaten und Übertragungsprotokolle zu bereinigen, nutze bitte das Lösch-Symbol.
            </span>
          </div>
        )}

      </div>
    </div>
  );
}
