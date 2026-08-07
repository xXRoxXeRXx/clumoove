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
      <div className="grid grid-cols-2 sm:grid-cols-3 md:flex md:flex-col gap-2">
        {providers.map((p) => {
          const isSelected = selectedProvider === p.id;
          return (
            <button
              key={p.id}
              type="button"
              onClick={() => onSelectProvider(p.id)}
              aria-pressed={isSelected}
              className={`flex items-center gap-3 p-2.5 text-left rounded-lg transition-all duration-150 cursor-pointer border ${
                isSelected
                  ? 'bg-[var(--color-bg-tertiary)] border-[var(--color-text-primary)] ring-2 ring-[var(--color-text-primary)]/20 shadow-sm'
                  : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]/70 hover:border-[var(--color-border-light)]'
              }`}
            >
              <div
                className={`p-1.5 rounded-md shrink-0 transition-colors ${
                  isSelected ? 'bg-[var(--color-bg-primary)] shadow-xs' : 'bg-[var(--color-bg-tertiary)]'
                }`}
              >
                <ProviderIcon provider={p.id} className="w-5 h-5" />
              </div>
              <span
                className={`text-xs font-medium truncate ${
                  isSelected ? 'text-[var(--color-text-primary)] font-semibold' : 'text-[var(--color-text-secondary)]'
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
