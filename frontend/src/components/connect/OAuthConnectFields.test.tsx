import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import { OAuthConnectFields } from './OAuthConnectFields';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

describe('OAuthConnectFields', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it('derives connect heading and button label from an unknown provider id without falling back to Dropbox', async () => {
    await act(async () => {
      root.render(
        <OAuthConnectFields
          provider="acme"
          oauthUser=""
          connected={false}
          onConnect={vi.fn()}
          onDisconnect={vi.fn()}
        />
      );
    });

    expect(container.textContent).toContain('acme Connection');
    expect(container.textContent).toContain('Connect with acme');
    expect(container.textContent).not.toContain('Dropbox');
  });

  it('derives the connected account fallback label from an unknown provider id in both en and de', async () => {
    await act(async () => {
      root.render(
        <OAuthConnectFields
          provider="acme-oauth"
          oauthUser=""
          connected={true}
          onConnect={vi.fn()}
          onDisconnect={vi.fn()}
        />
      );
    });

    expect(container.textContent).toContain('acme-oauth Connection');
    expect(container.textContent).toContain('acme-oauth Account');
    expect(container.textContent).not.toContain('Dropbox');

    await act(async () => {
      await i18n.changeLanguage('de');
    });

    expect(container.textContent).toContain('acme-oauth Verbindung');
    expect(container.textContent).toContain('acme-oauth-Konto');
    expect(container.textContent).not.toContain('Dropbox');
  });

  it.each([
    {
      provider: 'dropbox',
      enConnect: 'Dropbox Connection',
      enButton: 'Connect with Dropbox',
      enAccount: 'Dropbox Account',
      deConnect: 'Dropbox Verbindung',
      deButton: 'Mit Dropbox verbinden',
      deAccount: 'Dropbox-Konto',
    },
    {
      provider: 'google',
      enConnect: 'Google Connection',
      enButton: 'Connect with Google',
      enAccount: 'Google Account',
      deConnect: 'Google Verbindung',
      deButton: 'Mit Google verbinden',
      deAccount: 'Google-Konto',
    },
    {
      provider: 'onedrive',
      enConnect: 'OneDrive Connection',
      enButton: 'Connect with OneDrive',
      enAccount: 'OneDrive Account',
      deConnect: 'OneDrive Verbindung',
      deButton: 'Mit OneDrive verbinden',
      deAccount: 'OneDrive-Konto',
    },
    {
      provider: 'hidrive',
      enConnect: 'HiDrive Connection',
      enButton: 'Connect with HiDrive',
      enAccount: 'HiDrive Account',
      deConnect: 'HiDrive Verbindung',
      deButton: 'Mit HiDrive verbinden',
      deAccount: 'HiDrive-Konto',
    },
    {
      provider: 'box',
      enConnect: 'Box Connection',
      enButton: 'Connect with Box',
      enAccount: 'Box Account',
      deConnect: 'Box Verbindung',
      deButton: 'Mit Box verbinden',
      deAccount: 'Box-Konto',
    },
  ])('renders unchanged labels for $provider in en and de', async (spec) => {
    await act(async () => {
      root.render(
        <OAuthConnectFields
          provider={spec.provider}
          oauthUser=""
          connected={false}
          onConnect={vi.fn()}
          onDisconnect={vi.fn()}
        />
      );
    });
    expect(container.textContent).toContain(spec.enConnect);
    expect(container.textContent).toContain(spec.enButton);

    await act(async () => {
      root.render(
        <OAuthConnectFields
          provider={spec.provider}
          oauthUser=""
          connected={true}
          onConnect={vi.fn()}
          onDisconnect={vi.fn()}
        />
      );
    });
    expect(container.textContent).toContain(spec.enAccount);

    await act(async () => {
      await i18n.changeLanguage('de');
    });
    expect(container.textContent).toContain(spec.deConnect);
    expect(container.textContent).toContain(spec.deAccount);

    await act(async () => {
      root.render(
        <OAuthConnectFields
          provider={spec.provider}
          oauthUser=""
          connected={false}
          onConnect={vi.fn()}
          onDisconnect={vi.fn()}
        />
      );
    });
    expect(container.textContent).toContain(spec.deButton);
  });
});
