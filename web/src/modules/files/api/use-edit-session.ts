"use client"

import { useQuery } from "@tanstack/react-query"

import type { File as FileT } from "@/gen/pb/metadata/metadata_pb"
import { metadataCoreBrowserRpcClient, metadataFileBrowserRpcClient } from "@/lib/rpc"

export type EditSession = {
  canEdit: boolean
  /** type_not_editable | too_large | disabled — the reason canEdit is false. */
  unavailableReason: string
  version: number
  contentHash: string
  /** Version-pinned inline URL the editor loads its bytes from. */
  ticketUrl: string
  expiresInSeconds: number
  /** The size the file was written with; a save must re-split at exactly it. */
  blockSize: number
  maxEditBytes: number
  /** pdf | docx | xlsx, sniffed from the bytes, never from the content type. */
  sniffedType: string
  filename: string
  sizeBytes: number
  contentType: string
  blockHashes: string[]
}

/**
 * GetEditSession is the editor's entry point: it authorizes the caller, decides
 * whether the type is editable at all, and returns a ticket URL pinned to the
 * exact version the commit will be based on.
 *
 * A refusal is a successful response with canEdit=false, so this query has no
 * error path for "you cannot edit this" — only for "the file is gone".
 *
 * `staleTime` stays under the ticket's own lifetime: a session that ages out
 * while the editor is open refetches instead of loading bytes the content route
 * will refuse.
 */
export function useEditSession(fileId: string, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ["edit-session", fileId],
    queryFn: async (): Promise<EditSession> => {
      const response = await metadataCoreBrowserRpcClient.getEditSession({ fileId })
      return {
        canEdit: response.canEdit,
        unavailableReason: response.unavailableReason,
        version: response.version,
        contentHash: response.contentHash,
        ticketUrl: response.ticketUrl,
        expiresInSeconds: Number(response.expiresInSeconds),
        blockSize: Number(response.blockSize),
        maxEditBytes: Number(response.maxEditBytes),
        sniffedType: response.sniffedType,
        filename: response.filename,
        sizeBytes: Number(response.sizeBytes),
        contentType: response.contentType,
        blockHashes: response.blockHashes,
      }
    },
    enabled: Boolean(fileId) && (options?.enabled ?? true),
    staleTime: 60_000,
    retry: 1,
  })
}

/** The file row itself, for the header the edit route shows around the editor. */
export function useFileForEdit(fileId: string) {
  return useQuery({
    queryKey: ["edit-file", fileId],
    queryFn: async (): Promise<FileT> => metadataFileBrowserRpcClient.getFile({ fileId }),
    enabled: Boolean(fileId),
    staleTime: 60_000,
    retry: 1,
  })
}
