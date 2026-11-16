import { useRef, type ReactNode, type RefObject } from 'react';
import { createPortal } from 'react-dom';
import { useFocusTrap } from '../../../hooks/useFocusTrap';

type ModalDialogProps = {
  /** Id of the element that titles the dialog. */
  labelledBy: string;
  /** Receives focus when the dialog opens; usually the cancel button. */
  initialFocusRef: RefObject<HTMLElement | null>;
  onEscape: () => void;
  className: string;
  children: ReactNode;
};

/** Accessible modal frame: overlay, focus trap, Escape dismissal and focus restoration. */
export function ModalDialog({ labelledBy, initialFocusRef, onEscape, className, children }: ModalDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null);
  useFocusTrap(dialogRef, initialFocusRef, onEscape);

  return createPortal(
    <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby={labelledBy} tabIndex={-1} className={className}>
        {children}
      </div>
    </div>,
    document.body,
  );
}
