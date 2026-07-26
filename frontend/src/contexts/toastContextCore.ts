import { createContext } from 'react';

export type ToastType = 'success' | 'error' | 'info';

export type ToastItem = {
  id: number;
  text: string;
  type: ToastType;
};

export type ToastContextValue = {
  toast: (text: string, type?: ToastType) => void;
};

export const ToastContext = createContext<ToastContextValue | undefined>(undefined);
