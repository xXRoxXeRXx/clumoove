import { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowLeft, Users as UsersIcon, Activity, BarChart3, ScrollText, UserPlus, Ban, CheckCircle2, Trash2, ShieldCheck, ShieldOff, RefreshCw, CloudSync, SlidersHorizontal } from 'lucide-react';
import { useApiError } from '../utils/apiError';
import { adminApi, type AdminUser, type AdminStats, type AdminMigration, type AuditEntry, type ApiResult } from '../utils/adminApi';
import { useFormat } from '../utils/format';
import { Toggle } from './Toggle';

type Tab = 'users' | 'migrations' | 'stats' | 'audit' | 'system';

interface AdminPanelProps {
  apiUrl: string;
  token: string;
  user: { id?: string; role?: string } | null;
  onBack: () => void;
}

const LIMIT = 20;

function MessageBanner({ message }: { message: { text: string; type: 'success' | 'error' } | null }) {
  if (!message) return null;
  return (
    <div className={`p-3 rounded-xl border text-[11px] font-mono text-center leading-relaxed ${
      message.type === 'success' ? 'bg-emerald-50 border-emerald-200 text-emerald-800' : 'bg-[var(--color-error-bg)] border-[var(--color-error-border)] text-[var(--color-error-text)]'
    }`}>
      {message.text}
    </div>
  );
}

function SectionCard({ icon: Icon, title, children }: {
  icon: React.ComponentType<{ className?: string }>;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="glass-panel rounded-2xl p-6 border border-[var(--color-glass-border)]/50 shadow-portal space-y-5">
      <div className="flex items-center gap-2 pb-3 border-b border-[var(--color-border-light)]">
        <Icon className="w-4 h-4 text-[var(--color-portal-orange-themed)]" />
        <h3 className="font-display font-bold text-sm text-[var(--color-portal-navy-themed)]">{title}</h3>
      </div>
      {children}
    </div>
  );
}

