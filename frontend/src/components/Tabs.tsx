import { useId, useRef, type ReactNode } from 'react';

export interface TabItem<T extends string> {
  value: T;
  label: ReactNode;
}

interface TabsProps<T extends string> {
  label: string;
  items: readonly TabItem<T>[];
  value: T;
  onChange: (value: T) => void;
  className?: string;
  children: ReactNode;
}

/** Controlled tabs with the ARIA keyboard model and roving tab stop. */
export function Tabs<T extends string>({ label, items, value, onChange, className = '', children }: TabsProps<T>) {
  const id = useId();
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const selectAt = (index: number) => {
    const next = (index + items.length) % items.length;
    onChange(items[next].value);
    tabRefs.current[next]?.focus();
  };

  return (
    <>
      <div className={className} role="tablist" aria-label={label}>
        {items.map((item, index) => {
          const selected = item.value === value;
          return (
            <button
              key={item.value}
              ref={(node) => { tabRefs.current[index] = node; }}
              id={`${id}-tab-${item.value}`}
              type="button"
              role="tab"
              aria-selected={selected}
              aria-controls={`${id}-panel-${item.value}`}
              tabIndex={selected ? 0 : -1}
              onClick={() => onChange(item.value)}
              onKeyDown={(event) => {
                if (event.key === 'ArrowRight' || event.key === 'ArrowDown') { event.preventDefault(); selectAt(index + 1); }
                if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') { event.preventDefault(); selectAt(index - 1); }
                if (event.key === 'Home') { event.preventDefault(); selectAt(0); }
                if (event.key === 'End') { event.preventDefault(); selectAt(items.length - 1); }
              }}
              className={`ui-tab ${selected ? 'ui-tab-active' : ''}`}
            >
              {item.label}
            </button>
          );
        })}
      </div>
      <div id={`${id}-panel-${value}`} role="tabpanel" aria-labelledby={`${id}-tab-${value}`}>{children}</div>
    </>
  );
}
