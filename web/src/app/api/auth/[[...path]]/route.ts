import { createRpcProxy } from "@/lib/create-rpc-proxy"

// Covers authenticated auth-service calls: logout (via Go), GetCurrentUser, etc.
// login/register/refresh bypass this entirely — they go through server actions
// (loginAction, registerAction, refreshAccessTokenAction) which call the auth
// service directly via unauthenticatedAuthClient, no proxy needed.
const handler = createRpcProxy(
  process.env.AUTH_RPC_URL ?? "http://localhost:50051",
  "/api/rpc/auth",
)

export { handler as GET, handler as POST }
