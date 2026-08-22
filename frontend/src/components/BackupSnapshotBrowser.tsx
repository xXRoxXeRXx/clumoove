import { useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { FileIcon } from './FileIcon';
import { useFormat } from '../utils/format';
import { apiFetch, apiJson } from '../utils/apiClient';
import { useApiError } from '../utils/apiError';
import { useFocusTrap } from '../hooks/useFocusTrap';

type Snapshot = {
  id: string;
  state: string;
  created_at: string;
  total_files: number;
  total_dirs: number;
  total_bytes: number;
  omitted_unstable_count: number;
  omitted_error_count: number;
  integrity_state: string;
};

type SnapshotItem = {
  relative_path: string;
  name: string;
  is_dir: boolean;
  size_bytes: number;
  mtime: string | null;
  state: string;
  error_code: string | null;
};

type Props = {
  apiUrl: string;
  token: string;
  jobID: string;
  onBack: () => void;
};

type Profile = { id: string; name: string; provider: string };
type ProfileResponse = { profiles: Profile[] };
type RestorePreview = { id: string; status: string; total_files: number; total_directories: number; total_bytes: number; existing_file_conflicts: number; mergeable_directories: number; type_conflicts: number; unavailable_items: number; expected_skips: number; expected_renames: number; metadata_warnings: number; conflict_examples: Array<{ path: string; outcome: string }> };
type RestoreRun = { id: string; status: string; total_files: number; processed_files: number; total_bytes: number; processed_bytes: number; failed_files: number };
type RestoreItem = { id: string; snapshot_relative_path: string; target_path: string; status: string; verification_kind: string | null; error_code: string | null };
type BackupVerify = { id: string; state: string; verify_mode: string; byte_budget: number | null; processed_bytes: number; total_packs: number; checked_packs: number; missing_packs: number; damaged_packs: number };

export function BackupSnapshotBrowser({ apiUrl, token, jobID, onBack }: Props) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const { formatBytes, formatDateTime } = useFormat();
  const [snapshots, setSnapshots] = useState<Snapshot[]>([]);
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [directory, setDirectory] = useState('');
  const [items, setItems] = useState<SnapshotItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [selectedPaths, setSelectedPaths] = useState<string[]>([]);
  const [restoreOpen, setRestoreOpen] = useState(false);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [targetProfileID, setTargetProfileID] = useState('');
  const [targetMode, setTargetMode] = useState<'profile' | 'direct'>('profile');
  const [directProvider, setDirectProvider] = useState('nextcloud');
  const [directURL, setDirectURL] = useState('');
  const [directUsername, setDirectUsername] = useState('');
  const [directPassword, setDirectPassword] = useState('');
  const [directRefreshToken, setDirectRefreshToken] = useState('');
  const [targetRoot, setTargetRoot] = useState('/');
  const [strategy, setStrategy] = useState('RENAME');
  const [restoreThreads, setRestoreThreads] = useState(8);
  const [restoreBandwidthMbps, setRestoreBandwidthMbps] = useState(0);
  const [preview, setPreview] = useState<RestorePreview | null>(null);
  const [restoreLoading, setRestoreLoading] = useState(false);
  const [restoreRun, setRestoreRun] = useState<RestoreRun | null>(null);
  const [restoreRuns, setRestoreRuns] = useState<RestoreRun[]>([]);
  const [restoreItems, setRestoreItems] = useState<RestoreItem[]>([]);
  const [selectedRestoreRunID, setSelectedRestoreRunID] = useState('');
  const [verifyMode, setVerifyMode] = useState('METADATA');
  const [verifyBudgetMiB, setVerifyBudgetMiB] = useState(64);
  const [verifyFullConfirmed, setVerifyFullConfirmed] = useState(false);
  const [verifies, setVerifies] = useState<BackupVerify[]>([]);
  const [verifyLoading, setVerifyLoading] = useState(false);
  const restoreDialogRef = useRef<HTMLDivElement>(null);
  const restoreCloseRef = useRef<HTMLButtonElement>(null);
  const restoreTitleID = useId();

  useFocusTrap(restoreDialogRef, restoreCloseRef, () => setRestoreOpen(false), restoreOpen);

  useEffect(() => {
    let disposed = false;
    void apiJson<Snapshot[]>(`${apiUrl}/api/backup/${jobID}/snapshots`, { headers: { Authorization: `Bearer ${token}` } })
      .then((result) => {
        if (disposed) return;
        if (result.ok === false) {
          setError(t('backup.snapshotLoadFailed'));
          return;
        }
        setSnapshots(result.data || []);
      })
      .catch(() => !disposed && setError(t('backup.snapshotLoadFailed')))
      .finally(() => !disposed && setLoading(false));
    return () => { disposed = true; };
  }, [apiUrl, jobID, t, token]);

  const loadVerifies = async () => {
    const result = await apiJson<BackupVerify[]>(`${apiUrl}/api/backup/${jobID}/verify`, { headers: { Authorization: `Bearer ${token}` } });
    if (result.ok !== false) setVerifies(result.data || []);
  };

  useEffect(() => {
    let disposed = false;
    void apiJson<BackupVerify[]>(`${apiUrl}/api/backup/${jobID}/verify`, { headers: { Authorization: `Bearer ${token}` } })
      .then((result) => { if (!disposed && result.ok !== false) setVerifies(result.data || []); });
    return () => { disposed = true; };
  }, [apiUrl, jobID, token]);

  useEffect(() => {
    let disposed = false;
    void apiJson<RestoreRun[]>(`${apiUrl}/api/restore`, { headers: { Authorization: `Bearer ${token}` } })
      .then((result) => { if (!disposed && result.ok !== false) setRestoreRuns(result.data || []); });
    return () => { disposed = true; };
  }, [apiUrl, token]);

  const hasActiveRestoreHistory = restoreRuns.some((run) => !['COMPLETED', 'PARTIAL', 'FAILED', 'CANCELLED'].includes(run.status));
  useEffect(() => {
    if (!hasActiveRestoreHistory) return;
    const refresh = () => {
      void apiJson<RestoreRun[]>(`${apiUrl}/api/restore`, { headers: { Authorization: `Bearer ${token}` } })
        .then((result) => { if (result.ok !== false) setRestoreRuns(result.data || []); });
    };
    const timer = window.setInterval(refresh, 1500);
    return () => window.clearInterval(timer);
  }, [apiUrl, hasActiveRestoreHistory, token]);

  const hasActiveVerify = verifies.some((verify) => ['PENDING', 'RUNNING', 'RETRY_WAIT', 'CANCELLING'].includes(verify.state));
  useEffect(() => {
    if (!hasActiveVerify) return;
    const refresh = () => {
      void apiJson<BackupVerify[]>(`${apiUrl}/api/backup/${jobID}/verify`, { headers: { Authorization: `Bearer ${token}` } })
        .then((result) => { if (result.ok !== false) setVerifies(result.data || []); });
    };
    const timer = window.setInterval(refresh, 3000);
    return () => window.clearInterval(timer);
  }, [apiUrl, hasActiveVerify, jobID, token]);

  useEffect(() => {
    if (!snapshot) return;
    let disposed = false;
    const query = directory ? `?path=${encodeURIComponent(directory)}` : '';
    void apiJson<SnapshotItem[]>(`${apiUrl}/api/backup/${jobID}/snapshots/${snapshot.id}/items${query}`, { headers: { Authorization: `Bearer ${token}` } })
      .then((result) => {
        if (disposed) return;
        if (result.ok === false) {
          setError(t('backup.snapshotLoadFailed'));
          return;
        }
        setItems(result.data || []);
      })
      .catch(() => !disposed && setError(t('backup.snapshotLoadFailed')))
      .finally(() => !disposed && setLoading(false));
    return () => { disposed = true; };
  }, [apiUrl, directory, jobID, snapshot, t, token]);

  useEffect(() => {
    if (!restoreOpen || profiles.length > 0) return;
    void apiJson<ProfileResponse>(`${apiUrl}/api/profiles`, { headers: { Authorization: `Bearer ${token}` } })
      .then((result) => result.ok !== false && setProfiles((result.data?.profiles || []).filter((profile) => profile.provider !== 'immich')));
  }, [apiUrl, profiles.length, restoreOpen, token]);

  useEffect(() => {
    if (!preview || !['QUEUED', 'RUNNING'].includes(preview.status)) return;
    let disposed = false;
    const timer = window.setInterval(() => {
      void apiJson<RestorePreview>(`${apiUrl}/api/restore/previews/${preview.id}`, { headers: { Authorization: `Bearer ${token}` } })
        .then((result) => {
          if (!disposed && result.ok !== false && result.data) setPreview(result.data);
        });
    }, 1500);
    return () => { disposed = true; window.clearInterval(timer); };
  }, [apiUrl, preview, token]);

  useEffect(() => {
    if (!restoreRun || ['COMPLETED', 'PARTIAL', 'FAILED', 'CANCELLED'].includes(restoreRun.status)) return;
    const timer = window.setInterval(() => {
      void apiJson<RestoreRun>(`${apiUrl}/api/restore/runs/${restoreRun.id}`, { headers: { Authorization: `Bearer ${token}` } })
        .then((result) => result.ok !== false && result.data && setRestoreRun(result.data));
    }, 1500);
    return () => window.clearInterval(timer);
  }, [apiUrl, restoreRun, token]);

  const togglePath = (value: string) => setSelectedPaths((current) => {
    if (current.includes(value)) return current.filter((selected) => selected !== value);
    if (value === '') return [''];
    if (current.some((selected) => selected === '' || value.startsWith(`${selected}/`))) return current;
    return [...current.filter((selected) => !selected.startsWith(`${value}/`)), value];
  });
  const createPreview = async () => {
    if (!snapshot || selectedPaths.length === 0 || (targetMode === 'profile' ? !targetProfileID : (!directPassword && directProvider !== 'local'))) return;
    setRestoreLoading(true); setError('');
    try {
      const target = targetMode === 'profile'
        ? { target_profile_id: targetProfileID }
        : { target_provider: directProvider, target_url: directURL, target_username: directUsername, target_password: directPassword, target_refresh_token: directRefreshToken };
      const result = await apiJson<{ id: string }>(`${apiUrl}/api/backup/${jobID}/snapshots/${snapshot.id}/restore/previews`, { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ ...target, selected_paths: selectedPaths, target_root: targetRoot, conflict_strategy: strategy, threads: restoreThreads, bandwidth_mbps: restoreBandwidthMbps }) });
      if (result.ok === false) { setError(translateApiError(result.errorCode)); return; }
      if (!result.data?.id) throw new Error();
      const previewResult = await apiJson<RestorePreview>(`${apiUrl}/api/restore/previews/${result.data.id}`, { headers: { Authorization: `Bearer ${token}` } });
      if (previewResult.ok !== false && previewResult.data) setPreview(previewResult.data);
      setDirectPassword(''); setDirectRefreshToken('');
    } catch { setError(t('backup.restoreFailed')); } finally { setRestoreLoading(false); }
  };
  const consumePreview = async () => {
    if (!preview) return;
    setRestoreLoading(true);
    try {
      const result = await apiJson<{ restore_run_id: string }>(`${apiUrl}/api/restore/previews/${preview.id}/consume`, { method: 'POST', headers: { Authorization: `Bearer ${token}` } });
      if (result.ok === false) { setError(translateApiError(result.errorCode)); return; }
      if (!result.data?.restore_run_id) throw new Error();
      setRestoreRun({ id: result.data.restore_run_id, status: 'QUEUED', total_files: preview.total_files, processed_files: 0, total_bytes: preview.total_bytes, processed_bytes: 0, failed_files: 0 });
      setRestoreRuns((runs) => [{ id: result.data.restore_run_id, status: 'QUEUED', total_files: preview.total_files, processed_files: 0, total_bytes: preview.total_bytes, processed_bytes: 0, failed_files: 0 }, ...runs]);
      setPreview(null); setSelectedPaths([]);
    } catch { setError(t('backup.restoreFailed')); } finally { setRestoreLoading(false); }
  };
  const createVerify = async () => {
    if (verifyMode === 'FULL' && !verifyFullConfirmed) return;
    setVerifyLoading(true); setError('');
    try {
      const body: { mode: string; byte_budget?: number; confirm_full?: boolean } = { mode: verifyMode };
      if (verifyMode === 'BUDGETED') body.byte_budget = verifyBudgetMiB * 1024 * 1024;
      if (verifyMode === 'FULL') body.confirm_full = true;
      const result = await apiJson(`${apiUrl}/api/backup/${jobID}/verify`, { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      if (result.ok === false) { setError(translateApiError(result.errorCode)); return; }
      await loadVerifies();
    } catch { setError(t('backup.verifyFailed')); } finally { setVerifyLoading(false); }
  };
  const cancelVerify = async (verifyID: string) => {
    setVerifyLoading(true);
    try {
      const result = await apiJson(`${apiUrl}/api/backup/${jobID}/verify/${verifyID}/cancel`, { method: 'POST', headers: { Authorization: `Bearer ${token}` } });
      if (result.ok === false) { setError(translateApiError(result.errorCode)); return; }
      await loadVerifies();
    } catch { setError(t('backup.verifyFailed')); } finally { setVerifyLoading(false); }
  };
  const downloadRestoreReport = async (runID: string) => {
    try {
      const response = await apiFetch(`${apiUrl}/api/restore/runs/${runID}/report`, { headers: { Authorization: `Bearer ${token}` } });
      if (!response.ok) {
        const body = await response.json().catch(() => ({} as { error_code?: string }));
        setError(translateApiError(body.error_code));
        return;
      }
      const blobURL = URL.createObjectURL(await response.blob());
      const link = document.createElement('a');
      link.href = blobURL;
      link.download = 'restore-report.csv';
      link.click();
      window.setTimeout(() => URL.revokeObjectURL(blobURL), 0);
    } catch { setError(t('backup.restoreFailed')); }
  };
  const loadRestoreItems = async (runID: string) => {
    setSelectedRestoreRunID(runID);
    const result = await apiJson<RestoreItem[]>(`${apiUrl}/api/restore/runs/${runID}/items`, { headers: { Authorization: `Bearer ${token}` } });
    if (result.ok === false) { setError(translateApiError(result.errorCode)); return; }
    setRestoreItems(result.data || []);
  };
  const cancelRestoreHistoryRun = async (runID: string) => {
    const result = await apiJson(`${apiUrl}/api/restore/runs/${runID}/cancel`, { method: 'POST', headers: { Authorization: `Bearer ${token}` } });
    if (result.ok === false) { setError(translateApiError(result.errorCode)); return; }
    setRestoreRuns((runs) => runs.map((run) => run.id === runID ? { ...run, status: 'CANCELLING' } : run));
  };

  if (!snapshot) {
    return <section className="ui-card p-5" aria-label={t('backup.browseSnapshots')}>
      <div className="mb-4 flex items-center justify-between gap-3">
        <h2 className="font-display text-lg font-bold">{t('backup.browseSnapshots')}</h2>
        <button type="button" className="ui-button-secondary px-3 py-2 text-xs" onClick={onBack}>{t('common.back')}</button>
      </div>
      {loading && <p className="text-sm text-[var(--color-text-muted)]">{t('common.loading')}</p>}
       {error && <p className="ui-alert ui-alert-error text-sm" role="alert">{error}</p>}
       <div className="mb-4 space-y-2 border border-[var(--color-border)] p-3" aria-label={t('backup.verifyTitle')}>
         <h3 className="text-sm font-semibold">{t('backup.verifyTitle')}</h3>
         <div className="flex flex-wrap items-end gap-2 text-xs">
           <label>{t('backup.verifyMode')}<select className="ui-input mt-1" value={verifyMode} onChange={(event) => setVerifyMode(event.target.value)}><option value="METADATA">{t('backup.verifyMetadata')}</option><option value="BUDGETED">{t('backup.verifyBudgeted')}</option><option value="FULL">{t('backup.verifyFull')}</option></select></label>
           {verifyMode === 'BUDGETED' && <label>{t('backup.verifyBudget')}<input className="ui-input mt-1 w-24" type="number" min="64" max="1048576" value={verifyBudgetMiB} onChange={(event) => setVerifyBudgetMiB(Number(event.target.value))} /></label>}
           {verifyMode === 'FULL' && <label className="flex items-center gap-1"><input type="checkbox" checked={verifyFullConfirmed} onChange={(event) => setVerifyFullConfirmed(event.target.checked)} />{t('backup.verifyConfirmFull')}</label>}
           <button type="button" className="ui-button-secondary px-3 py-2" disabled={verifyLoading || (verifyMode === 'FULL' && !verifyFullConfirmed)} onClick={() => void createVerify()}>{t('backup.verifyStart')}</button>
         </div>
         {verifies.slice(0, 3).map((verify) => <div key={verify.id} className="flex flex-wrap items-center justify-between gap-2 text-xs" role="status"><span>{verify.verify_mode} · {verify.state} · {verify.checked_packs}/{verify.total_packs} · {formatBytes(verify.processed_bytes)} · {t('backup.verifyDamage', { missing: verify.missing_packs, damaged: verify.damaged_packs })}</span>{['PENDING', 'RUNNING', 'RETRY_WAIT', 'CANCELLING'].includes(verify.state) && <button type="button" className="ui-button-secondary px-2 py-1" disabled={verifyLoading} onClick={() => void cancelVerify(verify.id)}>{t('backup.verifyCancel')}</button>}</div>)}
        </div>
       {restoreRuns.length > 0 && <section className="mb-4 border border-[var(--color-border)] p-3" aria-label={t('backup.restoreHistory')}><h3 className="mb-2 text-sm font-semibold">{t('backup.restoreHistory')}</h3><div className="space-y-1 text-xs">{restoreRuns.slice(0, 10).map((run) => <div key={run.id} className="flex flex-wrap items-center justify-between gap-2"><span>{run.status} · {t('migrations.filesCount', { processed: run.processed_files, total: run.total_files })} · {formatBytes(run.processed_bytes)} / {formatBytes(run.total_bytes)}</span><span className="flex gap-2"><button type="button" className="ui-link" onClick={() => void loadRestoreItems(run.id)}>{t('backup.restoreItems')}</button>{!['COMPLETED', 'PARTIAL', 'FAILED', 'CANCELLED', 'CANCELLING'].includes(run.status) && <button type="button" className="ui-link" onClick={() => void cancelRestoreHistoryRun(run.id)}>{t('backup.restoreCancel')}</button>}{['COMPLETED', 'PARTIAL', 'FAILED', 'CANCELLED'].includes(run.status) && <button type="button" className="ui-link" onClick={() => void downloadRestoreReport(run.id)}>{t('backup.restoreReport')}</button>}</span></div>)}</div>{selectedRestoreRunID && <div className="mt-3 max-h-48 overflow-y-auto border-t border-[var(--color-border)] pt-2 text-xs">{restoreItems.map((item) => <div key={item.id} className="py-1"><strong>{item.status}</strong> · {item.snapshot_relative_path} → {item.target_path}{item.verification_kind ? ` · ${item.verification_kind}` : ''}{item.error_code ? ` · ${translateApiError(item.error_code)}` : ''}</div>)}</div>}</section>}
       {!loading && !error && snapshots.length === 0 && <p className="text-sm text-[var(--color-text-muted)]">{t('backup.noSnapshots')}</p>}
      <div className="space-y-2">
        {snapshots.map((candidate) => <button key={candidate.id} type="button" onClick={() => { setLoading(true); setError(''); setSnapshot(candidate); setDirectory(''); }} className="ui-button-secondary flex w-full items-center justify-between gap-4 px-4 py-3 text-left">
          <span><strong className="block text-sm">{formatDateTime(candidate.created_at)}</strong><span className="text-xs text-[var(--color-text-muted)]">{t('migrations.filesCount', { processed: candidate.total_files, total: candidate.total_files })} · {formatBytes(candidate.total_bytes)}</span></span>
          <span className="text-xs font-semibold">{candidate.state}</span>
        </button>)}
      </div>
    </section>;
  }

  const crumbs = directory ? directory.split('/') : [];
  return <section className="ui-card p-5" aria-label={t('backup.snapshotContents')}>
    <div className="mb-4 flex items-center justify-between gap-3">
      <div><h2 className="font-display text-lg font-bold">{t('backup.snapshotContents')}</h2><p className="text-xs text-[var(--color-text-muted)]">{formatDateTime(snapshot.created_at)}</p></div>
      <button type="button" className="ui-button-secondary px-3 py-2 text-xs" onClick={() => { setSnapshot(null); setItems([]); }}>{t('common.back')}</button>
    </div>
    <nav className="mb-3 flex flex-wrap items-center gap-1 text-xs" aria-label={t('backup.snapshotPath')}>
      <button type="button" className="ui-button-secondary px-2 py-1" onClick={() => { setLoading(true); setError(''); setDirectory(''); }}>/</button>
      {crumbs.map((crumb, index) => <button key={`${crumb}-${index}`} type="button" className="ui-button-secondary px-2 py-1" onClick={() => { setLoading(true); setError(''); setDirectory(crumbs.slice(0, index + 1).join('/')); }}>{crumb}</button>)}
    </nav>
    <div className="mb-3 flex flex-wrap items-center gap-2">
      <button type="button" className="ui-button-secondary px-3 py-2 text-xs" onClick={() => togglePath('')} aria-pressed={selectedPaths.includes('')}>{t('backup.restoreAll')}</button>
      <button type="button" className="ui-button-primary px-3 py-2 text-xs" disabled={selectedPaths.length === 0} onClick={() => setRestoreOpen(true)}>{t('backup.restoreSelected', { count: selectedPaths.length })}</button>
    </div>
    {restoreOpen && <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) setRestoreOpen(false); }}>
      <div ref={restoreDialogRef} role="dialog" aria-modal="true" aria-labelledby={restoreTitleID} tabIndex={-1} className="ui-card max-h-[90vh] w-full max-w-lg space-y-3 overflow-y-auto p-5">
      <div className="flex items-center justify-between gap-3"><h3 id={restoreTitleID} className="text-sm font-semibold">{t('backup.restoreTitle')}</h3><button ref={restoreCloseRef} type="button" className="ui-button-secondary px-2 py-1 text-xs" onClick={() => setRestoreOpen(false)} aria-label={t('common.close')} title={t('common.close')}>{t('common.close')}</button></div>
      <fieldset className="space-y-2 border border-[var(--color-border)] p-3"><legend className="px-1 text-xs font-medium">{t('backup.restoreTarget')}</legend><label className="mr-3 text-xs"><input type="radio" checked={targetMode === 'profile'} onChange={() => setTargetMode('profile')} /> {t('backup.restoreSavedTarget')}</label><label className="text-xs"><input type="radio" checked={targetMode === 'direct'} onChange={() => setTargetMode('direct')} /> {t('backup.restoreDirectTarget')}</label>{targetMode === 'profile' ? <label className="mt-2 block text-xs font-medium"><select className="ui-input mt-1 w-full" value={targetProfileID} onChange={(event) => setTargetProfileID(event.target.value)}><option value="">{t('backup.restoreChooseTarget')}</option>{profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name} ({profile.provider})</option>)}</select></label> : <div className="mt-2 grid gap-2"><label className="text-xs">{t('backup.restoreProvider')}<select className="ui-input mt-1 w-full" value={directProvider} onChange={(event) => setDirectProvider(event.target.value)}>{['nextcloud', 'opencloud', 'webdav', 'dropbox', 'google', 'onedrive', 'hidrive', 's3', 'smb', 'sftp', 'ftp', 'magentacloud', 'koofr', 'seafile', 'mega', 'local'].map((provider) => <option key={provider} value={provider}>{provider}</option>)}</select></label>{directProvider !== 'local' && <><label className="text-xs">{t('backup.restoreEndpoint')}<input className="ui-input mt-1 w-full" value={directURL} onChange={(event) => setDirectURL(event.target.value)} autoComplete="url" /></label><label className="text-xs">{t('backup.restoreUsername')}<input className="ui-input mt-1 w-full" value={directUsername} onChange={(event) => setDirectUsername(event.target.value)} autoComplete="username" /></label><label className="text-xs">{t('backup.restorePassword')}<input className="ui-input mt-1 w-full" type="password" value={directPassword} onChange={(event) => setDirectPassword(event.target.value)} autoComplete="current-password" /></label>{['dropbox', 'google', 'onedrive', 'hidrive'].includes(directProvider) && <label className="text-xs">{t('backup.restoreRefreshToken')}<input className="ui-input mt-1 w-full" type="password" value={directRefreshToken} onChange={(event) => setDirectRefreshToken(event.target.value)} autoComplete="off" /></label>}</>}</div>}</fieldset>
       <label className="block text-xs font-medium">{t('backup.restoreFolder')}<input className="ui-input mt-1 w-full" value={targetRoot} onChange={(event) => setTargetRoot(event.target.value)} /></label>
       <label className="block text-xs font-medium">{t('backup.restoreConflict')}<select className="ui-input mt-1 w-full" value={strategy} onChange={(event) => setStrategy(event.target.value)}><option value="RENAME">{t('backup.restoreRename')}</option><option value="SKIP">{t('backup.restoreSkip')}</option><option value="OVERWRITE">{t('backup.restoreOverwrite')}</option></select></label>
       <div className="grid gap-3 sm:grid-cols-2"><label className="block text-xs font-medium">{t('backup.restoreThreads')}<input className="ui-input mt-1 w-full" type="number" min="1" max="16" value={restoreThreads} onChange={(event) => setRestoreThreads(Math.max(1, Math.min(16, Number(event.target.value) || 1)))} /></label><label className="block text-xs font-medium">{t('backup.restoreBandwidth')}<input className="ui-input mt-1 w-full" type="number" min="0" max="1000" value={restoreBandwidthMbps} onChange={(event) => setRestoreBandwidthMbps(Math.max(0, Math.min(1000, Number(event.target.value) || 0)))} /></label></div>
       {!preview ? <button type="button" className="ui-button-primary px-3 py-2 text-xs" disabled={restoreLoading || (targetMode === 'profile' ? !targetProfileID : (!directPassword && directProvider !== 'local'))} onClick={() => void createPreview()}>{t('backup.restorePreview')}</button> : <div className="space-y-2 text-xs"><div className="flex flex-wrap items-center gap-3"><span>{preview.status === 'READY' ? t('backup.restorePreviewReady') : t('backup.restorePreviewRunning')}</span><span>{t('migrations.filesCount', { processed: preview.total_files, total: preview.total_files })} · {formatBytes(preview.total_bytes)}</span><button type="button" className="ui-button-primary px-3 py-2" disabled={restoreLoading || preview.status !== 'READY'} onClick={() => void consumePreview()}>{t('backup.restoreStart')}</button><button type="button" className="ui-button-secondary px-3 py-2" disabled={restoreLoading} onClick={() => void apiJson(`${apiUrl}/api/restore/previews/${preview.id}/cancel`, { method: 'POST', headers: { Authorization: `Bearer ${token}` } }).then(() => setPreview(null))}>{t('backup.restoreCancelPreview')}</button></div>{preview.status === 'READY' && <div className="border border-[var(--color-border)] p-2"><p>{t('backup.restorePreviewAdvisory')}</p><p>{t('backup.restorePreviewCounts', { dirs: preview.total_directories, files: preview.total_files, existing: preview.existing_file_conflicts, merge: preview.mergeable_directories, type: preview.type_conflicts, skipped: preview.expected_skips, renamed: preview.expected_renames, unavailable: preview.unavailable_items, metadata: preview.metadata_warnings })}</p>{preview.conflict_examples?.length > 0 && <ul className="mt-1 list-disc pl-4">{preview.conflict_examples.map((example, index) => <li key={`${example.path}-${index}`}>{example.path} · {example.outcome}</li>)}</ul>}</div>}</div>}
      </div>
    </div>}
    {restoreRun && <div className="mb-4 flex flex-wrap items-center justify-between gap-3 border border-[var(--color-border)] p-3 text-xs" role="status">
      <span><strong>{t('backup.restoreProgress')}</strong> {restoreRun.status} · {t('migrations.filesCount', { processed: restoreRun.processed_files, total: restoreRun.total_files })} · {formatBytes(restoreRun.processed_bytes)} / {formatBytes(restoreRun.total_bytes)}</span>
      {!['COMPLETED', 'PARTIAL', 'FAILED', 'CANCELLED'].includes(restoreRun.status) && <button type="button" className="ui-button-secondary px-3 py-2 text-xs" onClick={() => void apiJson(`${apiUrl}/api/restore/runs/${restoreRun.id}/cancel`, { method: 'POST', headers: { Authorization: `Bearer ${token}` } })}>{t('backup.restoreCancel')}</button>}
    </div>}
    {loading && <p className="text-sm text-[var(--color-text-muted)]">{t('common.loading')}</p>}
    {error && <p className="ui-alert ui-alert-error text-sm" role="alert">{error}</p>}
    {!loading && !error && items.length === 0 && <p className="text-sm text-[var(--color-text-muted)]">{t('backup.emptyFolder')}</p>}
    <ul className="divide-y divide-[var(--color-border-light)]">
      {items.map((item) => <li key={item.relative_path} className="flex items-center">
        <input type="checkbox" className="ml-2" checked={selectedPaths.includes(item.relative_path) || selectedPaths.includes('')} disabled={selectedPaths.includes('')} onChange={() => togglePath(item.relative_path)} aria-label={item.name} />
        {item.is_dir ? <button type="button" onClick={() => { setLoading(true); setError(''); setDirectory(item.relative_path); }} className="flex w-full items-center gap-3 px-2 py-3 text-left hover:bg-[var(--color-hover)]"><FileIcon name={item.name} isDir className="size-5" /><span className="flex-1 text-sm">{item.name}</span><span className="text-xs text-[var(--color-text-muted)]">{t('backup.folder')}</span></button>
          : <div className="flex items-center gap-3 px-2 py-3"><FileIcon name={item.name} className="size-5" /><span className="flex-1 truncate text-sm">{item.name}</span><span className="text-xs text-[var(--color-text-muted)]">{formatBytes(item.size_bytes)}{item.mtime ? ` · ${formatDateTime(item.mtime)}` : ''}</span></div>}
      </li>)}
    </ul>
  </section>;
}
