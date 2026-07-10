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

  return (
    <div className="w-full space-y-6">
      
      {/* Welcome Banner */}
      <div className="bg-portal-navy text-white rounded-2xl p-6 shadow-portal flex flex-col md:flex-row justify-between items-start md:items-center gap-4 relative overflow-hidden">
        <div className="absolute inset-0 bg-linear-to-r from-portal-navy to-portal-navy/60 opacity-30 pointer-events-none" />
        <div className="relative z-10 space-y-1">
          <p className="text-xs font-mono tracking-wider text-slate-300 uppercase">// WILLKOMMEN ZURÜCK</p>
          <h1 className="font-display font-extrabold text-2xl tracking-tight">
            Hallo, {user.display_name || 'Benutzer'}
          </h1>
          <p className="text-sm text-slate-300">
            Verwalte und überwache deine Datenübertragungen auf einer zentralen SaaS-Oberfläche.
          </p>
        </div>
        
        <button
          onClick={onStartNewMigration}
          className="relative z-10 flex items-center gap-2 bg-portal-orange text-white hover:bg-portal-orange-hover px-5 py-3 rounded-xl text-xs font-mono font-bold tracking-wider uppercase transition-all shadow-sm focus:outline-none focus:ring-2 focus:ring-portal-orange"
        >
          <Play className="w-4 h-4 fill-white" />
          Neue Migration starten
        </button>
      </div>

      {/* Main Section */}
      <div className="bg-white rounded-2xl border border-portal-border shadow-portal p-6">
        
        {/* Header toolbar */}
        <div className="flex justify-between items-center mb-6">
          <h2 className="font-display font-extrabold text-lg text-portal-navy">
            Deine Migrationen
          </h2>
          
          <button
            onClick={fetchMigrations}
            className="p-2 border border-portal-border rounded-xl text-slate-500 hover:text-portal-navy hover:bg-slate-50 transition-all focus:outline-none"
            title="Aktualisieren"
          >
            <RefreshCw className={`w-4 h-4 ${loading ? 'animate-spin' : ''}`} />
          </button>
        </div>

        {/* Status / Table */}
        {loading ? (
          <div className="flex flex-col items-center justify-center py-20 gap-4">
            <Loader2 className="w-8 h-8 text-portal-orange animate-spin" />
            <p className="text-xs font-mono text-slate-400">// LADE DATENSÄTZE...</p>
          </div>
        ) : error ? (
          <div className="p-4 bg-rose-50 border border-rose-200 text-rose-800 rounded-xl text-xs font-mono text-center">
            {error}
          </div>
        ) : migrations.length === 0 ? (
          <div className="text-center py-16 border-2 border-dashed border-slate-200 rounded-2xl bg-slate-50/50">
            <Layers className="w-10 h-10 text-slate-300 mx-auto mb-4" />
            <p className="font-display font-bold text-slate-700">Keine Migrationen gefunden</p>
            <p className="text-xs text-slate-400 font-mono mt-1 mb-5">// DATENBANK IST LEER</p>
            <button
              onClick={onStartNewMigration}
              className="bg-portal-orange text-white hover:bg-portal-orange-hover px-4 py-2.5 rounded-xl text-xs font-bold font-mono uppercase tracking-wider transition-all"
            >
              Erste Migration starten
            </button>
          </div>
        ) : (
          <div className="overflow-x-auto scrollbar-portal">
            <table className="w-full text-left border-collapse">
              <thead>
                <tr className="border-b border-portal-border text-[11px] font-bold text-slate-400 uppercase font-mono tracking-wider bg-slate-50/50">
                  <th className="py-4 px-4">Erstellt am</th>
                  <th className="py-4 px-4">Quelle / Ziel</th>
                  <th className="py-4 px-4">Status</th>
                  <th className="py-4 px-4">Fortschritt</th>
                  <th className="py-4 px-4 text-right">Aktionen</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-portal-border/60">
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
                      className="hover:bg-slate-50/80 transition-colors cursor-pointer group"
                    >
                      {/* Date */}
                      <td className="py-4 px-4 whitespace-nowrap">
                        <div className="flex items-center gap-2 text-xs font-mono text-slate-600">
                          <Calendar className="w-3.5 h-3.5 text-slate-400" />
                          {createdDate}
                        </div>
                      </td>

                      {/* Providers */}
                      <td className="py-4 px-4">
                        <div className="flex items-center gap-2.5">
                          <div className="flex flex-col">
                            <span className="text-xs font-bold text-slate-800 capitalize leading-snug">
                              {mig.source_provider}
                            </span>
                            <span className="text-[10px] text-slate-400 max-w-[140px] truncate">
                              {mig.source_url}
                            </span>
                          </div>
                          
                          <ArrowRight className="w-3 h-3 text-slate-400 shrink-0" />
                          
                          <div className="flex flex-col">
                            <span className="text-xs font-bold text-slate-800 capitalize leading-snug">
                              {mig.target_provider}
                            </span>
                            <span className="text-[10px] text-slate-400 max-w-[140px] truncate">
                              {mig.target_url}
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
                        <div className="flex flex-col gap-1 min-w-[120px]">
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
                          <div className="w-full bg-slate-100 rounded-full h-1.5 overflow-hidden">
                            <div
                              className={`h-full rounded-full transition-all duration-300 ${
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
                      <td className="py-4 px-4 text-right whitespace-nowrap">
                        <div className="flex justify-end gap-2">
                          <button
                            onClick={(e) => handleDelete(mig.id, e)}
                            disabled={deleteLoading === mig.id || mig.status === 'RUNNING' || mig.status === 'INDEXING'}
                            className="p-2 border border-portal-border rounded-lg text-slate-400 hover:text-rose-600 hover:border-rose-200 hover:bg-rose-50/50 transition-all focus:outline-none disabled:opacity-30 disabled:pointer-events-none"
                            title="Migration löschen"
                          >
                            {deleteLoading === mig.id ? (
                              <Loader2 className="w-4 h-4 animate-spin" />
                            ) : (
                              <Trash2 className="w-4 h-4" />
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
          <div className="mt-6 border-t border-portal-border/60 pt-4 flex items-center gap-2 text-[10px] font-mono text-slate-400">
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
