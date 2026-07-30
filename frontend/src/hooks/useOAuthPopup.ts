import { useCallback, useEffect, useRef } from 'react';
import { listenForOAuthMessage, type OAuthErrorMessage, type OAuthSuccessMessage } from '../utils/oauth';

export type OAuthPopupHandlers = {
  onSuccess: (msg: OAuthSuccessMessage) => void;
  onError: (errorCode: OAuthErrorMessage['error_code']) => void;
};

/**
 * Opens a centered OAuth popup and listens for a purpose-scoped postMessage
 * from the API origin (confused-deputy safe via listenForOAuthMessage).
 */
export function useOAuthPopup(apiUrl: string) {
  const cleanupRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    return () => {
      cleanupRef.current?.();
      cleanupRef.current = null;
    };
  }, []);

  const openOAuthPopup = useCallback(
    (provider: string, purpose: string, handlers: OAuthPopupHandlers) => {
      cleanupRef.current?.();
      cleanupRef.current = null;

      const width = 600;
      const height = 700;
      const left = window.screen.width / 2 - width / 2;
      const top = window.screen.height / 2 - height / 2;
      const expectedOrigin = new URL(apiUrl, window.location.origin).origin;

      const popup = window.open(
        `${apiUrl}/api/oauth/auth?provider=${encodeURIComponent(provider)}&purpose=${encodeURIComponent(purpose)}&origin=${encodeURIComponent(window.location.origin)}`,
        'OAuth',
        `width=${width},height=${height},left=${left},top=${top}`,
      );

      const cleanup = listenForOAuthMessage(expectedOrigin, {
        expectedPurpose: purpose,
        expectedSource: popup,
        onSuccess: (msg) => {
          if (msg.provider !== provider) return;
          handlers.onSuccess(msg);
          cleanupRef.current = null;
        },
        onError: (msg) => {
          handlers.onError(msg.error_code);
          cleanupRef.current = null;
        },
      });

      // Also drop the listener if the user closes the popup manually.
      const checkClosedInterval = setInterval(() => {
        let closed = false;
        try {
          closed = !popup || popup.closed;
        } catch {
          // COOP may block popup.closed; postMessage remains primary signal.
        }
        if (closed) {
          clearInterval(checkClosedInterval);
          cleanup();
          cleanupRef.current = null;
        }
      }, 1000);

      const wrappedCleanup = () => {
        clearInterval(checkClosedInterval);
        cleanup();
      };
      cleanupRef.current = wrappedCleanup;
    },
    [apiUrl],
  );

  return { openOAuthPopup };
}
