
import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"

import { metadataBrowserRpcClient } from "@/lib/rpc"

// Query keys
export const fileQueryKeys = {
  all: ["files"] as const,

  folders: () => [...fileQueryKeys.all, "folders"] as const,

  foldersByParent: (parentId: string) =>
    [...fileQueryKeys.folders(), { parentId }] as const,
}

interface CreateFolderInput {
  name: string
  parentId?: string
}

export const useCreateFolder = () => {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ name, parentId = "" }: CreateFolderInput) => {
      return metadataBrowserRpcClient.createFolder({
        parentId,
        name: name.trim(),
      })
    },
    onSuccess: async (_folder, variables) => {
      await queryClient.invalidateQueries({
        queryKey: fileQueryKeys.foldersByParent(variables.parentId ?? ""),
      })
    },
  })
}
