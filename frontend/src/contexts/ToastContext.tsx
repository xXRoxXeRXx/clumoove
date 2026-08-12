import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { ToastContext, type ToastItem, type ToastType } from './ToastContextCore';

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const seqRef = useRef(0);
  const timersRef = useRef(new Map<number, ReturnType<typeof window.setTimeout>>());

  const dismiss = useCallback((id: number) => {
    setItems((prev) => prev.filter((t) => t.id !== id));
    const timer = timersRef.current.get(id);
    if (timer !== undefined) {
      window.clearTimeout(timer);
      timersRef.current.delete(id);
    }
  }, []);

  const toast = useCallback((text: string, type: ToastType = 'info') => {
    const id = ++seqRef.current;
    setItems((prev) => {
      const removed = prev.slice(0, -4);
      for (const item of removed) {
        const timer = timersRef.current.get(item.id);
        if (timer !== undefined) {
          window.clearTimeout(timer);
          timersRef.current.delete(item.id);
        }
      }
      return [...prev.slice(-4), { id, text, type }];
    });
    const timer = window.setTimeout(() => dismiss(id), 4500);
    timersRef.current.set(id, timer);
  }, [dismiss]);

  useEffect(() => () => {
    for (const timer of timersRef.current.values()) {
      window.clearTimeout(timer);
    }
    timersRef.current.clear();
  }, []);

  const value = useMemo(() => ({ toast }), [toast]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div
        className="fixed bottom-4 right-4 z-[var(--layer-toast)] flex flex-col gap-2 max-w-sm w-[min(100%-2rem,24rem)] pointer-events-none"
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
