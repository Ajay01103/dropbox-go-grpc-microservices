import type { NextRequest } from "next/server"
import { NextResponse } from "next/server"
import { createClient } from "@connectrpc/connect"
import { createConnectTransport } from "@connectrpc/connect-web" // fetch-based — works on Edge runtime

import { AuthService } from "@/gen/pb/auth/auth_pb"
import { REFRESH_TOKEN_COOKIE_NAME, ACCESS_TOKEN_COOKIE_NAME } from "@/lib/auth-cookie"
import { validateRefreshToken } from "@/lib/server-jwt"
import { isAccessTokenExpiredOrMissing } from "@/lib/jwt-utils"

const authTransport = createConnectTransport({
  baseUrl: process.env.AUTH_RPC_URL ?? "http://localhost:50051",
  useBinaryFormat: true,
})
const authClient = createClient(AuthService, authTransport)

type RefreshedTokens = {
  accessToken: string
  refreshToken: string
}

// Middleware can run concurrently for the document, RSC, and RPC requests
// that make up one navigation. Refresh-token rotation is single-use, so share
// one refresh request per token across those overlapping middleware calls.
const refreshRequests = new Map<string, Promise<RefreshedTokens>>()

function refreshTokens(refreshToken: string): Promise<RefreshedTokens> {
  const existing = refreshRequests.get(refreshToken)
  if (existing) {
    return existing
  }

  const request = authClient.refreshToken({ refreshToken })
  refreshRequests.set(refreshToken, request)
  void request.then(
    () => refreshRequests.delete(refreshToken),
    () => refreshRequests.delete(refreshToken),
  )
  return request
}

function redirectToLogin(request: NextRequest) {
  const res = NextResponse.redirect(new URL("/sign-in", request.url))
  res.cookies.delete(REFRESH_TOKEN_COOKIE_NAME)
  res.cookies.delete(ACCESS_TOKEN_COOKIE_NAME)
  return res
}

function setAuthCookies(response: NextResponse, accessToken: string, refreshToken: string) {
  const secure = process.env.NODE_ENV === "production"

  response.cookies.set(REFRESH_TOKEN_COOKIE_NAME, refreshToken, {
    httpOnly: true,
    secure,
    sameSite: "strict",
    path: "/",
    maxAge: 7 * 24 * 60 * 60,
  })

  response.cookies.set(ACCESS_TOKEN_COOKIE_NAME, accessToken, {
    httpOnly: true,
    secure,
    sameSite: "strict",
    path: "/",
    maxAge: 15 * 60, // match your EdDSA access token TTL
  })
}

export default async function proxy(request: NextRequest) {
  const refreshToken = request.cookies.get(REFRESH_TOKEN_COOKIE_NAME)?.value

  const validation = await validateRefreshToken(refreshToken)
  if (!refreshToken || !validation.valid) {
    return redirectToLogin(request)
  }

  const accessToken = request.cookies.get(ACCESS_TOKEN_COOKIE_NAME)?.value

  // Access token still fresh — nothing to do.
  if (!isAccessTokenExpiredOrMissing(accessToken)) {
    return NextResponse.next()
  }

  // Access token missing/expired — refresh it here, where cookie writes
  // are permitted (proxy runs before RSC render / route handlers).
  try {
    const res = await refreshTokens(refreshToken)

    const response = NextResponse.next()
    setAuthCookies(response, res.accessToken, res.refreshToken)
    return response
  } catch (err) {
    // A hot reload or another Next.js worker can lose the in-process
    // single-flight entry while the backend is finishing the rotation. The
    // backend keeps the immediately previous pair briefly, so retry once
    // before treating the session as invalid.
    try {
      const retry = await authClient.refreshToken({ refreshToken })
      const response = NextResponse.next()
      setAuthCookies(response, retry.accessToken, retry.refreshToken)
      return response
    } catch {
      console.error("[proxy] token refresh failed", err)
      return redirectToLogin(request)
    }
  }
}

export const config = {
  matcher: [
    // Protect application pages and RPC routes, but leave public pages and
    // framework/static assets out of the auth refresh path.
    "/((?!api/auth|_next/static|_next/image|favicon.ico|sign-in|sign-up).*)",
  ],
}
