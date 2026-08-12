import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import i18n from '../i18n';
import { SelectedPathsViewer } from './SelectedPathsViewer';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

describe('SelectedPathsViewer accessibility', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage('en');
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
  });

  async function renderViewer(paths: string[] = ['/documents', '/photos/vacation.jpg']): Promise<HTMLButtonElement> {
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<SelectedPathsViewer paths={paths} maxVisible={1} />);
    });
    return container.querySelector<HTMLButtonElement>('button[aria-haspopup="dialog"]')!;
  }

  function setInputValue(input: HTMLInputElement, value: string): void {
    const valueSetter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set;
    valueSetter?.call(input, value);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  }

  it('names the search field and exposes the selected view through mutually exclusive pressed buttons', async () => {
    const openButton = await renderViewer(['/documents', '/photos']);
    await act(async () => openButton.click());

    expect(container.querySelector(`input[aria-label="${i18n.t('paths.searchLabel')}"]`)).not.toBeNull();
    const viewGroup = container.querySelector(`[role="group"][aria-label="${i18n.t('paths.viewModeLabel')}"]`)!;
    const [treeButton, listButton] = Array.from(viewGroup.querySelectorAll<HTMLButtonElement>('button'));
    expect(treeButton.getAttribute('aria-pressed')).toBe('true');
    expect(listButton.getAttribute('aria-pressed')).toBe('false');

    await act(async () => listButton.click());
    expect(treeButton.getAttribute('aria-pressed')).toBe('false');
    expect(listButton.getAttribute('aria-pressed')).toBe('true');
  });

  it('provides a modal dialog that traps focus and restores it when Escape closes it', async () => {
    const openButton = await renderViewer();
    openButton.focus();
    await act(async () => {
      openButton.click();
    });
    await act(async () => {
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });

    const dialog = container.querySelector<HTMLElement>('[role="dialog"]')!;
    const title = dialog.querySelector('h3')!;
    const closeButton = dialog.querySelector<HTMLButtonElement>(`button[aria-label="${i18n.t('paths.close')}"]`)!;
    const footerCloseButton = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button'))
      .find((button) => button.textContent === i18n.t('paths.close'))!;

    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(dialog.getAttribute('aria-labelledby')).toBe(title.id);
    expect(document.activeElement).toBe(closeButton);

    await act(async () => {
      footerCloseButton.focus();
    });
    expect(document.activeElement).toBe(footerCloseButton);
    await act(async () => {
      footerCloseButton.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
    });
    expect(document.activeElement).toBe(closeButton);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(openButton);
  });

  it('closes from the backdrop and filters paths from the labelled search input', async () => {
    const openButton = await renderViewer();
    await act(async () => openButton.click());

    const search = container.querySelector<HTMLInputElement>(`input[aria-label="${i18n.t('paths.searchLabel')}"]`)!;
    await act(async () => setInputValue(search, 'vacation'));
    const dialog = container.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(dialog.textContent).toContain('/photos/vacation.jpg');
    expect(dialog.textContent).not.toContain('/documents');

    const backdrop = container.querySelector<HTMLElement>('.fixed.inset-0')!;
    await act(async () => backdrop.click());
    expect(container.querySelector('[role="dialog"]')).toBeNull();
  });

  it('renders a root-path fallback when no paths were selected', async () => {
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<SelectedPathsViewer />);
    });

    expect(container.textContent).toContain('/');
    expect(container.querySelector('[aria-haspopup="dialog"]')).toBeNull();
  });
});
