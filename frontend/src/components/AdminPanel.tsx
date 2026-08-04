import { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { AdjustmentsHorizontalIcon as SlidersHorizontal, ArrowLeftIcon as ArrowLeft, ArrowPathIcon as RefreshCw, ArrowRightIcon as ArrowRight, ChartBarIcon as Activity, CheckCircleIcon as CheckCircle2, DocumentTextIcon as ScrollText, NoSymbolIcon as Ban, PresentationChartBarIcon as BarChart3, ShieldCheckIcon as ShieldCheck, ShieldExclamationIcon as ShieldOff, TrashIcon as Trash2, UsersIcon } from '@heroicons/react/24/outline';
import { useApiError } from '../utils/apiError';
import { adminApi, type AdminUser, type AdminStats, type AuditEntry, type ApiResult } from '../utils/adminApi';
import { useFormat } from '../utils/format';
import { useConfirm } from '../contexts/useConfirm';
import { MessageBanner } from './MessageBanner';
import { apiFetch } from '../utils/apiClient';
import { Toggle } from './Toggle';
import { Badge, StatusBadge } from './StatusBadge';
import { LoadingIndicator } from './LoadingIndicator';

type Tab = 'users' | 'migrations' | 'stats' | 'audit' | 'system';

interface AdminPanelProps {
  apiUrl: string;
  token: string;
  user: { id?: string; role?: string } | null;
  onBack: () => void;
}

const LIMIT = 20;

function SectionCard({ icon: Icon, title, children }: {
  icon: React.ComponentType<{ className?: string }>;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="ui-card p-5 space-y-5">
      <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
        <Icon className="h-4 w-4 text-[var(--color-text-muted)]" />
        <h3 className="font-display font-semibold text-sm text-[var(--color-text-primary)]">{title}</h3>
      </div>
      {children}
    </div>
  );
}

const inputCls = 'ui-input w-full px-3 py-2 text-sm font-sans';
const selectCls = 'ui-input px-3 py-2 text-sm font-sans';
const primaryBtnCls = 'ui-button-primary px-3 py-2 text-sm font-medium';
const secondaryBtnCls = 'ui-button-secondary px-3 py-2 text-sm';

export function AdminPanel({ apiUrl, token, user, onBack }: AdminPanelProps) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const { formatBytes, formatDateTime } = useFormat();

  const [tab, setTab] = useState<Tab>('users');
  const [message, setMessage] = useState<{ text: string; type: 'success' | 'error' } | null>(null);

  const showError = useCallback((errorCode: string) => {
    setMessage({ text: translateApiError(errorCode), type: 'error' });
  }, [translateApiError]);

  const tabs = [
    ['users', UsersIcon, 'admin.tabs.users'],
    ['migrations', Activity, 'admin.tabs.migrations'],
    ['stats', BarChart3, 'admin.tabs.stats'],
    ['audit', ScrollText, 'admin.tabs.audit'],
    ['system', SlidersHorizontal, 'admin.tabs.system'],
  ] as const;

  return (
    <div className="max-w-5xl w-full mx-auto my-4 space-y-6">
      {/* Back Header */}
      <div className="flex items-center justify-between pb-4 border-b border-[var(--color-border)]/50">
        <button
          onClick={onBack}
          className="ui-button-secondary flex items-center gap-2 px-3 py-2 font-medium text-sm cursor-pointer hover:bg-[var(--color-bg-tertiary)]"
        >
          <ArrowLeft className="w-4 h-4" />
          {t('common.back')}
        </button>
        <div className="flex items-center gap-2">
          <ShieldCheck className="w-5 h-5 text-[var(--color-text-primary)]" />
          <h1 className="font-display font-semibold text-xl text-[var(--color-text-primary)] leading-none">{t('admin.title')}</h1>
        </div>
      </div>

      {message && <MessageBanner message={message} />}

      {/* Administration tabs */}
      <div
        className="flex flex-wrap gap-2"
        role="tablist"
        aria-label={t('admin.title')}
        onKeyDown={(event) => {
          if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
          event.preventDefault();
          const current = tabs.findIndex(([value]) => value === tab);
          const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (current + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
          const nextTab = tabs[next][0];
          setTab(nextTab);
          document.getElementById(`admin-tab-${nextTab}`)?.focus();
        }}
      >
        {tabs.map(([value, Icon, label]) => (
          <button
            key={value}
            type="button"
            onClick={() => setTab(value)}
            id={`admin-tab-${value}`}
            role="tab"
            aria-selected={tab === value}
            aria-controls={`admin-panel-${value}`}
            tabIndex={tab === value ? 0 : -1}
            className={`flex items-center gap-1.5 px-4 py-2 border font-medium text-sm ${
              tab === value
                ? 'ui-button-primary border-[var(--color-bg-inverse)]'
                : 'ui-button-secondary hover:bg-[var(--color-bg-tertiary)]'
            }`}
          >
            <Icon className="w-4 h-4" aria-hidden="true" />
            {t(label)}
          </button>
        ))}
      </div>

      <div id={`admin-panel-${tab}`} role="tabpanel" aria-labelledby={`admin-tab-${tab}`} className="min-h-[60vh]">
        {tab === 'users' && (
          <UsersTab apiUrl={apiUrl} token={token} currentUserID={user?.id} onMessage={setMessage} onError={showError} />
        )}
        {tab === 'migrations' && <MigrationsTab apiUrl={apiUrl} token={token} formatBytes={formatBytes} formatDateTime={formatDateTime} />}
        {tab === 'stats' && <StatsTab apiUrl={apiUrl} token={token} />}
        {tab === 'audit' && <AuditTab apiUrl={apiUrl} token={token} formatDateTime={formatDateTime} />}
        {tab === 'system' && <SystemTab apiUrl={apiUrl} token={token} onMessage={setMessage} />}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Users tab
// ---------------------------------------------------------------------------

function UsersTab({ apiUrl, token, currentUserID, onMessage, onError }: {
  apiUrl: string; token: string; currentUserID?: string;
  onMessage: (m: { text: string; type: 'success' | 'error' } | null) => void;
  onError: (errorCode: string) => void;
}) {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const { formatDateTime } = useFormat();
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [roleFilter, setRoleFilter] = useState('');
  const [activeFilter, setActiveFilter] = useState('');
  const [q, setQ] = useState('');
  const [qInput, setQInput] = useState('');
  const [loading, setLoading] = useState(false);

  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ email: '', display_name: '', password: '', role: 'USER', must_change_password: true });

  useEffect(() => {
    const tmr = setTimeout(() => {
      setQ(qInput);
      setPage(1);
    }, 300);
    return () => clearTimeout(tmr);
  }, [qInput]);

  const load = async () => {
    setLoading(true);
    const res = await adminApi.listUsers(apiUrl, token, {
      page, limit: LIMIT, role: roleFilter || undefined, active: activeFilter || undefined, q: q || undefined,
    });
    setLoading(false);
    if (res.ok) {
      setUsers(res.data?.users ?? []);
      setTotal(res.data?.total ?? 0);
    } else {
      onError(res.errorCode);
    }
  };

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      const res = await adminApi.listUsers(apiUrl, token, {
        page, limit: LIMIT, role: roleFilter || undefined, active: activeFilter || undefined, q: q || undefined,
      });
      setLoading(false);
      if (res.ok && !cancelled) {
        setUsers(res.data?.users ?? []);
        setTotal(res.data?.total ?? 0);
      } else if (!cancelled) {
        onError(res.errorCode);
      }
    })();
    return () => { cancelled = true; };
  }, [apiUrl, token, page, roleFilter, activeFilter, q, onError]);

  const act = async <T,>(fn: () => Promise<ApiResult<T>>, successKey: string) => {
    const res = await fn();
    if (res.ok) {
      onMessage({ text: t(successKey), type: 'success' });
      load();
    } else {
      onError(res.errorCode);
    }
  };

  const create = async () => {
    if (!form.email || !form.password || !form.display_name) {
      onMessage({ text: t('auth.fillAllFields'), type: 'error' });
      return;
    }
    if (form.password.length < 12) {
      onMessage({ text: t('reset.tooShort'), type: 'error' });
      return;
    }
    const res = await adminApi.createUser(apiUrl, token, form);
    if (res.ok) {
      onMessage({ text: t('admin.users.created'), type: 'success' });
      setShowCreate(false);
      setForm({ email: '', display_name: '', password: '', role: 'USER', must_change_password: true });
      load();
    } else {
      onError(res.errorCode);
    }
  };

  const pages = Math.max(1, Math.ceil(total / LIMIT));

  return (
    <SectionCard icon={UsersIcon} title={t('admin.tabs.users')}>
      <div className="flex flex-wrap items-center gap-2">
          <input
          value={qInput}
          onChange={(e) => setQInput(e.target.value)}
            placeholder={t('common.search')}
            aria-label={t('common.search')}
          className={inputCls}
        />
        <select value={roleFilter} aria-label={t('admin.users.allRoles')} onChange={(e) => { setRoleFilter(e.target.value); setPage(1); }}
          className={selectCls}>
          <option value="">{t('admin.users.allRoles')}</option>
          <option value="USER">USER</option>
          <option value="ADMIN">ADMIN</option>
        </select>
        <select value={activeFilter} aria-label={t('admin.users.allStates')} onChange={(e) => { setActiveFilter(e.target.value); setPage(1); }}
          className={selectCls}>
          <option value="">{t('admin.users.allStates')}</option>
          <option value="true">{t('common.active')}</option>
          <option value="false">{t('admin.users.suspended')}</option>
        </select>
        <button onClick={() => setShowCreate((v) => !v)}
          className="ui-button-primary ml-auto px-3 py-2 text-sm font-medium">
          {t('admin.users.create')}
        </button>
      </div>

      {showCreate && (
        <form onSubmit={(event) => { event.preventDefault(); void create(); }} className="grid grid-cols-1 md:grid-cols-2 gap-3 p-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-tertiary)]/40">
          <label className="space-y-1 text-sm text-[var(--color-text-secondary)]"><span>{t('auth.email')}</span><input type="email" autoComplete="email" name="email" required value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} className={inputCls} /></label>
          <label className="space-y-1 text-sm text-[var(--color-text-secondary)]"><span>{t('auth.name')}</span><input type="text" autoComplete="name" name="display_name" required value={form.display_name} onChange={(e) => setForm({ ...form, display_name: e.target.value })} className={inputCls} /></label>
          <label className="space-y-1 text-sm text-[var(--color-text-secondary)]"><span>{t('auth.password')}</span><input type="password" autoComplete="new-password" name="password" minLength={12} required value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} className={inputCls} /></label>
          <label className="space-y-1 text-sm text-[var(--color-text-secondary)]"><span>{t('admin.users.role')}</span><select value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })} className={selectCls}><option value="USER">USER</option><option value="ADMIN">ADMIN</option></select></label>
          <label className="flex items-center gap-2 text-xs text-[var(--color-text-secondary)] md:col-span-2">
            <input type="checkbox" checked={form.must_change_password} onChange={(e) => setForm({ ...form, must_change_password: e.target.checked })} />
            {t('admin.users.forcePasswordChange')}
          </label>
          <div className="md:col-span-2 flex justify-end gap-2">
             <button type="button" onClick={() => setShowCreate(false)} className={secondaryBtnCls}>{t('common.cancel')}</button>
             <button type="submit" className={primaryBtnCls}>{t('common.save')}</button>
           </div>
         </form>
      )}

      <div className="overflow-x-auto rounded-lg border border-[var(--color-border)]">
        <table className="ui-responsive-table w-full text-xs">
          <thead className="bg-[var(--color-bg-tertiary)]/60 text-[var(--color-text-muted)]">
            <tr>
              <th className="text-left px-3 py-2 font-semibold">{t('auth.email')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('auth.name')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.users.role')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('common.active')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.users.createdAt')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.users.lastLoginAt')}</th>
              <th className="text-right px-3 py-2 font-semibold">{t('migrations.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id} className="border-t border-[var(--color-border)]">
                <td data-label={t('auth.email')} className="px-3 py-2">{u.email}</td>
                <td data-label={t('auth.name')} className="px-3 py-2">{u.display_name}</td>
                <td data-label={t('admin.users.role')} className="px-3 py-2">
                  <Badge size="sm" variant="muted" label={u.role} />
                </td>
                <td data-label={t('common.active')} className="px-3 py-2">
                  {u.active
                    ? <Badge size="sm" variant="success" label={t('common.active')} />
                    : <Badge size="sm" variant="error" label={t('admin.users.suspended')} />}
                </td>
                <td data-label={t('admin.users.createdAt')} className="px-3 py-2 text-[var(--color-text-muted)]">{u.created_at ? formatDateTime(u.created_at) : ''}</td>
                <td data-label={t('admin.users.lastLoginAt')} className="px-3 py-2 text-[var(--color-text-muted)]">{u.last_login_at ? formatDateTime(u.last_login_at) : t('admin.users.neverLoggedIn')}</td>
                <td data-label={t('migrations.actions')} className="px-3 py-2">
                  <div className="flex justify-end gap-1.5">
                    {u.active ? (
                      <button type="button" aria-label={t('admin.users.suspend')} onClick={() => act(() => adminApi.suspendUser(apiUrl, token, u.id!), 'admin.users.suspendedOk')}
                        className="ui-button-secondary p-1.5 text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)]"><Ban className="w-3.5 h-3.5" /></button>
                    ) : (
                      <button type="button" aria-label={t('admin.users.reactivate')} onClick={() => act(() => adminApi.reactivateUser(apiUrl, token, u.id!), 'admin.users.reactivatedOk')}
                        className="ui-button-secondary p-1.5 text-[var(--color-success-text)] hover:bg-[var(--color-success-bg)]"><CheckCircle2 className="w-3.5 h-3.5" /></button>
                    )}
                    <button type="button" aria-label={t('admin.users.toggleRole')} onClick={() => act(() => adminApi.updateRole(apiUrl, token, u.id!, u.role === 'ADMIN' ? 'USER' : 'ADMIN'), 'admin.users.roleChanged')}
                      className="ui-button-secondary p-1.5">
                      {u.role === 'ADMIN' ? <ShieldOff className="w-3.5 h-3.5" /> : <ShieldCheck className="w-3.5 h-3.5" />}
                    </button>
                    {u.id !== currentUserID && (
                      <button type="button" aria-label={t('admin.users.delete')} onClick={() => { void (async () => { if (await confirm({ message: t('admin.users.deleteConfirm') })) act(() => adminApi.deleteUser(apiUrl, token, u.id!), 'admin.users.deletedOk'); })(); }}
                        className="ui-button-secondary p-1.5 text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)]"><Trash2 className="w-3.5 h-3.5" /></button>
                    )}
                  </div>
                </td>
              </tr>
            ))}
            {!loading && users.length === 0 && (
              <tr><td colSpan={7} className="px-3 py-6 text-center text-[var(--color-text-muted)]">{t('migrations.dbEmpty')}</td></tr>
            )}
          </tbody>
        </table>
      </div>

      <Pager page={page} pages={pages} onPage={setPage} />
    </SectionCard>
  );
}

