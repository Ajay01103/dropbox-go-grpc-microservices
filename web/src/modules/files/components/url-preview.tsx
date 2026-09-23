"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
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
      open={Boolean(file)}
      onOpenChange={(open) => {
        if (!open) {
          router.back();
        }
      }}
    />
  );
}
