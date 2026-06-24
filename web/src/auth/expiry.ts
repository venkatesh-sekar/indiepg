// A tiny pub/sub bridge so the generic data-fetching hooks can signal a 401 /
// session-expiry to the SessionProvider WITHOUT importing the React context
// (keeping the hooks decoupled and the bundle small). In practice there is
// exactly one subscriber: the SessionProvider.
//
// Why this exists: the session cookie can expire mid-use, or an operator logging
// out in another tab rotates the server's HMAC secret and invalidates this tab's
// session. Without this bridge, every poll/fetch would just keep returning 401
// while the SPA still believes it is authenticated — leaving the operator stuck
// staring at a generic error instead of being routed back to /login.

type Listener = () => void;

const listeners = new Set<Listener>();

/** Subscribe to session-expiry signals. Returns an unsubscribe function. */
export function onSessionExpired(fn: Listener): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

/** Signal that a request failed authentication — the session is no longer valid. */
export function notifySessionExpired(): void {
  for (const fn of listeners) fn();
}
