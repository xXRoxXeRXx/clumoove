import { useContext } from 'react';
import { ToastContext } from './ToastContextCore';

export function useToast() {
  const ctx = useContext(ToastContext);
  if (ctx === undefined) {
    throw new Error('useToast must be used within a ToastProvider');
  }
  return ctx.toast;
}
