import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { apiFetch } from '../utils/apiClient';
import { AuthForm } from './AuthForm';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../utils/apiClient', () => ({ apiFetch: vi.fn() }));

function setInputValue(input: HTMLInputElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

describe('AuthForm TOTP completion', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    vi.mocked(apiFetch).mockReset();
    vi.mocked(apiFetch).mockImplementation((url) => {
      const path = String(url);
      if (path.endsWith('/api/auth/login')) {
        return Promise.resolve(new Response(JSON.stringify({ totp_required: true, temp_session: 'twofa-session' })));
      }
      if (path.endsWith('/api/auth/totp')) {
        return Promise.resolve(new Response(JSON.stringify({ must_change_password: true, temp_session: 'must-change-session' }), { status: 202 }));
      }
      return Promise.resolve(new Response(JSON.stringify({})));
    });
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
  });

  it('moves a completed TOTP login into password rotation when required', async () => {
    const onAuthSuccess = vi.fn();
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);

    await act(async () => {
      root.render(<AuthForm apiUrl="https://api.example.test" onAuthSuccess={onAuthSuccess} />);
      await Promise.resolve();
    });

    await act(async () => {
      setInputValue(container.querySelector<HTMLInputElement>('#auth-email')!, 'user@example.test');
      setInputValue(container.querySelector<HTMLInputElement>('#auth-password')!, 'correct-password');
      container.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });

    await act(async () => {
      setInputValue(container.querySelector<HTMLInputElement>('#totp-code')!, '123456');
      container.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });

    expect(onAuthSuccess).not.toHaveBeenCalled();
    expect(container.querySelector('#must-change-new-password')).not.toBeNull();
    expect(vi.mocked(apiFetch)).toHaveBeenCalledWith(
      'https://api.example.test/api/auth/totp',
      expect.objectContaining({ body: JSON.stringify({ temp_session: 'twofa-session', code: '123456' }) }),
    );
  });
});
