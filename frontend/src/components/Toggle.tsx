import { useId } from 'react';

interface ToggleProps {
  id?: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (value: boolean) => void;
  label: string;
  hideLabel?: boolean;
  className?: string;
}

export function Toggle({
  id,
  checked,
  disabled,
  onChange,
  label,
  hideLabel = false,
  className = '',
}: ToggleProps) {
  const generatedId = useId();
  const controlId = id ?? generatedId;

  return (
    <label
      htmlFor={controlId}
      className={`inline-flex items-center gap-2.5 select-none ${
        disabled ? 'cursor-not-allowed opacity-55' : 'cursor-pointer'
      } ${className}`}
    >
      <span className="relative inline-flex items-center shrink-0">
        <input
          id={controlId}
          type="checkbox"
          role="switch"
          aria-checked={checked}
          aria-label={label}
          checked={checked}
          disabled={disabled}
          onChange={(e) => onChange(e.target.checked)}
          className="sr-only peer"
        />
        {/* Switch track */}
        <span
          aria-hidden="true"
          className={`block h-5 w-9 rounded-full border transition-colors duration-200 ease-in-out peer-focus-visible:ring-2 peer-focus-visible:ring-[var(--color-focus)] peer-focus-visible:ring-offset-2 ${
            checked
              ? 'bg-blue-600 border-blue-600 dark:bg-blue-500 dark:border-blue-500'
              : 'bg-[var(--color-bg-tertiary)] border-[var(--color-border)]'
          }`}
        />
        {/* Switch thumb/knob */}
        <span
          aria-hidden="true"
          className={`pointer-events-none absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white shadow-xs transition-transform duration-200 ease-in-out ${
            checked ? 'translate-x-4' : 'translate-x-0'
          }`}
        />
      </span>
      {label && (
        <span
          className={
            hideLabel
              ? 'sr-only'
              : 'text-sm text-[var(--color-text-secondary)] select-none'
          }
        >
          {label}
        </span>
      )}
    </label>
  );
}