const inputCls = 'w-full px-4 py-2.5 bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange focus:bg-[var(--color-bg-secondary)] transition-all font-sans';
const selectCls = 'px-3 py-1.5 text-xs bg-[var(--color-bg-secondary)]/55 border border-[var(--color-border)] rounded-xl focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange transition-all font-sans';
const primaryBtnCls = 'bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-md py-2.5 rounded-xl text-xs font-bold font-mono transition-all uppercase tracking-wider cursor-pointer';
const secondaryBtnCls = 'px-4 py-2.5 rounded-xl text-xs font-mono border border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-portal-navy-themed)] transition-all cursor-pointer';

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
    <div className="max-w-4xl w-full mx-auto my-4 space-y-6">
      {/* Back Header */}
      <div className="flex items-center justify-between pb-4 border-b border-[var(--color-border)]/50">
        <button
          onClick={onBack}
          className="flex items-center gap-2 px-4 py-2 bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded-full hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)] transition-all font-mono font-bold text-xs cursor-pointer text-[var(--color-text-secondary)] hover:text-[var(--color-portal-navy-themed)] shadow-xs"
        >
          <ArrowLeft className="w-4 h-4" />
          {t('common.back')}
        </button>
        <div className="flex items-center gap-2">
          <ShieldCheck className="w-5 h-5 text-[var(--color-portal-navy-themed)]" />
          <h2 className="font-display font-extrabold text-xl text-[var(--color-portal-navy-themed)] leading-none">{t('admin.title')}</h2>
        </div>
      </div>

      {/* Tabs */}
      <div className="flex flex-wrap gap-2">
        {tabs.map(([key, Icon, label]) => (
          <button
            key={key}
            onClick={() => setTab(key)}
            className={`flex items-center gap-1.5 px-4 py-2 rounded-full border font-mono font-bold text-xs transition-all cursor-pointer ${
              tab === key
                ? 'bg-portal-orange/10 border-portal-orange text-[var(--color-portal-orange-themed)]'
                : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:text-[var(--color-portal-navy-themed)] hover:bg-[var(--color-bg-tertiary)] shadow-xs'
            }`}
          >
            <Icon className="w-4 h-4" />
            {t(label)}
          </button>
        ))}
      </div>

      {message && <MessageBanner message={message} />}

      <div className="min-h-[60vh]">
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
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [roleFilter, setRoleFilter] = useState('');
  const [activeFilter, setActiveFilter] = useState('');
  const [q, setQ] = useState('');
  const [loading, setLoading] = useState(false);

  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ email: '', display_name: '', password: '', role: 'USER', must_change_password: true });

  const load = useCallback(async () => {
    setLoading(true);
    const res = await adminApi.listUsers(apiUrl, token, {
      page, limit: LIMIT, role: roleFilter || undefined, active: activeFilter || undefined, q: q || undefined,
    });
    setLoading(false);
    if (res.ok) {
      setUsers(res.data.users || []);
      setTotal(res.data.total || 0);
    } else {
      onError(res.errorCode);
    }
  }, [apiUrl, token, page, roleFilter, activeFilter, q, onError]);

  useEffect(() => { load(); }, [load]);

  const act = async (fn: () => Promise<ApiResult>, successKey: string) => {
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
          value={q}
          onChange={(e) => { setQ(e.target.value); setPage(1); }}
          placeholder={t('common.search')}
          className={inputCls}
        />
        <select value={roleFilter} onChange={(e) => { setRoleFilter(e.target.value); setPage(1); }}
          className={selectCls}>
          <option value="">{t('admin.users.allRoles')}</option>
          <option value="USER">USER</option>
          <option value="ADMIN">ADMIN</option>
        </select>
        <select value={activeFilter} onChange={(e) => { setActiveFilter(e.target.value); setPage(1); }}
          className={selectCls}>
          <option value="">{t('admin.users.allStates')}</option>
          <option value="true">{t('common.active')}</option>
          <option value="false">{t('admin.users.suspended')}</option>
        </select>
        <button onClick={() => setShowCreate((v) => !v)}
          className="ml-auto flex items-center gap-1.5 px-4 py-2.5 rounded-xl text-xs font-bold bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-md transition-all cursor-pointer">
          <UserPlus className="w-4 h-4" /> {t('admin.users.create')}
        </button>
      </div>

      {showCreate && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3 p-4 rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-tertiary)]/40">
          <input placeholder={t('auth.email')} value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })}
            className={inputCls} />
          <input placeholder={t('auth.name')} value={form.display_name} onChange={(e) => setForm({ ...form, display_name: e.target.value })}
            className={inputCls} />
          <input type="password" placeholder={t('auth.password')} value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })}
            className={inputCls} />
          <select value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}
            className={selectCls}>
            <option value="USER">USER</option>
            <option value="ADMIN">ADMIN</option>
          </select>
          <label className="flex items-center gap-2 text-xs text-[var(--color-text-secondary)] md:col-span-2">
            <input type="checkbox" checked={form.must_change_password} onChange={(e) => setForm({ ...form, must_change_password: e.target.checked })} />
            {t('admin.users.forcePasswordChange')}
          </label>
          <div className="md:col-span-2 flex justify-end gap-2">
            <button onClick={() => setShowCreate(false)} className={secondaryBtnCls}>{t('common.cancel')}</button>
            <button onClick={create} className={primaryBtnCls}>{t('common.save')}</button>
          </div>
        </div>
      )}

      <div className="overflow-x-auto rounded-2xl border border-[var(--color-border)]">
        <table className="w-full text-xs">
          <thead className="bg-[var(--color-bg-tertiary)]/60 text-[var(--color-text-muted)]">
            <tr>
              <th className="text-left px-3 py-2 font-semibold">{t('auth.email')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('auth.name')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.users.role')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('common.active')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.users.createdAt')}</th>
              <th className="text-right px-3 py-2 font-semibold">{t('migrations.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id} className="border-t border-[var(--color-border)]">
                <td className="px-3 py-2">{u.email}</td>
                <td className="px-3 py-2">{u.display_name}</td>
                <td className="px-3 py-2">
                  <span className={`px-2 py-0.5 rounded-full text-[10px] font-bold ${u.role === 'ADMIN' ? 'bg-portal-orange/15 text-portal-orange' : 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-muted)]'}`}>
                    {u.role}
                  </span>
                </td>
                <td className="px-3 py-2">
                  {u.active
                    ? <span className="text-emerald-600 font-semibold">{t('common.active')}</span>
                    : <span className="text-rose-600 font-semibold">{t('admin.users.suspended')}</span>}
                </td>
                <td className="px-3 py-2 text-[var(--color-text-muted)]">{u.created_at ? new Date(u.created_at).toLocaleDateString() : ''}</td>
                <td className="px-3 py-2">
                  <div className="flex justify-end gap-1.5">
                    {u.active ? (
                      <button title={t('admin.users.suspend')} onClick={() => act(() => adminApi.suspendUser(apiUrl, token, u.id!), 'admin.users.suspendedOk')}
                        className="p-1.5 rounded-xl border border-[var(--color-border)] text-rose-600 hover:bg-rose-50/50 cursor-pointer"><Ban className="w-3.5 h-3.5" /></button>
                    ) : (
                      <button title={t('admin.users.reactivate')} onClick={() => act(() => adminApi.reactivateUser(apiUrl, token, u.id!), 'admin.users.reactivatedOk')}
                        className="p-1.5 rounded-xl border border-[var(--color-border)] text-emerald-600 hover:bg-emerald-50/50 cursor-pointer"><CheckCircle2 className="w-3.5 h-3.5" /></button>
                    )}
                    <button title={t('admin.users.toggleRole')} onClick={() => act(() => adminApi.updateRole(apiUrl, token, u.id!, u.role === 'ADMIN' ? 'USER' : 'ADMIN'), 'admin.users.roleChanged')}
                      className="p-1.5 rounded-xl border border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] cursor-pointer">
                      {u.role === 'ADMIN' ? <ShieldOff className="w-3.5 h-3.5" /> : <ShieldCheck className="w-3.5 h-3.5" />}
                    </button>
                    {u.id !== currentUserID && (
                      <button title={t('admin.users.delete')} onClick={() => { if (confirm(t('admin.users.deleteConfirm'))) act(() => adminApi.deleteUser(apiUrl, token, u.id!), 'admin.users.deletedOk'); }}
                        className="p-1.5 rounded-xl border border-[var(--color-border)] text-rose-600 hover:bg-rose-50/50 cursor-pointer"><Trash2 className="w-3.5 h-3.5" /></button>
                    )}
                  </div>
                </td>
              </tr>
            ))}
            {!loading && users.length === 0 && (
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
// Migrations tab
// ---------------------------------------------------------------------------

function MigrationsTab({ apiUrl, token, formatBytes, formatDateTime }: {
  apiUrl: string; token: string;
  formatBytes: (n: number) => string; formatDateTime: (iso: string) => string;
}) {
  const { t } = useTranslation();
  const [items, setItems] = useState<AdminMigration[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      const res = await adminApi.listMigrations(apiUrl, token, { page, limit: LIMIT });
      setLoading(false);
      if (res.ok && !cancelled) {
        setItems(res.data.migrations || []);
        setTotal(res.data.total || 0);
      }
    })();
    return () => { cancelled = true; };
  }, [apiUrl, token, page]);

  const pages = Math.max(1, Math.ceil(total / LIMIT));

  return (
    <SectionCard icon={Activity} title={t('admin.tabs.migrations')}>
      <div className="overflow-x-auto rounded-2xl border border-[var(--color-border)]">
        <table className="w-full text-xs">
          <thead className="bg-[var(--color-bg-tertiary)]/60 text-[var(--color-text-muted)]">
            <tr>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.migrations.owner')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('migrations.status')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.migrations.sourceTarget')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('dashboard.progress')}</th>
              <th className="text-left px-3 py-2 font-semibold">{t('admin.migrations.created')}</th>
            </tr>
          </thead>
          <tbody>
            {items.map((m) => (
              <tr key={m.id} className="border-t border-[var(--color-border)]">
                <td className="px-3 py-2">{m.owner_email || <span className="text-[var(--color-text-muted)]">—</span>}</td>
                <td className="px-3 py-2"><StatusBadge status={m.status} /></td>
                <td className="px-3 py-2">{m.source_provider} → {m.target_provider}</td>
                <td className="px-3 py-2">{m.processed_files}/{m.total_files} · {formatBytes(m.processed_bytes)}</td>
                <td className="px-3 py-2 text-[var(--color-text-muted)]">{formatDateTime(m.created_at)}</td>
              </tr>
            ))}
            {!loading && items.length === 0 && (
              <tr><td colSpan={5} className="px-3 py-6 text-center text-[var(--color-text-muted)]">{t('migrations.dbEmpty')}</td></tr>
            )}
          </tbody>
        </table>
      </div>
      <Pager page={page} pages={pages} onPage={setPage} />
    </SectionCard>
  );
}

