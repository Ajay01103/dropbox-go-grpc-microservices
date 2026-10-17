import { useMemo } from 'react';
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';

import { useSession } from '@/auth/session';
import type { Item } from '@/gen/pb/metadata/metadata_pb';
import { FileSort } from '@/gen/pb/metadata/metadata_pb';
import {
  metadataFileRpcClient,
  metadataFolderRpcClient,
  metadataRpcClient,
} from '@/lib/rpc';

/**
 * Every metadata read the file surfaces depend on.
 *
 * Mirrors web's `modules/files/api/use-files.ts`: the screens never touch an
 * RPC client, only these hooks, and query keys live here so an invalidation
 * cannot silently miss a cached page.
 */

const PAGE_SIZE = 30;

// The service caps one thumbnail batch at 100 ids; a grid that infinite-scrolls
// past that would otherwise blank every tile in the request.
const THUMBNAIL_URL_BATCH_SIZE = 100;

// Presigned URLs are valid for 10 minutes, so the cache stays comfortably under
// that or a live list would render expired signatures.
const THUMBNAIL_URL_STALE_MS = 8 * 60 * 1000;

const NO_THUMBNAIL_URLS: ReadonlyMap<string, string> = new Map();
const NO_LABELS: ReadonlyMap<string, string> = new Map();

/** What a row shows under its name when the parent folder could not be resolved. */
export const ROOT_LABEL = 'Dropbox';

export const fileQueryKeys = {
  all: ['files'] as const,
  recentItems: () => [...fileQueryKeys.all, 'recent-items'] as const,
  rootFolder: () => [...fileQueryKeys.all, 'root-folder'] as const,
  folderItems: (folderId: string, sort: FileSort) =>
    [...fileQueryKeys.all, 'folder-items', { folderId, sort }] as const,
  thumbnailUrls: (ids: string[]) => ['thumbnail-urls', ids] as const,
  folderLabels: (ids: string[]) => [...fileQueryKeys.all, 'folder-labels', ids] as const,
};

type ItemsPage = {
  items: Item[];
  nextPageToken: string;
};

/**
 * The account-wide recent list, newest access first.
 *
 * Gated on the session: the transport needs a bearer token to answer, and a
 * signed-out launch must not fire a request that can only 401.
 */
export function useRecentItems() {
  const status = useSession();

  return useInfiniteQuery({
    queryKey: fileQueryKeys.recentItems(),
    initialPageParam: '',
    enabled: status === 'signedIn',
    queryFn: async ({ pageParam }): Promise<ItemsPage> => {
      const response = await metadataFileRpcClient.listRecentItems({
        pageToken: pageParam,
        pageSize: PAGE_SIZE,
      });

      return {
        items: response.items,
        nextPageToken: response.nextPageToken,
      };
    },
    getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
    staleTime: 15_000,
  });
}

/**
 * Presigned thumbnail URLs for a set of files, keyed by file id.
 *
 * One batch call covers the whole visible list instead of one request per row,
 * and the bytes load straight from object storage. Empty means "no ready
 * preview yet" or "no longer owned" — either way the row keeps its icon.
 */
export function useThumbnailUrls(fileIds: string[]): {
  urls: ReadonlyMap<string, string>;
  isLoading: boolean;
} {
  const ids = useMemo(() => Array.from(new Set(fileIds.filter(Boolean))).sort(), [fileIds]);

  const { data, isLoading } = useQuery({
    queryKey: fileQueryKeys.thumbnailUrls(ids),
    queryFn: async () => {
      const batches: string[][] = [];
      for (let i = 0; i < ids.length; i += THUMBNAIL_URL_BATCH_SIZE) {
        batches.push(ids.slice(i, i + THUMBNAIL_URL_BATCH_SIZE));
      }

      const responses = await Promise.all(
        batches.map((batch) => metadataRpcClient.getThumbnailURLs({ fileIds: batch })),
      );

      const urls = new Map<string, string>();
      for (const response of responses) {
        for (const thumbnail of response.thumbnails) {
          if (thumbnail.url) urls.set(thumbnail.fileId, thumbnail.url);
        }
      }
      return urls;
    },
    // isPending is also true for a disabled query, which must not read as
    // "loading" forever when a list happens to contain no files.
    enabled: ids.length > 0,
    staleTime: THUMBNAIL_URL_STALE_MS,
    retry: 1,
  });

  return { urls: data ?? NO_THUMBNAIL_URLS, isLoading };
}

