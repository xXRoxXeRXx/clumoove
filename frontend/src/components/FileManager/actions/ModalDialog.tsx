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
  /** Disables focus trapping and hides the frame while another modal is stacked on top. */
  active?: boolean;
  children: ReactNode;
};

/** Accessible modal frame: overlay, focus trap, Escape dismissal and focus restoration. */
export function ModalDialog({ labelledBy, initialFocusRef, onEscape, className, active = true, children }: ModalDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null);
  useFocusTrap(dialogRef, initialFocusRef, onEscape, active);

  return createPortal(
    <div
      className={`fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4${active ? '' : ' hidden'}`}
      aria-hidden={active ? undefined : true}
    >
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby={labelledBy} tabIndex={-1} className={className}>
        {children}
      </div>
    </div>,
    document.body,
  );
}
