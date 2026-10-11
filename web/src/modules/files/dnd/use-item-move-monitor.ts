"use client"

import { useEffect, useRef } from "react"
import { monitorForElements } from "@atlaskit/pragmatic-drag-and-drop/element/adapter"

import { isFileViewDropData, isItemDragData } from "./dnd-types"

export type MoveItemPayload = {
  itemId: string
  itemType: "file" | "folder"
}

interface UseItemMoveMonitorArgs {
  // Selection snapshot, read through a ref AT DROP TIME so the monitor
  // subscribes once per mount (an unstable callback would re-subscribe on
  // every render of the view — wasteful, and racy against hover re-renders
  // during an active drag) while still seeing the current selection.
  getSelectedItems: () => MoveItemPayload[]
  // Executes the moves. Per-item guards (item already in the destination,
  // item vanished from the list) live in the caller, which owns the items.
  onMoveItems: (moves: MoveItemPayload[], targetFolderId: string) => void
}

// useItemMoveMonitor installs the global drop handler for the file view.
// Draggables and drop targets only carry data; this monitor is where the
// actual move decision lives:
//
//   - the dragged payload must be a file-view item,
//   - the innermost drop target decides the destination (folder drop target
//     beats the surrounding root drop target, matching location.current
//     ordering: innermost first),
//   - dropping a folder onto itself is a no-op,
//   - dragging any SELECTED item moves the whole selection (multi-drag);
//     dragging an unselected item moves just that item.
//
// The moves themselves are executed by the caller through TanStack mutations
// so cache invalidation and error handling stay in the query layer.
export function useItemMoveMonitor({ getSelectedItems, onMoveItems }: UseItemMoveMonitorArgs) {
  const selectedRef = useRef(getSelectedItems)
  selectedRef.current = getSelectedItems
  const handlerRef = useRef(onMoveItems)
  handlerRef.current = onMoveItems

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

        const selected = selectedRef.current()
        const isSelectedDrag = selected.some((s) => s.itemId === itemId)
        if (!isSelectedDrag && fromFolderId === targetFolderId) return // already there

        const moves = isSelectedDrag ? selected : [{ itemId, itemType }]
        handlerRef.current(moves, targetFolderId)
      },
    })
  }, [])
}