/**
 * Human location labels for a set of parent folder ids, e.g. "Dropbox › some".
 *
 * Recent items carry only the parent's id, so the names come from one
 * GetBreadcrumbs per distinct folder (there are usually one or two) rather than
 * one per row. A folder that fails to resolve falls back to the root label: a
 * missing subtitle is never worth failing the list over.
 */
export function useFolderLabels(parentFolderIds: string[]): ReadonlyMap<string, string> {
  const ids = useMemo(
    () => Array.from(new Set(parentFolderIds.filter(Boolean))).sort(),
    [parentFolderIds],
  );

  const { data } = useQuery({
    queryKey: fileQueryKeys.folderLabels(ids),
    queryFn: async () => {
      const results = await Promise.allSettled(
        ids.map((folderId) => metadataFolderRpcClient.getBreadcrumbs({ folderId })),
      );

      const labels = new Map<string, string>();
      results.forEach((result, index) => {
        if (result.status !== 'fulfilled') return;
        // The chain runs root -> parent and the root folder's own name is empty
        // (its name is "" and its path is "/"), so the root contributes the
        // product name rather than a segment.
        const names = result.value.folders.map((folder) => folder.name).filter(Boolean);
        labels.set(ids[index], [ROOT_LABEL, ...names].join(' › '));
      });
      return labels;
    },
    enabled: ids.length > 0,
    staleTime: 5 * 60 * 1000,
    retry: 1,
  });

  return data ?? NO_LABELS;
}

/**
 * The owner's root folder, created on first use.
 *
 * The tab is a folder browser without per-folder routes, so every session needs
 * this id before it can list anything; `GetOrCreateRootFolder` is idempotent.
 */
export function useRootFolder() {
  const status = useSession();

  return useQuery({
    queryKey: fileQueryKeys.rootFolder(),
    queryFn: async () => {
      const response = await metadataFolderRpcClient.getOrCreateRootFolder({});
      if (!response.folder) throw new Error('Could not resolve your root folder.');
      return response.folder;
    },
    enabled: status === 'signedIn',
    staleTime: 30 * 60 * 1000,
  });
}

/**
 * One folder's contents: sub-folders and files together, sorted by the service.
 *
 * The empty folder id means "the caller's root", which the service resolves, so
 * the screen can render before `useRootFolder` has answered.
 */
export function useFolderItems(folderId: string, sort: FileSort) {
  const status = useSession();

  return useInfiniteQuery({
    queryKey: fileQueryKeys.folderItems(folderId, sort),
    initialPageParam: '',
    enabled: status === 'signedIn',
    queryFn: async ({ pageParam }): Promise<ItemsPage> => {
      const response = await metadataFolderRpcClient.listFolderItems({
        folderId,
        pageToken: pageParam,
        pageSize: PAGE_SIZE,
        sort,
      });

      return {
        items: response.items,
        nextPageToken: response.nextPageToken,
      };
    },
    getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
    staleTime: 10_000,
  });
}

/** Soft-deletes one item. Files and folders live on different services. */
export function useDeleteItem() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ id, type }: { id: string; type: 'file' | 'folder' }) => {
      if (type === 'folder') {
        await metadataFolderRpcClient.deleteFolder({ folderId: id });
        return;
      }
      await metadataFileRpcClient.deleteFile({ fileId: id });
    },
    onSuccess: async () => {
      // The item disappears from the folder list, the recent feed, and every
      // cached page of both.
      await queryClient.invalidateQueries({ queryKey: fileQueryKeys.all });
    },
  });
}
