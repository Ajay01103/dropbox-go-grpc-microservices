"use client"

import { useQuery } from "@tanstack/react-query"

import { metadataCoreBrowserRpcClient } from "@/lib/rpc"

export type ContentUrl = {
  url: string
  filename: string
  contentType: string
  sizeBytes: number
}

/**
 * Mints a short-lived content URL for one file.
 *
 * GetDownloadTicket is the only place ownership is checked in this flow;
 * everything downstream (the presigned object URL or the content server)
 * trusts the URL itself. That is what lets a viewer embed the bytes — an
 * <img>/<video> tag or a WASM PDF engine — without a cookie or an
 * Authorization header.
 *
 * `disposition: "inline"` is what keeps the browser from saving the file when
 * a viewer fetches it; the download path uses "attachment" instead.
 *
 * Ticket lifetime is ~2 minutes, so `staleTime` stays under it: a viewer that
 * is still open when its URL ages out picks up a fresh one on the next
 * refetch rather than hitting a 403 it cannot explain.
 */
export function useContentUrl(
  fileId: string,
  options?: { disposition?: "inline" | "attachment"; enabled?: boolean },
) {
  const disposition = options?.disposition ?? "inline"
  const enabled = options?.enabled ?? true

  return useQuery({
    queryKey: ["content-url", fileId, disposition],
    queryFn: async (): Promise<ContentUrl> => {
      const response = await metadataCoreBrowserRpcClient.getDownloadTicket({
        fileId,
        disposition,
      })
      if (!response.url) throw new Error("Content URL unavailable")
      return {
        url: response.url,
        filename: response.filename,
        contentType: response.contentType,
        sizeBytes: Number(response.sizeBytes),
      }
    },
    enabled: Boolean(fileId) && enabled,
    staleTime: 90_000,
    retry: 1,
  })
}
