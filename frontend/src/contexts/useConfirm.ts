import { useContext } from 'react';
import { ConfirmationContext } from './ConfirmationContext';

export function useConfirmationContext() {
  const context = useContext(ConfirmationContext);
  if (context === undefined) {
    throw new Error('useConfirm must be used within a ConfirmationProvider');
  }
  return context;
}

export function useConfirm() {
  return useConfirmationContext().confirm;
}

/** Cancel any open confirmation (e.g. when the app navigates to another step). */
export function useDismissConfirm() {
  return useConfirmationContext().dismiss;
}
