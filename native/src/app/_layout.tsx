import { QueryClientProvider } from '@tanstack/react-query';
import { DarkTheme, DefaultTheme, Stack, ThemeProvider } from 'expo-router';
import * as SplashScreen from 'expo-splash-screen';
import { useEffect } from 'react';
import { AppState, useColorScheme } from 'react-native';
import { GestureHandlerRootView } from 'react-native-gesture-handler';
import { TamaguiProvider } from 'tamagui';

import { tamaguiConfig } from '../../tamagui.config';
import { useSession } from '@/auth/session';
import { bootstrapSession, getValidAccessToken } from '@/auth/tokenManager';
import { queryClient, watchAppStateForQueries } from '@/lib/query-client';

// Held until the stored session has been resolved, so the first frame the user
// sees is the screen they belong on rather than a flash of the login form.
SplashScreen.preventAutoHideAsync();

/**
 * The root layout owns three things: the providers, the splash gate, and which
 * navigation stack exists at all.
 *
 * Routing is guard-driven rather than redirect-driven: a sign-out from
 * anywhere (the account screen, or the transport interceptor rejecting a
 * rotated refresh token) flips the session store, and `Stack.Protected` removes
 * the tab stack on its own. No screen has to remember to navigate away.
 */
export default function RootLayout() {
  const colorScheme = useColorScheme();
  const status = useSession();

  useEffect(() => {
    void bootstrapSession();
  }, []);

  useEffect(() => {
    if (status !== 'loading') void SplashScreen.hideAsync();
  }, [status]);

  // Native has no window focus event; AppState stands in for it.
  useEffect(() => watchAppStateForQueries(), []);

  // Coming back to the app after hours is exactly when the access token has
  // expired, so refresh in the background instead of making the first tap pay
  // for it.
  useEffect(() => {
    const subscription = AppState.addEventListener('change', (nextState) => {
      if (nextState !== 'active') return;
      void getValidAccessToken().catch(() => {
        // The interceptor's own retry path still covers a failed refresh here.
      });
    });
    return () => subscription.remove();
  }, []);

  // Sign-out must not leave the previous user's data in memory for the next one.
  useEffect(() => {
    if (status === 'signedOut') queryClient.clear();
  }, [status]);

  if (status === 'loading') return null;

  return (
    <GestureHandlerRootView style={{ flex: 1 }}>
      <TamaguiProvider config={tamaguiConfig} defaultTheme={colorScheme ?? 'light'}>
        <QueryClientProvider client={queryClient}>
          <ThemeProvider value={colorScheme === 'dark' ? DarkTheme : DefaultTheme}>
            <Stack screenOptions={{ headerShown: false }}>
              <Stack.Protected guard={status === 'signedIn'}>
                <Stack.Screen name="(tabs)" />
              </Stack.Protected>
              <Stack.Protected guard={status === 'signedOut'}>
                <Stack.Screen name="(auth)" />
              </Stack.Protected>
              {/* The entry route stays available in both states and forwards to
                  whichever stack the session allows. */}
              <Stack.Screen name="index" />
            </Stack>
          </ThemeProvider>
        </QueryClientProvider>
      </TamaguiProvider>
    </GestureHandlerRootView>
  );
}
