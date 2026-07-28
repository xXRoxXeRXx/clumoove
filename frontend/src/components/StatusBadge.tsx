import { useTranslation } from 'react-i18next';

type BadgeVariant = 'success' | 'error' | 'warning' | 'info' | 'muted' | 'cyan';

const variantCls: Record<BadgeVariant, string> = {
  success: 'bg-[var(--color-success-bg)] text-[var(--color-success-text)] border-[var(--color-success-border)]',
  error: 'bg-[var(--color-error-bg)] text-[var(--color-error-text)] border-[var(--color-error-border)]',
  warning: 'bg-[var(--color-warning-bg)] text-[var(--color-text-primary)] border-[var(--color-warning-border)]',
  info: 'bg-[var(--color-info-bg)] text-[var(--color-info-text)] border-[var(--color-info-border)]',
  muted: 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-secondary)] border-[var(--color-border)]',
  cyan: 'bg-[var(--color-info-bg)] text-[var(--color-info-text)] border-[var(--color-info-border)]',
};

function Badge({
  variant,
  pulse,
  label,
  size = 'md',
}: {
  variant: BadgeVariant;
  pulse?: boolean;
  label: string;
  size?: 'sm' | 'md';
}) {
  const sizeCls = size === 'sm' ? 'px-2 py-0.5 text-[11px]' : 'px-2.5 py-1 text-xs';
  return (
    <span
      className={`inline-flex items-center rounded-md font-medium border ${variantCls[variant]} ${sizeCls} ${pulse ? 'animate-pulse' : ''}`}
    >
      {label}
    </span>
  );
}

export function StatusBadge({
  status,
  size = 'md',
}: {
  status: string;
  size?: 'sm' | 'md';
}) {
  const { t } = useTranslation();

  switch (status) {
    case 'COMPLETED':
      return (
        <Badge
          size={size}
          variant="success"
          label={t('status.completed')}
        />
      );
    case 'FAILED':
      return (
        <Badge
          size={size}
          variant="error"
          label={t('status.failed')}
        />
      );
    case 'COMPLETED_WITH_ERRORS':
      return (
        <Badge
          size={size}
          variant="warning"
          label={t('status.completedWithErrors')}
        />
      );
    case 'VERIFYING':
      return (
        <Badge
          size={size}
          variant="cyan"
          pulse
          label={t('status.verifying')}
        />
      );
    case 'PAUSED':
      return (
        <Badge
          size={size}
          variant="muted"
          label={t('status.paused')}
        />
      );
    case 'PAUSED_CONNECTION_LOSS':
      return (
        <Badge
          size={size}
          variant="warning"
          pulse
          label={t('status.paused')}
        />
      );
    case 'CANCELLED':
      return (
        <Badge
          size={size}
          variant="error"
          label={t('status.cancelled')}
        />
      );
    case 'RUNNING':
      return (
        <Badge
          size={size}
          variant="info"
          pulse
          label={t('status.transfer')}
        />
      );
    case 'INDEXING':
      return (
        <Badge
          size={size}
          variant="warning"
          label={t('status.indexing')}
        />
      );
    case 'IDLE':
      return (
        <Badge
          size={size}
          variant="success"
          label={t('sync.statusIdle')}
        />
      );
    case 'PENDING':
    case 'SCHEDULED':
      return (
        <Badge size={size} variant="muted" label={status} />
      );
    default:
      return (
        <Badge
          size={size}
          variant="muted"
          pulse={status === 'RUNNING' || status === 'INDEXING'}
          label={status}
        />
      );
  }
}
