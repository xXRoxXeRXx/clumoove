interface ProgressBarProps {
  value: number;
  label: string;
  valueText?: string;
  status?: string;
  className?: string;
  indicatorClassName?: string;
}

/** A bounded progressbar that announces its name and current value to assistive tech. */
export function ProgressBar({ value, label, valueText, status, className = '', indicatorClassName = 'bg-[var(--color-bg-inverse)]' }: ProgressBarProps) {
  const clampedValue = Math.min(100, Math.max(0, Number.isFinite(value) ? value : 0));
  const accessibleValue = valueText ?? `${Math.round(clampedValue)}%`;

  return (
    <div className={className}>
      <div
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(clampedValue)}
        aria-valuetext={accessibleValue}
        className="ui-progress h-full"
      >
        <div className={`h-full transition-[width] duration-500 ease-out ${indicatorClassName}`} style={{ width: `${clampedValue}%` }} />
      </div>
      {status && <span className="sr-only" aria-live="polite">{status}</span>}
    </div>
  );
}
