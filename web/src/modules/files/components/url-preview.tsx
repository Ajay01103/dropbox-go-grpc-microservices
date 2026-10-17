"use client";

import { useEffect, useRef } from "react";
import { usePathname, useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";

import { create } from "@bufbuild/protobuf";
import {
  File,
  Item,
  ItemSchema,
} from "@/gen/pb/metadata/metadata_pb";
import type { File as FileT } from "@/gen/pb/metadata/metadata_pb";
import { metadataFileBrowserRpcClient } from "@/lib/rpc";
import { FilePreviewDrawer } from "@/modules/files/components/file-preview-drawer";

/**
 * Converts a pb File into the Item shape FilePreviewDrawer expects.
 */
function itemFromFile(file: FileT): Item {
  return create(ItemSchema, {
    itemType: "file",
    itemId: file.fileId,
    folderId: file.folderId,
    details: { case: "file", value: file },
  });
}

/**
 * URL-driven preview: fetches the file by id and renders the same
 * FilePreviewDrawer used by in-grid previews. Used by both the intercepted
 * @modal route (drawer over the live grid) and the full-page route (hard
 * refresh / shared link).
 *
 * History semantics: callers push once when opening from the grid; closing
 * always goes back so the grid page (still mounted underneath the modal) is
 * restored with its scroll position.
 */
export function UrlFilePreview({ fileId, folderId }: { fileId: string; folderId: string }) {
  const router = useRouter();
  // Set while navigating to the editor. Closing the drawer calls router.back()
  // to restore the grid underneath the modal, but that would cancel an edit
  // navigation that is still in flight: the URL change closes the drawer as
  // the edit route takes over, and the back() would win. The flag makes "we are
  // leaving on purpose" distinguishable from "the user dismissed the preview".
  const leavingForEditor = useRef(false);

  // The @modal slot survives soft navigation: moving to a route with no
  // matching intercepted page (the editor, a folder, …) can leave the last
  // rendered drawer mounted. Deriving `open` from the URL closes the preview
  // the moment the route stops naming it — including the Edit navigation —
  // and reopens it if Back returns here, without relying on an unmount.
  const pathname = usePathname();
  const isPreviewRoute = pathname === `/files/${folderId}/preview/${fileId}`;

  const query = useQuery({
    queryKey: ["preview-file", fileId],
    queryFn: async () => {
      const response = await metadataFileBrowserRpcClient.getFile({ fileId });
      return response;
    },
    staleTime: 60_000,
  });

  useEffect(() => {
    if (query.isError) {
      // File gone or not owned — return to the folder rather than dangling.
      router.replace(`/files/${folderId}`);
    }
  }, [query.isError, router, folderId]);

  const file = query.data;

  return (
    <FilePreviewDrawer
      item={file ? itemFromFile(file) : null}
      open={Boolean(file) && isPreviewRoute}
      onEdit={(item) => {
        // Edit is a real route, not an overlay: the editor replaces the preview
        // (push, so Back returns here) and the grid is left behind because a
        // document engine needs the whole viewport.
        leavingForEditor.current = true;
        router.push(`/files/${folderId}/edit/${item.itemId}`);
      }}
      onOpenChange={(open) => {
        if (open || leavingForEditor.current) {
          return;
        }
        router.back();
      }}
    />
  );
}
