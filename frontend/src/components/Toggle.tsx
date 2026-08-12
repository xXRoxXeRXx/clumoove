import { useId } from 'react';

interface ToggleProps {
  id?: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (value: boolean) => void;
  label: string;
}

export function Toggle({ id, checked, disabled, onChange, label }: ToggleProps) {
  const generatedId = useId();
  const controlId = id ?? generatedId;

  return (
    <label htmlFor={controlId} className="relative inline-flex cursor-pointer items-center gap-2 select-none">
      <input
        id={controlId}
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        className="sr-only peer"
      />
      <div aria-hidden="true" className="ui-toggle peer h-5 w-9 rounded-full after:absolute after:left-0.5 after:top-0.5 after:h-4 after:w-4 after:rounded-full after:bg-[var(--color-bg-inverse)] after:content-[''] peer-checked:ui-toggle-checked peer-checked:after:translate-x-4 peer-checked:after:bg-[var(--color-text-inverse)] peer-focus-visible:outline-2 peer-focus-visible:outline-[var(--color-focus)] peer-focus-visible:outline-offset-2 peer-disabled:cursor-not-allowed peer-disabled:opacity-55"></div>
      <span className="text-sm text-[var(--color-text-secondary)]">{label}</span>
    </label>
  );
}
