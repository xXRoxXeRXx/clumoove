import type { ButtonHTMLAttributes, ReactNode } from 'react';
import { Spinner } from './Spinner';

type ButtonVariant = 'primary' | 'secondary' | 'danger' | 'quiet' | 'icon';
type ButtonSize = 'sm' | 'md';

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
  children: ReactNode;
}

const variantClasses: Record<ButtonVariant, string> = {
  primary: 'ui-button-primary',
  secondary: 'ui-button-secondary',
  danger: 'ui-button-danger',
  quiet: 'ui-button-quiet',
  icon: 'ui-icon-button ui-button-secondary',
};

export function Button({ variant = 'secondary', size = 'md', loading = false, disabled, className = '', children, ...props }: ButtonProps) {
  const padding = variant === 'icon' ? (size === 'sm' ? 'p-1.5' : 'p-2') : (size === 'sm' ? 'px-2.5 py-1.5 text-xs' : 'px-3 py-2 text-sm');
  return (
    <button {...props} disabled={disabled || loading} className={`${variantClasses[variant]} inline-flex items-center justify-center gap-2 font-medium ${padding} ${className}`}>
      {loading && <Spinner size="xs" />}
      {children}
    </button>
  );
}
