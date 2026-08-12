import { type KeyboardEvent, useId, useRef } from 'react';
import { ProviderIcon } from './ProviderIcon';

export interface ProviderSelectorProps<T extends string = string> {
  providers: { id: T; name: string }[];
  selectedProvider: T;
  onSelectProvider: (id: T) => void;
  label: string;
}

export function ProviderSelector<T extends string = string>({
  providers,
  selectedProvider,
  onSelectProvider,
  label,
}: ProviderSelectorProps<T>) {
  const labelId = useId();
  const optionRefs = useRef<Array<HTMLButtonElement | null>>([]);

  const selectProviderFromKeyboard = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    let nextIndex: number | null = null;
    if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') {
      nextIndex = (index - 1 + providers.length) % providers.length;
    } else if (event.key === 'ArrowRight' || event.key === 'ArrowDown') {
      nextIndex = (index + 1) % providers.length;
    } else if (event.key === 'Home') {
      nextIndex = 0;
    } else if (event.key === 'End') {
      nextIndex = providers.length - 1;
    }
    if (nextIndex === null) return;

    event.preventDefault();
    onSelectProvider(providers[nextIndex].id);
    optionRefs.current[nextIndex]?.focus();
  };

  return (
    <div className="space-y-3">
      <div id={labelId} className="block text-xs font-bold text-[var(--color-text-muted)] uppercase tracking-wider font-mono">
        {label}
      </div>
      <div className="grid grid-cols-3 gap-2.5" role="radiogroup" aria-labelledby={labelId}>
        {providers.map((p, index) => {
          const isSelected = selectedProvider === p.id;
          return (
            <button
              key={p.id}
              type="button"
              ref={(element) => { optionRefs.current[index] = element; }}
              onClick={() => onSelectProvider(p.id)}
              onKeyDown={(event) => selectProviderFromKeyboard(event, index)}
              role="radio"
              aria-checked={isSelected}
              tabIndex={isSelected ? 0 : -1}
              className={`flex flex-col items-center justify-center text-center gap-2 p-3.5 rounded-xl border-2 transition-all cursor-pointer ${
                isSelected
                  ? 'border-[var(--color-text-primary)] bg-[var(--color-bg-tertiary)]'
                  : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)]/50 hover:border-[var(--color-text-muted)] hover:bg-[var(--color-bg-tertiary)]'
              }`}
            >
              <div className="p-1 rounded-md">
                <ProviderIcon provider={p.id} className="w-6 h-6" />
              </div>
              <span
                className={`text-xs font-bold font-mono truncate max-w-full ${
                  isSelected ? 'text-[var(--color-text-primary)]' : 'text-[var(--color-text-secondary)]'
                }`}
              >
                {p.name}
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}
