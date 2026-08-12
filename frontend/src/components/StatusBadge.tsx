import { useTranslation } from 'react-i18next';
import { Badge } from './Badge';

export { Badge } from './Badge';

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
          variant="info"
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
      return (
        <Badge size={size} variant="muted" label={t('status.pending')} />
      );
    case 'SCHEDULED':
      return (
        <Badge size={size} variant="muted" label={t('status.scheduled')} />
      );
    default:
      return (
        <Badge
          size={size}
          variant="muted"
          label={t('status.unknown', { status })}
        />
      );
  }
}
