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
        className="fixed bottom-4 right-4 z-[var(--layer-toast)] flex flex-col gap-2 max-w-sm w-[min(100%-2rem,24rem)] pointer-events-none"
        aria-live="off"
      >
        {items.map((item) => {
          const styles =
            item.type === 'success'
              ? 'ui-alert-success'
              : item.type === 'info'
                ? 'ui-alert-info'
                : 'ui-alert-error';
          return (
            <div
              key={item.id}
              role={item.type === 'error' ? 'alert' : 'status'}
              className={`ui-alert pointer-events-auto px-3 py-2 text-sm leading-relaxed ${styles}`}
            >
              {item.text}
            </div>
          );
        })}
      </div>
    </ToastContext.Provider>
  );
}
