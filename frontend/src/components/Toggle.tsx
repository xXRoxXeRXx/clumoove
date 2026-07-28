interface ToggleProps {
  checked: boolean;
  disabled?: boolean;
  onChange: (value: boolean) => void;
  label: string;
}

export function Toggle({ checked, disabled, onChange, label }: ToggleProps) {
  return (
    <label className="relative inline-flex items-center cursor-pointer select-none">
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        aria-label={label}
        onChange={(e) => onChange(e.target.checked)}
        className="sr-only peer"
      />
      <div aria-hidden="true" className="ui-toggle peer h-5 w-9 rounded-full after:absolute after:left-0.5 after:top-0.5 after:h-4 after:w-4 after:rounded-full after:bg-[var(--color-bg-secondary)] after:content-[''] peer-checked:ui-toggle-checked peer-checked:after:translate-x-4"></div>
    </label>
  );
}
