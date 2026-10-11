"use client"

import { useCallback, useState } from "react"
import { metadataCoreBrowserRpcClient } from "@/lib/rpc"

/**
 * The two-tier download flow:
 *
 * 1. GetDownloadTicket (connect-rpc, cookie-authenticated via the Next.js
 *    proxy) — the ONLY place an ownership check happens. Returns either a
 *    presigned S3 URL (Tier B: file consolidated) or a content-server URL
 *    with an HMAC ticket (Tier A: live block reconstruction).
 * 2. Hand the URL to the browser (<a download> + click) — the native
 *    download manager does Range/resume on its own; no blob assembly.
 *
 * The ticket is single-file, short-lived (~120s), and URL-scoped, so letting
 * the browser navigate to it is safe: leak blast radius is one file for one
 * ticket TTL.
 */
export function useDownloadFile() {
  const [pendingIds, setPendingIds] = useState<Set<string>>(new Set())

  const downloadFile = useCallback(async (fileId: string, filename: string) => {
    setPendingIds((current) => new Set(current).add(fileId))
    try {
      const response = await metadataCoreBrowserRpcClient.getDownloadTicket({
        fileId,
        // attachment = force download; inline previews embed the same URL
        // with disposition=inline (drawer preview path).
        disposition: "attachment",
      })
      const url = response.url
      if (!url) throw new Error("Download URL unavailable")

      const anchor = document.createElement("a")
      anchor.href = url
      // The server sends Content-Disposition with the real filename; the
      // attribute is a same-origin-only hint that keeps
      // content initiated by a stray click from replacing the SPA state.
      anchor.download = filename || "download"
      anchor.rel = "noopener"
      document.body.appendChild(anchor)
      anchor.click()
      anchor.remove()
    } finally {
      setPendingIds((current) => {
        const next = new Set(current)
        next.delete(fileId)
        return next
      })
    }
  }, [])

  return {
    downloadFile,
    isPending: (fileId: string) => pendingIds.has(fileId),
    isAnyPending: pendingIds.size > 0,
  }
}
