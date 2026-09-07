import { createRpcProxy } from "@/lib/create-rpc-proxy"

const handler = createRpcProxy(
  process.env.UPLOAD_RPC_URL ?? "http://localhost:50052",
  "/api/rpc/upload",
)

export { handler as GET, handler as POST }
