"use client"

import { createClient } from "@connectrpc/connect"
import { createConnectTransport, createGrpcWebTransport } from "@connectrpc/connect-web"

import { AuthService } from "../gen/pb/auth/auth_pb"
import { UploadService } from "../gen/pb/upload/upload_pb"

// Same-origin proxy paths — browser calls Next.js, Next.js attaches Bearer
// from the HttpOnly cookie and forwards to the real Go service.
// No NEXT_PUBLIC_*_RPC_URL, no credentials:include, no token in JS.
const AUTH_BASE_URL = "/api/rpc/auth"
const UPLOAD_BASE_URL = "/api/rpc/upload"

function createTransport(baseUrl: string) {
  // Go services use connectrpc with h2c — they accept grpc-web but not the
  // Connect protocol's application/connect+proto content type.
  // gRPC-Web works over HTTP/1.1 (what the browser → Next.js leg uses) and
  // the proxy forwards it unchanged to the Go service over HTTP/1.1 h2c.
  return createGrpcWebTransport({
    baseUrl,
    useBinaryFormat: true,
  })
}

const uploadBrowserTransport = createConnectTransport({
  baseUrl: UPLOAD_BASE_URL,
  useBinaryFormat: true,
})

// authBrowserRpcClient: authenticated calls (GetCurrentUser, etc.) via proxy.
// login/register/logout go through server actions, not this client.
export const authBrowserRpcClient = createClient(AuthService, createTransport(AUTH_BASE_URL))
export const uploadBrowserRpcClient = createClient(UploadService, uploadBrowserTransport)
