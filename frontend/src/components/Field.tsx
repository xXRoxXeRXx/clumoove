import { cloneElement, useId, type ReactElement, type ReactNode } from 'react';

interface FieldProps {
  label: ReactNode;
  children: ReactElement<{ id?: string; 'aria-describedby'?: string; 'aria-errormessage'?: string; 'aria-invalid'?: boolean }>;
  id?: string;
  hint?: ReactNode;
  error?: ReactNode;
  className?: string;
}

/** Associates one native control with its visible label, help text, and error. */
export function Field({ label, children, id, hint, error, className = '' }: FieldProps) {
  const generatedId = useId();
  const controlId = id ?? children.props.id ?? `field-${generatedId}`;
  const hintId = hint ? `${controlId}-hint` : undefined;
  const errorId = error ? `${controlId}-error` : undefined;
  const describedBy = [children.props['aria-describedby'], hintId, errorId].filter(Boolean).join(' ') || undefined;

  return (
    <div className={`ui-field ${className}`}>
      <label htmlFor={controlId} className="ui-field-label">{label}</label>
      {cloneElement(children, {
        id: controlId,
        'aria-describedby': describedBy,
        'aria-errormessage': errorId,
        'aria-invalid': error ? true : children.props['aria-invalid'],
      })}
      {hint && <p id={hintId} className="ui-field-hint">{hint}</p>}
      {error && <p id={errorId} role="alert" className="ui-field-error">{error}</p>}
    </div>
  );
}
