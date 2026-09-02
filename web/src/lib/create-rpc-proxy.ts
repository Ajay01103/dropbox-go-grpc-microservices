import "server-only";

import { cookies } from "next/headers";
import type { NextRequest } from "next/server";

import { ACCESS_TOKEN_COOKIE_NAME } from "@/lib/auth-cookie";

/**
 * Factory for a Connect-protocol proxy Route Handler.
 *
 * The browser calls Next.js at `/api/rpc/<service>/…` (same-origin, no CORS,
 * no token in JS). This handler:
 *   1. Reads the HttpOnly access-token cookie server-side.
 *   2. Strips the Next.js mount prefix from the URL path.
 *   3. Forwards the request byte-for-byte to the real Go service.
 *   4. Attaches `Authorization: Bearer <token>` so the Go authInterceptor
 *      sees a normal Bearer request — zero backend changes required.
 *
 * @param upstreamBaseUrl  e.g. "http://localhost:9091"
 * @param mountPath        e.g. "/api/rpc/workspace" (prefix to strip)
 */
export function createRpcProxy(upstreamBaseUrl: string, mountPath: string) {
  return async function handler(request: NextRequest): Promise<Response> {
    const store = await cookies();
    const token = store.get(ACCESS_TOKEN_COOKIE_NAME)?.value;

    // Strip the Next.js mount prefix so the upstream path is correct.
    const upstreamPath = request.nextUrl.pathname.slice(mountPath.length) || "/";
    const upstreamUrl = `${upstreamBaseUrl}${upstreamPath}${request.nextUrl.search}`;

    const headers = new Headers(request.headers);
    // Never forward the browser Host header — it confuses HTTP/2 upstreams.
    headers.delete("host");
    // Never forward browser cookies upstream — the upstream only speaks Bearer.
    headers.delete("cookie");
    if (token) {
      headers.set("Authorization", `Bearer ${token}`);
    }

    const upstreamResponse = await fetch(upstreamUrl, {
      method: request.method,
      headers,
      body: request.method === "GET" || request.method === "HEAD" ? undefined : request.body,
      // Required by Node.js for streaming request bodies (Connect streaming RPCs).
      // @ts-expect-error — Node fetch duplex option not in DOM types
      duplex: "half",
    });

    return new Response(upstreamResponse.body, {
      status: upstreamResponse.status,
      headers: upstreamResponse.headers,
    });
  };
}
