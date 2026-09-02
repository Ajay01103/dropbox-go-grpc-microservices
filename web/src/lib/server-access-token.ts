import "server-only";

import { cache } from "react";
import { cookies } from "next/headers";
import { ACCESS_TOKEN_COOKIE_NAME } from "@/lib/auth-cookie";

/**
 * Per-request memoised access token READ.
 *
 * No refresh, no cookie mutation happens here — that's handled
 * entirely by middleware.ts before this render ever starts, since
 * Server Components are only permitted to READ cookies, never write
 * them (Server Actions / Route Handlers only).
 *
 * By the time a Server Component runs, middleware has already
 * guaranteed a fresh access token cookie is present (or redirected
 * to /login if the session couldn't be refreshed at all).
 */
async function readAccessTokenFromCookie(): Promise<string | null> {
  const store = await cookies();
  return store.get(ACCESS_TOKEN_COOKIE_NAME)?.value ?? null;
}

export const getServerAccessToken = cache(readAccessTokenFromCookie);