// ---------------------------------------------------------------------------
// Migrations & Syncs tab (Transfers)
// ---------------------------------------------------------------------------

interface UnifiedTransfer {
  id: string;
  type: 'MIGRATION' | 'SYNC';
  owner_email: string;
  status: string;
  source_provider: string;
  target_provider: string;
  processed_files: number;
  total_files: number;
  processed_bytes: number;
  created_at: string;
}

function MigrationsTab({ apiUrl, token, formatBytes, formatDateTime }: {
  apiUrl: string; token: string;
  formatBytes: (n: number) => string; formatDateTime: (iso: string) => string;
}) {
  const { t } = useTranslation();
  const [items, setItems] = useState<UnifiedTransfer[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      const [mRes, sRes] = await Promise.all([
        adminApi.listMigrations(apiUrl, token, { page, limit: LIMIT }),
        adminApi.listSyncs(apiUrl, token, { page, limit: LIMIT }),
      ]);
      setLoading(false);
      if (cancelled) return;

      const mItems: UnifiedTransfer[] = (mRes.ok ? mRes.data?.migrations ?? [] : []).map((m) => ({
        id: m.id,
        type: 'MIGRATION',
        owner_email: m.owner_email,
        status: m.status,
        source_provider: m.source_provider,
        target_provider: m.target_provider,
        processed_files: m.processed_files,
        total_files: m.total_files,
        processed_bytes: m.processed_bytes,
        created_at: m.created_at,
      }));

      const sItems: UnifiedTransfer[] = (sRes.ok ? sRes.data?.syncs ?? [] : []).map((s) => ({
        id: s.id,
        type: 'SYNC',
        owner_email: s.owner_email,
        status: s.status,
        source_provider: s.source_provider,
        target_provider: s.target_provider,
        processed_files: s.processed_files,
        total_files: s.total_files,
        processed_bytes: s.processed_bytes,
        created_at: s.created_at,
      }));

      const combined = [...mItems, ...sItems].sort(
        (a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
      );

      setItems(combined);
      setTotal((mRes.ok ? mRes.data?.total ?? 0 : 0) + (sRes.ok ? sRes.data?.total ?? 0 : 0));
    })();
    return () => { cancelled = true; };
  }, [apiUrl, token, page]);

  const pages = Math.max(1, Math.ceil(total / LIMIT));

  return (
    <SectionCard icon={Activity} title={t('admin.tabs.migrations')}>
      <div className="overflow-x-auto rounded-lg border border-[var(--color-border)]">
        <table className="ui-responsive-table w-full text-xs">
          <thead className="bg-[var(--color-bg-tertiary)]/60 text-[var(--color-text-muted)]">
            <tr>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.migrations.owner')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.transfers.type')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('migrations.status')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.migrations.sourceTarget')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('dashboard.progress')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.migrations.created')}</th>
            </tr>
          </thead>
          <tbody>
            {items.map((m) => (
              <tr key={m.id} className="border-t border-[var(--color-border)]">
                <td data-label={t('admin.migrations.owner')} className="px-3 py-2">{m.owner_email || <span className="text-[var(--color-text-muted)]">—</span>}</td>
                <td data-label={t('admin.transfers.type')} className="px-3 py-2">
                  <Badge size="sm" variant={m.type === 'SYNC' ? 'info' : 'muted'} label={m.type === 'SYNC' ? t('admin.transfers.sync') : t('admin.transfers.migration')} />
                </td>
                <td data-label={t('migrations.status')} className="px-3 py-2"><StatusBadge status={m.status} size="sm" /></td>
                <td data-label={t('admin.migrations.sourceTarget')} className="px-3 py-2">{m.source_provider} → {m.target_provider}</td>
                <td data-label={t('dashboard.progress')} className="px-3 py-2">{m.processed_files}/{m.total_files} · {formatBytes(m.processed_bytes)}</td>
                <td data-label={t('admin.migrations.created')} className="px-3 py-2 text-[var(--color-text-muted)]">{formatDateTime(m.created_at)}</td>
              </tr>
            ))}
            {!loading && items.length === 0 && (
              <tr><td colSpan={6} className="px-3 py-6 text-center text-[var(--color-text-muted)]">{t('migrations.dbEmpty')}</td></tr>
            )}
          </tbody>
        </table>
      </div>
      <Pager page={page} pages={pages} onPage={setPage} />
    </SectionCard>
  );
}

