interface ToggleProps {
  checked: boolean;
  disabled?: boolean;
  onChange: (value: boolean) => void;
}

export function Toggle({ checked, disabled, onChange }: ToggleProps) {
  return (
    <label className="relative inline-flex items-center cursor-pointer select-none">
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        className="sr-only peer"
      />
      <div className="w-10 h-6 bg-[var(--color-border)] peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-[var(--color-glass-border)] after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-[var(--color-bg-secondary)] after:border after:border-[var(--color-border)] after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-portal-orange"></div>
    </label>
  );
}
