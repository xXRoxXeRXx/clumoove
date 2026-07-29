interface SpinnerProps {
  size?: 'xs' | 'sm' | 'md' | 'lg';
  label?: string;
  className?: string;
}

const sizes = { xs: 'h-3 w-3', sm: 'h-4 w-4', md: 'h-6 w-6', lg: 'h-8 w-8' };

export function Spinner({ size = 'sm', label, className = '' }: SpinnerProps) {
  return <span role={label ? 'status' : undefined} aria-label={label} aria-hidden={label ? undefined : true} className={`ui-loading inline-block animate-spin rounded-full border-2 border-t-transparent ${sizes[size]} ${className}`} />;
}