// ---------------------------------------------------------------------------
// Stats tab
// ---------------------------------------------------------------------------

function StatsTab({ apiUrl, token }: { apiUrl: string; token: string }) {
  const { t } = useTranslation();
  const [stats, setStats] = useState<AdminStats | null>(null);

  useEffect(() => {
    (async () => {
      const res = await adminApi.stats(apiUrl, token);
      if (res.ok) setStats(res.data ?? null);
    })();
  }, [apiUrl, token]);

  if (!stats) return <div className="flex justify-center py-8"><LoadingIndicator label={t('common.loading')} /></div>;

  const card = (label: string, value: number | string) => (
    <div className="p-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-tertiary)]/40">
      <div className="text-2xl font-display font-semibold text-[var(--color-text-primary)]">{value}</div>
      <div className="text-xs uppercase tracking-wider text-[var(--color-text-muted)] mt-1">{label}</div>
    </div>
  );

  const totalSyncs = Object.values(stats.syncs_by_status || {}).reduce((a, b) => a + b, 0);

  return (
    <SectionCard icon={BarChart3} title={t('admin.tabs.stats')}>
      <div className="grid grid-cols-2 md:grid-cols-5 gap-3">
        {card(t('admin.stats.totalUsers'), stats.total_users)}
        {card(t('admin.stats.activeUsers'), stats.active_users)}
        {card(t('admin.stats.totalMigrations'), Object.values(stats.migrations_by_status).reduce((a, b) => a + b, 0))}
        {card(t('admin.stats.totalSyncs'), totalSyncs)}
        {card(t('admin.stats.totalTasks'), Object.values(stats.tasks_by_status).reduce((a, b) => a + b, 0))}
      </div>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div className="p-4 rounded-lg border border-[var(--color-border)]">
          <div className="text-xs font-bold text-[var(--color-text-primary)] mb-3">{t('admin.stats.migrationsByStatus')}</div>
          <div className="space-y-1.5">
            {Object.entries(stats.migrations_by_status).map(([k, v]) => (
              <div key={k} className="flex items-center justify-between text-xs">
                <StatusBadge status={k} size="sm" />
                <span className="font-mono">{v}</span>
              </div>
            ))}
            {Object.keys(stats.migrations_by_status).length === 0 && <div className="text-[var(--color-text-muted)]">—</div>}
          </div>
        </div>
        <div className="p-4 rounded-lg border border-[var(--color-border)]">
          <div className="text-xs font-bold text-[var(--color-text-primary)] mb-3">{t('admin.stats.syncsByStatus')}</div>
          <div className="space-y-1.5">
            {Object.entries(stats.syncs_by_status || {}).map(([k, v]) => (
              <div key={k} className="flex items-center justify-between text-xs">
                <StatusBadge status={k} size="sm" />
                <span className="font-mono">{v}</span>
              </div>
            ))}
            {Object.keys(stats.syncs_by_status || {}).length === 0 && <div className="text-[var(--color-text-muted)]">—</div>}
          </div>
        </div>
        <div className="p-4 rounded-lg border border-[var(--color-border)]">
          <div className="text-xs font-bold text-[var(--color-text-primary)] mb-3">{t('admin.stats.tasksByStatus')}</div>
          <div className="space-y-1.5">
            {Object.entries(stats.tasks_by_status).map(([k, v]) => (
              <div key={k} className="flex items-center justify-between text-xs">
                <StatusBadge status={k} size="sm" />
                <span className="font-mono">{v}</span>
              </div>
            ))}
            {Object.keys(stats.tasks_by_status).length === 0 && <div className="text-[var(--color-text-muted)]">—</div>}
          </div>
        </div>
      </div>
    </SectionCard>
  );
}

