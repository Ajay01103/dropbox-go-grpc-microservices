import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web"; // fetch-based — works on Edge runtime

import { AuthService } from "@/gen/pb/auth/auth_pb";
import { REFRESH_TOKEN_COOKIE_NAME, ACCESS_TOKEN_COOKIE_NAME } from "@/lib/auth-cookie";
import { validateRefreshToken } from "@/lib/server-jwt";
import { isAccessTokenExpiredOrMissing } from "@/lib/jwt-utils";

const authTransport = createConnectTransport({
  baseUrl: process.env.AUTH_RPC_URL ?? "http://localhost:50051",
  useBinaryFormat: true,
});
const authClient = createClient(AuthService, authTransport);

function redirectToLogin(request: NextRequest) {
  const res = NextResponse.redirect(new URL("/login", request.url));
  res.cookies.delete(REFRESH_TOKEN_COOKIE_NAME);
  res.cookies.delete(ACCESS_TOKEN_COOKIE_NAME);
  return res;
}

function setAuthCookies(response: NextResponse, accessToken: string, refreshToken: string) {
  const secure = process.env.NODE_ENV === "production";

  response.cookies.set(REFRESH_TOKEN_COOKIE_NAME, refreshToken, {
    httpOnly: true,
    secure,
    sameSite: "strict",
    path: "/",
    maxAge: 7 * 24 * 60 * 60,
  });

  response.cookies.set(ACCESS_TOKEN_COOKIE_NAME, accessToken, {
    httpOnly: true,
    secure,
    sameSite: "strict",
    path: "/",
    maxAge: 15 * 60, // match your EdDSA access token TTL
  });
}

export default async function proxy(request: NextRequest) {
  const refreshToken = request.cookies.get(REFRESH_TOKEN_COOKIE_NAME)?.value;

  const validation = await validateRefreshToken(refreshToken);
  if (!validation.valid) {
    return redirectToLogin(request);
  }

  const accessToken = request.cookies.get(ACCESS_TOKEN_COOKIE_NAME)?.value;

  // Access token still fresh — nothing to do.
  if (!isAccessTokenExpiredOrMissing(accessToken)) {
    return NextResponse.next();
  }

  // Access token missing/expired — refresh it here, where cookie writes
  // are permitted (proxy runs before RSC render / route handlers).
  try {
    const res = await authClient.refreshToken({ refreshToken: refreshToken! });

    const response = NextResponse.next();
    setAuthCookies(response, res.accessToken, res.refreshToken);
    return response;
  } catch (err) {
    console.error("[proxy] token refresh failed", err);
    return redirectToLogin(request);
  }
}

export const config = {
  matcher: [
    "/",
    "/dashboard",
    "/dashboard/:path*",
    "/workspace",
    "/workspace/:path*",
    "/notes",
    "/notes/:path*",
    "/whiteboard",
    "/whiteboard/:path*",
    // NOTE: /api/rpc/* is intentionally excluded — the route handler
    // reads the access_token cookie directly and attaches it as Bearer.
    // Running the full refresh-token validation here would race against
    // the JWKS fetch and incorrectly block valid browser RPC calls.
  ],
};
