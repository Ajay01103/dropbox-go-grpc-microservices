import { createRpcProxy } from "@/lib/create-rpc-proxy"

const handler = createRpcProxy(
  process.env.METADATA_RPC_URL ?? "http://localhost:50053",
  "/api/rpc/metadata",
)

export { handler as GET, handler as POST }
