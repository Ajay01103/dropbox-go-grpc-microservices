// Drag payloads for the file view. Kept in one place so draggable and drop
// target registration stay in sync.

export type ItemDragData = {
  type: "file-view-item"
  itemType: "file" | "folder"
  itemId: string
  name: string
  // The folder the item currently lives in ("" = root).
  fromFolderId: string
}

export type FolderDropData = {
  type: "file-view-folder"
  folderId: string
}

export type RootDropData = {
  type: "file-view-root"
}

export type FileViewDropData = FolderDropData | RootDropData

// isItemDragData / isFileViewDropData are cheap runtime guards: pragmatic
// drag and drop stores payloads as unknown, so both sides type-narrow through
// these instead of casting.
export function isItemDragData(data: unknown): data is ItemDragData {
  return (
    typeof data === "object" &&
    data !== null &&
    (data as ItemDragData).type === "file-view-item" &&
    typeof (data as ItemDragData).itemId === "string"
  )
}

export function isFileViewDropData(data: unknown): data is FileViewDropData {
  if (typeof data !== "object" || data === null) return false
  const type = (data as FileViewDropData).type
  return type === "file-view-folder" || type === "file-view-root"
}
