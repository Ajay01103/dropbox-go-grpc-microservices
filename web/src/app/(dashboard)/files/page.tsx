import { redirect } from "next/navigation"

import { create } from "@bufbuild/protobuf"
import { getServerRpcClients } from "@/lib/rpc-server"
import { GetOrCreateRootFolderRequestSchema } from "@/gen/pb/metadata/metadata_pb"

// /files is a bare alias; the canonical browsing surface is /files/<rootFolderId>
// so every view (including the root) has a shareable, rename-proof URL.
export default async function FilesIndexPage() {
  const { folderClient } = await getServerRpcClients()
  const root = await folderClient.getOrCreateRootFolder(create(GetOrCreateRootFolderRequestSchema, {}))
  if (!root.folder?.folderId) {
    // GetOrCreateRootFolder creates the root on first access, so a missing id
    // means the backend is misbehaving — surface it rather than loop.
    throw new Error("Metadata service returned no root folder")
  }
  redirect(`/files/${root.folder.folderId}`)
}
