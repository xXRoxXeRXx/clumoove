import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react';

export type ToastType = 'success' | 'error' | 'info';

export type ToastItem = {
  id: number;
  text: string;
  type: ToastType;
};

type ToastContextValue = {
  toast: (text: string, type?: ToastType) => void;
};

const ToastContext = createContext<ToastContextValue | undefined>(undefined);

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
              ? 'bg-emerald-50 border-emerald-200 text-emerald-900'
              : item.type === 'info'
                ? 'bg-blue-50 border-blue-200 text-blue-900'
                : 'bg-rose-50 border-rose-200 text-rose-900';
          return (
            <div
              key={item.id}
              role="status"
              className={`pointer-events-auto px-3.5 py-2.5 rounded-xl border shadow-lg text-xs font-mono leading-relaxed animate-fade-in ${styles}`}
            >
              {item.text}
            </div>
          );
        })}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast() {
  const ctx = useContext(ToastContext);
  if (!ctx) {
    throw new Error('useToast must be used within a ToastProvider');
  }
  return ctx.toast;
}
