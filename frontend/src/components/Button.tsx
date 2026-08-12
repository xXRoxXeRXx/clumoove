import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';
import { Spinner } from './Spinner';

type ButtonVariant = 'primary' | 'secondary' | 'danger' | 'quiet' | 'icon';
type ButtonSize = 'sm' | 'md';

type ButtonBaseProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  size?: ButtonSize;
  loading?: boolean;
  children: ReactNode;
};

type StandardButtonProps = ButtonBaseProps & {
  variant?: Exclude<ButtonVariant, 'icon'>;
  ariaLabel?: never;
};

type IconButtonProps = Omit<ButtonBaseProps, 'aria-label' | 'title'> & {
  variant: 'icon';
  /** Localized accessible name, forwarded to both aria-label and title. */
  ariaLabel: string;
};

export type ButtonProps = StandardButtonProps | IconButtonProps;

const variantClasses: Record<ButtonVariant, string> = {
  primary: 'ui-button-primary',
  secondary: 'ui-button-secondary',
  danger: 'ui-button-danger',
  quiet: 'ui-button-quiet',
  icon: 'ui-icon-button ui-button-secondary',
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  function Button(
    {
      variant = 'secondary',
      size = 'md',
      loading = false,
      disabled,
      className = '',
      children,
      type = 'button',
      ariaLabel,
      ...props
    },
    ref,
  ) {
    const padding = variant === 'icon' ? (size === 'sm' ? 'p-1.5' : 'p-2') : (size === 'sm' ? 'px-2.5 py-1.5 text-xs' : 'px-3 py-2 text-sm');
    return (
      <button
        ref={ref}
        {...props}
        type={type}
        {...(variant === 'icon' ? { 'aria-label': ariaLabel, title: ariaLabel } : {})}
        aria-busy={loading || undefined}
        disabled={disabled || loading}
        className={`${variantClasses[variant]} inline-flex items-center justify-center gap-2 font-medium ${padding} ${className}`}
      >
        {loading && <Spinner size="xs" />}
        {children}
      </button>
    );
  },
);
