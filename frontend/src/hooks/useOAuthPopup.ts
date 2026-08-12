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
      const screenWithPosition = window.screen as Screen & { availLeft?: number; availTop?: number };
      const left = (screenWithPosition.availLeft ?? 0) + (window.screen.availWidth - width) / 2;
      const top = (screenWithPosition.availTop ?? 0) + (window.screen.availHeight - height) / 2;
      const apiOrigin = new URL(apiUrl, window.location.origin);
      const expectedOrigin = apiOrigin.origin;
      const authorizationUrl = new URL('/api/oauth/auth', apiOrigin);
      authorizationUrl.searchParams.set('provider', provider);
      authorizationUrl.searchParams.set('purpose', purpose);
      authorizationUrl.searchParams.set('origin', window.location.origin);

      const popup = window.open(
        authorizationUrl.toString(),
        'OAuth',
        `width=${width},height=${height},left=${left},top=${top}`,
      );

      if (!popup) {
        handlers.onError('OAUTH_POPUP_BLOCKED');
        return;
      }

      let cleanup = () => {};
      let checkClosedInterval = 0;
      let disposed = false;
      const dispose = () => {
        if (disposed) return;
        disposed = true;
        clearInterval(checkClosedInterval);
        cleanup();
        if (cleanupRef.current === dispose) cleanupRef.current = null;
      };

      cleanup = listenForOAuthMessage(expectedOrigin, {
        expectedPurpose: purpose,
        expectedSource: popup,
        onSuccess: (msg) => {
          if (msg.provider !== provider) return;
          dispose();
          handlers.onSuccess(msg);
        },
        onError: (msg) => {
          dispose();
          handlers.onError(msg.error_code);
        },
      });

      // If a future receiver delivers synchronously, remove the listener now
      // that it has been returned instead of leaving a dead subscription.
      if (disposed) {
        cleanup();
        return;
      }

      // Also drop the listener if the user closes the popup manually.
      checkClosedInterval = window.setInterval(() => {
        let closed = false;
        try {
          closed = popup.closed;
        } catch {
          // COOP may block popup.closed; postMessage remains primary signal.
        }
        if (closed) {
          dispose();
          handlers.onError('OAUTH_CANCELLED');
        }
      }, 1000);

      cleanupRef.current = dispose;
    },
    [apiUrl],
  );

  return { openOAuthPopup };
}