// ---------------------------------------------------------------------------
// Audit tab
// ---------------------------------------------------------------------------

function AuditTab({ apiUrl, token, formatDateTime }: {
  apiUrl: string; token: string; formatDateTime: (iso: string) => string;
}) {
  const { t } = useTranslation();
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [action, setAction] = useState('');
  const [userID, setUserID] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      const res = await adminApi.auditLog(apiUrl, token, {
        page, limit: LIMIT, action: action || undefined, user_id: userID || undefined, from: from || undefined, to: to || undefined,
      });
      setLoading(false);
      if (res.ok && !cancelled) {
        setEntries(res.data?.entries ?? []);
        setTotal(res.data?.total ?? 0);
      }
    })();
    return () => { cancelled = true; };
  }, [apiUrl, token, page, action, userID, from, to]);

  const pages = Math.max(1, Math.ceil(total / LIMIT));
  const actions: string[] = [
    'LOGIN_SUCCESS', 'LOGIN_FAILED', 'REGISTRATION', 'USER_CREATED', 'USER_SUSPENDED', 'USER_REACTIVATED',
    'USER_DELETED', 'USER_ROLE_CHANGED', 'MIGRATION_CREATED', 'MIGRATION_STARTED', 'MIGRATION_COMPLETED',
    'MIGRATION_FAILED', 'MIGRATION_PAUSED', 'MIGRATION_RESUMED', 'MIGRATION_CANCELLED', 'MIGRATION_DELETED',
    'SYNC_CREATED', 'SYNC_STARTED', 'SYNC_COMPLETED', 'SYNC_FAILED', 'SYNC_PAUSED', 'SYNC_RESUMED', 'SYNC_DELETED',
    'SETTING_UPDATED', '2FA_ENABLED', '2FA_DISABLED',
  ];

  return (
    <SectionCard icon={ScrollText} title={t('admin.tabs.audit')}>
      <div className="flex flex-wrap items-center gap-2">
        <select value={action} aria-label={t('admin.audit.allActions')} onChange={(e) => { setAction(e.target.value); setPage(1); }}
          className={selectCls}>
          <option value="">{t('admin.audit.allActions')}</option>
          {actions.map((a) => <option key={a} value={a}>{t(`admin.audit.actions.${a}`)}</option>)}
        </select>
        <input value={userID} aria-label={t('admin.audit.userId')} onChange={(e) => { setUserID(e.target.value); setPage(1); }} placeholder={t('admin.audit.userId')}
          className="ui-input w-44 px-3 py-2 text-sm font-sans" />
        <input type="date" aria-label={t('admin.audit.when')} value={from} onChange={(e) => { setFrom(e.target.value); setPage(1); }}
          className={selectCls} />
        <input type="date" aria-label={t('admin.audit.when')} value={to} onChange={(e) => { setTo(e.target.value); setPage(1); }}
          className={selectCls} />
        <button type="button" aria-label={t('common.refresh')} onClick={() => { setPage(1); }} className="ui-button-secondary p-1.5"><RefreshCw className="w-3.5 h-3.5" /></button>
      </div>

      <div className="overflow-x-auto rounded-lg border border-[var(--color-border)]">
        <table className="ui-responsive-table w-full text-xs">
          <thead className="bg-[var(--color-bg-tertiary)]/60 text-[var(--color-text-muted)]">
            <tr>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.audit.when')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.audit.action')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.audit.actor')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.audit.target')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.audit.ip')}</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e) => (
              <tr key={e.id} className="border-t border-[var(--color-border)]">
                <td data-label={t('admin.audit.when')} className="px-3 py-2 text-[var(--color-text-muted)] whitespace-nowrap">{formatDateTime(e.created_at)}</td>
                <td data-label={t('admin.audit.action')} className="px-3 py-2"><Badge size="sm" variant="muted" label={t(`admin.audit.actions.${e.action}`)} /></td>
                <td data-label={t('admin.audit.actor')} className="px-3 py-2 font-mono text-xs">{e.user_id ? e.user_id.slice(0, 8) : '—'}</td>
                <td data-label={t('admin.audit.target')} className="px-3 py-2 font-mono text-xs max-w-[160px] truncate" title={e.target}>{e.target || '—'}</td>
                <td data-label={t('admin.audit.ip')} className="px-3 py-2 font-mono text-xs">{e.ip || '—'}</td>
              </tr>
            ))}
            {!loading && entries.length === 0 && (
              <tr><td colSpan={5} className="px-3 py-6 text-center text-[var(--color-text-muted)]">{t('migrations.dbEmpty')}</td></tr>
            )}
          </tbody>
        </table>
      </div>

      <Pager page={page} pages={pages} onPage={setPage} />
    </SectionCard>
  );
}

