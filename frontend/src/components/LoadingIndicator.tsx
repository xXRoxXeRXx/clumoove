interface LoadingIndicatorProps {
  label: string;
  size?: 'sm' | 'md';
}

import { Spinner } from './Spinner';

export function LoadingIndicator({ label, size = 'md' }: LoadingIndicatorProps) {
  return (
    <span className="inline-flex items-center gap-2 text-xs text-[var(--color-text-muted)]" role="status">
      <Spinner size={size === 'sm' ? 'sm' : 'lg'} />
      <span>{label}</span>
    </span>
  );
}
