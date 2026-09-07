"use client"

// Single source of truth for client-side auth state.
//
// Auth is now entirely cookie-driven. The middleware (proxy.ts) ensures a
// fresh access-token cookie exists before any protected page renders, and
// server actions forward it to the backend. Client JS never sees the token.
//
// `useAuth()` derives everything from the `currentUser` TanStack Query:
//   - isAuthenticated  → currentUser !== null
//   - isLoadingAuth    → query is still in its initial fetch
//   - currentUser      → the fetched profile (or null when signed out)
//
// `AuthProvider` is now a plain passthrough — no silent-refresh side-effect
// needed because middleware already guarantees a valid cookie on page load.

import { useMemo, type ReactNode } from "react"

import type { CurrentUser } from "@/modules/auth/api/use-current-user"
import { useCurrentUser } from "@/modules/auth/api/use-current-user"

export interface AuthState {
  /** True once the session has been confirmed (user fetched or confirmed absent). */
  isAuthenticated: boolean
  /**
   * True during the initial current-user fetch on first mount.
   * Use this to gate protected queries and avoid flicker on first paint.
   */
  isLoadingAuth: boolean
  /** Profile returned by the backend, null when signed out. */
  currentUser: CurrentUser | null
  /** True while the current-user query is in flight. */
  isLoadingUser: boolean
}

/**
 * Single auth hook. Derives all auth state from the `currentUser` query —
 * no in-memory token store, no manual refresh, no tokenStore subscription.
 */
export function useAuth(): AuthState {
  const userQuery = useCurrentUser()

  return useMemo<AuthState>(
    () => ({
      isAuthenticated: userQuery.data !== null && userQuery.data !== undefined,
      // Still in the initial load if the query has never settled.
      isLoadingAuth: userQuery.isLoading,
      currentUser: userQuery.data ?? null,
      isLoadingUser: userQuery.isFetching,
    }),
    [userQuery.data, userQuery.isLoading, userQuery.isFetching],
  )
}

/**
 * Mount once near the root of the client tree.
 * No side-effects needed — middleware has already ensured a valid cookie.
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  return <>{children}</>
}
