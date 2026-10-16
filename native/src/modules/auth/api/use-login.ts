import { useMutation, useQueryClient } from '@tanstack/react-query';

import { authClient } from '@/auth/authClient';
import { session } from '@/auth/session';
import { setTokens } from '@/auth/tokenManager';
import { currentUserKey } from './use-current-user';

export type LoginInput = {
  email: string;
  password: string;
};

/**
 * Signs a user in and hands the app a session.
 *
 * Order matters: the token pair is stored before the session flips to
 * signed-in, because the router reacts to that flip immediately and every query
 * it mounts assumes a readable token.
 */
export function useLogin() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ email, password }: LoginInput) => {
      const response = await authClient.login({ email: email.trim(), password });
      await setTokens(response.accessToken, response.refreshToken);

      // A previous session's cached files must never be visible to this one.
      queryClient.removeQueries({ queryKey: currentUserKey });
      session.set('signedIn');

      return response;
    },
  });
}
