import { UrlFilePreview } from "@/modules/files/components/url-preview"

// Intercepted soft navigation: clicking a file in the grid lands here instead
// of the real /preview route, so the drawer overlays the still-mounted grid.
export default async function InterceptedPreviewPage({
  params,
}: {
  params: Promise<{ folderId: string; fileId: string }>
}) {
  const { folderId, fileId } = await params
  return <UrlFilePreview fileId={fileId} folderId={folderId} />
}
