const CONTROL_SELECTOR = 'input:not([type="hidden"]), select, textarea';

type FormControl = HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;

let nextFormControlId = 0;

/**
 * The control a label most plausibly belongs to: the nearest unlabelled control after it, or
 * for checkbox-then-label markup the nearest one before it. A control that already has a
 * label is never taken, so several label/control pairs in one parent each keep their own.
 */
function nearestControl(label: HTMLLabelElement, parent: Element): FormControl | undefined {
  const candidates = Array.from(parent.querySelectorAll<FormControl>(CONTROL_SELECTOR))
    .filter((control) => !control.labels?.length);
  const following = candidates.find((control) => label.compareDocumentPosition(control) & Node.DOCUMENT_POSITION_FOLLOWING);
  if (following) return following;
  return [...candidates].reverse().find((control) => label.compareDocumentPosition(control) & Node.DOCUMENT_POSITION_PRECEDING);
}

/**
 * Provider-specific fields mount dynamically; this keeps native label semantics for labels
 * that were rendered without `htmlFor`, without duplicating IDs across every provider branch.
 * Prefer `Field` (or an explicit `htmlFor`) in new code; this only repairs what is missing.
 */
export function associateUnlinkedFormLabels(root: ParentNode): void {
  const labels = root instanceof HTMLLabelElement
    ? [root]
    : Array.from(root.querySelectorAll<HTMLLabelElement>('label:not([for])'));
  labels.forEach((label) => {
    if (label.control || label.closest('[role="group"], [role="radiogroup"]')) return;
    const parent = label.parentElement;
    const control = parent ? nearestControl(label, parent) : undefined;
    if (!control) return;
    if (!control.id) {
      nextFormControlId += 1;
      control.id = `ui-field-${nextFormControlId}`;
    }
    label.htmlFor = control.id;
  });
}
