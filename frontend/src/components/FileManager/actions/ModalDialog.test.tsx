import { act, useRef, useState } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { RenameDialog } from './EntryDialogs';
import { ModalDialog } from './ModalDialog';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

/** Mounts the dialog only while open, as the action dialogs do. */
function Harness({ onEscape }: { onEscape: () => void }) {
  const [open, setOpen] = useState(false);
  const cancelRef = useRef<HTMLButtonElement>(null);
  return (
    <>
      <button type="button" data-testid="trigger" onClick={() => setOpen(true)}>Open</button>
      {open && (
        <ModalDialog
          labelledBy="dlg-title"
          initialFocusRef={cancelRef}
          onEscape={() => {
            onEscape();
            setOpen(false);
          }}
          className="dlg"
        >
          <h2 id="dlg-title">Title</h2>
          <button ref={cancelRef} type="button">Cancel</button>
          <button type="button">Confirm</button>
        </ModalDialog>
      )}
    </>
  );
}

describe('ModalDialog', () => {
  let container: HTMLDivElement;
  let root: Root;
  const onEscape = vi.fn();

  beforeEach(() => {
    vi.useFakeTimers();
    onEscape.mockReset();
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    act(() => root.render(<Harness onEscape={onEscape} />));
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.useRealTimers();
  });

  const trigger = () => container.querySelector<HTMLButtonElement>('[data-testid="trigger"]')!;
  const dialog = () => document.body.querySelector<HTMLElement>('[role="dialog"]');
  const key = (name: string, init: KeyboardEventInit = {}) => {
    const event = new KeyboardEvent('keydown', { key: name, bubbles: true, cancelable: true, ...init });
    act(() => {
      document.dispatchEvent(event);
    });
    return event;
  };
  const open = () => {
    trigger().focus();
    act(() => trigger().click());
    act(() => {
      vi.runAllTimers();
    });
  };

  it('is a labelled modal portalled to the body, focusing the initial control on open', () => {
    expect(dialog()).toBeNull();
    open();
    expect(container.contains(dialog())).toBe(false);
    expect(dialog()?.getAttribute('aria-modal')).toBe('true');
    expect(dialog()?.getAttribute('aria-labelledby')).toBe('dlg-title');
    expect(document.activeElement?.textContent).toBe('Cancel');
    expect(document.body.style.overflow).toBe('hidden');
  });

  it('wraps Tab focus within the dialog', () => {
    open();
    const [cancel, confirm] = Array.from(dialog()!.querySelectorAll('button'));
    confirm.focus();
    expect(key('Tab').defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(cancel);
    expect(key('Tab', { shiftKey: true }).defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(confirm);
  });

  it('dismisses on Escape, then restores the trigger focus and page scrolling', () => {
    open();
    key('Escape');
    expect(onEscape).toHaveBeenCalledTimes(1);
    expect(dialog()).toBeNull();
    expect(document.activeElement).toBe(trigger());
    expect(document.body.style.overflow).toBe('');
  });

  it('does not react to keys after it closed', () => {
    open();
    key('Escape');
    key('Escape');
    expect(onEscape).toHaveBeenCalledTimes(1);
  });

  it('yields focus trapping and Escape handling to ConflictDialog during rename conflicts', () => {
    const close = vi.fn();
    const cancel = vi.fn();
    act(() => {
      root.render(
        <RenameDialog
          model={{
            name: 'taken.txt',
            busy: false,
            error: '',
            setName: vi.fn(),
            submit: vi.fn(),
            close,
            conflict: {
              strategies: ['SKIP', 'RENAME'],
              busy: false,
              resolve: vi.fn(),
              cancel,
            },
          }}
        />,
      );
    });
    act(() => {
      vi.runAllTimers();
    });

    const conflictDlg = document.getElementById('mutation-conflict-title')?.closest<HTMLElement>('[role="dialog"]');
    const renameOverlay = document.getElementById('rename-entry-title')?.closest<HTMLElement>('[aria-hidden="true"]');
    expect(conflictDlg).not.toBeNull();
    expect(renameOverlay).not.toBeNull();

    const buttons = Array.from(conflictDlg!.querySelectorAll('button'));
    expect(document.activeElement).toBe(buttons[0]);

    buttons[buttons.length - 1].focus();
    expect(key('Tab').defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(buttons[0]);

    key('Escape');
    expect(cancel).toHaveBeenCalledTimes(1);
    expect(close).not.toHaveBeenCalled();
  });
});
