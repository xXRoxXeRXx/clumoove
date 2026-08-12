export type BadgeVariant = 'success' | 'error' | 'warning' | 'info' | 'muted';

const variantCls: Record<BadgeVariant, string> = {
  success: 'ui-badge-success',
  error: 'ui-badge-error',
  warning: 'ui-badge-warning',
  info: 'ui-badge-info',
  muted: 'ui-badge-muted',
};

export interface BadgeProps {
  variant: BadgeVariant;
  pulse?: boolean;
  label: string;
  size?: 'sm' | 'md';
}

/** A compact, semantic label for non-interactive status or metadata. */
export function Badge({ variant, pulse, label, size = 'md' }: BadgeProps) {
  const sizeCls = size === 'sm' ? 'px-2 py-0.5 text-[11px]' : 'px-2.5 py-1 text-xs';
  return (
    <span
      className={`ui-badge inline-flex items-center font-medium ${variantCls[variant]} ${sizeCls} ${pulse ? 'animate-pulse' : ''}`}
    >
      {label}
    </span>
  );
}
