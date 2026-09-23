import { UrlFilePreview } from "@/modules/files/components/url-preview"

// Non-intercepted full-page preview: hard refresh, shared link, or any
// navigation the router can't intercept renders the preview without the
// folder grid behind it.
export default async function PreviewPage({
  params,
}: {
  params: Promise<{ folderId: string; fileId: string }>
}) {
  const { folderId, fileId } = await params
  return <UrlFilePreview fileId={fileId} folderId={folderId} />
}
