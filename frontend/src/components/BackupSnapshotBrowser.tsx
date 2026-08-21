import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { FileIcon } from './FileIcon';
import { useFormat } from '../utils/format';
import { apiJson } from '../utils/apiClient';

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

export function BackupSnapshotBrowser({ apiUrl, token, jobID, onBack }: Props) {
  const { t } = useTranslation();
  const { formatBytes, formatDateTime } = useFormat();
  const [snapshots, setSnapshots] = useState<Snapshot[]>([]);
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [directory, setDirectory] = useState('');
  const [items, setItems] = useState<SnapshotItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

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

  if (!snapshot) {
    return <section className="ui-card p-5" aria-label={t('backup.browseSnapshots')}>
      <div className="mb-4 flex items-center justify-between gap-3">
        <h2 className="font-display text-lg font-bold">{t('backup.browseSnapshots')}</h2>
        <button type="button" className="ui-button-secondary px-3 py-2 text-xs" onClick={onBack}>{t('common.back')}</button>
      </div>
      {loading && <p className="text-sm text-[var(--color-text-muted)]">{t('common.loading')}</p>}
      {error && <p className="ui-alert ui-alert-error text-sm" role="alert">{error}</p>}
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
    {loading && <p className="text-sm text-[var(--color-text-muted)]">{t('common.loading')}</p>}
    {error && <p className="ui-alert ui-alert-error text-sm" role="alert">{error}</p>}
    {!loading && !error && items.length === 0 && <p className="text-sm text-[var(--color-text-muted)]">{t('backup.emptyFolder')}</p>}
    <ul className="divide-y divide-[var(--color-border-light)]">
      {items.map((item) => <li key={item.relative_path}>
        {item.is_dir ? <button type="button" onClick={() => { setLoading(true); setError(''); setDirectory(item.relative_path); }} className="flex w-full items-center gap-3 px-2 py-3 text-left hover:bg-[var(--color-hover)]"><FileIcon name={item.name} isDir className="size-5" /><span className="flex-1 text-sm">{item.name}</span><span className="text-xs text-[var(--color-text-muted)]">{t('backup.folder')}</span></button>
          : <div className="flex items-center gap-3 px-2 py-3"><FileIcon name={item.name} className="size-5" /><span className="flex-1 truncate text-sm">{item.name}</span><span className="text-xs text-[var(--color-text-muted)]">{formatBytes(item.size_bytes)}{item.mtime ? ` · ${formatDateTime(item.mtime)}` : ''}</span></div>}
      </li>)}
    </ul>
  </section>;
}