// ---------------------------------------------------------------------------
// System tab
// ---------------------------------------------------------------------------

function SystemTab({ apiUrl, token, onMessage }: {
  apiUrl: string; token: string;
  onMessage: (m: { text: string; type: 'success' | 'error' } | null) => void;
}) {
  const { t } = useTranslation();
  const translateApiError = useApiError();

  const [registrationsEnabled, setRegistrationsEnabled] = useState<boolean>(false);
  const [loading, setLoading] = useState<boolean>(false);
  const [message, setMessage] = useState<{ text: string; type: 'success' | 'error' } | null>(null);

  useEffect(() => {
    let cancelled = false;
    apiFetch(`${apiUrl}/api/settings`)
      .then((res) => res.json())
      .then((data) => {
        if (!cancelled) setRegistrationsEnabled(data.registrations_enabled === 'true');
      })
      .catch((err) => {
        console.error('Failed to fetch settings:', err);
      });
    return () => { cancelled = true; };
  }, [apiUrl]);

  const handleToggleRegistrations = async (checked: boolean) => {
    setMessage(null);
    setLoading(true);
    try {
      const res = await apiFetch(`${apiUrl}/api/settings`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify({
          key: 'registrations_enabled',
          value: checked ? 'true' : 'false',
        }),
      });

      if (!res.ok) {
        const data = await res.json().catch(() => ({}) as { error_code?: string });
        throw new Error(translateApiError(data.error_code));
      }

      setRegistrationsEnabled(checked);
      onMessage({
        text: checked ? t('settings.messages.adminSavedOn') : t('settings.messages.adminSavedOff'),
        type: 'success',
      });
    } catch (err) {
      setMessage({ text: (err as Error).message, type: 'error' });
    } finally {
      setLoading(false);
    }
  };

  return (
    <SectionCard icon={SlidersHorizontal} title={t('admin.system.title')}>
      <MessageBanner message={message} />

      <div className="flex items-center justify-between p-3.5 bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)]/50 rounded-lg">
        <div className="text-left space-y-1 pr-4">
          <h4 className="text-xs font-bold text-[var(--color-text-primary)] font-display">{t('settings.allowRegistrations')}</h4>
          <p className="text-[10px] text-[var(--color-text-muted)] leading-normal">
            {t('settings.allowRegistrationsHint')}
          </p>
        </div>
        <Toggle
          checked={registrationsEnabled}
          disabled={loading}
          onChange={handleToggleRegistrations}
          label={t('settings.allowRegistrations')}
        />
      </div>

      <SMTPSettingsCard apiUrl={apiUrl} token={token} onMessage={onMessage} />
    </SectionCard>
  );
}

