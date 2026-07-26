import {
  AlertTriangle,
  CheckCircle2,
  Loader2,
  Pause,
  XCircle,
} from 'lucide-react';
import { useTranslation } from 'react-i18next';

type BadgeVariant = 'success' | 'error' | 'warning' | 'info' | 'muted' | 'cyan';

const variantCls: Record<BadgeVariant, string> = {
  success: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  error: 'bg-[var(--color-error-bg)] text-rose-700 border-[var(--color-error-border)]',
  warning: 'bg-amber-50 text-amber-700 border-amber-200',
  info: 'bg-[var(--color-info-bg)] text-blue-700 border-[var(--color-info-border)]',
  muted: 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-secondary)] border-[var(--color-border)]',
  cyan: 'bg-cyan-50 text-cyan-700 border-cyan-200',
};

function Badge({
  variant,
  pulse,
  icon,
  label,
  size = 'md',
}: {
  variant: BadgeVariant;
  pulse?: boolean;
  icon: React.ReactNode;
  label: string;
  size?: 'sm' | 'md';
}) {
  const sizeCls = size === 'sm' ? 'px-2.5 py-1 text-[11px] gap-1.5' : 'px-3 py-1 text-xs gap-1.5';
  return (
    <span
      className={`inline-flex items-center rounded-full font-bold border ${variantCls[variant]} ${sizeCls} ${pulse ? 'animate-pulse' : ''}`}
    >
      {icon}
      {label}
    </span>
  );
}

export function StatusBadge({
  status,
  size = 'md',
  context = 'migration',
}: {
  status: string;
  size?: 'sm' | 'md';
  context?: 'migration' | 'sync';
}) {
  const { t } = useTranslation();
  const iconCls = size === 'sm' ? 'w-3.5 h-3.5' : 'w-3.5 h-3.5';
  const iconMd = size === 'sm' ? 'w-3.5 h-3.5' : 'w-4 h-4';

  switch (status) {
    case 'COMPLETED':
      return (
        <Badge
          size={size}
          variant="success"
          icon={<CheckCircle2 className={iconMd} />}
          label={t('status.completed')}
        />
      );
    case 'FAILED':
      return (
        <Badge
          size={size}
          variant="error"
          icon={<XCircle className={iconMd} />}
          label={t('status.failed')}
        />
      );
    case 'COMPLETED_WITH_ERRORS':
      return (
        <Badge
          size={size}
          variant="warning"
          icon={<AlertTriangle className={iconMd} />}
          label={t('status.completedWithErrors')}
        />
      );
    case 'VERIFYING':
      return (
        <Badge
          size={size}
          variant="cyan"
          pulse
          icon={<Loader2 className={`${iconCls} animate-spin`} />}
          label={t('status.verifying')}
        />
      );
    case 'PAUSED':
      return (
        <Badge
          size={size}
          variant="muted"
          icon={<Pause className={iconMd} />}
          label={t('status.paused')}
        />
      );
    case 'PAUSED_CONNECTION_LOSS':
      return (
        <Badge
          size={size}
          variant="warning"
          pulse
          icon={
            context === 'sync' ? (
              <AlertTriangle className={iconMd} />
            ) : (
              <Pause className={iconMd} />
            )
          }
          label={
            context === 'sync' ? t('dashboard.eta.waitingConn') : t('status.paused')
          }
        />
      );
    case 'CANCELLED':
      return (
        <Badge
          size={size}
          variant="error"
          icon={<XCircle className={iconMd} />}
          label={t('status.cancelled')}
        />
      );
    case 'RUNNING':
      return (
        <Badge
          size={size}
          variant="info"
          pulse
          icon={<Loader2 className={`${iconCls} animate-spin`} />}
          label={context === 'sync' ? t('status.active') : t('status.transfer')}
        />
      );
    case 'INDEXING':
      return (
        <Badge
          size={size}
          variant="warning"
          icon={<Loader2 className={`${iconCls} animate-spin`} />}
          label={t('status.indexing')}
        />
      );
    case 'IDLE':
      return (
        <Badge
          size={size}
          variant="success"
          icon={<CheckCircle2 className={iconMd} />}
          label={t('sync.statusIdle')}
        />
      );
    case 'PENDING':
    case 'SCHEDULED':
      return (
        <Badge size={size} variant="muted" icon={null} label={status} />
      );
    default:
      return (
        <Badge
          size={size}
          variant="muted"
          pulse={status === 'RUNNING' || status === 'INDEXING'}
          icon={
            status === 'RUNNING' || status === 'INDEXING' ? (
              <Loader2 className={`${iconCls} animate-spin`} />
            ) : null
          }
          label={status}
        />
      );
  }
}
