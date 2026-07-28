import {
  useCallback,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import { ToastContext, type ToastItem, type ToastType } from './toastContextCore';

let toastSeq = 0;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);

  const dismiss = useCallback((id: number) => {
    setItems((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const toast = useCallback((text: string, type: ToastType = 'error') => {
    const id = ++toastSeq;
    setItems((prev) => [...prev.slice(-4), { id, text, type }]);
    window.setTimeout(() => dismiss(id), 4500);
  }, [dismiss]);

  const value = useMemo(() => ({ toast }), [toast]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div
        className="fixed bottom-4 right-4 z-[200] flex flex-col gap-2 max-w-sm w-[min(100%-2rem,24rem)] pointer-events-none"
        aria-live="polite"
      >
        {items.map((item) => {
          const styles =
            item.type === 'success'
              ? 'bg-[var(--color-success-bg)] border-[var(--color-success-border)] text-[var(--color-success-text)]'
              : item.type === 'info'
                ? 'bg-[var(--color-info-bg)] border-[var(--color-info-border)] text-[var(--color-info-text)]'
                : 'bg-[var(--color-error-bg)] border-[var(--color-error-border)] text-[var(--color-error-text)]';
          return (
            <div
              key={item.id}
              role="status"
              className={`pointer-events-auto rounded-md border px-3 py-2 text-sm leading-relaxed ${styles}`}
            >
              {item.text}
            </div>
          );
        })}
      </div>
    </ToastContext.Provider>
  );
}
