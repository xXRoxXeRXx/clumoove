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

  it('names the search field and exposes the selected view through mutually exclusive pressed buttons', async () => {
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);

    await act(async () => {
      root.render(<SelectedPathsViewer paths={['/documents', '/photos']} maxVisible={1} />);
    });

    const openButton = container.querySelector<HTMLButtonElement>('button[aria-haspopup="dialog"]')!;
    await act(async () => openButton.click());

    expect(container.querySelector('input[aria-label="Search selected paths"]')).not.toBeNull();
    const viewGroup = container.querySelector('[role="group"][aria-label="Selected paths view"]')!;
    const [treeButton, listButton] = Array.from(viewGroup.querySelectorAll<HTMLButtonElement>('button'));
    expect(treeButton.getAttribute('aria-pressed')).toBe('true');
    expect(listButton.getAttribute('aria-pressed')).toBe('false');

    await act(async () => listButton.click());
    expect(treeButton.getAttribute('aria-pressed')).toBe('false');
    expect(listButton.getAttribute('aria-pressed')).toBe('true');
  });
});
