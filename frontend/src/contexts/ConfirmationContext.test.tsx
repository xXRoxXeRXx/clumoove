import { act, memo, useEffect } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import i18n from '../i18n';
import { ConfirmationProvider, type ConfirmFn } from './ConfirmationContext';
import { useConfirm } from './useConfirm';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const captured = {
  confirm: undefined as ConfirmFn | undefined,
  probeRenderCount: 0,
};

function ConfirmationCapture() {
  const confirm = useConfirm();
  useEffect(() => {
    captured.confirm = confirm;
  }, [confirm]);
  return null;
}

const ConfirmationIdentityProbe = memo(function ConfirmationIdentityProbe() {
  const confirm = useConfirm();
  useEffect(() => {
    captured.confirm = confirm;
    captured.probeRenderCount += 1;
  });
  return null;
});

describe('ConfirmationProvider', () => {
  let container: HTMLDivElement;
  let root: Root;
  let originalLanguage: string;

  const renderProvider = (children = <ConfirmationCapture />) => {
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    act(() => root.render(<ConfirmationProvider>{children}</ConfirmationProvider>));
  };

  afterEach(async () => {
    act(() => root?.unmount());
    container?.remove();
    await act(async () => {
      await i18n.changeLanguage(originalLanguage);
    });
  });

  it('cancels a replaced dialog and an outstanding dialog on unmount', async () => {
    originalLanguage = i18n.language;
    renderProvider();

    let first!: Promise<boolean>;
    let second!: Promise<boolean>;
    act(() => {
      first = captured.confirm!({ message: 'first dialog' });
      second = captured.confirm!({ message: 'second dialog' });
    });

    await expect(first).resolves.toBe(false);

    act(() => root.unmount());
    await expect(second).resolves.toBe(false);
  });

  it('resolves true after the user confirms', async () => {
    originalLanguage = i18n.language;
    renderProvider();

    let result!: Promise<boolean>;
    act(() => {
      result = captured.confirm!({ message: 'confirm this action' });
    });
    const buttons = container.querySelectorAll('[role="alertdialog"] button');

    act(() => (buttons[1] as HTMLButtonElement | undefined)?.click());

    await expect(result).resolves.toBe(true);
  });

  it('keeps confirm stable across language changes while using the new default title', async () => {
    originalLanguage = i18n.language;
    captured.probeRenderCount = 0;
    renderProvider(<ConfirmationIdentityProbe />);
    const firstConfirm = captured.confirm;
    const nextLanguage = originalLanguage === 'en' ? 'de' : 'en';

    await act(async () => {
      await i18n.changeLanguage(nextLanguage);
    });

    expect(captured.confirm).toBe(firstConfirm);
    expect(captured.probeRenderCount).toBe(1);

    act(() => {
      void captured.confirm!({ message: 'translated title' });
    });
    expect(container.querySelector('[role="alertdialog"] h3')?.textContent)
      .toBe(i18n.t('common.confirmTitle'));
  });
});
