"use client"

import { useEffect } from "react"
import { monitorForElements } from "@atlaskit/pragmatic-drag-and-drop/element/adapter"

import { isFileViewDropData, isItemDragData } from "./dnd-types"

interface UseItemMoveMonitorArgs {
  onMoveItem: (args: { itemId: string; itemType: "file" | "folder"; targetFolderId: string }) => void
}

// useItemMoveMonitor installs the global drop handler for the file view.
// Draggables and drop targets only carry data; this monitor is where the
// actual move decision lives:
//
//   - the dragged payload must be a file-view item,
//   - the innermost drop target decides the destination (folder drop target
//     beats the surrounding root drop target, matching location.current
//     ordering: innermost first),
//   - dropping an item onto itself or into the folder it already lives in
//     is a no-op.
//
// The move itself is executed by the caller through TanStack mutations so
// cache invalidation and error handling stay in the query layer.
export function useItemMoveMonitor({ onMoveItem }: UseItemMoveMonitorArgs) {
  useEffect(() => {
    return monitorForElements({
      onDrop({ source, location }) {
        if (!isItemDragData(source.data)) return

        const target = location.current.dropTargets
          .map((t) => t.data)
          .find(isFileViewDropData)
        if (!target) return

        const { itemType, itemId, fromFolderId } = source.data
        const targetFolderId = target.type === "file-view-folder" ? target.folderId : ""

        if (itemType === "folder" && itemId === targetFolderId) return // drop on itself
        if (fromFolderId === targetFolderId) return // already there

        onMoveItem({ itemId, itemType, targetFolderId })
      },
    })
  }, [onMoveItem])
}
