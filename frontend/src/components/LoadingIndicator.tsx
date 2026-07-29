interface LoadingIndicatorProps {
  label: string;
  size?: 'sm' | 'md';
}

export function LoadingIndicator({ label, size = 'md' }: LoadingIndicatorProps) {
  const dimensions = size === 'sm' ? 'h-4 w-4' : 'h-8 w-8';
  return (
    <span className="inline-flex items-center gap-2 text-xs text-[var(--color-text-muted)]" role="status">
      <span aria-hidden="true" className={`ui-loading animate-spin rounded-full border-2 border-t-transparent ${dimensions}`} />
      <span>{label}</span>
    </span>
  );
}
