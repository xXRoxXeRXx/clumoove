// Hardened OAuth postMessage receiver (M-3).
//
// The backend's OAuth callback page posts tokens back to window.opener via
// postMessage(data, targetOrigin), where targetOrigin is a validated allowlist
// entry — correct on the SENDER side. The receiver (this code) MUST independently
// verify event.origin before trusting any token, otherwise a malicious page
// could impersonate the OAuth callback (confused-deputy). Tokens are kept in
// memory (never localStorage) exactly like the password flow.

export interface OAuthSuccessMessage {
  type: 'oauth-success';
  provider: string;
  purpose: string;
  token: string;
  refreshToken: string;
  expiresIn: number;
  username: string;
}

export interface OAuthErrorMessage {
  type: 'oauth-error';
  error: string;
}

export type OAuthMessage = OAuthSuccessMessage | OAuthErrorMessage;

/**
 * Registers a window 'message' listener for OAuth results. Only messages whose
 * event.origin matches the API origin are accepted. Returns a cleanup function
 * that removes the listener.
 *
 * @param expectedOrigin The API origin (e.g. new URL(API_URL).origin). Messages
 *   from any other origin are ignored to prevent confused-deputy token theft.
 * @param handlers Callbacks invoked with the validated message.
 */
export function listenForOAuthMessage(
  expectedOrigin: string,
  handlers: {
    onSuccess: (msg: OAuthSuccessMessage) => void;
    onError: (msg: OAuthErrorMessage) => void;
    expectedPurpose?: string;
  }
): () => void {
  const listener = (event: MessageEvent) => {
    // 1. Origin allowlist check — the single most important control (M-3).
    if (event.origin !== expectedOrigin) {
      return;
    }
    // 2. Reject messages without a source (cannot be the popup window).
    if (!event.source) {
      return;
    }
    const data = event.data as OAuthMessage | undefined;
    if (!data || (data.type !== 'oauth-success' && data.type !== 'oauth-error')) {
      return;
    }

    // 3. Scope the message to its intended purpose so the account-connect
    //    flow and the (separate) OAuth-login flow don't cross-fire.
    if (data.type === 'oauth-success' && handlers.expectedPurpose !== undefined && data.purpose !== handlers.expectedPurpose) {
      return;
    }

    if (data.type === 'oauth-success') {
      handlers.onSuccess(data);
    } else {
      handlers.onError(data);
    }

    // Close the popup if we opened it (best-effort).
    try {
      (event.source as WindowProxy).close();
    } catch {
      // cross-origin close may throw; ignore.
    }
  };

  window.addEventListener('message', listener);
  return () => window.removeEventListener('message', listener);
}
