import { FileEditView } from "@/modules/files/views/file-edit-view"

// Editing gets its own full-page route rather than a drawer: a WASM document
// engine plus the save flow needs the whole viewport, and a save replaces the
// file's content rather than navigating within it. There is deliberately no
// @modal interception here — an editor is never a peek at the grid.
//
// The route is reachable from the preview drawer's Edit action, and shows its
// refusal state (wrong type, too large, editing switched off) in place, so a
// file that cannot be edited never lands in a blank editor.
export default async function EditPage({
  params,
}: {
  params: Promise<{ folderId: string; fileId: string }>
}) {
  const { folderId, fileId } = await params
  return <FileEditView fileId={fileId} folderId={folderId} />
}
