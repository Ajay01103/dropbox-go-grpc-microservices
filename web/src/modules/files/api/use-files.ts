import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import type { Item } from "@/gen/pb/metadata/metadata_pb"
import { FileSort } from "@/gen/pb/metadata/metadata_pb"
import {
  metadataBrowserRpcClient,
  metadataCoreBrowserRpcClient,
  metadataFileBrowserRpcClient,
} from "@/lib/rpc"

const PAGE_SIZE = 50

type FolderItemsPage = {
  items: Item[]
  nextPageToken: string
}

type RecentItemsPage = {
  items: Item[]
  nextPageToken: string
}

// Query keys
export const fileQueryKeys = {
  all: ["files"] as const,
  trash: () => [...fileQueryKeys.all, "trash"] as const,

  folders: () => [...fileQueryKeys.all, "folders"] as const,

  foldersByParent: (parentId: string) => [...fileQueryKeys.folders(), { parentId }] as const,

  folderItems: (folderId: string, sort: FileSort) =>
    [...fileQueryKeys.all, "folder-items", { folderId, sort }] as const,

  breadcrumbs: (folderId: string) => [...fileQueryKeys.all, "breadcrumbs", { folderId }] as const,

  recentItems: () => [...fileQueryKeys.all, "recent-items"] as const,
}

// Breadcrumbs are resolved from data (GetBreadcrumbs), never parsed from the
// URL — the URL carries only the opaque folder id.
export function useBreadcrumbs(folderId: string) {
  return useQuery({
    queryKey: fileQueryKeys.breadcrumbs(folderId),
    queryFn: async () => {
      const response = await metadataBrowserRpcClient.getBreadcrumbs({ folderId })
      return response.folders
    },
    staleTime: 60_000,
  })
}

export function useFolderItems(folderId = "", sort = FileSort.UPDATED_AT) {
  return useInfiniteQuery({
    queryKey: fileQueryKeys.folderItems(folderId, sort),
    initialPageParam: "",
    queryFn: async ({ pageParam }): Promise<FolderItemsPage> => {
      const response = await metadataBrowserRpcClient.listFolderItems({
        folderId,
        pageToken: pageParam,
        pageSize: PAGE_SIZE,
        sort,
      })

      return {
        items: response.items,
        nextPageToken: response.nextPageToken,
      }
    },
    getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
    staleTime: 10_000,
  })
}

/**
 * Recents are served by the owner-scoped recent-items RPC; the owner is derived by the backend from the session.
 */
export function useThumbnailUrl(fileId: string, thumbnailStatus: string, thumbnailKey: string) {
  return useQuery({
    queryKey: ["thumbnail-url", fileId, thumbnailKey],
    queryFn: async () => {
      const response = await metadataCoreBrowserRpcClient.getThumbnailURL({ fileId })
      return response.url
    },
    enabled: Boolean(fileId && thumbnailKey && (thumbnailStatus === "ready" || !thumbnailStatus)),
    staleTime: 5 * 60_000,
  })
}

export function useTrashItems() {
  return useInfiniteQuery({
    queryKey: fileQueryKeys.trash(),
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const response = await metadataFileBrowserRpcClient.listTrash({
        pageToken: pageParam,
        pageSize: PAGE_SIZE,
      })

      return response
    },
    getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
    staleTime: 10_000,
  })
}

export function useRecentItems() {
  return useInfiniteQuery({
    queryKey: fileQueryKeys.recentItems(),
    initialPageParam: "",
    queryFn: async ({ pageParam }): Promise<RecentItemsPage> => {
      const response = await metadataFileBrowserRpcClient.listRecentItems({
        pageToken: pageParam,
        pageSize: PAGE_SIZE,
      })

      return {
        items: response.items,
        nextPageToken: response.nextPageToken,
      }
    },
    getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
    staleTime: 10_000,
  })
}

interface CreateFolderInput {
  name: string
  parentId?: string
}

export const useRenameFolder = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ folderId, newName }: { folderId: string; newName: string }) =>
      metadataBrowserRpcClient.renameFolder({ folderId, newName: newName.trim() }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: fileQueryKeys.all })
    },
  })
}

export const useDeleteFolder = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (folderId: string) => metadataBrowserRpcClient.deleteFolder({ folderId }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: fileQueryKeys.all })
    },
  })
}

export const useDeleteFile = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (fileId: string) => metadataCoreBrowserRpcClient.deleteFile({ fileId }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: fileQueryKeys.all })
    },
  })
}

export const useRestoreFile = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (fileId: string) => metadataFileBrowserRpcClient.restoreFile({ fileId }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: fileQueryKeys.all })
    },
  })
}

export const useRestoreFolder = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (folderId: string) => metadataBrowserRpcClient.restoreFolder({ folderId }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: fileQueryKeys.all })
    },
  })
}

interface MoveItemInput {
  itemId: string
  targetFolderId: string
}

// useMoveFile / useMoveFolder back the drag-and-drop move interaction. Both
// invalidate the whole file query tree: the move changes the origin folder,
// the destination folder, the recent-items feed, and every cached page of
// each. Scoping per folder would miss the origin folder page the user is
// currently looking at.
export const useMoveFile = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ itemId, targetFolderId }: MoveItemInput) =>
      metadataFileBrowserRpcClient.moveFile({ fileId: itemId, newFolderId: targetFolderId }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: fileQueryKeys.all })
    },
  })
}

export const useMoveFolder = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ itemId, targetFolderId }: MoveItemInput) =>
      metadataBrowserRpcClient.moveFolder({ folderId: itemId, newParentId: targetFolderId }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: fileQueryKeys.all })
    },
  })
}

export const usePermanentlyDeleteFile = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (fileId: string) => metadataFileBrowserRpcClient.permanentlyDeleteFile({ fileId }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: fileQueryKeys.all })
    },
  })
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
      await queryClient.invalidateQueries({
        queryKey: fileQueryKeys.folderItems(variables.parentId ?? "", FileSort.UPDATED_AT),
      })
      await queryClient.invalidateQueries({
        queryKey: fileQueryKeys.recentItems(),
      })
    },
  })
}
