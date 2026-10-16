import { useMutation, useQueryClient } from '@tanstack/react-query';

import { authClient } from '@/auth/authClient';
import { session } from '@/auth/session';
import { clearTokens, getStoredRefreshToken } from '@/auth/tokenManager';

/**
 * Signs out.
 *
 * The server is told first so the refresh token is revoked rather than merely
 * forgotten — a token only the client forgot stays valid for anyone who copied
 * it. That call is best-effort: if the phone is offline the user still expects
 * to be signed out locally, and the token expires on its own server-side.
 */
export function useLogout() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async () => {
      const refreshToken = await getStoredRefreshToken();
      if (refreshToken) {
        try {
          await authClient.logout({ refreshToken });
        } catch {
          // Offline or already revoked: signing out locally is still correct.
        }
      }

      await clearTokens();
      // Drop every cached answer — including currentUserKey — before the
      // guards swap to the auth stack, so the next user of this device never
      // sees the previous one's data.
      queryClient.clear();
      session.set('signedOut');
    },
  });
}
