import "server-only";

import { cache } from "react";
import { createClient, type Interceptor } from "@connectrpc/connect";
import { createGrpcTransport } from "@connectrpc/connect-node"; // ✅ NOT connect-web
import { AuthService } from "@/gen/pb/auth/auth_pb";
import { getServerAccessToken } from "@/lib/server-access-token";

// ─────────────────────────────────────────────────────────────
// Unauthenticated auth client — login, register, logout, refresh.
// These happen pre-session or with credentials supplied in the
// request body, so no Bearer token is needed or available.
// ─────────────────────────────────────────────────────────────
const AUTH_RPC_URL = process.env.AUTH_RPC_URL ?? "http://localhost:50051";

export const unauthenticatedAuthClient = createClient(
  AuthService,
  createGrpcTransport({ baseUrl: AUTH_RPC_URL }),
);

// ✅ Private — no NEXT_PUBLIC_ prefix, never reaches browser bundle
const AUTH_BASE_URL = process.env.AUTH_RPC_URL ?? "http://localhost:50051";

function bearerInterceptor(token: string | null): Interceptor {
  return (next) => (req) => {
    if (token) req.header.set("Authorization", `Bearer ${token}`);
    return next(req);
  };
}

function makeTransport(baseUrl: string, token: string | null) {
  return createGrpcTransport({
    // ✅ HTTP/2 + gRPC framing — works in Node.js
    baseUrl,
    // httpVersion: "2",
    interceptors: [bearerInterceptor(token)],
  });
}

/**
 * React cache() = one evaluation per request.
 * All nested Server Components share the same clients + token within a render.
 */
export const getServerRpcClients = cache(async () => {
  const token = await getServerAccessToken();

  const make = (url: string) => makeTransport(url, token);

  return {
    token,
    authClient: createClient(AuthService, make(AUTH_BASE_URL)),
  };
});