function SMTPSettingsCard({ apiUrl, token, onMessage }: { apiUrl: string; token: string; onMessage: (m: { text: string; type: 'success' | 'error' } | null) => void }) {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const confirm = useConfirm();
  const onMessageRef = useRef(onMessage);
  const translateApiErrorRef = useRef(translateApiError);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [configured, setConfigured] = useState(false);
  const [passwordSet, setPasswordSet] = useState(false);
  const [form, setForm] = useState({ smtp_host: '', smtp_port: '587', smtp_username: '', smtp_password: '', smtp_from_email: '', smtp_from_name: '', smtp_encryption: 'starttls' as 'tls' | 'starttls' });

  useEffect(() => {
    onMessageRef.current = onMessage;
    translateApiErrorRef.current = translateApiError;
  }, [onMessage, translateApiError]);

  const load = useCallback(async () => {
    setLoading(true);
    const result = await adminApi.getSMTP(apiUrl, token);
    if (!result.ok) { onMessageRef.current({ text: translateApiErrorRef.current(result.errorCode), type: 'error' }); setLoading(false); return; }
    const cfg = result.data!;
    setConfigured(cfg.configured);
    setPasswordSet(cfg.smtp_password_set);
    setForm({ smtp_host: cfg.smtp_host ?? '', smtp_port: String(cfg.smtp_port ?? 587), smtp_username: cfg.smtp_username ?? '', smtp_password: '', smtp_from_email: cfg.smtp_from_email ?? '', smtp_from_name: cfg.smtp_from_name ?? '', smtp_encryption: cfg.smtp_encryption ?? 'starttls' });
    setLoading(false);
  }, [apiUrl, token]);

  useEffect(() => {
    const timer = window.setTimeout(() => { void load(); }, 0);
    return () => window.clearTimeout(timer);
  }, [load]);
  const update = (key: keyof typeof form, value: string) => setForm((current) => ({ ...current, [key]: value }));
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    const port = Number(form.smtp_port);
    if (!Number.isInteger(port) || port < 1 || port > 65535) { onMessage({ text: t('settings.messages.smtpPortRange'), type: 'error' }); return; }
    setSaving(true);
    const body = { ...form, smtp_port: port };
    if (!body.smtp_password) delete (body as { smtp_password?: string }).smtp_password;
    const result = await adminApi.updateSMTP(apiUrl, token, body);
    setSaving(false);
    if (!result.ok) { onMessage({ text: translateApiError(result.errorCode), type: 'error' }); return; }
    setConfigured(true); setPasswordSet(true); setForm((current) => ({ ...current, smtp_password: '' }));
    onMessage({ text: t('admin.system.smtp.saved'), type: 'success' });
  };
  const test = async () => { setSaving(true); const result = await adminApi.testSMTP(apiUrl, token); setSaving(false); onMessage(result.ok ? { text: t('admin.system.smtp.testSent'), type: 'success' } : { text: translateApiError(result.errorCode), type: 'error' }); };
  const remove = async () => { if (!await confirm({ message: t('admin.system.smtp.removeConfirm'), confirmLabel: t('admin.system.smtp.remove') })) return; setSaving(true); const result = await adminApi.deleteSMTP(apiUrl, token); setSaving(false); if (!result.ok) { onMessage({ text: translateApiError(result.errorCode), type: 'error' }); return; } setConfigured(false); setPasswordSet(false); setForm({ smtp_host: '', smtp_port: '587', smtp_username: '', smtp_password: '', smtp_from_email: '', smtp_from_name: '', smtp_encryption: 'starttls' }); onMessage({ text: t('admin.system.smtp.removed'), type: 'success' }); };

  return <div className="border-t border-[var(--color-border-light)] pt-5 space-y-4">
    <div className="flex items-center justify-between gap-3"><h4 className="font-display font-semibold text-sm text-[var(--color-text-primary)]">{t('admin.system.smtp.title')}</h4><Badge variant={configured ? 'success' : 'muted'} label={configured ? t('admin.system.smtp.configured') : t('admin.system.smtp.notConfigured')} /></div>
    {!loading && <form onSubmit={submit} className="grid grid-cols-1 gap-3 sm:grid-cols-2">
      <label className="text-xs text-[var(--color-text-secondary)]">{t('settings.smtpHost')}<input required value={form.smtp_host} onChange={(e) => update('smtp_host', e.target.value)} className={`${inputCls} mt-1`} /></label>
      <label className="text-xs text-[var(--color-text-secondary)]">{t('settings.smtpPort')}<input required type="number" min="1" max="65535" value={form.smtp_port} onChange={(e) => update('smtp_port', e.target.value)} className={`${inputCls} mt-1`} /></label>
      <label className="text-xs text-[var(--color-text-secondary)]">{t('settings.smtpUsername')}<input required value={form.smtp_username} onChange={(e) => update('smtp_username', e.target.value)} className={`${inputCls} mt-1`} /></label>
      <label className="text-xs text-[var(--color-text-secondary)]">{t('settings.smtpPassword')}<input type="password" required={!passwordSet} value={form.smtp_password} placeholder={passwordSet ? t('settings.smtpPasswordUnchanged') : ''} onChange={(e) => update('smtp_password', e.target.value)} className={`${inputCls} mt-1`} /></label>
      <label className="text-xs text-[var(--color-text-secondary)]">{t('settings.smtpFromEmail')}<input required type="email" value={form.smtp_from_email} onChange={(e) => update('smtp_from_email', e.target.value)} className={`${inputCls} mt-1`} /></label>
      <label className="text-xs text-[var(--color-text-secondary)]">{t('settings.smtpFromName')}<input value={form.smtp_from_name} onChange={(e) => update('smtp_from_name', e.target.value)} className={`${inputCls} mt-1`} /></label>
      <label className="text-xs text-[var(--color-text-secondary)]">{t('settings.smtpEncryption')}<select value={form.smtp_encryption} onChange={(e) => update('smtp_encryption', e.target.value)} className={`${selectCls} mt-1 w-full`}><option value="starttls">STARTTLS</option><option value="tls">TLS</option></select></label>
      <div className="flex flex-wrap items-end gap-2"><button disabled={saving} className={primaryBtnCls}>{t('admin.system.smtp.save')}</button>{configured && <><button type="button" disabled={saving} onClick={() => void test()} className={secondaryBtnCls}>{t('admin.system.smtp.test')}</button><button type="button" disabled={saving} onClick={() => void remove()} className="ui-button-danger px-3 py-2 text-sm">{t('admin.system.smtp.remove')}</button></>}</div>
    </form>}
  </div>;
}

// ---------------------------------------------------------------------------
// Shared pager
// ---------------------------------------------------------------------------

function Pager({ page, pages, onPage }: { page: number; pages: number; onPage: (p: number) => void }) {
  const { t } = useTranslation();
  const { formatNumber } = useFormat();

  return (
    <div className="flex items-center justify-between text-xs">
      <button
        type="button"
        aria-label={t('common.previousPage')}
        title={t('common.previousPage')}
        disabled={page <= 1}
        onClick={() => onPage(page - 1)}
        className="ui-button-secondary px-3 py-1.5 disabled:opacity-40 hover:bg-[var(--color-bg-tertiary)] transition-colors"
      >
        <ArrowLeft className="w-4 h-4" />
      </button>
      <span className="text-[var(--color-text-muted)] font-mono">{formatNumber(page)} / {formatNumber(pages)}</span>
      <button
        type="button"
        aria-label={t('common.nextPage')}
        title={t('common.nextPage')}
        disabled={page >= pages}
        onClick={() => onPage(page + 1)}
        className="ui-button-secondary px-3 py-1.5 disabled:opacity-40 hover:bg-[var(--color-bg-tertiary)] transition-colors"
      >
        <ArrowRight className="w-4 h-4" />
      </button>
    </div>
  );
}
