import { Stack } from 'expo-router';

/**
 * The signed-out stack.
 *
 * Screens are declared in order because the first one is where the router lands
 * when the guard opens this group — so a sign-out always lands on login, not on
 * whichever file happens to sort first.
 */
export default function AuthLayout() {
  return (
    <Stack screenOptions={{ headerShown: false }}>
      <Stack.Screen name="login" />
      <Stack.Screen name="register" />
    </Stack>
  );
}
