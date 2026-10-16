import { cookies } from "next/headers"
import type { NextRequest } from "next/server"

import { createClient } from "@connectrpc/connect"
import { createGrpcTransport } from "@connectrpc/connect-node"

import { MetadataService } from "@/gen/pb/metadata/metadata_pb"
import { ACCESS_TOKEN_COOKIE_NAME } from "@/lib/auth-cookie"

// The Go services speak gRPC over h2c, so this route needs the Node runtime.
export const runtime = "nodejs"

const METADATA_RPC_URL = process.env.METADATA_RPC_URL ?? "http://localhost:50053"

// Thumbnails are content-addressed: the `v` query param is the thumbnail key's
// hash, so a regenerated preview resolves to a different URL and the bytes for
// this one can never go stale — there is nothing to revalidate and the browser
// may keep them for as long as it likes. `private` keeps the response out of
// shared caches, because these are one user's bytes.
const CACHE_CONTROL = "private, max-age=31536000, immutable"

/**
 * Stable, cookie-authenticated thumbnail byte route.
 *
 * The browser fetches `/api/thumbnails/<file_id>?v=<key hash>` from <img> tags
 * on every render of a grid and gets the same URL every time, so the response
 * is served from the browser cache instead of minting a fresh, uncacheable
 * presigned S3 URL per tile per render.
 *
 * Auth is the HttpOnly access-token cookie the browser already sends
 * same-origin (the route is under the same Next.js origin the app is served
 * from). Ownership and the "is a preview ready" check still happen in the
 * metadata service: this route is a byte pipe, exactly like /content is for
 * whole files, and it never invents its own authorization rule.
 */
export async function GET(
  request: NextRequest,
  { params }: { params: Promise<{ fileId: string }> },
) {
  const { fileId } = await params
  if (!fileId) {
    return new Response("missing file id", { status: 400 })
  }

  const store = await cookies()
  const token = store.get(ACCESS_TOKEN_COOKIE_NAME)?.value
  if (!token) {
    return new Response("unauthenticated", { status: 401 })
  }

  const version = request.nextUrl.searchParams.get("v") ?? ""
  const etag = `"${version || fileId}"`
  if (version && request.headers.get("if-none-match") === etag) {
    return new Response(null, {
      status: 304,
      headers: { ETag: etag, "Cache-Control": CACHE_CONTROL },
    })
  }

  const metadataClient = createClient(
    MetadataService,
    createGrpcTransport({
      baseUrl: METADATA_RPC_URL,
      interceptors: [
        (next) => (req) => {
          req.header.set("Authorization", `Bearer ${token}`)
          return next(req)
        },
      ],
    }),
  )

  let objectUrl = ""
  try {
    objectUrl = (await metadataClient.getThumbnailURL({ fileId })).url
  } catch {
    // An unknown id and a file this user does not own are indistinguishable
    // here, and both are just a 404 to an <img>.
    return new Response("thumbnail unavailable", { status: 404 })
  }
  if (!objectUrl) {
    return new Response("thumbnail not ready", { status: 404 })
  }

  const upstream = await fetch(objectUrl)
  if (!upstream.ok || !upstream.body) {
    return new Response("thumbnail fetch failed", { status: 502 })
  }

  const headers = new Headers({
    "Content-Type": upstream.headers.get("content-type") ?? "image/jpeg",
    "Cache-Control": CACHE_CONTROL,
    ETag: etag,
  })
  const contentLength = upstream.headers.get("content-length")
  if (contentLength) {
    headers.set("Content-Length", contentLength)
  }

  return new Response(upstream.body, { status: 200, headers })
}