function StatusBadge({ status }: { status: string }) {
  const { t } = useTranslation();
  const map: Record<string, string> = {
    COMPLETED: 'bg-emerald-100 text-emerald-700',
    FAILED: 'bg-rose-100 text-rose-700',
    CANCELLED: 'bg-gray-100 text-gray-600',
    RUNNING: 'bg-portal-orange/15 text-portal-orange',
    INDEXING: 'bg-portal-orange/15 text-portal-orange',
    PAUSED: 'bg-amber-100 text-amber-700',
    PAUSED_CONNECTION_LOSS: 'bg-amber-100 text-amber-700',
    SCHEDULED: 'bg-sky-100 text-sky-700',
  };
  const cls = map[status] || 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-muted)]';
  return <span className={'px-2 py-0.5 rounded-full text-[10px] font-bold ' + cls}>{t('status.' + status.toLowerCase())}</span>;
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
      if (res.ok) setStats(res.data);
    })();
  }, [apiUrl, token]);

  if (!stats) return <div className="py-8 text-center text-xs text-[var(--color-text-muted)]">{t('common.loading')}</div>;

  const card = (label: string, value: number | string) => (
    <div className="p-4 rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-tertiary)]/40">
      <div className="text-2xl font-display font-extrabold text-[var(--color-portal-navy-themed)]">{value}</div>
      <div className="text-[10px] uppercase tracking-wider text-[var(--color-text-muted)] mt-1">{label}</div>
    </div>
  );

  return (
    <SectionCard icon={BarChart3} title={t('admin.tabs.stats')}>
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        {card(t('admin.stats.totalUsers'), stats.total_users)}
        {card(t('admin.stats.activeUsers'), stats.active_users)}
        {card(t('admin.stats.totalMigrations'), Object.values(stats.migrations_by_status).reduce((a, b) => a + b, 0))}
        {card(t('admin.stats.totalTasks'), Object.values(stats.tasks_by_status).reduce((a, b) => a + b, 0))}
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div className="p-4 rounded-2xl border border-[var(--color-border)]">
          <div className="text-xs font-bold text-[var(--color-portal-navy-themed)] mb-3">{t('admin.stats.migrationsByStatus')}</div>
          <div className="space-y-1.5">
            {Object.entries(stats.migrations_by_status).map(([k, v]) => (
              <div key={k} className="flex items-center justify-between text-xs">
                <StatusBadge status={k} />
                <span className="font-mono">{v}</span>
              </div>
            ))}
            {Object.keys(stats.migrations_by_status).length === 0 && <div className="text-[var(--color-text-muted)]">—</div>}
          </div>
        </div>
        <div className="p-4 rounded-2xl border border-[var(--color-border)]">
          <div className="text-xs font-bold text-[var(--color-portal-navy-themed)] mb-3">{t('admin.stats.tasksByStatus')}</div>
          <div className="space-y-1.5">
            {Object.entries(stats.tasks_by_status).map(([k, v]) => (
              <div key={k} className="flex items-center justify-between text-xs">
                <span className="px-2 py-0.5 rounded-full text-[10px] font-bold bg-[var(--color-bg-tertiary)] text-[var(--color-text-muted)]">{k}</span>
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
        setEntries(res.data.entries || []);
        setTotal(res.data.total || 0);
      }
    })();
    return () => { cancelled = true; };
  }, [apiUrl, token, page, action, userID, from, to]);

  const pages = Math.max(1, Math.ceil(total / LIMIT));
  const actions: string[] = [
    'LOGIN_SUCCESS', 'LOGIN_FAILED', 'REGISTRATION', 'USER_CREATED', 'USER_SUSPENDED', 'USER_REACTIVATED',
    'USER_DELETED', 'USER_ROLE_CHANGED', 'MIGRATION_CREATED', 'MIGRATION_STARTED', 'MIGRATION_COMPLETED',
    'MIGRATION_FAILED', 'MIGRATION_PAUSED', 'MIGRATION_RESUMED', 'MIGRATION_CANCELLED', 'MIGRATION_DELETED',
    'SETTING_UPDATED', '2FA_ENABLED', '2FA_DISABLED',
  ];

  return (
    <SectionCard icon={ScrollText} title={t('admin.tabs.audit')}>
      <div className="flex flex-wrap items-center gap-2">
        <select value={action} onChange={(e) => { setAction(e.target.value); setPage(1); }}
          className={selectCls}>
          <option value="">{t('admin.audit.allActions')}</option>
          {actions.map((a) => <option key={a} value={a}>{t(`admin.audit.actions.${a}`)}</option>)}
        </select>
        <input value={userID} onChange={(e) => { setUserID(e.target.value); setPage(1); }} placeholder={t('admin.audit.userId')}
          className="px-4 py-2.5 text-sm border border-[var(--color-border)] rounded-xl focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange transition-all font-sans w-44" />
        <input type="date" value={from} onChange={(e) => { setFrom(e.target.value); setPage(1); }}
          className={selectCls} />
        <input type="date" value={to} onChange={(e) => { setTo(e.target.value); setPage(1); }}
          className={selectCls} />
        <button onClick={() => { setPage(1); }} className="p-1.5 rounded-xl border border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] cursor-pointer"><RefreshCw className="w-3.5 h-3.5" /></button>
      </div>

      <div className="overflow-x-auto rounded-2xl border border-[var(--color-border)]">
        <table className="w-full text-xs">
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
                <td className="px-3 py-2 text-[var(--color-text-muted)] whitespace-nowrap">{formatDateTime(e.created_at)}</td>
                <td className="px-3 py-2"><span className="px-2 py-0.5 rounded-full text-[10px] font-bold bg-[var(--color-bg-tertiary)] text-[var(--color-text-secondary)]">{t(`admin.audit.actions.${e.action}`)}</span></td>
                <td className="px-3 py-2 font-mono text-[10px]">{e.user_id ? e.user_id.slice(0, 8) : '—'}</td>
                <td className="px-3 py-2 font-mono text-[10px] max-w-[160px] truncate" title={e.target}>{e.target || '—'}</td>
                <td className="px-3 py-2 font-mono text-[10px]">{e.ip || '—'}</td>
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

  const [registrationsEnabled, setRegistrationsEnabled] = useState<boolean>(true);
  const [loading, setLoading] = useState<boolean>(false);
  const [message, setMessage] = useState<{ text: string; type: 'success' | 'error' } | null>(null);

  useEffect(() => {
    fetch(`${apiUrl}/api/settings`)
      .then((res) => res.json())
      .then((data) => {
        setRegistrationsEnabled(data.registrations_enabled !== 'false');
      })
      .catch((err) => {
        console.error('Failed to fetch settings:', err);
      });
  }, [apiUrl]);

  const handleToggleRegistrations = async (checked: boolean) => {
    setMessage(null);
    setLoading(true);
    try {
      const res = await fetch(`${apiUrl}/api/settings`, {
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
    <SectionCard icon={CloudSync} title={t('admin.system.title')}>
      <MessageBanner message={message} />

      <div className="flex items-center justify-between p-3.5 bg-[var(--color-bg-tertiary)]/50 border border-[var(--color-border)]/50 rounded-2xl">
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
        />
      </div>
    </SectionCard>
  );
}

// ---------------------------------------------------------------------------
// Shared pager
// ---------------------------------------------------------------------------

function Pager({ page, pages, onPage }: { page: number; pages: number; onPage: (p: number) => void }) {
  return (
    <div className="flex items-center justify-between text-xs">
      <button
        disabled={page <= 1}
        onClick={() => onPage(page - 1)}
        className="px-3 py-1.5 rounded-xl border border-[var(--color-border)] disabled:opacity-40 cursor-pointer hover:bg-[var(--color-bg-tertiary)] transition-all"
      >
        ←
      </button>
      <span className="text-[var(--color-text-muted)] font-mono">{page} / {pages}</span>
      <button
        disabled={page >= pages}
        onClick={() => onPage(page + 1)}
        className="px-3 py-1.5 rounded-xl border border-[var(--color-border)] disabled:opacity-40 cursor-pointer hover:bg-[var(--color-bg-tertiary)] transition-all"
      >
        →
      </button>
    </div>
  );
}
