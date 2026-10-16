import { queryOptions, useQuery } from '@tanstack/react-query';

import { useSession } from '@/auth/session';
import { authRpcClient } from '@/lib/rpc';

export type CurrentUser = {
  userId: string;
  email: string;
  name: string;
};

/**
 * Single source-of-truth query key for the current user, so sign-out flows can
 * drop it without hard-coding the array. Same key and role as web's
 * use-current-user.ts.
 */
export const currentUserKey = ['currentUser'] as const;

export function currentUserOptions() {
  return queryOptions({
    queryKey: currentUserKey,
    // The bearer token is attached by the transport interceptor; the id in the
    // answer is the server's, never something a screen supplied.
    queryFn: async (): Promise<CurrentUser> => {
      const response = await authRpcClient.getCurrentUser({});
      return {
        userId: response.userId,
        email: response.email,
        name: response.name,
      };
    },
    staleTime: 5 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
  });
}

/**
 * The signed-in user's profile.
 *
 * Gated on the session rather than always-on: while signed out there is no
 * token to attach, and a 401 storm at launch is not useful.
 */
export function useCurrentUser() {
  const status = useSession();

  return useQuery({
    ...currentUserOptions(),
    enabled: status === 'signedIn',
  });
}
