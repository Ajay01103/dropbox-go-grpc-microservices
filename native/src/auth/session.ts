import { useSyncExternalStore } from 'react';

/**
 * Session state lives in a tiny external store rather than React context.
 *
 * The transport interceptor is not a React component: when a refresh is
 * definitively rejected it has to flip the app to signed-out from inside a
 * plain async function. Routing that through a subscribe/snapshot store means
 * the interceptor and the router guards read exactly the same value, and
 * nothing needs to be threaded through providers.
 */
export type SessionStatus = 'loading' | 'signedIn' | 'signedOut';

let status: SessionStatus = 'loading';

const listeners = new Set<() => void>();

export const session = {
  get: (): SessionStatus => status,
  set: (next: SessionStatus) => {
    if (next === status) return;
    status = next;
    listeners.forEach((listener) => listener());
  },
  subscribe: (listener: () => void) => {
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  },
};

/** Reactive read of the session status, for components and router guards. */
export function useSession(): SessionStatus {
  return useSyncExternalStore(session.subscribe, session.get, session.get);
}
