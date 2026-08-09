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

  function selectAt(index: number): void {
    const next = (index + items.length) % items.length;
    onChange(items[next].value);
    tabRefs.current[next]?.focus();
  }

  function handleKeyDown(event: React.KeyboardEvent<HTMLButtonElement>, index: number): void {
    switch (event.key) {
      case 'ArrowRight':
        event.preventDefault();
        selectAt(index + 1);
        return;
      case 'ArrowLeft':
        event.preventDefault();
        selectAt(index - 1);
        return;
      case 'Home':
        event.preventDefault();
        selectAt(0);
        return;
      case 'End':
        event.preventDefault();
        selectAt(items.length - 1);
        return;
      default:
        return;
    }
  }

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
              onKeyDown={(event) => handleKeyDown(event, index)}
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
