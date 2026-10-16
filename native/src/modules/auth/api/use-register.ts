import { useMutation, useQueryClient } from '@tanstack/react-query';

import { authClient } from '@/auth/authClient';
import { session } from '@/auth/session';
import { setTokens } from '@/auth/tokenManager';
import { currentUserKey } from './use-current-user';

export type RegisterInput = {
  name: string;
  email: string;
  password: string;
};

/**
 * Creates an account, which also signs the new user in: the auth service
 * returns a token pair from Register, so there is no second round trip.
 */
export function useRegister() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ name, email, password }: RegisterInput) => {
      const response = await authClient.register({
        name: name.trim(),
        email: email.trim(),
        password,
      });
      await setTokens(response.accessToken, response.refreshToken);

      queryClient.removeQueries({ queryKey: currentUserKey });
      session.set('signedIn');

      return response;
    },
  });
}
