import { useMemo } from "react"
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

// The service caps one batch at 100 ids (MaxThumbnailURLBatch) and a grid page
// is 50, but infinite scroll can push the visible list past the cap — so the
// hook chunks instead of letting the whole request be rejected, which would
// blank every tile in the grid.
const THUMBNAIL_URL_BATCH_SIZE = 100

// The presigned URL it returns is valid for 10 minutes; caching it for less
// means a live grid re-mints before a signature can expire underneath it.
const THUMBNAIL_URL_STALE_MS = 8 * 60 * 1000

const NO_THUMBNAIL_URLS: ReadonlyMap<string, string> = new Map()

/**
 * Presigned thumbnail URLs for a set of files, keyed by file id.
 *
 * The metadata service checks ownership and preview readiness and hands back a
 * signed object-storage URL per file, so the browser loads each tile straight
 * from storage: no thumbnail bytes pass through the Next.js server, and one
 * round trip covers a whole page of cards instead of one request per card.
 *
 * What the `/api/thumbnails/<file_id>` pipe bought and this gives up: its URL
 * was content-addressed (`?v=<key hash>`), identical on every render, and
 * therefore cacheable as immutable across page loads. A presigned URL carries
 * its signature in the query string, so it is stable only while this query is
 * cached — a reload re-mints each tile (the bytes still live at the same
 * content-addressed key; only the URL is per-session).
 */
export function useThumbnailUrls(fileIds: string[]): {
  urls: ReadonlyMap<string, string>
  isLoading: boolean
  error: Error | undefined
} {
  const ids = useMemo(() => Array.from(new Set(fileIds.filter(Boolean))).sort(), [fileIds])

  // isLoading (= isPending && isFetching), not isPending: a query disabled for
  // an empty id list is pending forever, and that must not read as "loading".
  const { data, isLoading, error } = useQuery({
    queryKey: ["thumbnail-urls", ids],
    queryFn: async () => {
      const batches: string[][] = []
      for (let i = 0; i < ids.length; i += THUMBNAIL_URL_BATCH_SIZE) {
        batches.push(ids.slice(i, i + THUMBNAIL_URL_BATCH_SIZE))
      }
      const responses = await Promise.all(
        batches.map((fileIds) => metadataCoreBrowserRpcClient.getThumbnailURLs({ fileIds })),
      )
      const urls = new Map<string, string>()
      for (const response of responses) {
        for (const thumbnail of response.thumbnails) {
          // No URL means "no ready preview yet" (thumbnailStatus) or "not this
          // caller's file" (error) — either way the tile keeps its icon.
          if (thumbnail.url) urls.set(thumbnail.fileId, thumbnail.url)
        }
      }
      return urls
    },
    enabled: ids.length > 0,
    staleTime: THUMBNAIL_URL_STALE_MS,
    retry: 1,
  })

  return { urls: data ?? NO_THUMBNAIL_URLS, isLoading, error: error ?? undefined }
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

// useRenameFile renames a file in place. The rename lands in files_by_id,
// files_by_folder and the folder item index together (see
// MetadataService.RenameFileOwned), so a single invalidate of the file tree is
// what makes the new name appear in the grid, in folder listings, and in the
// recent-items feed at once.
export const useRenameFile = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ fileId, newName }: { fileId: string; newName: string }) =>
      metadataFileBrowserRpcClient.renameFile({ fileId, newName: newName.trim() }),
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
//
// useMoveItems batches N selected items into ONE mutation: a single
// invalidate after every move settles (Promise.allSettled), so the grid
// doesn't flicker through N intermediate states — and a failed move is
// reported per item instead of silently dropped.
export type MoveItemResult = { itemId: string; ok: true } | { itemId: string; ok: false; name?: string }

export const useMoveItems = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      items,
      targetFolderId,
    }: {
      items: { itemId: string; itemType: "file" | "folder"; name?: string }[]
      targetFolderId: string
    }): Promise<MoveItemResult[]> => {
      const results = await Promise.allSettled(
        items.map((it) =>
          it.itemType === "folder"
            ? metadataBrowserRpcClient.moveFolder({ folderId: it.itemId, newParentId: targetFolderId })
            : metadataFileBrowserRpcClient.moveFile({ fileId: it.itemId, newFolderId: targetFolderId }),
        ),
      )
      return results.map((result, index) => {
        const item = items[index]
        if (result.status === "fulfilled") return { itemId: item.itemId, ok: true }
        return { itemId: item.itemId, ok: false, name: item.name }
      })
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: fileQueryKeys.all })
    },
  })
}

// Legacy single-item hooks, kept for callers that move exactly one item
// (e.g. keyboard actions); the DnD path uses useMoveItems.
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

// usePermanentlyDeleteFolder deletes a folder together with every descendant
// folder and file inside it. It resolves to counts rather than a job because a
// subtree is not one unit of work: files that could not be queued come back in
// failedCount, so the caller can report a partial delete instead of assuming
// the whole tree is gone. Garbage-collecting the files is asynchronous, so the
// trash and folder listings are refreshed but may briefly show queued files.
export const usePermanentlyDeleteFolder = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (folderId: string) =>
      metadataBrowserRpcClient.permanentlyDeleteFolder({ folderId }),
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
