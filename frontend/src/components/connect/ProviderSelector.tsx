import { ProviderIcon } from './ProviderIcon';

export interface ProviderSelectorProps<T extends string = string> {
  providers: { id: T; name: string }[];
  selectedProvider: T;
  onSelectProvider: (id: T) => void;
  label?: string;
}

export function ProviderSelector<T extends string = string>({
  providers,
  selectedProvider,
  onSelectProvider,
  label,
}: ProviderSelectorProps<T>) {
  return (
    <div className="space-y-3">
      {label && (
        <div className="block text-xs font-bold text-[var(--color-text-muted)] uppercase tracking-wider font-mono">
          {label}
        </div>
      )}
      <div className="grid grid-cols-2 gap-3">
        {providers.map((p) => {
          const isSelected = selectedProvider === p.id;
          return (
            <button
              key={p.id}
              type="button"
              onClick={() => onSelectProvider(p.id)}
              aria-pressed={isSelected}
              className={`flex flex-col items-center justify-center text-center gap-2 p-3.5 rounded-xl border-2 transition-all cursor-pointer ${
                isSelected
                  ? 'border-[var(--color-text-primary)] bg-[var(--color-bg-tertiary)] shadow-sm'
                  : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)]/50 hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]'
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
