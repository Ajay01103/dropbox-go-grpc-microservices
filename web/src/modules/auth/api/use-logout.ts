import { useMutation, useQueryClient } from "@tanstack/react-query"

import { logoutAction } from "@/actions/auth"
import { currentUserKey } from "./use-current-user"

export function useLogout() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async () => {
      // Server action revokes the refresh token in Redis and clears both
      // auth cookies. After this, the currentUser query will return null.
      await logoutAction()

      // Drop the cached user so every subscriber sees signed-out state
      // immediately without waiting for the next refetch interval.
      queryClient.removeQueries({ queryKey: currentUserKey })
    },
  })
}
