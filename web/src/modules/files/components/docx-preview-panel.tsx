"use client"

import * as React from "react"
import dynamic from "next/dynamic"

import {
  DocumentErrorState,
  DocumentLoadingState,
  useAppDarkMode,
  useDocumentProbe,
} from "@/modules/files/components/document-preview-shell"

// The DOCX engine parses OOXML in a web worker backed by a WASM module, so the
// viewer is imported only when a document is actually opened, and never during
// SSR (it touches window).
const DocxViewerPreview = dynamic(
  () => import("@/components/extend/docx-viewer").then((module) => module.DocxViewerPreview),
  {
    ssr: false,
    loading: () => <DocumentLoadingState label="Loading document" />,
  },
)

/**
 * DOCX surface for the file preview drawer / preview route.
 *
 * Word documents have no server-side rendition (no thumbnail family, no
 * DOCX→PDF worker yet), so the drawer renders the real document with the
 * Extend viewer. That viewer takes a URL and fetches it itself — it has no
 * way to pass an Authorization header — which is exactly what the short-lived
 * content ticket URL is for: the bytes stream from the content route with no
 * credential beyond the ticket in the query string.
 *
 * The viewer is fed the ticket URL directly, so file bytes never pass through
 * the app's own API route, and it is probed first (see useDocumentProbe) so an
 * expired ticket can be refreshed silently before showing an error.
 */
export function DocxPreviewPanel({
  documentKey,
  filename,
  url,
  isLoading,
  isError,
  onRetry,
}: {
  documentKey: string
  filename: string
  url?: string
  isLoading: boolean
  isError: boolean
  onRetry: () => void
}) {
  const probe = useDocumentProbe({ documentKey, onExpired: onRetry, url })
  const appIsDark = useAppDarkMode()
  // Seeded from the app's appearance and then owned by the viewer's
  // night-render toggle, which changes how the document renders — not the
  // surrounding app.
  const [isDark, setIsDark] = React.useState(appIsDark)

  React.useEffect(() => {
    setIsDark(appIsDark)
  }, [appIsDark])

  if (isError) {
    return (
      <DocumentErrorState
        message="This document could not be opened for preview."
        onRetry={onRetry}
        retrying={isLoading}
      />
    )
  }

  if (!url || probe === "probing") {
    return <DocumentLoadingState label="Loading document" />
  }

  if (probe === "failed") {
    return (
      <DocumentErrorState
        message="The preview link expired before the document loaded."
        onRetry={onRetry}
        retrying={isLoading}
      />
    )
  }

  return (
    <DocxViewerPreview
      className="h-full"
      // The viewer defaults to 50%, which leaves a letter page floating in the
      // middle of the panel; fit-width fills the drawer like the PDF viewer.
      defaultZoom="fit-width"
      fileName={filename}
      isDark={isDark}
      onIsDarkChange={setIsDark}
      // Download goes through the drawer's action bar, which mints an
      // attachment ticket (native download manager, real filename).
      showDownload={false}
      // Uploading a replacement here would bypass the block store entirely.
      showUpload={false}
      src={url}
    />
  )
}
