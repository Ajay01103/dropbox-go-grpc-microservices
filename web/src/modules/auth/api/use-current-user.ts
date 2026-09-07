"use client"

import { queryOptions, useQuery } from "@tanstack/react-query"

import { getCurrentUserAction } from "@/actions/auth"

export type CurrentUser = {
  userId: string
  email: string
  name: string
}

/**
 * Single source-of-truth query key for the current user. Use this with
 * `queryClient.invalidateQueries({ queryKey: currentUserKey })` /
 * `queryClient.removeQueries({ queryKey: currentUserKey })` everywhere so
 * logout flows don't have to hard-code the array.
 */
export const currentUserKey = ["currentUser"] as const

export function currentUserOptions() {
  return queryOptions({
    queryKey: currentUserKey,
    // Server action reads the HttpOnly access-token cookie — no token ever
    // reaches client JS. Returns null when the session is absent/invalid.
    queryFn: (): Promise<CurrentUser | null> => getCurrentUserAction(),
    staleTime: 5 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
    refetchOnWindowFocus: false,
    retry: 1,
  })
}

type UseCurrentUserOptions = {
  /** Override whether the query runs. Defaults to always enabled. */
  enabled?: boolean
}

/**
 * Returns the current user's profile.
 *
 * No token-store gate — auth is cookie-driven. The query always runs; it
 * returns null when the user is signed out (server action returns null when
 * the access-token cookie is absent or the RPC call fails).
 */
export function useCurrentUser(options: UseCurrentUserOptions = {}) {
  const { enabled = true } = options

  return useQuery({
    ...currentUserOptions(),
    enabled,
  })
}
