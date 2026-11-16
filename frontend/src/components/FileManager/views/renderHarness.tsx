import { act, type ReactNode } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach } from 'vitest';
import i18n from '../../../i18n';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

/** Mounts a presentation module into jsdom for a test, with English strings. */
export function useRenderHarness() {
  const state = { container: null as unknown as HTMLDivElement, root: null as unknown as Root };

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    state.container = document.createElement('div');
    document.body.append(state.container);
    state.root = createRoot(state.container);
  });

  afterEach(() => {
    act(() => state.root.unmount());
    state.container.remove();
  });

  return {
    get container() {
      return state.container;
    },
    render: async (node: ReactNode) => {
      await act(async () => state.root.render(node));
    },
    click: async (element: Element | null) => {
      if (!element) throw new Error('element to click not found');
      await act(async () => {
        element.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
      });
    },
  };
}
