import { FileView } from "@/modules/files/views/file-view"

// Folder browsing is ID-routed (/files/<folderId>): one single-partition
// lookup resolves the view, and deep links survive renames and moves because
// ids never change. Breadcrumb names are rendered from data, never parsed
// from the URL.
export default async function FolderPage({
  params,
}: {
  params: Promise<{ folderId: string }>
}) {
  const { folderId } = await params
  return <FileView folderId={folderId} />
}
