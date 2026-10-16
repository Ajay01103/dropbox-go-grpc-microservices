import { QueryClient, focusManager } from '@tanstack/react-query';
import { AppState, type AppStateStatus } from 'react-native';

/**
 * The app's single QueryClient.
 *
 * Native has no window focus event, so React Query's refetch-on-focus is wired
 * to AppState instead: returning to the app marks the cache focused, which is
 * what makes a backgrounded-for-hours screen refresh itself on foreground.
 */
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Mobile networks drop constantly; one automatic retry keeps a flaky
      // connection from turning into an error screen.
      retry: 1,
      staleTime: 30_000,
      gcTime: 5 * 60_000,
      refetchOnWindowFocus: true,
      refetchOnReconnect: true,
    },
    mutations: {
      // A mutation is a user action: retrying it silently could double-charge
      // or double-create. Screens surface the failure instead.
      retry: 0,
    },
  },
});

export function setFocusedFromAppState(nextState: AppStateStatus): void {
  focusManager.setFocused(nextState === 'active');
}

/** Subscribes React Query to AppState. Returns the unsubscribe function. */
export function watchAppStateForQueries(): () => void {
  const subscription = AppState.addEventListener('change', setFocusedFromAppState);
  return () => subscription.remove();
}
