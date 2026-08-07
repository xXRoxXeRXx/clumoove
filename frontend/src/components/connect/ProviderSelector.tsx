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
      <div className="grid grid-cols-2 sm:grid-cols-2 lg:grid-cols-3 gap-2.5">
        {providers.map((p) => {
          const isSelected = selectedProvider === p.id;
          return (
            <button
              key={p.id}
              type="button"
              onClick={() => onSelectProvider(p.id)}
              aria-pressed={isSelected}
              className={`group flex items-center gap-2 p-2.5 text-left rounded-lg transition-all duration-150 cursor-pointer border ${
                isSelected
                  ? 'bg-[var(--color-bg-primary)] border-[var(--color-text-primary)] ring-2 ring-[var(--color-text-primary)]/20 shadow-sm'
                  : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]/80 hover:border-[var(--color-border-light)]'
              }`}
            >
              <div
                className={`p-1.5 rounded-md shrink-0 transition-transform duration-150 group-hover:scale-105 ${
                  isSelected ? 'bg-[var(--color-bg-tertiary)]' : 'bg-[var(--color-bg-tertiary)]/60'
                }`}
              >
                <ProviderIcon provider={p.id} className="w-5 h-5" />
              </div>
              <span
                className={`text-xs truncate ${
                  isSelected ? 'text-[var(--color-text-primary)] font-bold' : 'text-[var(--color-text-secondary)] group-hover:text-[var(--color-text-primary)]'
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
