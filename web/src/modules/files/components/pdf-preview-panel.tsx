"use client"

import dynamic from "next/dynamic"

import {
  DocumentErrorState,
  DocumentLoadingState,
  useDocumentProbe,
} from "@/modules/files/components/document-preview-shell"

// pdfium ships as a WASM module plus a worker, so the viewer is imported only
// when a PDF is actually opened, and never during SSR (it touches window).
const PDFViewer = dynamic(
  () => import("@/components/extend/pdf-viewer").then((module) => module.PDFViewer),
  {
    ssr: false,
    loading: () => <DocumentLoadingState label="Loading PDF" />,
  },
)

/**
 * PDF surface for the file preview drawer / preview route.
 *
 * The Extend PDF viewer is fed the ticket URL directly, so the document streams
 * from object storage (consolidated files) or the content server (files still
 * in blocks) without a byte flowing through the app's own API route.
 *
 * Before mounting the WASM engine, one cheap probe (a 5-byte range GET) checks
 * that the URL really resolves. Ticket URLs expire after ~2 minutes, and a dead
 * URL surfaces inside the engine as an opaque load failure — by probing first
 * the panel can tell "expired ticket" apart from "broken file" and refresh the
 * ticket silently once before showing an error.
 */
export function PdfPreviewPanel({
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

  if (isError) {
    return (
      <DocumentErrorState
        message="This PDF could not be opened for preview."
        onRetry={onRetry}
        retrying={isLoading}
      />
    )
  }

  if (!url || probe === "probing") {
    return <DocumentLoadingState label="Loading PDF" />
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
    <PDFViewer
      className="h-full"
      defaultZoom="fit-width"
      fileName={filename}
      // Download is offered by the drawer's action bar, which goes through the
      // ticket flow (native download manager, correct filename, resumable).
      // The viewer's own button would re-assemble the PDF in page memory.
      showDownload={false}
      // Editing lands later; uploading a replacement here would bypass the
      // block store entirely.
      showUpload={false}
      src={url}
    />
  )
}
