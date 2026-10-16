import { Redirect } from 'expo-router';

import { useSession } from '@/auth/session';

/**
 * The entry route.
 *
 * It exists so a cold start has somewhere deterministic to land: the session
 * store has already been resolved by the root layout's splash gate, so this
 * only has to send the user to the stack their session allows. The guards in
 * the root layout do the same job for every later navigation.
 */
export default function IndexRedirect() {
  const status = useSession();

  return <Redirect href={status === 'signedIn' ? '/home' : '/login'} />;
}
