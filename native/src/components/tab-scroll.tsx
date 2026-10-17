import { createContext, useContext, type ReactNode } from 'react';
import { useSharedValue, type SharedValue } from 'react-native-reanimated';

/**
 * The active tab's scroll offset, shared between the screen and the tab bar.
 *
 * Reanimated reads it on the UI thread, so the search field can collapse in the
 * same frame the list scrolls instead of waiting for a React render. There is
 * one value per tab navigator: switching tabs leaves it where the screen left
 * it, which is exactly the state the bar should reflect.
 */
const TabScrollContext = createContext<SharedValue<number> | null>(null);

export function TabScrollProvider({ children }: { children: ReactNode }) {
  const scrollY = useSharedValue(0);
  return <TabScrollContext.Provider value={scrollY}>{children}</TabScrollContext.Provider>;
}

export function useTabScrollY(): SharedValue<number> {
  const scrollY = useContext(TabScrollContext);
  if (!scrollY) {
    throw new Error('useTabScrollY must be used inside TabScrollProvider');
  }
  return scrollY;
}
